# calendar — `Calendar.sqlitedb`

- **Backup location:** `HomeDomain` / `Library/Calendar/Calendar.sqlitedb`
  (sibling `Extras.db`, notification blobs — not the event store).
- **Storage idiom:** plain app SQLite (no CoreData) — EventKit's own schema.
- **Fingerprint:** `calendar.1` — status **observed** (iOS 18.x baseline).
- **WAL:** header `wal`; no sidecar present (checkpointed).

## Core tables

| Table | Role |
|---|---|
| `Store` | accounts/sources (iCloud, local, subscribed, …) |
| `Calendar` | calendars: `title`, `color`, `type`, `store_id`, sharing/subscription info |
| `CalendarItem` | **events (and reminders/tasks)** — the main record table |
| `Location` | locations attached to items/alarms (`title`, `address`, lat/long) |
| `Participant` | attendees (`email`, `phone_number`, `status`, `role`) |
| `Recurrence` | recurrence rules (`frequency`, `interval`, `end_date`, `specifier`) |
| `ExceptionDate` | recurrence exceptions |
| `Alarm` | alarms/reminders-to-fire (`trigger_date`, `type`, `proximity`) |
| `Attachment` / `AttachmentFile` | event attachments (file `url`, `filename`, `local_path`) |
| `Identity` | participant identities (name/address) |
| `OccurrenceCache` / `OccurrenceCacheDays` | expanded-recurrence cache (derived; not source of truth) |

`*Changes`, `ClientCursor*`, `AlarmCache`, `ScheduledTaskCache` are sync/derived
bookkeeping.

## Join topology

```
Store (ROWID) ◀─ Calendar.store_id
Calendar (ROWID) ◀─ CalendarItem.calendar_id
CalendarItem (ROWID)  ── the event/reminder
  ├─◀ Location.item_owner_id           (also start_/end_/client_ loc owner variants)
  ├─◀ Participant.owner_id
  ├─◀ Recurrence.owner_id
  ├─◀ ExceptionDate.owner_id
  ├─◀ Alarm.calendaritem_owner_id
  └─◀ Attachment.owner_id → Attachment.file_id → AttachmentFile.ROWID
```

Relations are by id convention (no explicit FKs); deletes are enforced by triggers,
not FK cascade.

## Events vs reminders

`CalendarItem.entity_type` distinguishes the item kind. This parser targets
**events**; modern iOS keeps Reminders in a separate store, though reminder-only
columns (`due_date`, `completion_date`, `priority`) still exist on `CalendarItem`.
The exact `entity_type` code→kind mapping is interpretation, to validate against
iLEAPP/EventKit.

## Timestamps

All calendar dates are **Cocoa 2001 epoch, seconds, REAL**:

| Column | Meaning |
|---|---|
| `CalendarItem.start_date` / `end_date` | event span (+ `start_tz`/`end_tz` TEXT time zones) |
| `CalendarItem.creation_date` / `last_modified` | bookkeeping |
| `CalendarItem.due_date` / `completion_date` | reminders (if present) |
| `Alarm.trigger_date`, `Recurrence.end_date`, `ExceptionDate.date`, `OccurrenceCache.*` | related dates |

**Caveat — sentinel / out-of-range dates.** EventKit represents floating, all-day,
and birthday-type items with **sentinel** date values — large **negative** (far-past)
and far-future values are normal, not corruption. The epoch/unit is uniform, but a
consumer must tolerate out-of-range values rather than assume "recent." All-day and
floating events also depend on the paired `*_tz` column; a naive UTC conversion
mis-places them.

## Capability mapping

| Record field (intended) | Source | Notes |
|---|---|---|
| title / notes | `CalendarItem.summary` / `description` | |
| start / end (+ tz) | `start_date`/`end_date` (+ `start_tz`/`end_tz`) | Cocoa **seconds**; honor tz & all-day |
| all-day | `CalendarItem.all_day` | |
| location | `Location` via `item_owner_id` | title/address/coords |
| attendees | `Participant` via `owner_id` | + `Identity` |
| recurrence | `Recurrence` + `ExceptionDate` | rule + exceptions |
| alarms | `Alarm` via `calendaritem_owner_id` | |
| calendar / account | `Calendar` → `Store` | name, color |
| attachments | `Attachment` → `AttachmentFile` | file ref (url/filename) |
| status / availability / privacy | `CalendarItem.status` / `availability` / `privacy_level` | enums, interpretation |

**`Missing[]` candidates:** conferencing (`Conference`, `conference_url`),
`SuggestedEventInfo`, sharing/`Sharee` columns are newer — absent on older
fingerprints.
