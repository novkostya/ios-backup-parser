# notes — `NoteStore.sqlite`

- **Backup location:** `AppDomainGroup-group.com.apple.notes` / `NoteStore.sqlite`.
  Media/attachments live under the same group domain
  (`Accounts/<account-uuid>/Media/…`, `.../Previews/…`, `.../FallbackImages/…`).
- **Storage idiom:** **CoreData, single-table inheritance** + **gzip+protobuf note
  bodies**. Persistent-history tables (`ACHANGE`/`ATRANSACTION`/`ATRANSACTIONSTRING`)
  are also present.
- **Fingerprint:** `notes.1` — status **observed** (iOS 18.x baseline; carries
  handwriting-summary / paperform / OCR-summary columns).
- **WAL:** header `wal`; no sidecar present (checkpointed).

## Single-table inheritance

Almost everything — notes, folders, accounts, attachments, media — lives in one wide
table, **`ZICCLOUDSYNCINGOBJECT`**, discriminated by `Z_ENT`. The entity map
(`Z_PRIMARYKEY`):

| `Z_ENT` | Entity | | `Z_ENT` | Entity |
|---|---|---|---|---|
| 12 | **ICNote** (a note) | | 14 | ICAccount |
| 19 | ICNoteData (body rows) | | 15 | ICFolder |
| 5 | ICAttachment | | 11 | ICMedia |
| 9 | ICInlineAttachment | | 17/18 | ICLocation |
| 20 | ICNoteParticipant | | 16 | ICInvitation |

So "a note" = `ZICCLOUDSYNCINGOBJECT` rows where `Z_ENT = 12` (ICNote); folders are
`Z_ENT = 15`, accounts `Z_ENT = 14`. Filtering by `Z_ENT` is mandatory — a naive
`SELECT *` mixes notes, folders, and attachments. Note-relevant columns include
`ZTITLE*`, `ZSNIPPET`, `ZFOLDER`, `ZNOTEDATA` (→ `ZICNOTEDATA.Z_PK`), and the date
columns below.

## Note body — the gzip+protobuf wrinkle (in scope)

`ZICNOTEDATA` (one row per note body): `ZNOTE → ZICCLOUDSYNCINGOBJECT.Z_PK`, and
**`ZDATA BLOB`** = the note content.

- **`ZDATA` is gzip-compressed** — the note-body blob carries the gzip magic `1F 8B`.
  Inside the gzip is Apple's **Notes protobuf** (the "Note Store proto" /
  mergeable-data structure) carrying the attributed text, attachment placeholders, and
  formatting. (An empty `ZDATA` occurs for a blank note.)
- **This is routine for every ordinary note** and is fully **in scope for M5**:
  without gunzip → protobuf-decode, a note has *no* text at all. Do not mistake the
  opaque blob for "encrypted."

## Password-protected notes — the encryption wrinkle (report-only)

Distinct from the body encoding. `ZICCLOUDSYNCINGOBJECT` carries
`ZISPASSWORDPROTECTED` plus `ZCRYPTO*` columns (`ZCRYPTOINITIALIZATIONVECTOR`,
`ZCRYPTOTAG`, `ZCRYPTOWRAPPEDKEY`, `ZCRYPTOSALT`, `ZCRYPTOVERIFIER`,
`ZCRYPTOITERATIONCOUNT`) and `ZPASSWORDHINT`. For a locked note the `ZICNOTEDATA.ZDATA`
is AES-GCM-encrypted (IV/tag in `ZICNOTEDATA.ZCRYPTOINITIALIZATIONVECTOR`/`ZCRYPTOTAG`)
under a key derived from the user's note password.

- **v0.1 scope:** locked notes are **reported** (present + `locked=true`), **never
  decrypted**.
- **Validation status:** this locked-note handling is documented **from the schema**
  (the columns above) and is **awaiting differential validation** — confirming it
  requires a backup that exercises a locked note. Recorded as designed-not-validated
  per the [status legend](README.md), not asserted from data.

## Timestamps

All Cocoa 2001 epoch, **seconds**, REAL/TIMESTAMP (the table has many date columns;
the note-relevant ones):

| Column | Meaning |
|---|---|
| `ZICCLOUDSYNCINGOBJECT.ZCREATIONDATE` | note created |
| `ZICCLOUDSYNCINGOBJECT.ZMODIFICATIONDATE1` | note last modified |

(Numerous other `Z*DATE` columns are view/sync/activity bookkeeping.)

## Capability mapping

| Record field (intended) | Source | Notes |
|---|---|---|
| note identity | `ZICCLOUDSYNCINGOBJECT` where `Z_ENT = ICNote(12)` | filter by entity |
| title | `ZTITLE*` / derived from body | |
| body text | `ZICNOTEDATA.ZDATA` → gunzip → protobuf | **decode required** |
| created / modified | `ZCREATIONDATE` / `ZMODIFICATIONDATE1` | Cocoa **seconds** |
| folder | `ZFOLDER` → `ZICCLOUDSYNCINGOBJECT` (`Z_ENT = ICFolder(15)`) | |
| account | `ZACCOUNT*` → `ZICCLOUDSYNCINGOBJECT` (`Z_ENT = ICAccount(14)`) | |
| locked? | `ZISPASSWORDPROTECTED` | **report-only**; body not decrypted |
| attachments/media | `Z_ENT` ∈ {ICAttachment(5), ICMedia(11)} ; files under group domain | `FileRef` into `AppDomainGroup-group.com.apple.notes` |

**`Missing[]` candidates:** handwriting-summary, paperform, OCR-summary, and
Synapse/activity columns are iOS-18-era — absent on older fingerprints, so they
degrade the capability report rather than break note extraction.
