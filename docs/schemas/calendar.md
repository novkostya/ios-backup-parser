# calendar — `Calendar.sqlitedb`

- **Backup location:** `HomeDomain` / `Library/Calendar/Calendar.sqlitedb`
  (sibling `Extras.db`, notification blobs — not the event store).
- **Storage idiom:** plain app SQLite (no CoreData) — EventKit's own schema.
- **Fingerprint:** `calendar.1` — status **validated** (M4 differential vs iLEAPP;
  iOS 18.x baseline).
- **WAL:** header `wal`; no sidecar present (checkpointed). A read-only open needs
  `immutable=1` (or a writable scratch copy — what `Materialize` provides).

## Core tables

| Table | Role |
|---|---|
| `Store` | accounts/sources (`name`, `type`) — iCloud, local, subscribed, CalDAV, … |
| `Calendar` | calendars: `title`, `color`, `type`, `store_id`, `sharing_status`, … |
| `CalendarItem` | **events** (and, in principle, reminders/tasks) — the main record table |
| `Location` | locations (`title`, `address`, `latitude`, `longitude`) |
| `Participant` | organizers and invitees (`email`, `phone_number`, `status`, `role`, `type`, `entity_type`) |
| `Identity` | participant identities (`display_name`, `address`) — **implicit rowid, no declared ROWID** |
| `Recurrence` | recurrence rules (`frequency`, `interval`, `count`, `end_date`, `specifier`) |
| `ExceptionDate` | recurrence exceptions (owned by `CalendarItem`) — not surfaced in v0.1 |
| `Alarm` | alarms (`trigger_date`, `trigger_interval`, `type`, `proximity`) |
| `Attachment` / `AttachmentFile` | event attachments (`file_id` → file `url`, `filename`, `local_path`) |
| `OccurrenceCache` / `OccurrenceCacheDays` | expanded-recurrence cache (derived; not source of truth) |

`*Changes`, `ClientCursor*`, `AlarmCache`, `ScheduledTaskCache` are sync/derived
bookkeeping. `Conference`, `SuggestedEventInfo`, `Sharee` exist on this fingerprint
(older iOS lines may lack them — the natural `Missing[]` candidates).

## Join topology (confirmed by introspection, matches iLEAPP `calendarAll.py`)

```
Store (ROWID) ◀─ Calendar.store_id
Calendar (ROWID) ◀─ CalendarItem.calendar_id
CalendarItem (ROWID)  ── the event
  ├─ location_id      ─▶ Location.ROWID              (primary location)
  ├─ organizer_id     ─▶ Participant.ROWID (entity_type 8) ─▶ Identity.ROWID
  ├─◀ Participant.owner_id      (entity_type 7 = invitees)  ─▶ Identity.ROWID
  ├─◀ Recurrence.owner_id
  ├─◀ Alarm.calendaritem_owner_id
  └─◀ Attachment.owner_id → Attachment.file_id → AttachmentFile.ROWID
```

Relations are by id convention (no explicit FKs); deletes are enforced by triggers.
Note the **direction differs per child**: the location is reached *forward* from
`CalendarItem.location_id` (not from `Location.item_owner_id`, which is a reverse
pointer that also exists), while participants/recurrence/alarms/attachments are
collected *back* from their `owner_id` columns. `Identity` has no declared `ROWID`
column — the joins use its implicit rowid.

## Events vs reminders vs birthdays — the discriminator (M0 guess corrected)

M0 recorded `CalendarItem.entity_type` as the event/reminder discriminator. That is
**wrong for this store**: introspection shows `entity_type` is a single uniform
value for every row (reminders live in a separate store on modern iOS and are absent
here), so it does not separate anything. The parser surfaces it raw and does not
filter on it.

The real split, confirmed against iLEAPP, is **`CalendarItem.calendar_scale`**:

- **events** — `calendar_scale IS NOT 'gregorian'` (the value is NULL for an
  ordinary event). This is exactly the set the parser's `Events()` streams and
  iLEAPP's *Calendar Events* artifact reports.
- **birthday items** — `calendar_scale = 'gregorian'`. A **distinct kind with a
  special date encoding** (iLEAPP's separate *Calendar Birthdays* artifact decodes
  `start_date` differently). Lumping them into events would produce
  wrong-but-plausible dates, so `Events()` **excludes** them; a dedicated birthday
  reader is deferred (forward note).

## Timestamps

