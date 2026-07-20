# notes — `NoteStore.sqlite`

- **Backup location:** `AppDomainGroup-group.com.apple.notes` / `NoteStore.sqlite`.
  Media/attachments live under the same group domain
  (`Accounts/<account-uuid>/Media/…`, `.../Previews/…`, `.../FallbackImages/…`).
- **Storage idiom:** **CoreData, single-table inheritance** + **gzip+protobuf note
  bodies**. Persistent-history tables (`ACHANGE`/`ATRANSACTION`/`ATRANSACTIONSTRING`)
  are also present.
- **Fingerprint:** `notes.1` — status **validated** (M5; iOS 18.x baseline; carries
  handwriting-summary / paperform / OCR-summary columns). The differential is
  described under *Validation* below.
- **WAL:** header `wal`; no sidecar present (checkpointed).

## Single-table inheritance

Almost everything — notes, folders, accounts, attachments, media — lives in one wide
table, **`ZICCLOUDSYNCINGOBJECT`**, discriminated by `Z_ENT`. The entity map is
**`Z_PRIMARYKEY`** (`Z_ENT`, `Z_NAME`, `Z_SUPER`), observed as:

| `Z_ENT` | Entity | | `Z_ENT` | Entity |
|---|---|---|---|---|
| 12 | **ICNote** (a note) | | 14 | ICAccount |
| 19 | ICNoteData (body rows) | | 15 | ICFolder |
| 5 | ICAttachment | | 11 | ICMedia |
| 9 | ICInlineAttachment | | 17/18 | ICLocation |
| 20 | ICNoteParticipant | | 16 | ICInvitation |

So "a note" = `ZICCLOUDSYNCINGOBJECT` rows where `Z_ENT = ICNote`; folders are
`ICFolder`, accounts `ICAccount`. Filtering by `Z_ENT` is mandatory — a naive
`SELECT *` mixes notes, folders, and attachments.

**The `Z_ENT` ordinals are a per-model fact, resolved at run time, never hard-coded.**
Those integers (12/15/14/…) belong to *this* model version; a different model can
renumber them. The parser therefore reads `Z_PRIMARYKEY` at Open and looks up
`ICNote`/`ICFolder`/`ICAccount`/`ICAttachment`/`ICMedia` **by name** — the same
"detect, never assume" discipline the charter applies to schemas. (Hard-coding
`Z_ENT = 12` is exactly the kind of brittle assumption that breaks silently.)

## Note columns — the single-table-inheritance suffix trap (corrected from M0)

Because one table holds every entity, CoreData disambiguates colliding attributes
with **numeric suffixes**, and *which* suffix a note uses is version-specific. M0
guessed the unsuffixed names; introspection of the real store corrected them, and
the differential confirmed the corrections. On `notes.1`:

