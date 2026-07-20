# reminders — `Container_v1/Stores/Data-*.sqlite`

- **Backup location:** `AppDomainGroup-group.com.apple.reminders` /
  `Container_v1/Stores/` — the domain spans **several** CoreData stores in one
  directory:
  - `Data-<UUID>.sqlite` — one per account (a CloudKit-mirrored store; the UUID
    is assigned at store-creation time and recorded in **no manifest**).
  - `Data-local.sqlite` — the on-device (non-synced) account's store; the only
    store whose filename is fixed.
  - The unrelated `MLModels/RDCoreBehaviorModel.sqlite` (an on-device ML model)
    lives in a sibling directory and is **not** a reminder store.
- **Storage idiom:** CoreData, **mixed inheritance** — reminders have their own
  table (`ZREMCDREMINDER`), lists share `ZREMCDBASELIST`
  (`REMCDList`/`REMCDSmartList`), and accounts, recurrence rules, assignments and
  sharees are `REMCDObject` subclasses sharing the wide `ZREMCDOBJECT` table,
  discriminated by `Z_ENT`.
- **Reminders moved to their own store around iOS 13.** Before that they lived in
  the calendar database; the M0 calendar doc recorded `CalendarItem`'s
  `due_date`/`completion_date`/`priority` columns as present-but-unused for exactly
  this reason. This domain reads the dedicated Reminders store, not
  `Calendar.sqlitedb`.
- **Fingerprint:** `reminders.1` — status **validated** (M8 differential; iOS 18.x
  baseline).
- **WAL:** the stores were observed checkpointed (no `-wal`/`-shm` sidecar). A
  read-only open needs a writable scratch copy — what `Materialize` provides;
  sidecars, when present, are materialized alongside.

## Many stores — one domain, one capability report

