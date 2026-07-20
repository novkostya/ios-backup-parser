# Domain schemas — observed reference

Per-domain schema documentation for the five domains, from **direct introspection
of a real decrypted backup** (M0 schema spike). These docs are the ground truth M1+
parsers are written against.

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

M0 left all five at **`observed`**. Since then: **`contacts.1` → validated** (M1),
**`calls.1` → validated** (M2), **`messages.1` → validated** (M3) and
**`calendar.1` → validated** (M4), each by an operator-local differential vs iLEAPP.
The remaining one (notes) is still `observed` (no parser yet).

## Storage idioms

Three idioms hide under "SQLite domain file"; each changes the join/PK/timestamp
strategy:

| Idiom | Domains | Consequence for the parser |
|---|---|---|
| Plain app SQLite | contacts, messages, calendar | Natural tables; relations by id columns / explicit FKs |
| **CoreData** | calls, notes | `Z`-prefixed tables, `Z_PK`/`Z_ENT` indirection, `Z_PRIMARYKEY` entity map, `Z_METADATA` model version; single-table inheritance (notes) |
| Blob-encoded payload | messages (`attributedBody`), notes (`ZICNOTEDATA.ZDATA`) | Text/content is **not** a column — it is a serialized blob (typedstream / gzip+protobuf) that must be decoded |

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

**All five share the Cocoa 2001 epoch; only `messages` is in nanoseconds.** The unit
divergence is the single most dangerous cross-domain footgun — a shared "Cocoa date"
helper must take the unit per column, not assume one. To Unix: `unix = cocoa_seconds
+ 978307200` (divide the nanosecond columns by 1e9 first). Confirmed against iLEAPP's
`fix_cocoa_date` (divide-by-1e9 when the value exceeds ~1e15, then Cocoa→UTC).

Caveat (calendar): EventKit uses **negative / far-past sentinel** date values for
floating, all-day, and birthday items, and permits far-future values; consumers must
tolerate out-of-range values, not assume every date is "recent."

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
timezone sentinel), each attributed in `NOTICE` and inline. iLEAPP is also the differential oracle for those domains — for messages
its own typedstream decoder (python-typedstream) is the independent oracle that
validated the from-scratch Go decoder. **imessage-exporter** (GPL) is a black-box
oracle only (source never read), documented as the stronger manual cross-check in the
`diff-study-messages` Makefile target.

## The domains

| Doc | Domain file | Idiom | Fingerprint status |
|---|---|---|---|
| [contacts.md](contacts.md) | `HomeDomain/Library/AddressBook/AddressBook.sqlitedb` | plain | `contacts.1` validated |
| [calls.md](calls.md) | `HomeDomain/Library/CallHistoryDB/CallHistory.storedata` | CoreData | `calls.1` validated |
| [messages.md](messages.md) | `HomeDomain/Library/SMS/sms.db` | plain + typedstream | `messages.1` validated |
| [calendar.md](calendar.md) | `HomeDomain/Library/Calendar/Calendar.sqlitedb` | plain | `calendar.1` validated |
| [notes.md](notes.md) | `AppDomainGroup-group.com.apple.notes/NoteStore.sqlite` | CoreData + gzip/protobuf | `notes.1` observed |
