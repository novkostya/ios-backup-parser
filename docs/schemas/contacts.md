# contacts — `AddressBook.sqlitedb`

- **Backup location:** `HomeDomain` / `Library/AddressBook/AddressBook.sqlitedb`
  (sibling `AddressBookImages.sqlitedb` holds contact photos — out of scope).
- **Storage idiom:** plain app SQLite (no CoreData).
- **Fingerprint:** `contacts.1` — status **validated** (2026-07-20): the M1
  operator-local differential vs iLEAPP passed on the study backup — record
  counts matched exactly and every compared field agreed record-by-record, in
  two phases: (1) black-box (iLEAPP executed as a tool, its Address Book TSV
  compared on the fields its export carries) and (2) oracle-logic (iLEAPP's
  addressBook query semantics re-run against a scratch copy, keyed by
  `ABPerson.ROWID`, covering all parser fields incl. last name, organization,
  addresses, groups, store, created/modified). Phase 2 exists because iLEAPP
  v2026.1.0's export drops some non-empty columns (its empty-column-removal
  count query is offset by one — Last Name/Company vanish when Suffix/
  Organization counts are zero); an upstream-reportable quirk, not a data
  problem. The rung-4 iMazing rendering spot-check (Operator-manual) remains a
  recommended extra, not a blocker. Observed base: iOS 18.x study backup.
- **WAL:** the file header declares `wal`, but no `-wal`/`-shm` sidecar was present in
  the study backup (checkpointed at capture). See [README](README.md) for the
  copy-to-scratch rule; a capture-time uncommitted WAL, if it ever appears, must be
  materialized alongside the DB.

## Core tables

| Table | Role |
|---|---|
| `ABPerson` | one row per contact — the anchor record |
| `ABMultiValue` | multi-valued properties (phones, emails, addresses, URLs, IM, dates…) — one row per value |
| `ABMultiValueLabel` | label strings (`home`, `work`, …); referenced by `ABMultiValue.label` |
| `ABMultiValueEntry` | components of a structured multi-value (e.g. street/city/zip of an address) |
| `ABMultiValueEntryKey` | key strings for those components |
| `ABStore` / `ABAccount` | sources/accounts a contact belongs to |
| `ABGroup` / `ABGroupMembers` | groups and membership |

`ABPersonFullTextSearch*` and `ABPersonSmartDialerFullTextSearch*` are FTS4 search
indexes — **not** a data source; ignore for extraction. `*Changes`, `ClientCursor*`,
`*SortSection*`, `_SqliteDatabaseProperties` are sync/sort/version bookkeeping.

## Join topology

```
ABPerson (ROWID)
  └─◀ ABMultiValue.record_id            one contact → many values
        ├─ .property  INTEGER           value kind (phone/email/address/URL/…) — AB property constant*
        ├─ .label    → ABMultiValueLabel.ROWID     ("home"/"work"/custom)
        ├─ .value    TEXT               scalar value (a phone/email string)
        └─◀ ABMultiValueEntry.parent_id (= ABMultiValue.UID)   structured sub-values
              └─ .key → ABMultiValueEntryKey.ROWID  (street/city/zip/country…)
ABPerson.StoreID → ABStore.ROWID → ABStore.AccountID → ABAccount.ROWID
ABGroup (ROWID) ◀─ ABGroupMembers.group_id ; ABGroupMembers.member_id → ABPerson.ROWID
```

\* `property` is an integer AB constant (cross-referenced from iLEAPP's
`addressBook.py` artifact, MIT — see `NOTICE`; validated differentially at M1):
3=phone, 4=email, 5=address, 13=instant message, 22=URL, 23=related names,
46=profiles. (M0's "13=birthday-ish" guess was wrong — 13 is IM.) Address and
similar composite kinds fan out into `ABMultiValueEntry` and carry a NULL
`value`; scalar kinds carry their value directly in `ABMultiValue.value`.

`ABMultiValueLabel` and `ABMultiValueEntryKey` are single-column (`value TEXT`)
tables keyed by their **implicit SQLite rowid** — `ABMultiValue.label` and
`ABMultiValueEntry.key` reference rowids no declared column exposes. Built-in
labels use the wrapper form `_$!<Home>!$_`; user-defined labels are plain text.

Store/account detail (introspected at M1): `ABStore(ROWID, Name, Type,
AccountID DEFAULT -1, …)` → `ABAccount(ROWID, AccountIdentifier, …)`; a store
with `AccountID = -1` is local (no account row).

## Key `ABPerson` columns

Names: `First`, `Last`, `Middle`, `Prefix`, `Suffix`, `Nickname`, phonetic and
pronunciation variants; `Organization`, `Department`, `JobTitle`; `Note`; `Birthday`
(TEXT); `Kind` (person vs organization); `DisplayName`, `CompositeNameFallback`.
Linking: `PersonLink` / `IsPreferredName` (unified/linked contacts across accounts).

## Timestamps

| Column | Epoch | Unit | Type |
|---|---|---|---|
| `ABPerson.CreationDate` | Cocoa 2001 | seconds | INTEGER |
| `ABPerson.ModificationDate` | Cocoa 2001 | seconds | INTEGER |

Unit confirmed by magnitude (reading the integers as Cocoa **seconds** yields
plausible present-day dates; nanosecond or Unix readings do not). `Birthday` is a
free TEXT field, not a numeric timestamp. `ClientSequence`
/ `ClientCursor` carry `REAL` sync timestamps (bookkeeping, not contact data).

## Capability mapping

| Record field (intended) | Source | Notes |
|---|---|---|
| given/family/middle/prefix/suffix | `ABPerson.*` | direct |
| nickname / organization / department / job title | `ABPerson.*` | direct |
| note | `ABPerson.Note` | direct |
| phones / emails / URLs | `ABMultiValue` where `property` matches, `.value` | label via `ABMultiValueLabel` |
| postal addresses | `ABMultiValue` + `ABMultiValueEntry` | composite via entry keys |
| birthday | `ABPerson.Birthday` (TEXT) + birthday multi-value | mixed representation |
| created / modified | `ABPerson.CreationDate` / `ModificationDate` | Cocoa **seconds** |
| account / source | `ABStore` / `ABAccount` via `StoreID` | |
| groups | `ABGroup` / `ABGroupMembers` | |
| contact photo | `AddressBookImages.sqlitedb` | **out of scope** → `Missing[]` |

**`Missing[]` candidates** for schema drift: pronunciation/phonetic columns, memoji /
wallpaper / sensitive-content columns (`MemojiMetadata`, `Wallpaper*`,
`SensitiveContentConfiguration`) are newer `ABPerson` additions — absent on older
fingerprints, so they degrade the capability report rather than break extraction.