`reminders.Open` **enumerates** `Container_v1/Stores/Data-*.sqlite` and opens every
match; each store is introspected against the same `reminders.1` fingerprint (they
share one model). A reminder's identity is therefore **(store, `Z_PK`)** — each
store has its own `Z_PK` sequence, so a bare id repeats across stores.
`Reminder.Store` (the store's base filename) carries the namespace.

**Enumeration needs `backup.ReadDirFS`** (a new *optional* FS capability added at
M8, mirroring `io/fs.ReadDirFS`; the base `FS` stays `Materialize`+`Exists`). The
built-in `DirFS` implements it. A host that does **not** implement it cannot
discover the UUID-named stores, so `Open` falls back to the fixed-name
`Data-local.sqlite` alone and adds **`cloudkit_stores`** to `Capability.Missing` —
honest degradation, never a silent partial read. (On the study backup the account
data lives in a `Data-<UUID>.sqlite`, and `Data-local.sqlite` holds **zero**
reminders, so the fallback path would surface nothing — which is exactly why the
capability must announce it rather than read empty.)

## Entity ordinals are resolved per store — the silent-corruption trap

Every store carries a `Z_PRIMARYKEY` entity map (`Z_ENT` → `Z_NAME`). The parser
resolves the ordinals it needs (`REMCDReminder`, `REMCDAccount`,
`REMCDRecurrenceRule`, `REMCDAssignment`, `REMCDSharee`) **by name, per store, at
Open** — never hard-coded. This is not academic: on the study backup the
`Data-local.sqlite` store **renumbers** entities relative to the CloudKit stores (a
`REMCDGroceryOperationQueueItem` entity inserted mid-map shifts `REMCDAccount`,
`REMCDRecurrenceRule`, `REMCDAssignment` and `REMCDSharee` by one). A hard-coded
ordinal — or one entity map shared across stores — would read the wrong entity from
one of the stores: the exact wrong-but-plausible failure this library exists to
prevent. (This is the same detect-never-assume rule notes applies to its
single-table inheritance, generalized across multiple stores.)

## Core tables

| Table | Role |
|---|---|
| `ZREMCDREMINDER` | **the record table** — one row per reminder (`Z_ENT` = `REMCDReminder`) |
| `ZREMCDBASELIST` | reminder **lists** — `REMCDList` / `REMCDSmartList` (a list-family container) |
| `ZREMCDOBJECT` | wide shared table for `REMCDObject` subclasses: **accounts**, **recurrence rules**, **assignments**, **sharees**, alarms, triggers (discriminated by `Z_ENT`) |
| `Z_PRIMARYKEY` | CoreData entity map (`Z_ENT` → `Z_NAME`) — resolved per store |

### `ZREMCDREMINDER` — the reminder

Plain columns hold the text — **no blob decode** (unlike messages/notes): `ZTITLE`
(the title) and `ZNOTES` (the free-text body) are ordinary `VARCHAR`. (A rich
`ZTITLEDOCUMENT`/`ZNOTESDOCUMENT` blob also exists for formatted text; the plain
columns carry the text and are what v0.1 surfaces.)

| Column | Meaning |
|---|---|
| `Z_PK` | primary key (within the store) |
| `ZIDENTIFIER` | the reminder's stable UUID, stored as a **16-byte BLOB** (formatted to a canonical UUID string) |
| `ZTITLE` / `ZNOTES` | title / notes (plain text) |
| `ZCOMPLETED` / `ZCOMPLETIONDATE` | completion flag / when completed |
| `ZFLAGGED` | flagged |
| `ZPRIORITY` | priority (EKReminderPriority: `0` none, `1` high, `5` medium, `9` low — surfaced raw) |
| `ZALLDAY` | the due date is a date only (time-of-day not meaningful) |
| `ZDUEDATE` / `ZSTARTDATE` | due / start date (NULL = undated — no sentinel) |
| `ZCREATIONDATE` / `ZLASTMODIFIEDDATE` | created / last-modified |
| `ZMARKEDFORDELETION` | pending purge (still present) |
| `ZPARENTREMINDER` | parent reminder `Z_PK` for a subtask, else NULL (surfaced raw) |
| `ZLIST` | → `ZREMCDBASELIST.Z_PK` (the reminder's list) |
| `ZACCOUNT` | → `ZREMCDOBJECT.Z_PK` (a `REMCDAccount` row) |

### Join topology

```
ZREMCDREMINDER (one per reminder)  ── the record
  ├─ ZLIST     ─▶ ZREMCDBASELIST.Z_PK                   (its list)
  │                └─ ZACCOUNT ─▶ ZREMCDOBJECT.Z_PK  [Z_ENT=REMCDAccount]  (list's account)
  ├─ ZACCOUNT  ─▶ ZREMCDOBJECT.Z_PK  [Z_ENT=REMCDAccount]                   (its account)
  ├─ (recurrence) ◀─ ZREMCDOBJECT.ZREMINDER4  [Z_ENT=REMCDRecurrenceRule]   (back-pointer)
  └─ (assignment) ◀─ ZREMCDOBJECT.ZREMINDER1  [Z_ENT=REMCDAssignment]
                        └─ ZASSIGNEE ─▶ ZREMCDOBJECT.Z_PK  [Z_ENT=REMCDSharee]  (assignee)
```

All descriptive references resolve **soft-nil** (LEFT-JOIN semantics): an
unresolved list/account/recurrence/assignee leaves the field nil/empty and never
withholds the reminder. The only row-scoped defect is a row that fails to scan
(e.g. a corrupt date), yielded as a `*backup.RowError` while the stream continues.

### Lists (`ZREMCDBASELIST`)

`Lists()` streams every `ZREMCDBASELIST` row (the whole list family — regular lists,
smart lists, groups) with `ZNAME`, `ZISGROUP`, `ZSHARINGSTATUS` (raw) and its
account. `Reminder.List` resolves by `Z_PK` regardless of the list's `Z_ENT`.

### Recurrence & assignment — surfaced raw, documented-to-validate

Recurrence (`REMCDRecurrenceRule` rows in `ZREMCDOBJECT`, linked to the reminder by
`ZREMINDER4`) surfaces `ZFREQUENCY` / `ZINTERVAL` / `ZOCCURRENCECOUNT` / `ZENDDATE`
**raw** — the frequency constant space is not differentially validated in this
milestone (no MIT oracle interprets it; iLEAPP surfaces no recurrence). Sharing
metadata is likewise raw: `List.SharingStatus`, and `Reminder.Assignee` (resolved
best-effort through `REMCDAssignment` → `REMCDSharee` first/last name or address).

## Timestamps — one epoch, one unit (no cross-store surprise)

Unlike safari, every reminder date column shares the same epoch/unit — verified by
magnitude, not assumed:

| Column | Epoch | **Unit** | SQL type | Converter |
|---|---|---|---|---|
| `ZCREATIONDATE`, `ZLASTMODIFIEDDATE`, `ZDUEDATE`, `ZCOMPLETIONDATE`, `ZSTARTDATE` | 2001-01-01 (Cocoa) | **seconds** | REAL (`TIMESTAMP`) | `cocoa.FromSecondsFloat` |

The magnitudes land in the 2012–2026 range as Cocoa seconds (1981–1995 as Unix — an
impossible past); iLEAPP's `reminders.py` confirms the epoch with
`DATETIME(col + 978307200, 'UNIXEPOCH')`. **Undated reminders are NULL** (not a
floating/sentinel value like calendar's `_float`); an all-day reminder carries a
date-only `ZDUEDATE` flagged by `ZALLDAY`.

## Attachments — none surfaced in v0.1

Reminders can carry file/image/URL attachments (`REMCDAttachment` and its
`REMCDFileAttachment`/`REMCDImageAttachment`/`REMCDURLAttachment` subclasses in
`ZREMCDOBJECT`). None were exercised on the study backup, and a validated
backup-path convention would need one to confirm; attachments are **deferred**
(a forward note, like calendar's downloaded attachments). The domain emits **no**
`backup.FileRef` (the never-fabricate rule).

## Capability mapping (validated against reality)

`reminders.1` requires only the entity map (`Z_PRIMARYKEY`) and the reminder anchor
(`ZREMCDREMINDER.Z_PK` / `Z_ENT` / `ZTITLE`); everything else is an Optional unit
whose absence lands its name in `Capability.Missing`:

| Unit (`Missing[]` name) | Source | Record field |
|---|---|---|
| `notes` | `ZREMCDREMINDER.ZNOTES` | `Reminder.Notes` |
| `completion` | `ZCOMPLETED` + `ZCOMPLETIONDATE` | `Reminder.Completed` / `Completion` |
| `flagged` | `ZFLAGGED` | `Reminder.Flagged` |
| `priority` | `ZPRIORITY` | `Reminder.Priority` |
| `all_day` | `ZALLDAY` | `Reminder.AllDay` |
| `created` / `modified` / `due` / `start` | the matching date column | `Reminder.Created` / `Modified` / `Due` / `Start` |
| `deletion` | `ZMARKEDFORDELETION` | `Reminder.MarkedForDeletion` |
| `parent` | `ZPARENTREMINDER` | `Reminder.ParentID` |
| `identifier` | `ZIDENTIFIER` | `Reminder.Identifier` |
| `list_link` | `ZREMCDREMINDER.ZLIST` | the reminder→list pointer |
| `lists` | `ZREMCDBASELIST` (`Z_PK`/`Z_ENT`/`ZIDENTIFIER`/`ZNAME`/`ZISGROUP`/`ZSHARINGSTATUS`/`ZACCOUNT`) | the `Lists()` stream + `Reminder.List` |
| `account` | `ZREMCDREMINDER.ZACCOUNT` + `ZREMCDOBJECT.ZNAME` | `Reminder.Account` / `List.Account` |
| `recurrence` | `ZREMCDOBJECT` (`ZREMINDER4`/`ZFREQUENCY`/`ZINTERVAL`/`ZOCCURRENCECOUNT`/`ZENDDATE`) | `Reminder.Recurrence` |
| `assignment` | `ZREMCDOBJECT` (`ZREMINDER1`/`ZASSIGNEE`/`ZFIRSTNAME`/`ZLASTNAME`/`ZADDRESS1`) | `Reminder.Assignee` |
| `cloudkit_stores` | *(cross-file)* the per-account stores when a host cannot enumerate them | — added at Open only when the FS lacks `ReadDirFS` |

On the study backup every column-backed unit was present (empty `Capability.Missing`),
zero row errors.

## Validation — split-oracle (iLEAPP's reminders export is stale)

iLEAPP's `reminders.py` reads `SELECT … FROM ZREMCDOBJECT WHERE ZTITLE1 <> ''` and
guards each store on `does_column_exist_in_db(ZREMCDOBJECT, 'ZLASTMODIFIEDDATE')`. On
this schema reminders live in **`ZREMCDREMINDER`** (title `ZTITLE`, not
`ZREMCDOBJECT.ZTITLE1`) and `ZREMCDOBJECT` has **no** `ZLASTMODIFIEDDATE`, so the
guard is false, iLEAPP skips every store and produces **zero** reminders — the same
notes-class staleness (confirmed by reading its source and by an empirical run
producing no reminders output). So the oracle is **split**, still independent and
MIT-derived (attributed in `NOTICE`):

- iLEAPP's **store glob** (`Container_v1/Stores/*.sqlite*`) and its **Cocoa epoch**
  (`+ 978307200`, `UNIXEPOCH`) are correct and reused by `deploy/diff_reminders.py`.
- The **authoritative oracle** is that harness's own SQL, re-run against a scratch
  copy of **every** store, resolving entity ordinals per store and keyed by
  (store, `ZREMCDREMINDER.Z_PK`), with a both-directions set check (db rows ==
  yielded ids + row-errored ids: no invented, no silently-dropped reminder).

The differential cross-checked **every** reminder across all stores on title, notes,
completion, flagged, priority, all-day, every timestamp, marked-for-deletion, parent,
identifier, list, account, recurrence (frequency/interval) and assignee, and every
list on name/group/identifier/account — **zero mismatches**, both-directions set
clean, zero row errors — moving `reminders.1` from `observed` to **validated**. The
parser needed **no** correctness change after the pre-coding introspection. Passing
this proves the per-store ordinal resolution on real data: the study's stores use
different `Z_ENT` maps, and reading either with the other's ordinals would have
mis-mapped its accounts and reminders.