| Record field | Column | Note |
|---|---|---|
| id | `Z_PK` | CoreData primary key |
| identifier | `ZIDENTIFIER` | the note's own UUID |
| title | `ZTITLE1` | (a **folder**'s title is `ZTITLE2`) |
| snippet | `ZSNIPPET` | Apple's own stored plain-text preview of the body |
| body | `ZNOTEDATA` → `ZICNOTEDATA.ZDATA` | gzip+protobuf — see below |
| created | **`ZCREATIONDATE3`** | Cocoa **seconds**, REAL (M0 said `ZCREATIONDATE` — wrong) |
| modified | `ZMODIFICATIONDATE1` | Cocoa **seconds**, REAL |
| folder | `ZFOLDER` → `ICFolder.Z_PK` (title `ZTITLE2`) | |
| account | **`ZACCOUNT7`** → `ICAccount.Z_PK` (name `ZNAME`) | M0 said generic `ZACCOUNT*` |
| locked | `ZISPASSWORDPROTECTED`, hint `ZPASSWORDHINT` | report-only (below) |
| pinned / deleted | `ZISPINNED` / `ZMARKEDFORDELETION` | |

The `(creation column, account column)` pair is version-specific: iLEAPP's notes.py
branches between `(ZCREATIONDATE3, ZACCOUNT7)`, `(ZCREATIONDATE1, ZACCOUNT3)` and
`(ZCREATIONDATE1, ZACCOUNT2)`. `notes.1` is the first pair. A store using a different
pair is a **different fingerprint** (`notes.2`), not a silent degradation — it is not
invented here (single-version honesty), but the fingerprint layout appends cleanly
when one is observed.

## Note body — the gzip+protobuf wrinkle (in scope)

`ZICNOTEDATA` (one row per note body): `ZNOTE → ZICCLOUDSYNCINGOBJECT.Z_PK`, and
**`ZDATA BLOB`** = the note content.

- **`ZDATA` is gzip-compressed** — the note-body blob carries the gzip magic `1F 8B`.
  Inside the gzip is Apple's **Note Store protobuf** carrying the attributed text,
  attachment placeholders, and formatting. (An empty/NULL `ZDATA` occurs for a blank
  note — decoded to an empty body, never "undecoded".)
- **The note text sits at a fixed protobuf field path:**
  `document (field 2) → note (field 3) → note_text (field 2)`, confirmed against
  instrumented dumps of the store. The decoder (`internal/applenotes`) is a
  from-scratch recursive-descent protobuf reader that walks exactly that path and
  returns `note_text` as UTF-8. Rich formatting / attribute runs (bold, tables,
  embedded objects) are **deferred for v0.1**; embedded-object placeholders (U+FFFC)
  are kept verbatim and the objects themselves surface as attachments.
- **This is routine for every ordinary note** and is fully **in scope**: without
  gunzip → protobuf-decode, a note has *no* text at all. A blob that is present but
  cannot be decoded surfaces as `BodyUndecoded` (body unknown), never as a silent
  empty note.

## Attachments and media

An embedded attachment is an **ICAttachment** row (`ZNOTE →` the note); a media-backed
attachment additionally has an **ICMedia** row (`ZATTACHMENT1 →` the attachment,
`ZTYPEUTI`, `ZIDENTIFIER`, `ZGENERATION1`, `ZFILENAME`). The media file resolves to:

```
Accounts/<ICAccount.ZIDENTIFIER>/Media/<ICMedia.ZIDENTIFIER>/<ICMedia.ZGENERATION1>/<ICMedia.ZFILENAME>
```

in the notes group domain — surfaced as a `FileRef{Domain, RelativePath}` that
round-trips into `BackupFS.Materialize`. The `ZGENERATION1` segment (a `1_<uuid>`
generation directory) is a real column, not a guess; every emitted FileRef was
confirmed to resolve to a file on disk (the never-fabricate rule). A non-media
attachment (table, drawing, link) carries metadata only, no FileRef. iLEAPP's
hard-coded `Media/<id>/<filename>` path (no account dir, no generation segment) does
not resolve on this layout.

## Password-protected notes — the encryption wrinkle (report-only)

Distinct from the body encoding. `ZICCLOUDSYNCINGOBJECT` carries
`ZISPASSWORDPROTECTED` plus `ZCRYPTO*` columns (`ZCRYPTOINITIALIZATIONVECTOR`,
`ZCRYPTOTAG`, `ZCRYPTOWRAPPEDKEY`, `ZCRYPTOSALT`, `ZCRYPTOVERIFIER`,
`ZCRYPTOITERATIONCOUNT`) and `ZPASSWORDHINT`. For a locked note the `ZICNOTEDATA.ZDATA`
is AES-GCM-encrypted (IV/tag in `ZICNOTEDATA.ZCRYPTOINITIALIZATIONVECTOR`/`ZCRYPTOTAG`)
under a key derived from the user's note password.

- **v0.1 scope:** locked notes are **reported** (present + `Locked = true`, with
  `PasswordHint`), **never decrypted**. The body is left empty and is *not* flagged
  `BodyUndecoded` (it is intentionally not decoded, which is not a decode failure).
- **Validation status:** the locked-note report path is exercised by the synthetic
  fixture and is **designed from the schema** (the columns above); a real-backup
  differential of a locked note **awaits a backup that exercises one**. Recorded as
  designed-and-fixture-tested, per the [status legend](README.md) — not asserted from
  study data.

## Timestamps

All Cocoa 2001 epoch, **seconds**, REAL/TIMESTAMP (the table has many date columns;
the note-relevant ones):

| Column | Meaning |
|---|---|
| `ZICCLOUDSYNCINGOBJECT.ZCREATIONDATE3` | note created |
| `ZICCLOUDSYNCINGOBJECT.ZMODIFICATIONDATE1` | note last modified |

(Numerous other `Z*DATE` columns — `ZCREATIONDATE`/`1`/`2`, `ZMODIFICATIONDATE`,
`Z*VIEWEDDATE`, … — are view/sync/activity bookkeeping or older-schema variants.)

## Capability mapping

| Record field (intended) | Source | Notes |
|---|---|---|
| note identity | `ZICCLOUDSYNCINGOBJECT` where `Z_ENT = ICNote` | filter by entity (ordinal from `Z_PRIMARYKEY`) |
| title | `ZTITLE1` | optional unit `title` |
| snippet | `ZSNIPPET` | Apple's plain-text preview; optional unit `snippet` |
| body text | `ZICNOTEDATA.ZDATA` → gunzip → protobuf | **decode required** (Required unit) |
| created / modified | `ZCREATIONDATE3` / `ZMODIFICATIONDATE1` | Cocoa **seconds**; units `created`/`modified` |
| folder | `ZFOLDER` → `ICFolder` (title `ZTITLE2`) | optional unit `folders` |
| account | `ZACCOUNT7` → `ICAccount` (`ZNAME`) | optional unit `account` |
| locked? | `ZISPASSWORDPROTECTED` / `ZPASSWORDHINT` | **report-only**; unit `locked` |
| pinned / deleted | `ZISPINNED` / `ZMARKEDFORDELETION` | units `pinned` / `deletion` |
| attachments/media | ICAttachment (`ZNOTE`) → ICMedia (`ZATTACHMENT1`) | `FileRef` into the group domain; unit `attachments` |

**`Missing[]` candidates:** handwriting-summary, paperform, OCR-summary, and
Synapse/activity columns are iOS-18-era — absent on older fingerprints, so they
degrade the capability report rather than break note extraction. (The parser surfaces
only fields it maps to a record; those bookkeeping columns are not modelled as units.)

## Validation

`notes.1` reached **validated** by an operator-local differential (testing ladder
rung 3). Because iLEAPP's `notes.py` hard-codes the note→account join on `ZACCOUNT4`
— which on the iOS 17/18 schema is `ZACCOUNT7` — its INNER JOIN matches nothing and
it returns **zero notes** (confirmed by its own `sample_data`: "iOS 18.x | 0 rows").
Its full export is therefore useless as a phase-1 oracle here, so the oracle is split
(still independent, still MIT):

- **Body decoder:** iLEAPP's own note-body decoder (`get_uncompressed_data` +
  `process_note_body_blob`, a fixed-offset byte-walk) ported into
  `deploy/diff_notes.py` and run against a scratch copy — cross-checking the
  from-scratch Go recursive-descent protobuf reader blob-for-blob (a *different*
  algorithm validating the same bytes, as python-typedstream does for messages).
- **Snippet:** every decoded body cross-checked against Apple's own stored `ZSNIPPET`
  preview — an oracle-independent confirmation the text is real.
- **Metadata + set:** iLEAPP's column choices re-run against the scratch copy keyed
  by ICNote `Z_PK`, with the exact both-directions set check (db ICNote rows ==
  yielded ids + row-errored ids: no invented, no silently-dropped note).
- **Media:** every media `FileRef` checked to exist on disk under the study tree.

Result: every decoded body agreed with the independent decoder and with the stored
snippet; every note's metadata, folder, account and attachments matched the store's
own SQL; every media FileRef resolved on disk; the set matched exactly, with zero row
errors. The parser needed no correctness change after the pre-coding introspection.
(iLEAPP is MIT and attributed in `NOTICE`; it credits mac_apt's Notes plugin, also
MIT. `apple_cloud_notes_parser` is a documented black-box manual escalation.)