All calendar dates are **Cocoa 2001 epoch, seconds, REAL** (`cocoa.FromSecondsFloat`):

| Column | Meaning |
|---|---|
| `CalendarItem.start_date` / `end_date` | event span (+ `start_tz`/`end_tz` TEXT time zones) |
| `CalendarItem.creation_date` / `last_modified` | bookkeeping |
| `Alarm.trigger_date`, `Recurrence.end_date` | related dates |

**Caveat — sentinel / out-of-range dates.** EventKit represents floating, all-day
and birthday items with **sentinel** date values — large negative (far-past) and
far-future values are normal, not corruption; consumers must tolerate out-of-range
times. **`start_tz`/`end_tz` sentinel:** the literal string **`_float`** marks a
floating (time-zone-less) event (surfaced raw; `Event.Floating()` detects it). A
naive UTC conversion that ignores the tz column mis-places all-day / floating events.

## Enum interpretations (cross-referenced from iLEAPP `calendarAll.py`, MIT)

- **`Participant.status`** (invitation response): 0 / 7 no response, 1 accepted,
  2 declined, 3 maybe. Validated differentially.
- **`Participant.entity_type`**: 7 = invitee, 8 = organizer (the row referenced by
  `CalendarItem.organizer_id`).
- **`Calendar.sharing_status`**: 0 not shared, 1 shared by me, 2 shared with me.

`CalendarItem.status` / `availability` / `privacy_level`, `Participant.role` /
`type`, `Recurrence.frequency`, and `Alarm.type` / `proximity` are surfaced **raw** —
their constant spaces are not interpreted in v0.1 (no MIT oracle covers them; the
differential validates only that the raw code round-trips), following the same
raw-code discipline as the other domains.

## Attachments — no `FileRef` (deliberate)

`Attachment.file_id → AttachmentFile` carries `filename`, `file_size`, `url`,
`UUID`, `local_path`. On the observed schema calendar attachments are **server-side
references** (`local_path` is NULL; `url` is a mail content-id reference), so the
file is **not present in the backup**. Emitting a `backup.FileRef` would fabricate a
path — forbidden by the never-fabricate hard rule — so the parser surfaces the
attachment metadata verbatim (including `local_path` when a downloaded copy exists)
and emits **no** `FileRef`. A validated backup-path convention (and thus a FileRef)
is deferred until a backup exercises a downloaded calendar attachment.

## Capability mapping (validated against reality)

The `calendar.1` fingerprint requires only the `CalendarItem` anchor
(`ROWID`, `summary`, `start_date`, `end_date`, `all_day`, `calendar_id`,
`calendar_scale`); everything else is an Optional unit whose absence lands its name
in `Capability.Missing`:

| Unit (`Missing[]` name) | Source | Record field |
|---|---|---|
| `notes` | `CalendarItem.description` | `Event.Notes` |
| `entity_type` | `CalendarItem.entity_type` | `Event.EntityType` |
| `timezone` | `CalendarItem.start_tz` / `end_tz` | `Event.StartTZ` / `EndTZ` |
| `status` / `availability` / `privacy` | `CalendarItem.status` / `availability` / `privacy_level` | raw enums |
| `url` | `CalendarItem.url` | `Event.URL` |
| `created` / `modified` | `CalendarItem.creation_date` / `last_modified` | timestamps |
| `conference` | `CalendarItem.conference_url_detected` (→ `conference_url` fallback) | `Event.ConferenceURL` |
| `calendar` | `Calendar` → `Store` | `Event.Calendar` (+ account) |
| `location` | `CalendarItem.location_id` → `Location` | `Event.Location` |
| `attendees` | `CalendarItem.organizer_id` + `Participant` (entity_type 7/8) → `Identity` | `Event.Organizer` / `Attendees` |
| `recurrence` | `Recurrence` via `owner_id` | `Event.Recurrences` |
| `alarms` | `Alarm` via `calendaritem_owner_id` | `Event.Alarms` |
| `attachments` | `Attachment` → `AttachmentFile` | `Event.Attachments` |

On the observed study backup every unit was present (empty `Capability.Missing`),
zero row errors, and the differential (phase 1: iLEAPP's Calendar Events export;
phase 2: iLEAPP's query semantics re-run against a scratch copy, keyed by
`CalendarItem.ROWID` with a both-directions set check) agreed on every surfaced
field of every event — moving `calendar.1` from `observed` to **validated**.
