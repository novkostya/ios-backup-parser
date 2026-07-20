# Domain schemas — observed reference

Per-domain schema documentation for the five M0 domains plus the post-v0.1 additions
(safari, M7; reminders, M8), from **direct introspection of a real decrypted backup**.
These docs are the ground truth the parsers are written against.

**Scrubbed by construction.** Structure only — table names, columns, joins, types,
timestamp epochs, storage idioms. Never row data, never counts, date ranges, names,
numbers, identifiers, or Operator infrastructure. Every fact here is Apple's schema,
not the study device's contents.

## Fingerprint model

A **fingerprint** is the observed set of tables and columns relevant to a domain —
detected by introspection, never inferred from an iOS version string (charter hard
rule). Its `Schema` label is a **discovery-order ordinal** (`contacts.1`, `calls.1`,
…), *not* a version-shaped name. The iOS version a fingerprint was *observed on* is
recorded as **evidence**, not identity.

Each domain doc carries exactly one baseline fingerprint (single-version honesty: one
study backup = one iOS line). The doc layout is per-fingerprint so a later-observed
version **appends** a section rather than forcing a rewrite. Fingerprints for
versions we have not observed are never invented.

**Matcher semantics (settled at M1).** In code, a fingerprint matches when its
*required* tables/columns are all present in the introspected database; extra,
unknown columns never disqualify (new iOS releases add columns constantly), and
each absent *optional unit* lands its field name in `Capability.Missing` instead
of failing. The full observed structure documented here is the fingerprint's
identity and evidence; the requirement set is the operational test derived from
it. A database missing a *required* piece is a different (possibly unsupported)
fingerprint — never a silent degradation.

**Observed baseline (all five domains):** the study device is on the **iOS 18.x**
line — inferred from schema features (e.g. `message.is_pending_satellite_send` /
`needs_relay` / RCS service; key-transparency and custom-emoji-tapback columns; Notes
handwriting-summary / paperform columns), **not** from any version file. Exact point
release is not asserted (no version string is trusted).

### Status — fixture-only vs validated

Per the charter, **green fixtures do not prove correctness**. Every fingerprint
carries a status:

- **`observed`** — structure confirmed by introspection of a real backup (this M0
  deliverable). No parser exists yet.
- **`fixture-only`** — a synthetic fixture + parser exist and pass, but no real-data
  differential has run. This is where M1+ domains start.
- **`validated`** — a differential (iLEAPP and/or iMazing spot-check) has passed on a
  real backup for this fingerprint. The differential is a required manual gate, not a
  nicety.

M0 left the five spike domains at **`observed`**; all are now **`validated`** by an
operator-local differential — **`contacts.1`** (M1), **`calls.1`** (M2),
**`messages.1`** (M3), **`calendar.1`** (M4) and **`notes.1`** (M5) — as are the
post-v0.1 domains **`safari.1`** (M7: bookmarks, reading list and history) and
**`reminders.1`** (M8). Five used iLEAPP directly as the oracle (safari via its
Bookmarks and History exports, keyed by `bookmarks.id` and `history_visits.id`); notes
and reminders used a **split oracle**, because iLEAPP's export returns zero rows on the
iOS-18 schema for both (notes: a stale `ZACCOUNT4` join; reminders: a stale
`ZREMCDOBJECT.ZTITLE1` query for text that now lives in `ZREMCDREMINDER`). Notes used
iLEAPP's own (MIT) note-body decoder plus the store's SQL and Apple's stored snippet;
reminders used the store's own SQL (keyed by (store, `Z_PK`) across every per-account
store) plus iLEAPP's confirmed store glob and Cocoa epoch (see [reminders.md](reminders.md)
→ Validation).

## Storage idioms

Three idioms hide under "SQLite domain file"; each changes the join/PK/timestamp
strategy:

| Idiom | Domains | Consequence for the parser |
|---|---|---|
| Plain app SQLite | contacts, messages, calendar, safari | Natural tables; relations by id columns / explicit FKs |
| **CoreData** | calls, notes, reminders | `Z`-prefixed tables, `Z_PK`/`Z_ENT` indirection, `Z_PRIMARYKEY` entity map, `Z_METADATA` model version; single-table inheritance (notes) / mixed inheritance across a shared `ZREMCDOBJECT` (reminders) |
| Blob-encoded payload | messages (`attributedBody`), notes (`ZICNOTEDATA.ZDATA`) | Text/content is **not** a column — it is a serialized blob (typedstream / gzip+protobuf) that must be decoded. (Reminders keep title/notes in plain columns — no decode.) |

Two domains span **multiple database files**: safari (two different stores —
`Bookmarks.db` + `History.db`) and reminders (N same-model CloudKit stores —
`Container_v1/Stores/Data-*.sqlite`, one per account). Reminders' store filenames are
UUIDs discoverable only by directory listing, so its enumeration uses the optional
`backup.ReadDirFS` capability (see [reminders.md](reminders.md)).

## Timestamp epochs — the cross-domain trap

Getting an epoch or unit wrong yields wrong-but-plausible output (off by 31 years, or
off by 10⁹). Every date column's epoch and unit is documented per domain; the summary:

| Domain | Representative column | Epoch | **Unit** | SQL type |
|---|---|---|---|---|
| contacts | `ABPerson.CreationDate` | 2001-01-01 (Cocoa) | **seconds** | INTEGER |
| calls | `ZCALLRECORD.ZDATE` | 2001-01-01 (Cocoa) | **seconds** | REAL |
| calendar | `CalendarItem.start_date` | 2001-01-01 (Cocoa) | **seconds** | REAL |
| notes | `ZICCLOUDSYNCINGOBJECT.ZCREATIONDATE` | 2001-01-01 (Cocoa) | **seconds** | REAL |
| **messages** | `message.date` | 2001-01-01 (Cocoa) | **NANOseconds** | INTEGER |
| **safari** (bookmarks) | `bookmarks.last_modified` | **1970-01-01 (Unix)** | **seconds** | REAL |
| safari (history) | `history_visits.visit_time` | 2001-01-01 (Cocoa) | **seconds** | REAL |
| reminders | `ZREMCDREMINDER.ZCREATIONDATE` | 2001-01-01 (Cocoa) | **seconds** | REAL (`TIMESTAMP`) |

**Cocoa 2001 everywhere EXCEPT Safari's `Bookmarks.db`, which is Unix 1970; and only
`messages` is in nanoseconds.** Two footguns, not one. (1) The unit divergence: a shared
"Cocoa date" helper must take the unit per column, not assume one — to Unix,
`unix = cocoa_seconds + 978307200` (divide the nanosecond columns by 1e9 first),
confirmed against iLEAPP's `fix_cocoa_date`. (2) The **epoch** divergence: Safari's two
stores disagree with each other — `Bookmarks.db.last_modified` is already Unix seconds
(no delta), while `History.db.visit_time` is Cocoa seconds. Reading either as the other
is off by 31 years (see [safari.md](safari.md), the two-epoch trap).

Caveat (calendar): EventKit uses **negative / far-past sentinel** date values for
floating, all-day, and birthday items, and permits far-future values; consumers must
tolerate out-of-range values, not assume every date is "recent." Reminders, by
contrast, use a plain **NULL** for an undated reminder (no sentinel); an all-day
reminder carries a date-only `ZDUEDATE` flagged by `ZALLDAY`.

## Capability report — validated against reality

The output-model contract is:

```go
type Capability struct {
    Domain    string   // "messages"
    Supported bool     // fingerprint recognized?
    Schema    string   // fingerprint label, e.g. "messages.1"
    Missing   []string // fields this fingerprint's schema cannot provide
}
```

M0 checked this against all five observed schemas (see each doc's *Capability
mapping* section): for every field the domain's record type intends to expose, the
schema either provides a source column/join or the field lands in `Missing[]`. The
model holds — the observed schemas map cleanly, and the columns that vary across iOS
versions (the natural `Missing[]` candidates) are called out per domain. No change to
the `Capability` shape is proposed.

## Cross-reference & license

Interpretations were cross-referenced against **iLEAPP** (MIT; reading/translating
its parsing logic is permitted **with attribution** — see `NOTICE`), primarily its
`sms.py` for the messages domain (timestamp conversion, `attributedBody`/typedstream
handling, join topology, attachment-path handling). Per-domain parsers cross-reference
the matching artifact as they land: `addressBook.py` (M1, contacts),
`callHistory.py` (M2, calls — the `ZCALLTYPE`/`ZORIGINATED` enums), `sms.py`
(M3, messages — text-else-attributedBody, the join topology, `chat.style` /
`associated_message_type` / `item_type` conventions) and `calendarAll.py`
(M4, calendar — the events/birthday `calendar_scale` split, the join topology,
`Participant.status` / `entity_type` and `sharing_status` enums, the `_float`
timezone sentinel) and `notes.py` (M5, notes — itself modified from mac_apt's Notes
plugin, MIT; the note column choices, the gzip+protobuf body encoding, and its own
body decoder) and `safariBookmarks.py` / `safariHistory.py` (M7, safari — the
`title`/`url`/`hidden` bookmarks read, the `history_visits` ⟕ `history_items` join, the
`origin` and redirect id→url resolution, and the `visit_time + 978307200` Cocoa
conversion that pinned History's epoch against Bookmarks' Unix one) and `reminders.py`
(M8, reminders — the `Container_v1/Stores/*.sqlite*` store glob and the
`col + 978307200` Cocoa epoch; its `ZREMCDOBJECT.ZTITLE1` query is stale on the
iOS-18 schema, so it is a split-oracle contributor, not the differential oracle), each
attributed in `NOTICE` and inline. iLEAPP is also the differential
oracle for those domains — for messages its own typedstream decoder (python-typedstream)
is the independent oracle that validated the from-scratch Go decoder, and for notes its
own note-body decoder plays the same role (its full notes export is unusable on the
iOS-18 schema, so the store's SQL and Apple's stored snippet complete the oracle). **imessage-exporter** (GPL) is a black-box
oracle only (source never read), documented as the stronger manual cross-check in the
`diff-study-messages` Makefile target.

## The domains

| Doc | Domain file | Idiom | Fingerprint status |
|---|---|---|---|
| [contacts.md](contacts.md) | `HomeDomain/Library/AddressBook/AddressBook.sqlitedb` | plain | `contacts.1` validated |
| [calls.md](calls.md) | `HomeDomain/Library/CallHistoryDB/CallHistory.storedata` | CoreData | `calls.1` validated |
| [messages.md](messages.md) | `HomeDomain/Library/SMS/sms.db` | plain + typedstream | `messages.1` validated |
| [calendar.md](calendar.md) | `HomeDomain/Library/Calendar/Calendar.sqlitedb` | plain | `calendar.1` validated |
| [notes.md](notes.md) | `AppDomainGroup-group.com.apple.notes/NoteStore.sqlite` | CoreData + gzip/protobuf | `notes.1` validated |
| [safari.md](safari.md) | `HomeDomain/Library/Safari/Bookmarks.db` (+ `History.db`) | plain (two stores) | `safari.1` validated |
| [reminders.md](reminders.md) | `AppDomainGroup-group.com.apple.reminders/Container_v1/Stores/Data-*.sqlite` | CoreData (N stores) | `reminders.1` validated |
