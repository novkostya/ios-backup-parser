# ios-backup-parser — progress & decisions

Milestone tracker and decisions log. One milestone per session (see `CLAUDE.md` →
Process). This file is **public and privacy-gated**: it records *what* was decided
and the state of each milestone, never *where* work runs — Operator-private
infrastructure lives outside this repo.

## Milestone states

| Milestone | Scope | State |
| --- | --- | --- |
| **M0** | Schema spike — document the real schemas of the five domains (docs-only) | **Complete** — five domain docs committed; fingerprints `observed` |
| **M1** | Core + `contacts` — `BackupFS`, introspection helpers, capability report | **Complete** — `backup` + `contacts` packages, gates green, differential passed; `contacts.1` **validated** |
| **M2** | `calls` (`CallHistory.storedata`) | **Complete** — `calls` package (first CoreData domain), gates green, differential passed; `calls.1` **validated** |
| **M3** | `messages` — chats / messages / attachments join, typedstream text | **Complete** — `messages` package + `internal/typedstream` decoder, gates green, differential passed; `messages.1` **validated** |
| **M4** | `calendar` (`Calendar.sqlitedb`) | **Complete** — `calendar` package (plain SQLite, join-heavy events + calendars), gates green, differential passed; `calendar.1` **validated** |
| **M5** | `notes` (`NoteStore.sqlite`) — locked notes reported, not decrypted | **Complete** — `notes` package + `internal/applenotes` gzip+protobuf decoder, gates green, differential passed; `notes.1` **validated** |
| **M6** | v0.1 — docs, examples, schema-coverage table, tag | **Complete** — per-domain godoc examples, README schema-coverage table, `CHANGELOG.md`, docs finalized; M5 coverage backfilled; gates green; `v0.1.0` tagged |
| **M7** | `safari` (`Bookmarks.db` + `History.db`) — first post-v0.1 backlog domain | **Complete** — `safari` package (bookmarks / reading list / history over two plain-SQLite stores), gates green, differential passed; `safari.1` **validated** |
| **M8** | `reminders` (`Container_v1/Stores/Data-*.sqlite`) — second post-v0.1 backlog domain | **Complete** — `reminders` package (CoreData, mixed inheritance, multi-store) + additive `backup.ReadDirFS` capability, gates green, differential passed; `reminders.1` **validated** |

## M0 — schema spike (complete)

**Goal.** Against the Operator-local decrypted study backup (with iLEAPP as
cross-reference), document the *actual* schema of each of the five domains for the
one iOS version present, as scrubbed `docs/schemas/<domain>.md` (structure, never
data). Docs-only milestone: zero Go, no `go.mod`, no `tools/` — schema dumps run via
sqlite3 in a pinned toolchain container; any throwaway script lives in session
scratch and dies there.

**Definition of done** (per the 2026-07-20 rulings — see decisions log):

- One baseline **fingerprint per domain** (single-version honesty), documented in a
  per-fingerprint structure so a later-observed version *appends* rather than forces
  a rewrite. Fingerprints from versions we haven't observed are never invented.
- **Every timestamp column** records its epoch + unit (Cocoa-2001 seconds,
  iMessage-2001 *nanoseconds*, Unix, …) — the off-by-31-years / off-by-10⁹ trap is
  the wrong-but-plausible failure this library exists to prevent.
- **Every domain doc names its storage idiom** — plain app SQLite vs CoreData
  (`Z`-tables, `Z_PK`/`Z_ENT` indirection) vs blob-encoded (typedstream,
  gzip+protobuf) — so M2–M5 inherit the join/PK/epoch strategy.
- The **notes** doc keeps the two note wrinkles separate: (a) per-note password
  protection (out of scope, reported-only) vs (b) the routine gzip+protobuf encoding
  of every ordinary note body (in scope — must be decoded or no note has text).
- **Attachment/media reference shape** recorded per domain (structured
  `FileRef{Domain, RelativePath}`, not a bare path).
- **Capability-report design validated against reality**: for each domain, the fields
  the output model wants vs what the observed schema can actually provide — proving
  the `Capability{Domain, Supported, Schema, Missing}` mechanism (esp. `Missing[]`)
  is real and sufficient.

**Current state — spike complete (uncommitted).** The dedicated dev environment is
provisioned; the corrected study backup (ios-backup-crypt v0.1.1, which restored the
content databases) was introspected **read-only, from scratch copies** — originals
never opened. All five domains are documented in [docs/schemas/](schemas/): storage
idiom, tables, joins, per-column timestamp epoch/unit, domain wrinkles, and a
capability mapping each — plus a cross-domain [schema index](schemas/README.md) and a
`NOTICE` recording the iLEAPP (MIT) cross-reference. Every fingerprint is at status
`observed` (real-backup structure confirmed; no parser exists yet; the differential is
the manual gate to reach `validated`). Deliverables are authored but **not
committed** — commit is Operator-gated and runs `make privacy-check` first.

## M1 — core + contacts (complete)

**Goal.** The shared core (`BackupFS`, schema introspection, capability report)
plus the easiest domain, `contacts`, proving the whole shape end to end —
streaming iterators, eager validation, capability degradation, fixtures, gates,
and the operator-local differential.

**Delivered.**

- Root package **`backup`**: the `FS` accessor contract (the charter's
  "BackupFS"), built-in `DirFS` over a reconstructed `<Domain>/<relativePath>`
  tree (Materialize = private mutation-safe copy incl. `-wal`/`-shm`/`-journal`
  sidecars; Close removes scratch; path-escape guards), `FileRef`, `Capability`,
  and the error taxonomy (`ErrUnsupportedSchema` + `UnsupportedSchemaError`
  carrying the observed fingerprint, `ErrUnavailable`, row-scoped `RowError`).
- `internal/introspect` — generic fingerprint detection for all domains:
  required tables/columns + optional units whose absence lands in
  `Capability.Missing`; unknown extra columns never disqualify. `internal/cocoa`
  — per-unit Cocoa-epoch conversion (no magnitude guessing, by design).
  `internal/sqlitedb` — modernc.org/sqlite open helper.
- **`contacts`** domain: `Open` (eager validation), `People()`/`Groups()` as
  `iter.Seq2`, multi-value resolution (phones/emails/URLs/addresses with entry
  fan-out), store/account join, group membership, `CanonicalLabel`. Dangling
  label/entry-key references are row-scoped defects: the person is yielded as a
  `RowError` and the stream continues.
- Testing ladder rungs 1–3: builder-generated synthetic fixture (committed,
  `make fixtures` regenerates), round-trip + committed-fixture + degraded-schema
  + unsupported-schema + row/stream error-scoping tests; containerized gates
  (gofmt / vet / golangci-lint / `go test -race`) green on the project dev CT.
  Coverage: `backup` 81.8%, `contacts` 80.4%, `internal/cocoa` 100%,
  `internal/introspect` 90.7% (debug CLI `cmd/ibp-dump` untested by design).
- **Differential vs iLEAPP passed (2026-07-20)** on the operator-local study
  backup → `contacts.1` is **validated** (see `docs/schemas/contacts.md` for
  the two-phase design and the upstream iLEAPP export quirk it works around).
  Real-backup run: schema detected as supported `contacts.1`, `Missing` =
  `["photo"]` only, zero row errors. The rung-4 iMazing spot-check
  (Operator-manual) is a recommended extra, not a blocker.
- Toolchain scaffolding per house pattern: `versions.env` (all pins looked up
  live 2026-07-20), `deploy/Dockerfile` (Go gate stage + iLEAPP oracle stage),
  Makefile gate/fixture/study targets, `.golangci.yml` mirroring the sibling.

## M2 — call history (complete)

**Goal.** The `calls` domain (`CallHistory.storedata`) — the first **CoreData**
domain, proving the Z-table / `Z_PK` / entity-indirection strategy the later
CoreData domain (notes) inherits — with the same shape as M1: streaming
iterator, eager validation, capability degradation, fixtures, gates, and the
operator-local differential.

**Delivered.**

- **`calls`** domain: `Open` (eager validation), `Calls()` as `iter.Seq2`, the
  `Call`/`Handle` types, and multi-party participant resolution through the
  CoreData many-to-many join (`Z_2REMOTEPARTICIPANTHANDLES` → `ZHANDLE`).
  `Time`/`Duration` from Cocoa **seconds** (`cocoa.FromSecondsFloat`, REAL
  `ZDATE`) and elapsed-seconds `ZDURATION`; `Missed()` derived from
  direction + answered. A dangling participant-handle reference is a row-scoped
  defect: the call is withheld as a `*backup.RowError` and the stream continues.
- **Schema re-confirmed before coding.** A read-only introspection of a scratch
  copy of the real store (originals never opened) pinned the exact CoreData
  names and caught two would-be *wrong-but-plausible* bugs that M0's doc had
  guessed: `ZJUNKIDENTIFICATIONCATEGORY` is **VARCHAR**, not INTEGER (→
  `Call.JunkCategory string`); and the FaceTime `ZCALLTYPE` ordering is **8 =
  video, 16 = audio**, the reverse of M0's "8/16 audio/video" guess (per iLEAPP
  `callHistory.py`).
- Testing ladder rungs 1–3: builder-generated synthetic CoreData fixture
  (committed, `make fixtures` regenerates all packages), round-trip +
  committed-fixture + degraded-schema + unsupported-schema + row/stream
  error-scoping + `Missed()` tests; containerized gates (gofmt / vet /
  golangci-lint / `go test -race`) green on the project dev CT. Coverage:
  `calls` 83.0% (`backup` 81.8%, `contacts` 80.4%, `internal/cocoa` 100%,
  `internal/introspect` 90.7%; debug CLI `cmd/ibp-dump` untested by design).
- **Differential vs iLEAPP passed (2026-07-20)** on the operator-local study
  backup → `calls.1` is **validated**. Two phases both clean: black-box (parser
  stream vs iLEAPP's Call History export) and oracle-logic (parser vs the
  store's own SQL, keyed by `ZCALLRECORD.Z_PK`, every surfaced field including
  participants). Zero row errors; the observed schema carries every optional
  unit (empty `Capability.Missing`).
- `cmd/ibp-dump` gained `-domain calls`; `deploy/diff_calls.py` +
  `dump-study-calls` / `diff-study-calls` Makefile targets added; `NOTICE` and
  `docs/schemas/calls.md` updated in the same change.

## M3 — messages (complete)

**Goal.** The `messages` domain (`sms.db`) — the project's headline domain and its
known hard part: message text on modern iOS lives in a typedstream `attributedBody`
blob, not the `text` column. Same shape as M1/M2 (streaming iterators, eager
validation, capability degradation, fixtures, gates, operator-local differential)
**plus** a from-scratch typedstream decoder.

**Delivered.**

- **`internal/typedstream`** — a recursive-descent decoder for Apple's streamtyped
  NSArchiver format, extracting the plain text of an `NSAttributedString`'s backing
  `NSString`. Written from **public prose format docs only** (Sardegna write-up;
  python-typedstream *docstrings* — LGPL, facts only, bodies unread, no code ported;
  file(1) magic; GNUstep notes); the GPL imessage-exporter / crabstep source is never
  read (black-box oracles only). Unit-tested incl. a **golden test asserting the
  encoder reproduces the real study-backup header bytes**, multi-byte lengths, emoji,
  empty, truncated-errors-not-empty, and a bare-`NSString` root.
- **`messages`** domain: `Open` (eager `messages.1` validation), `Messages()` and
  `Chats()` as `iter.Seq2`; body = `text` when it has real content else the decoded
  `attributedBody`; sender/handle resolution; the `chat_message_join` M:N as
  `Message.ChatIDs`; attachments via `message_attachment_join → attachment` surfaced as
  `backup.FileRef` into `MediaDomain` (NULL filename → absent, never fabricated);
  tapbacks / edits / replies-threads / app-message balloons as capability-gated fields;
  nanosecond timestamps via `cocoa.FromNanoseconds` (the lone nanosecond domain).
  Dangling handle / attachment references are row-scoped defects.
- **Decode-failure contract (Operator amendment).** A sole-source `attributedBody`
  that fails to decode is surfaced with `Message.BodyUndecoded = true` (body *unknown*,
  metadata intact) — never streamed as an empty message. On the real backup this is
  **0** after the decoder was corrected (see below).
- **Schema re-confirmed before coding (the M2 lesson).** Read-only introspection of a
  scratch copy pinned the exact structure and enum spaces: `chat.style` {45 direct,
  43 group}, `associated_message_type` {2000–2007 add / 3000–3006 remove}, `item_type`
  {0–5}, and every optional unit present. Interpretations stay documented-not-asserted;
  the parser surfaces raw codes with range helpers.
- Testing ladder rungs 1–3: builder-generated synthetic `sms.db` fixture embedding
  **real typedstream blobs** (committed; `make fixtures` regenerates), round-trip +
  committed-fixture + degraded-schema + unsupported-schema + row/stream error-scoping +
  chats-unavailable + BodyUndecoded + tapback/group-helper tests; containerized gates
  (gofmt / vet / golangci-lint / `go test -race`) green on the project dev CT.
  Coverage: `messages` 79.6%, `internal/typedstream` 75.6% (`calls` 83.0%,
  `contacts` 80.4%, `internal/introspect` 90.7%, `internal/cocoa` 100%; the debug CLI
  `cmd/ibp-dump` and the fixture/round-trip encoder untested by design).
- **Differential vs iLEAPP passed → `messages.1` validated.** Phase 1 (iLEAPP's SMS
  export, black-box) and phase 2 (oracle-logic: iLEAPP's query semantics + its own
  decoder, python-typedstream, re-run against a scratch copy, keyed by `message.ROWID`)
  cross-checked **every message** on text (incl. every typedstream-decoded body),
  timestamp, service, direction, associated-message type, handle, chat ids and
  attachments, and every chat on participants — **zero mismatches**, with the
  both-directions exact set check (no invented, no silently-dropped message) passing.
  Zero row errors; empty `Capability.Missing` (the observed schema carries every
  optional unit).
- **The differential earned its architecture.** The first real-data run flagged a
  substantial share of message bodies failing to decode; the oracle cross-check proved
  they were a *decoder bug*, not text-less messages — typedstream uses **two
  independent reference tables** (strings vs objects/classes), not one, which the
  observed bytes and python-typedstream's docstrings confirmed. The single-table model
  decoded the short `NSAttributedString` chain by luck but mis-resolved the longer
  `NSMutableAttributedString` superclass chain. After the fix, `body_undecoded` was
  zero across the whole backup.
- `cmd/ibp-dump` gained `-domain messages`; `deploy/diff_messages.py` +
  `dump-study-messages` / `diff-study-messages` Makefile targets added; `NOTICE`,
  `docs/schemas/messages.md` and `README.md` updated in the same change.

## M4 — calendar (complete)

**Goal.** The `calendar` domain (`Calendar.sqlitedb`) — plain app SQLite (EventKit's
own schema) but the most join-heavy domain so far: each event fans out to a calendar
/ account, a location, an organizer, invitees, recurrence rules, alarms and
attachments. Same shape as M1–M3 (streaming iterators, eager validation, capability
degradation, fixtures, gates, operator-local differential).

**Delivered.**

- **`calendar`** domain: `Open` (eager `calendar.1` validation), `Events()` and
  `Calendars()` as `iter.Seq2`. The open handle is `calendar.Reader` (the record
  type `Calendar` — one calendar in the list — takes the natural name). `Events()`
  streams each `CalendarItem` in ROWID order with its children resolved through
  owner-keyed lookups preloaded once per iteration (no per-event N+1); `Calendars()`
  streams the calendar list with its account/store. Timestamps are Cocoa **seconds**
  (`cocoa.FromSecondsFloat`, REAL) — the sentinel/floating-date caveat is documented,
  not mishandled. `Event.Floating()` detects the `_float` timezone sentinel;
  `Location.HasCoordinates()` mirrors iLEAPP's `0,0`-is-absent guard.
- **Schema re-confirmed before coding (the M2/M3 lesson), correcting an M0 guess.**
  Read-only introspection of a scratch copy (originals never opened) pinned the exact
  structure and caught a would-be *wrong-but-plausible* bug: M0 recorded
  `CalendarItem.entity_type` as the event/reminder discriminator, but it is a single
  uniform value across the store — the real events-vs-birthdays split is
  **`calendar_scale`** (events = `IS NOT 'gregorian'`; birthday items, a distinct kind
  with a special date encoding, are excluded). Introspection also pinned the join
  directions (location *forward* via `CalendarItem.location_id`; participants /
  recurrence / alarms / attachments *back* via their `owner_id` columns), that
  `Participant.entity_type` is 7 for invitees / 8 for the organizer, that `Identity`
  has no declared `ROWID` (implicit rowid), and that `Location.latitude`/`longitude`
  hold REAL despite an INTEGER declaration. All cross-referenced against iLEAPP's
  `calendarAll.py` (MIT, attributed in `NOTICE`).
- Testing ladder rungs 1–3: builder-generated synthetic fixture (committed,
  `make fixtures` regenerates) exercising a full event, a floating all-day event, an
  excluded gregorian birthday, a dangling-calendar soft-nil, and a corrupt-`start_date`
  row-scoped defect; round-trip + committed-fixture + `Calendars()` + degraded-schema
  + calendars-unavailable + unsupported-schema + birthday-exclusion/floating +
  row/stream error-scoping tests. Containerized gates (gofmt / vet / golangci-lint /
  `go test -race`) green on the project dev CT. Coverage: `calendar` 83.8%
  (`backup` 81.8%, `calls` 83.0%, `contacts` 80.4%, `messages` 79.6%,
  `internal/typedstream` 75.6%, `internal/introspect` 90.7%, `internal/cocoa` 100%;
  debug CLI `cmd/ibp-dump` untested by design).
- **Differential vs iLEAPP passed → `calendar.1` validated.** Phase 1 (iLEAPP's
  Calendar Events export, black-box) and phase 2 (oracle-logic: `calendarAll.py`'s
  query semantics re-run against a scratch copy, keyed by `CalendarItem.ROWID`)
  cross-checked **every event** on start/end time, timezone, all-day, calendar +
  account, location, organizer, invitees (email + status), conference URL, notes,
  status/availability/privacy, recurrence, alarms and attachments — **zero
  mismatches**, with the both-directions exact set check (no invented, no
  silently-dropped event) passing. Zero row errors; empty `Capability.Missing` (the
  observed schema carries every optional unit). The parser needed **no changes**: the
  first run was clean under the ROWID-exact phase 2. Phase 1 initially flagged a
  handful of events — all traced to the harness keying colliding on `(start, title)`
  for events that legitimately share both (a holiday duplicated across calendars,
  paired train-ticket bookings); the harness was corrected to a full-field multiset
  match (parser untouched), after which both phases are clean.
- `cmd/ibp-dump` gained `-domain calendar` (events + calendars);
  `deploy/diff_calendar.py` + `dump-study-calendar` / `diff-study-calendar` Makefile
  targets added; `NOTICE`, `docs/schemas/calendar.md` and `docs/schemas/README.md`
  updated in the same change. A pre-existing gofmt nit in
  `internal/typedstream/typedstream.go` (committed at M3, flagged by the current
  toolchain image's gofmt) was corrected in passing so the gates are green.

## M5 — notes (complete)

**Goal.** The `notes` domain (`NoteStore.sqlite`) — the second **CoreData** domain and
the first with **single-table inheritance** (every entity in one `ZICCLOUDSYNCINGOBJECT`
table, discriminated by `Z_ENT`), plus the second blob-decode hard part: every ordinary
note's text lives only in a gzip+protobuf `ZICNOTEDATA.ZDATA` blob. Same shape as M1–M4
(streaming iterators, eager validation, capability degradation, fixtures, gates,
operator-local differential) **plus** a from-scratch note-body decoder. Locked notes are
reported, never decrypted.

**Delivered.**

- **`internal/applenotes`** — a from-scratch decoder for the Apple Notes body blob:
  gunzip (stdlib `compress/gzip`) then a minimal recursive-descent protobuf reader that
  walks the fixed note-text path `document(2) → note(3) → note_text(2)` and returns the
  plain text. Written from **public format docs only** (CCL/Ciofeca "Apple Notes"
  write-ups; the protobuf wire spec; the field numbers as used by MIT iLEAPP `notes.py`),
  independently confirmed against instrumented dumps; no GPL source read. Rich runs /
  embedded objects deferred (U+FFFC placeholders kept verbatim). Unit-tested incl. a
  **golden test asserting the encoder reproduces the real body header bytes**
  (`08 00 12` … `08 00 10`), emoji/non-Latin/long round-trips, attachment-only (no
  note_text → empty, not error), and not-gzip / no-document / truncated error paths.
- **`notes`** domain: `Open` (eager `notes.1` validation), `Notes()` and `Folders()` as
  `iter.Seq2`. **Entity ordinals are resolved from the `Z_PRIMARYKEY` map by name at
  Open** (ICNote/ICFolder/ICAccount/ICAttachment/ICMedia), never hard-coded — a model
  that renumbers entities still parses. Body = decoded `ZDATA` (`""` for a blank or
  locked note); a present-but-undecodable blob → `BodyUndecoded` (body unknown, never a
  silent empty). Folder (`ZFOLDER`) and account (`ZACCOUNT7`) resolve soft-nil
  (LEFT-JOIN semantics); attachments = ICAttachment (`ZNOTE`) → ICMedia (`ZATTACHMENT1`),
  media surfaced as a **resolvable `backup.FileRef`**
  (`Accounts/<account>/Media/<id>/<generation>/<filename>`). Cocoa **seconds** timestamps.
  The only row-scoped defect is a scan failure (no collection-integrity withhold, like
  calendar).
- **Schema re-confirmed before coding (the M2/M3/M4 lesson), correcting two M0 guesses.**
  Read-only introspection of a scratch copy (originals never opened) pinned the exact
  single-table-inheritance suffixes and caught *wrong-but-plausible* bugs: a note's
  creation date is **`ZCREATIONDATE3`** (M0 said `ZCREATIONDATE`) and its account is
  **`ZACCOUNT7`** (M0 said generic `ZACCOUNT*`); a note's title is `ZTITLE1` while a
  folder's is `ZTITLE2`; the media path inserts a real `ZGENERATION1` generation
  directory. The `(creation, account)` suffix pair is version-specific — a different pair
  is a different fingerprint (`notes.2`), not a silent degradation. M0's `notes.md`
  corrected in place (structure/interpretation only, no data).
- Testing ladder rungs 1–3: builder-generated synthetic single-table-inheritance fixture
  (committed; `make fixtures` regenerates) embedding **real gzip+protobuf bodies** and
  using **deliberately non-standard `Z_ENT` ordinals** to prove the `Z_PRIMARYKEY`
  resolution; round-trip + committed-fixture + degraded-schema + unsupported-schema +
  Folders-unavailable + locked-reported + BodyUndecoded + blank-note + media-FileRef +
  row/stream error-scoping tests. Containerized gates (gofmt / vet / golangci-lint /
  `go test -race`) green on the project dev CT. Coverage: `notes` 84.0%,
  `internal/applenotes` 85.7% (`backup` 81.8%, `calls` 83.0%, `contacts` 80.4%,
  `messages` 79.6%, `calendar` 83.8%, `internal/typedstream` 75.6%,
  `internal/introspect` 90.7%, `internal/cocoa` 100%; debug CLI `cmd/ibp-dump`
  untested by design).
- **Differential passed → `notes.1` validated.** iLEAPP's own `notes.py` returns **zero
  rows** on the iOS-18 schema (it hard-codes the account join on `ZACCOUNT4`, which moved
  to `ZACCOUNT7` — confirmed by its own `sample_data`), so its export is unusable as a
  phase-1 oracle. The oracle is therefore split, still independent and MIT: iLEAPP's own
  note-body decoder (`get_uncompressed_data` + `process_note_body_blob`, a fixed-offset
  byte-walk) ported into `deploy/diff_notes.py` cross-checks the from-scratch Go decoder
  **blob-for-blob**; Apple's stored `ZSNIPPET` cross-checks every decoded body; iLEAPP's
  column choices re-run against a scratch copy (keyed by ICNote `Z_PK`, both-directions
  set check) cross-check every metadata field, folder, account and attachment; and every
  media `FileRef` is checked to exist on disk. All clean: every decoded body agreed with
  the independent decoder and the stored snippet, every field matched the store, every
  media FileRef resolved, the set matched exactly, zero row errors. The parser needed no
  correctness change after the pre-coding introspection.
- **Locked notes — designed + fixture-tested, real-backup differential deferred.** The
  report-only path (present + `Locked` + `PasswordHint`, body not decrypted, not flagged
  `BodyUndecoded`) is exercised by the fixture and designed from the schema's `ZCRYPTO*`
  columns; confirming it on real data awaits a backup that exercises a locked note
  (recorded as designed-and-fixture-tested, not asserted from study data).
- `cmd/ibp-dump` gained `-domain notes` (note + folder line types);
  `deploy/diff_notes.py` + `dump-study-notes` / `diff-study-notes` Makefile targets added;
  `NOTICE`, `docs/schemas/notes.md`, `docs/schemas/README.md` and `README.md` updated in
  the same change.

## M6 — v0.1 (complete)

**Goal.** The v0.1 release: docs, examples, a schema-coverage table, and the tag.
No new domain and no behavior change — the milestone packages what M1–M5 built into
a coherent first release and hands the tag to the Operator.

**Delivered.**

- **Runnable examples per domain.** A godoc `Example` function in each domain
  package (`example_test.go`, `package <domain>_test`) showing the shape every domain
  shares — `backup.NewDirFS` → `Open` (eager schema validation) → `Capability()` →
  stream with the **row-scoped (`*backup.RowError`, continue) vs stream-scoped (end)**
  switch. `contacts` is the flagship (full error handling + capability print); the
  other four each surface their domain-specific field (`calls` `Missed()`/`Duration`,
  `messages` `Text`/`BodyUndecoded`, `calendar` the `Reader` handle + `AllDay`, `notes`
  `Locked`/`Body`). They are illustrative — no `// Output:`, because they read a real
  backup tree — so the gates **compile and vet** them (via `go vet` / `go test`) without
  executing, the standard pattern for a library that reads external data. Rendered on
  pkg.go.dev.
- **README finalized for v0.1.** Status line updated to "v0.1 — first release"; a
  user-facing **schema-coverage table** added (domain → package → database → storage
  idiom → fingerprint → validated) with a pointer to the per-domain schema reference
  and the `ErrUnsupportedSchema`-fails-loudly contract; an examples pointer linking the
  five `example_test.go` files. The structural reference table stays in
  `docs/schemas/README.md`; both agree all five fingerprints are **validated**.
- **`CHANGELOG.md`** (Keep a Changelog format) with the `0.1.0` entry: core
  (`FS`/`DirFS`/`FileRef`, capability report, error taxonomy, streaming iterators,
  CGO-free) and the five domains, each naming its database, fingerprint, and the
  from-scratch decoders (typedstream, gzip+protobuf). Marked pre-1.0 (minor releases
  may break API until v1).
- **M5 coverage-reporting gap fixed.** The M5 entry omitted the per-package `Coverage:`
  line the house style adopted at M1 and M2–M4 all carry; backfilled from a fresh
  `go test -cover` run: `notes` **84.0%**, `internal/applenotes` **85.7%** (others
  unchanged from M4: `backup` 81.8%, `calls` 83.0%, `contacts` 80.4%, `messages` 79.6%,
  `calendar` 83.8%, `internal/typedstream` 75.6%, `internal/introspect` 90.7%,
  `internal/cocoa` 100%; `cmd/ibp-dump` + `internal/sqlitedb` are untested by design).
- **No API, dependency, or behavior change.** Docs, examples, and a changelog only —
  no non-test Go changed, `go.mod`/`go.sum` unchanged. Containerized gates (gofmt clean
  / `go vet` / golangci-lint **0 issues** / `go test -race` across all packages) green
  on the project dev CT; the five new `example_test.go` files build and vet clean.
- **Tag is Operator-gated (charter: never tag unasked).** The version string is
  `v0.1.0` (per the charter's "M6 — v0.1"); the annotated tag is **not** applied here.
  The diff is delivered to the canonical Mac checkout for the `make privacy-check` gate
  and the Operator's commit + tag — the milestone's final step.

## M7 — safari (complete)

**Goal.** The `safari` domain — the first **post-v0.1 backlog** domain (charter
ordering: Safari first, a clean two-oracle differential). Bookmarks, the reading list
and browsing history, following the M1–M5 house shape (streaming iterators, eager
validation, capability degradation, fixtures, gates, operator-local differential). The
new wrinkle: the domain **spans two plain-SQLite stores** (`Bookmarks.db` +
`History.db`).

**Delivered.**

- **`safari`** domain: `Open` (eager `safari.1` validation on `Bookmarks.db`), three
  `iter.Seq2` streams from one `Reader` (named `Reader`, per the house handle
  convention): `Bookmarks()` (the self-referential bookmark tree — folders vs leaves by
  `Bookmark.IsFolder`, the built-in special folders by `SpecialID`), `ReadingList()`
  (leaf rows under the `com.apple.ReadingList` folder, discriminated by a non-NULL
  `bookmarks.read` column — `0` unread / `1` read), and `History()` (one record per
  `history_visits` row, the page URL and visit count joined from `history_items`,
  redirects as raw visit ids, `origin` raw). Reading-list items are excluded from
  `Bookmarks()`; the two streams partition the table (union == every row), so the
  differential's both-directions set matches iLEAPP's flat bookmarks export.
- **Two databases, one capability report.** `History.db` is an **optional second
  store**: `Open` opens it when present and recognized; absent or unrecognized, it never
  fails `Open` — `History()` yields `backup.ErrUnavailable` and `history` lands in
  `Capability.Missing`. `Bookmarks.db` alone determines the `safari.1` fingerprint.
- **The two-epoch trap — the headline catch of the pre-coding introspection.**
  `Bookmarks.db.last_modified` is **Unix-epoch seconds** while `History.db.visit_time`
  is **Cocoa-2001 seconds** — the two Safari stores disagree on their epoch. Reading
  bookmarks as Cocoa would date them to ~2043–2052 (a plausible-looking but wrong
  future); the magnitude and a cross-check against the reading-list plist `DateAdded`
  (which equals `last_modified`-as-Unix exactly) pinned it. A safari-local `unixFromFloat`
  converts bookmarks (kept out of `internal/cocoa`, which is Cocoa-only by design);
  history uses `cocoa.FromSecondsFloat`.
- **Schema re-confirmed by introspection before coding (the M2–M5 lesson).** Read-only
  introspection of scratch copies (originals never opened) pinned: `bookmarks.type`
  {0 leaf, 1 folder}; `special_id` {1 BookmarksBar, 2 BookmarksMenu, 3
  `com.apple.ReadingList`}; the tree via `parent` (Root is id 0); the reading-list
  discriminator (`read IS NOT NULL`, coinciding exactly with the reading-list folder's
  children); the history join (`history_visits` ⟕ `history_items`, `origin` 0/1,
  redirect self-refs); and the two epochs. Reading-list plist metadata (`DateAdded` /
  `PreviewText` in the `extra_attributes` binary plist) is **deferred** — decoding it
  needs a binary-plist reader; `LastModified` (the add/refresh time) is surfaced from
  the column instead. Inline favicon BLOBs and the separate favicon store are out of
  scope; no `FileRef` is emitted (never-fabricate).
- Testing ladder rungs 1–3: builder-generated synthetic fixtures for **both** stores
  (committed; `make fixtures` regenerates) exercising the tree, special folders, both
  reading-list read states, a redirect chain, the two epochs, and a row-scoped defect
  in each store (corrupt `last_modified` / `visit_time`); round-trip + committed-fixture
  + degraded-schema + reading-list-unavailable + history-unavailable +
  history-unsupported-degrades + unsupported-schema + row/stream error-scoping tests.
  Containerized gates (gofmt / `go vet` / golangci-lint **0 issues** / `go test -race`)
  green on the project dev CT. Coverage: `safari` **86.6%** (`backup` 81.8%,
  `calls` 83.0%, `contacts` 80.4%, `messages` 79.6%, `calendar` 83.8%, `notes` 84.0%,
  `internal/applenotes` 85.7%, `internal/typedstream` 75.6%, `internal/introspect`
  90.7%, `internal/cocoa` 100%; `cmd/ibp-dump` + `internal/sqlitedb` untested by design).
- **Differential vs iLEAPP passed → `safari.1` validated.** Phase 1 (iLEAPP's Safari
  **Bookmarks** and **History** exports, black-box) and phase 2 (its query semantics —
  the flat bookmarks read and the `history_visits` ⟕ `history_items` join with origin /
  redirect resolution — re-run against scratch copies, keyed by `bookmarks.id` and
  `history_visits.id`, both-directions set check) cross-checked **every** bookmark and
  reading-list row on title/url/hidden/tree/special/order/read/timestamp and **every**
  visit on time/title/url/visit_count/redirect/origin — the full history set on the
  study backup. Zero row errors; empty `Capability.Missing` (both stores present, every
  optional unit present). The parser needed **no** correctness change after the
  pre-coding introspection. The **only** phase-1 divergence was a **±1-second rendering
  artifact** on a handful of visits: iLEAPP renders `visit_time` via SQLite
  `datetime(…,'unixepoch')`, which **rounds** the fractional second, while the parser
  keeps the precise sub-second value and truncates on display (the two disagree only for
  a fraction ≥ 0.5). Verified against the raw sub-second values; the parser holds the
  exact value, so `diff_safari.py` phase 1 tolerates ±1s (the calls domain's Julian-day
  precedent) and phase 2 — truncation-identical on both sides — is exact. The harness
  was corrected; the parser was not.
- `cmd/ibp-dump` gained `-domain safari` (bookmark / reading_list / visit line types);
  `deploy/diff_safari.py` + `dump-study-safari` / `diff-study-safari` Makefile targets
  added; `NOTICE`, `docs/schemas/safari.md`, `docs/schemas/README.md`, `README.md` and
  `CHANGELOG.md` updated in the same change.

## M8 — reminders (complete)

**Goal.** The `reminders` domain — the second **post-v0.1 backlog** domain (charter
ordering: safari → **reminders** → voicemail → whatsapp). Reminders moved to their own
store around iOS 13 (the M0 calendar doc recorded `CalendarItem`'s reminder columns as
present-but-unused for exactly this reason), so the first spike task was finding the
real store — **not** `Calendar.sqlitedb`. Same house shape as M1–M7 (streaming
iterators, eager validation, capability degradation, fixtures, gates, operator-local
differential). Two new wrinkles: the domain is **CoreData spanning multiple store
files**, and reading it needs a new **optional FS capability**.

**Delivered.**

- **`reminders`** domain: `Open` (eager `reminders.1` validation on every store),
  `Reminders()` and `Lists()` as `iter.Seq2` from one `Reader`. The store is CoreData
  with **mixed inheritance**: reminders in `ZREMCDREMINDER`, lists in `ZREMCDBASELIST`
  (`REMCDList`/`REMCDSmartList`), and accounts / recurrence rules / assignments /
  sharees as `REMCDObject` subclasses sharing the wide `ZREMCDOBJECT`. Each `Reminder`
  carries title/notes (plain columns — **no blob decode**, unlike messages/notes),
  completion, flag, priority, all-day, created/modified/due/start/completion dates
  (Cocoa **seconds**, `cocoa.FromSecondsFloat`), marked-for-deletion, subtask
  `ParentID`, its `List` and `Account` (soft-nil), and — **surfaced raw,
  documented-to-validate** — `Recurrence` (frequency/interval/count/end via the
  `REMCDRecurrenceRule` back-pointer `ZREMINDER4`) and `Assignee` (best-effort through
  `REMCDAssignment` → `REMCDSharee`). `ZIDENTIFIER` is a 16-byte UUID BLOB, formatted.
  The only row-scoped defect is a scan failure (a corrupt date) → `*backup.RowError`,
  stream continues.
- **Multi-store, with per-store identity.** A backup holds several stores
  (`Container_v1/Stores/Data-<UUID>.sqlite` per account + the fixed-name
  `Data-local.sqlite`). `Open` enumerates and opens **every** matching store; a
  reminder's identity is **(`Store`, `Z_PK`)** — each store has its own `Z_PK`
  sequence. A stream-scoped error in any store terminates the whole iteration; the
  both-directions set check is keyed by (store, id).
- **New additive core capability `backup.ReadDirFS`** (Operator-approved this session
  — see decisions log). The base `FS` (`Materialize`+`Exists`) cannot list a
  directory, but the reminder stores are UUID-named and recorded in no manifest.
  `ReadDirFS` (a new *optional* interface mirroring `io/fs.ReadDirFS`; `DirFS`
  implements it, read-only) lets the domain enumerate them. A host lacking it reads
  only `Data-local.sqlite` and reports **`cloudkit_stores`** in `Capability.Missing` —
  honest degradation, never a silent partial read.
- **Z_ENT ordinals resolved per store from `Z_PRIMARYKEY`, never hard-coded** — the
  M5 lesson, and it bit for real: the study's `Data-local.sqlite` renumbers
  `REMCDAccount`/`REMCDRecurrenceRule`/`REMCDAssignment`/`REMCDSharee` relative to the
  CloudKit stores (a grocery entity inserted mid-map). A hard-coded or store-shared
  ordinal would mis-read one store. The committed fixture uses **two stores with
  different non-standard ordinals** and a **reused `Z_PK` across stores** to prove
  both per-store resolution and (store, id) namespacing.
- **Schema re-confirmed by introspection before coding (the M2–M7 lesson).**
  Read-only introspection of scratch copies (originals never opened) pinned the store
  layout, the reminder/list/account tables and joins, the recurrence/assignment
  back-pointers (`ZREMINDER4`/`ZREMINDER1`), the 16-byte `ZIDENTIFIER`, and — by
  magnitude — that all six reminder date columns are Cocoa-2001 seconds (undated =
  NULL, no floating sentinel). It also caught that iLEAPP's oracle is stale (below).
- Testing ladder rungs 1–3: builder-generated synthetic **two-store** fixture
  (committed; `make fixtures` regenerates) with renumbered per-store ordinals, a
  reused cross-store `Z_PK`, recurrence, assignment, a subtask, all-day, and a
  row-scoped defect; round-trip + committed-fixture + capability + `Lists()` +
  store-namespacing + recurrence/assignment + no-`ReadDirFS`-fallback + degraded-schema
  + unsupported-schema + row/stream error-scoping tests, plus a `backup` `ReadDir`
  test (listing, `fs.ErrNotExist`, path-escape guards). Containerized gates (gofmt /
  `go vet` / golangci-lint **0 issues** / `go test -race`) green on the project dev CT.
  Coverage: `reminders` **81.2%**, `backup` **83.9%** (up from 81.8% with `ReadDir`);
  others unchanged (`calls` 83.0%, `contacts` 80.4%, `messages` 79.6%, `calendar`
  83.8%, `notes` 84.0%, `safari` 86.6%, `internal/applenotes` 85.7%,
  `internal/typedstream` 75.6%, `internal/introspect` 90.7%, `internal/cocoa` 100%;
  `cmd/ibp-dump` + `internal/sqlitedb` untested by design).
- **Differential passed → `reminders.1` validated.** iLEAPP's `reminders.py` is stale
  on the iOS-18 schema — it queries `ZREMCDOBJECT.ZTITLE1` and guards on
  `ZREMCDOBJECT.ZLASTMODIFIEDDATE`, but reminders live in `ZREMCDREMINDER` (title
  `ZTITLE`) and `ZREMCDOBJECT` has no `ZLASTMODIFIEDDATE`, so it returns **zero**
  reminders (the same notes-class staleness; confirmed by reading its source and by an
  empirical run producing no reminders output). So the oracle is **split**: iLEAPP's
  confirmed store glob and Cocoa epoch (MIT, attributed) plus, as the authoritative
  cross-check, `deploy/diff_reminders.py`'s own SQL re-run against a scratch copy of
  **every** store, resolving ordinals per store and keyed by (store,
  `ZREMCDREMINDER.Z_PK`) with a both-directions set check. It cross-checked **every**
  reminder and list across **every** store on title, notes, completion, flagged,
  priority, all-day, every timestamp, marked-for-deletion, parent, identifier, list,
  account, recurrence (frequency/interval) and assignee — **zero mismatches**,
  both-directions set clean, zero row errors, empty `Capability.Missing`.
  The parser needed **no** correctness change after the pre-coding introspection.
- `cmd/ibp-dump` gained `-domain reminders` (reminder + list line types);
  `deploy/diff_reminders.py` + `dump-study-reminders` / `diff-study-reminders` Makefile
  targets added; `NOTICE`, `docs/schemas/reminders.md`, `docs/schemas/README.md`,
  `README.md` and `CHANGELOG.md` (`[Unreleased]`) updated in the same change. The
  release stays uncut — the version bump and tag are the Operator's.

## Decisions log

Append-only. Adjudicated canon carries a date; in-milestone gap-decisions cite the
milestone (per the Process gap rule). Charter rulings are cross-referenced here, not
restated in full.

- **2026-07-20 — M0 onboarding review adjudicated.** The charter absorbed the review
  outcomes (all marked "ruled 2026-07-20" in `CLAUDE.md`): `Materialize` returns a
  mutation-safe scratch copy and never hands out a path into the original backup;
  never-mutate-input is a hard rule; validation is eager (schema check +
  `ErrUnsupportedSchema` at open) with row-scoped vs stream-scoped iterator errors
  kept distinct; attachments surface as a structured `FileRef`; **fingerprint
  identity is the introspected structure**, and the `Schema` label is a
  discovery-order ordinal (`sms.1`, …), never a version-shaped name; per-column
  timestamp epoch+unit and per-domain storage idiom are required M0 outputs;
  single-version honesty defines M0-done; the license rule's operational form routes
  oracle disagreements through write-ups / format docs / own instrumented dumps —
  never the GPL source; the privacy gate sees staged *content* only (commit-message
  and branch-name hygiene are manual; diff-header matching is intentional); the two
  notes wrinkles stay separate; M0 is docs-only.
- **2026-07-20 — one project, one dev host.** After a shared-box contention incident,
  each sibling library runs its gates on its own dedicated dev host; another project's
  box is never used. ios-backup-parser is provisioned its own at M0 go. (Host
  identity and provisioning are Operator-private, outside this repo.)
- **2026-07-20 — M0 paused: study input lacks the content databases (routed
  upstream).** The dedicated dev environment was provisioned and the decrypted study
  backup mounted read-only, but the tree contains none of the five domains' primary
  databases (`AddressBook.sqlitedb`, `CallHistory.storedata`, `sms.db`,
  `Calendar.sqlitedb`, `NoteStore.sqlite`) — nor Photos/Health/Safari content stores —
  while class-None files (app caches, thumbnails, media, preference plists) are
  present. The absence tracks iOS Data Protection class, i.e. the input's
  higher-protection-class files were not decrypted/reconstructed. Verified read-only
  and tree-wide (exact-name + by-extension); no database content was read (there was
  none). M0 cannot proceed without inventing schemas, which the honesty rules forbid,
  so it is **paused** and the gap is **routed to `ios-backup-crypt`** for a corrected
  study tree. No repo behavior changed; nothing committed.
- **2026-07-20 — M0 unblocked and completed.** `ios-backup-crypt` v0.1.1 fixed the
  real cause (live DBs captured mid-write during Wi-Fi backup: size-aware PKCS#7
  padding strip + lossless partial extraction), restoring all content databases; the
  corrected tree was introspected. Five domain schemas documented, all fingerprints
  `observed`. Storage idioms confirmed — plain SQLite: contacts, messages, calendar;
  CoreData: calls, notes; blob payloads: messages (typedstream `attributedBody`),
  notes (gzip+protobuf `ZDATA`). **Timestamp epochs verified per column: all Cocoa
  2001, but `messages` is in NANOseconds while the other four are seconds** — the
  cross-domain unit trap, now documented. Notes: the locked-note report path is
  documented from the schema and **awaiting differential validation** (needs a backup
  that exercises a locked note) — recorded as designed-not-validated, not asserted
  from data.
  Messages interpretations cross-referenced against iLEAPP `sms.py` (MIT, attributed
  in `NOTICE`); imessage-exporter (GPL) untouched. The `Capability{Domain, Supported,
  Schema, Missing}` shape was validated against all five observed schemas — it holds;
  no change proposed.
- **Forward note (M1, not an M0 decision).** Whether `Missing[]` should also express
  "present-but-partial / capability-gated" (e.g. typedstream rich runs, tapbacks,
  edits) versus only "column absent in this fingerprint" is an API-design question for
  M1 (M0 designs no API). Recorded for M1; not proposed here.
- **M1 — package naming and the final `BackupFS` shape.** Root package is `backup`
  (import `github.com/novkostya/ios-backup-parser`), interface `backup.FS`: the
  sibling's root package took `iosbackup`, `backup.BackupFS` would stutter, and
  `backup` + `iosbackup` coexist in a host without import aliases. `DirFS` is the
  built-in implementation; hot `-journal` sidecars are materialized alongside
  `-wal`/`-shm` (same mutation-safety reason). Charter's Input boundary updated in
  the same change.
- **M1 — `Missing[]` semantics (answers the M0 forward note).** `Missing[]` expresses
  schema absence only: optional-unit tables/columns not present in this database,
  plus fields a fingerprint can never provide (out-of-scope sources, e.g. contacts
  `photo`). "Present-but-partial / capability-gated" is a different axis and gets its
  own additive `Capability` field when the first domain needs it (M3); the ruled
  four-field shape is unchanged today.
- **M1 — fingerprint matcher semantics.** A fingerprint matches when its *required*
  tables/columns are present; unknown extra columns never disqualify; each absent
  *optional unit* puts its field name in `Missing[]`. The documented observed
  structure remains the identity/evidence; the requirement set is the operational
  test (recorded in `docs/schemas/README.md`).
- **M1 — error-contract concretes.** Row-scoped = `*backup.RowError` (stream
  continues); any other yielded error is stream-scoped (stream ends); a stream whose
  data the schema cannot provide yields `backup.ErrUnavailable` instead of reading
  empty. Dangling label / entry-key references are row-scoped defects — the affected
  person is withheld (never silently degraded) and the stream continues.
- **M1 — contacts interpretation sources.** ABMultiValue property constants
  (3 phone / 4 email / 5 address / 22 URL; 13 is *instant message* — correcting M0's
  "birthday-ish" guess) and the `_$!<Label>!$_` wrapper cross-referenced from
  iLEAPP's `addressBook.py` (MIT, attributed in NOTICE) and confirmed by the passing
  differential. Non-scope multi-value kinds (12 dates, 13 IM, 23 related names,
  46 profiles) are deliberately not surfaced in M1; `Birthday` is exposed verbatim
  (TEXT) — the multi-value date kind is deferred until differential evidence pins its
  constant.
- **M1 — fixture policy.** The builder lives in in-package test code
  (`contacts/fixture_test.go`); `make fixtures` writes the committed binary fixture
  (`contacts/testdata/`), and a dedicated test parses the *committed* artifact so
  green CI covers the checked-in bytes, not just the in-memory builder. Fixture DDL
  mirrors observed structure (tables/columns only); all data invented.
- **M1 — differential harness shape (the Architect's input-type risk, confirmed
  live).** iLEAPP's addressBook artifact globs `*/mobile/Library/AddressBook/…`, so
  fs-mode over a backup-domain tree runs many artifacts but never contacts;
  `diff-study` therefore stages scratch copies into a `/private/var/mobile/…` shim.
  Two phases: (1) black-box — run iLEAPP, compare its TSV on the fields its export
  carries; (2) oracle-logic — iLEAPP's query semantics re-run against a scratch copy,
  keyed by ROWID, covering all fields (needed because iLEAPP v2026.1.0's
  empty-column-removal is off by one and drops non-empty Last Name/Company columns —
  upstream-reportable). The stronger escalation (`-t itunes` vs the original
  encrypted backup, decrypt-pipeline-independent) is documented in the Makefile as a
  manual step needing Operator-held material.
- **M1 — pins (looked up live 2026-07-20).** Go 1.26.5 (go.dev/dl), golangci-lint
  v2.12.2, modernc.org/sqlite v1.54.0 (proxy.golang.org), iLEAPP v2026.1.0 (GitHub
  releases; not on PyPI — the oracle image clones the tag). Oracle Python is 3.12,
  not 3.13: iLEAPP pins numpy 1.26.x which ships no cp313 wheels (verified failing);
  the oracle's floor wins over newest-stable. Consumer floor `go 1.25.0`
  (modernc.org/sqlite requirement), matching the sibling.
- **M1 — coverage declaration adopted** (quince-program house style): per-package
  `go test -cover` summaries recorded in the milestone entry from this milestone on.
- **M2 — CoreData strategy (first CoreData domain).** Entities live in per-entity
  `Z`-tables; `Z_PK` is a real declared column (`INTEGER PRIMARY KEY`), so it is a
  required column by name (unlike AddressBook's implicit `ROWID`, which the matcher
  special-cases). No `Z_ENT` filtering is needed for `ZCALLRECORD` (each entity has
  its own table — not the single-table inheritance notes will use). This is the join/
  PK/epoch template M5 (notes) inherits.
- **M2 — the participant join is an Optional unit keyed on exact CoreData names.**
  CoreData's many-to-many join table name and columns embed the entities' `Z_ENT`
  ordinals (`Z_2REMOTEPARTICIPANTHANDLES`: 2 = CallRecord, 4 = Handle). Those
  ordinals are a per-model fact; a future model that renumbers is a *different*
  fingerprint (`calls.2`), not a silent degradation — so the join is Optional
  (`participants` in `Missing[]`) matched on the exact observed names, never inferred.
- **M2 — schema re-confirmed by introspection before coding; two M0 guesses
  corrected.** Read-only introspection of a scratch copy pinned the real names and
  types, catching bugs the honesty rules exist to prevent: (a)
  `ZJUNKIDENTIFICATIONCATEGORY` is **VARCHAR** (→ `JunkCategory string`), not the
  INTEGER M0 implied by lumping it with `ZJUNKCONFIDENCE`; (b) FaceTime `ZCALLTYPE`
  is **8 = video / 16 = audio**, the reverse of M0's guessed ordering. Both
  interpretations (and `ZORIGINATED` 0/1) are cross-referenced from iLEAPP
  `callHistory.py` (MIT, attributed) and confirmed by the passing differential. M0's
  `calls.md` was corrected in place (structure/interpretation only, no data).
- **M2 — canonical store only; `CallHistoryTemp.storedata` out of scope.** The parser
  reads `CallHistory.storedata`. The temp buffer of not-yet-migrated recent calls
  would need two-database merge and `Z_PK` namespacing across stores; deferred as a
  forward note (additive later), not a blocker. iLEAPP merges both, so the
  differential requires parser records ⊆ iLEAPP records and treats iLEAPP-only rows
  as the expected temp delta; the phase-2 `Z_PK` cross-check on the canonical store is
  the exact gate. No public API implication.
- **M2 — no optional *stream*; degradation is field-level.** Unlike contacts
  (`Groups()` yields `ErrUnavailable` when its tables are absent), calls has a single
  stream (`Calls()`); optional data (participants, name, spam, …) are *fields*, so
  their absence degrades to zero-value + `Missing[]`, exactly as contacts' optional
  scalar columns do. `ErrUnavailable` remains reserved for a whole unavailable stream
  — none exists in calls. The ruled `Capability` four-field shape is unchanged.
- **M2 — `Duration` as `time.Duration` from `FLOAT` seconds; differential tolerance.**
  `Call.Duration` keeps the full fractional `ZDURATION`. iLEAPP renders duration via
  `strftime('%H:%M:%S', …)`, which is second-granular and subject to Julian-day float
  rounding (±1s), so `diff_calls.py` phase 1 floors and tolerates ±1s; phase 2 checks
  duration exactly against the raw `ZDURATION`. The parser value is the precise one.
- **M2 — pins.** No new module dependencies (calls uses only stdlib + the M1 internal
  packages); `go.mod`/`go.sum` unchanged. Same toolchain + iLEAPP oracle pins as M1
  (`callHistory.py` ships in the same iLEAPP v2026.1.0 image). `make fixtures` now
  regenerates every package's committed fixture (`./...`).
- **CI — GitHub Actions added (2026-07-20).** `.github/workflows/gates.yml`: on
  push-to-`main` and pull requests, a single `make gates` step on `ubuntu-latest` —
  the Makefile's "CI calls only these targets, no logic in YAML", so CI and the dev
  host compile in the identical pinned toolchain container (docker autodetected on the
  runner). `actions/checkout` pinned to **v7** (node24 runtime) — looked up live per
  the pins rule; older majors emit deprecated-Node warnings.
- **M3 — Capability stays four fields (Operator ruling).** The "present-but-partial"
  field the M1 log scheduled for "when the first domain needs it (M3)" is **not** added:
  plain text from typedstream is *complete*, so nothing is partial data. Rich runs
  (mentions/formatting) are deferred for v0.1 and documented, not modeled as a
  capability; per-record app-message/tapback signals are `Message` fields. `Capability`
  remains `{Domain, Supported, Schema, Missing}`.
- **M3 — full validation this session (Operator ruling).** Re-introspect the real
  `sms.db`, drive gates on the dev CT, and run the differential → target `messages.1`
  validated (achieved).
- **M3 — typedstream two-table reference model (the decoder trap, confirmed on real
  data).** streamtyped uses TWO independent shared-reference tables — one for strings
  (type-encodings, class names), one for objects/classes — each numbered from 0x92; a
  back-reference indexes the table matching its context, and an object's number is
  assigned before its class. A single combined table decodes `NSAttributedString →
  NSObject` by luck but mis-resolves the longer `NSMutableAttributedString` chain
  (~16% of real bodies). Caught by the differential (parser-fail vs oracle-success),
  root-caused via python-typedstream's docstrings (LGPL — facts only). The decoder is
  text-only (backing string); attribute runs are deferred.
- **M3 — decode-failure semantics (Operator amendment).** A sole-source
  `attributedBody` that fails to decode → `Message.BodyUndecoded = true` (body unknown,
  record still yielded), never a silent empty message. U+FFFC (attachment-position
  placeholder) is stripped from extracted text.
- **M3 — differential both-directions + named asymmetries (Operator amendment).** The
  harness asserts parser *misses* as well as inventions (exact ROWID-set equality: db
  rows == yielded ids + row-errored ids). Named asymmetries: iLEAPP's LEFT-JOIN row
  expansion (regrouped by Message Row ID) and U+FFFC (normalized on both sides — iLEAPP
  does not strip it). Oracle = iLEAPP: phase 1 black-box SMS export, phase 2 its query
  semantics + its own decoder (python-typedstream) re-run against a scratch copy — an
  independent decoder validating our from-scratch Go one.
- **M3 — iteration model.** `Messages()` flat in (date, ROWID) order + a `Chats()`
  stream with resolved participants; `Message.ChatIDs []int64` for the
  `chat_message_join` M:N. Plain text is the M3 typedstream deliverable.
- **M3 — fingerprint shape.** `Required` minimal (`message` anchor + guid/text/date/
  is_from_me/handle_id). Everything degradable is an Optional unit → `Missing[]`
  (`attributed_text`, `service`, `delivery`, `tapbacks`, `tapback_emoji`, `edits`,
  `threads`, `app_messages`, `group_events`, `handles`, `chats`, `attachments`);
  `attributedBody` is Optional (honest degrade to text-only), not Required. iOS-18-era
  columns the parser does not surface (satellite/off-grid, key-transparency,
  scheduled-send) are deliberately NOT units — a `Missing[]` name must map to a record
  field the parser provides.
- **M3 — imessage-exporter oracle: documented manual escalation.** iLEAPP is the
  containerized default oracle and already supplies an independent typedstream decoder
  (python-typedstream), so imessage-exporter (GPL, black-box only, source never read)
  is documented as the stronger manual cross-check in the `diff-study-messages`
  Makefile target rather than wired into the default image (a Rust build is heavy) —
  mirroring M1's documented `-t itunes` escalation.
- **M3 — pins.** No new Go module dependencies (`internal/typedstream` is stdlib-only;
  `messages` uses stdlib + the internal packages); `go.mod`/`go.sum` unchanged. Same
  toolchain + iLEAPP oracle pins as M1/M2 (`sms.py` ships in the same iLEAPP image).
- **M4 — events/birthdays discriminator is `calendar_scale`, not `entity_type`
  (M0 guess corrected by introspection).** M0's `calendar.md` recorded
  `CalendarItem.entity_type` as the event/reminder discriminator; the real store has a
  single uniform `entity_type` (reminders live in a separate store, absent here). The
  events set is `calendar_scale IS NOT 'gregorian'`; the `'gregorian'` rows are
  **birthday items**, a distinct kind whose `start_date` uses a special encoding
  (iLEAPP decodes them in a separate artifact). `Events()` streams the former and
  **excludes** the latter — matching iLEAPP's `calendarAll.py` split and avoiding the
  wrong-but-plausible birthday-date trap. `entity_type` is surfaced raw (its own
  optional unit). A birthday reader is deferred (forward note). `docs/schemas/calendar.md`
  corrected in place (structure/interpretation only, no data).
- **M4 — domain handle named `Reader`; two streams.** The natural domain-noun handle
  (`Calendar`) collides with the record type `Calendar` (one calendar in the list,
  embedded as `Event.Calendar`), which deserves the natural name; the open handle is
  therefore `calendar.Reader` (idiomatic Go). The domain exposes `Events()` (the
  primary stream) and `Calendars()` (the calendar list, yielding `ErrUnavailable` when
  the calendar tables are absent), mirroring the two-stream shape of contacts
  (People/Groups) and messages (Messages/Chats).
- **M4 — descriptive references are soft-nil (LEFT-JOIN), matching the oracle.**
  Calendar/location/organizer resolve to nil when their id does not resolve (never
  withholding the event), exactly as iLEAPP LEFT-JOINs them; withholding would fail the
  differential's both-directions set check. Unlike calls/messages, calendar has no
  collection-integrity withhold case (children are gathered from the child side by
  `owner_id`, so a dangling reference cannot silently shrink a set); the row-scoped
  defect path is the scan-failure case (e.g. a corrupt `start_date`) → `*backup.RowError`,
  stream continues. Children are preloaded into owner-keyed maps once per iteration
  (no per-event N+1), the same bounded-lookup pattern as contacts' `loadLookups`.
- **M4 — calendar attachments surface metadata, no `FileRef` (never-fabricate hard
  rule).** On the observed schema calendar attachments are server-side references
  (`AttachmentFile.local_path` NULL; `url` a mail content-id), so the file is not in
  the backup; emitting a `backup.FileRef` would fabricate a path, which the charter
  forbids. `calendar.Attachment` surfaces filename/size/uuid/url/local_path verbatim
  and emits no FileRef. This satisfies the charter's structured-reference intent (no
  bare path that lost its domain) while honoring never-fabricate; a validated
  backup-path convention (and a FileRef) is deferred until a backup exercises a
  downloaded calendar attachment. `ExceptionDate` (recurrence exceptions) is also
  deferred — iLEAPP does not surface it and the recurrence rule stands without it.
- **M4 — enum interpretation sources.** `Participant.status` (0/7 no response,
  1 accepted, 2 declined, 3 maybe), `Participant.entity_type` (7 invitee / 8 organizer)
  and `Calendar.sharing_status` (0/1/2) are cross-referenced from iLEAPP `calendarAll.py`
  (MIT, attributed) and validated differentially. `CalendarItem.status` / `availability`
  / `privacy_level`, `Participant.role` / `type`, `Recurrence.frequency`, and
  `Alarm.type` / `proximity` are surfaced raw (no MIT oracle, no differential coverage of
  the mapping) — the same raw-code discipline as calls' `Handle.Type` and contacts'
  `GroupMember.Type`.
- **M4 — differential harness: full-field phase 1, ROWID-exact phase 2.** Phase 2
  (iLEAPP's query semantics re-run against a scratch copy, keyed by `CalendarItem.ROWID`,
  both-directions set check) is the authoritative gate and was clean on the first parser
  run. Phase 1 (iLEAPP's Calendar Events export) initially mispaired a few events because
  distinct events can share `(start, title)` — a holiday duplicated across calendars,
  paired train-ticket bookings — so the harness was corrected to match on the **full**
  compared-field tuple (a coarse key invents mismatches; the parser was never wrong). No
  real conference detected-vs-stored divergence surfaced (full-field phase 1 is clean).
- **M4 — pins.** No new Go module dependencies (`calendar` uses stdlib + the internal
  packages); `go.mod`/`go.sum` unchanged. Same toolchain + iLEAPP oracle pins as M1–M3
  (`calendarAll.py` ships in the same iLEAPP v2026.1.0 image). A pre-existing gofmt nit
  in `internal/typedstream/typedstream.go` (committed at M3) that the current toolchain
  image's gofmt flags was corrected in passing to keep the gates green.
- **M5 — single-table inheritance: entity ordinals resolved at run time, not assumed.**
  Every Notes entity lives in `ZICCLOUDSYNCINGOBJECT` discriminated by `Z_ENT`; those
  ordinals (ICNote 12, ICFolder 15, ICAccount 14, ICAttachment 5, ICMedia 11 on this
  model) are per-model facts. The parser reads the `Z_PRIMARYKEY` entity map at Open and
  looks entities up **by name**, never hard-coding `Z_ENT = 12` — the same detect-never-
  assume rule the charter applies to schemas. The synthetic fixture uses deliberately
  renumbered ordinals to prove the resolution. This is the single-table-inheritance
  template a future CoreData single-table domain inherits (distinct from calls' one-table-
  per-entity model).
- **M5 — column-suffix fingerprint; two M0 guesses corrected by introspection.** Under
  single-table inheritance CoreData suffixes colliding attributes, and the suffix a note
  uses is version-specific. Introspection corrected M0: creation = `ZCREATIONDATE3` (not
  `ZCREATIONDATE`), account = `ZACCOUNT7` (not generic `ZACCOUNT*`); note title `ZTITLE1`,
  folder title `ZTITLE2`. `notes.1`'s Required is minimal (`ZICCLOUDSYNCINGOBJECT`
  Z_PK/Z_ENT/ZIDENTIFIER, `Z_PRIMARYKEY` Z_ENT/Z_NAME, `ZICNOTEDATA` ZNOTE/ZDATA — the
  body source, the headline deliverable); everything else is an Optional unit. A store
  with a different `(creation, account)` suffix pair (the `ZCREATIONDATE1`/`ZACCOUNT2-3`
  layout iLEAPP branches on) is a different fingerprint (`notes.2`), not invented now.
- **M5 — the note body is gzip+protobuf; decoder is from-scratch and text-only.**
  `ZICNOTEDATA.ZDATA` is gzip of a "Note Store proto"; the text is at the fixed path
  `document(2) → note(3) → note_text(2)`. `internal/applenotes` gunzips then walks that
  path with a minimal protobuf reader, returning `note_text` verbatim (U+FFFC embedded-
  object placeholders kept; rich runs deferred, documented). Implemented from public
  format docs + the MIT iLEAPP field numbers + own instrumented dumps; no GPL source
  read. A present-but-undecodable blob → `Message`-style `BodyUndecoded` (body unknown),
  a blank/NULL blob → empty body, a locked note → empty body (not decoded, not flagged
  undecoded).
- **M5 — media FileRef is resolvable and validated, not fabricated.** The media path is
  `Accounts/<ICAccount.ZIDENTIFIER>/Media/<ICMedia.ZIDENTIFIER>/<ICMedia.ZGENERATION1>/<ICMedia.ZFILENAME>`;
  `ZGENERATION1` (the `1_<uuid>` generation dir) is a real column, so every emitted
  `FileRef` resolves to a real file (confirmed on disk in the differential) — unlike M4
  calendar, where the file was absent and the FileRef was deferred. A non-media
  attachment (table/drawing/link) surfaces metadata only, no FileRef (never-fabricate).
- **M5 — locked notes report-only (charter v0.1); real-data validation deferred.** A
  password-protected note is yielded present with `Locked` + `PasswordHint`, body empty,
  never decrypted and never `BodyUndecoded`. Exercised by the fixture and designed from
  the `ZCRYPTO*` schema; a real-backup differential of a locked note awaits a backup that
  exercises one.
- **M5 — differential: iLEAPP's notes export is broken on iOS 18, so the oracle is
  split.** iLEAPP `notes.py` hard-codes the account INNER JOIN on `ZACCOUNT4` and returns
  zero note rows on the iOS-17/18 schema (its own `sample_data` records "0 rows"). So the
  oracle is: iLEAPP's own note-body decoder (`get_uncompressed_data` +
  `process_note_body_blob`) ported into `deploy/diff_notes.py` as an independent
  blob-for-blob decoder check; Apple's stored `ZSNIPPET` as a second body oracle; iLEAPP's
  column choices re-run against a scratch copy (ICNote `Z_PK`-keyed, both-directions set
  check) for metadata/folder/account/attachments; and on-disk existence for every media
  FileRef. `apple_cloud_notes_parser` is documented as a black-box manual escalation. All
  clean on first run after the pre-coding introspection.
- **M5 — pins.** No new Go module dependencies (`internal/applenotes` is stdlib-only;
  `notes` uses stdlib + the internal packages); `go.mod`/`go.sum` unchanged. Same
  toolchain + iLEAPP oracle pins as M1–M4 (`notes.py` ships in the same iLEAPP v2026.1.0
  image; it credits mac_apt's Notes plugin, MIT — attributed in `NOTICE`).
- **M6 — examples are godoc `Example` functions, not a standalone program
  (in-milestone gap decision).** A package-level `Example` per domain
  (`<domain>/example_test.go`, `package <domain>_test`) is idiomatic Go, renders on
  pkg.go.dev, and is compiled + vet-checked by the existing gates — so it cannot rot
  against the API the way a separate `examples/` main could. It also honors the
  charter's "no CLI product": `cmd/ibp-dump` stays the only `main` package (a debug
  tool, not the deliverable). The examples are illustrative (no `// Output:`) because
  they open a real `<Domain>/<relativePath>` backup tree — the standard library uses
  the same non-executing form for examples that need external resources.
- **M6 — schema-coverage table lives in the README (user-facing); the structural
  reference stays in `docs/schemas/README.md`.** The README table is the front-door
  support matrix (domain → package → database → idiom → fingerprint → validated); the
  schemas doc remains the tables/joins/epochs reference. Both assert all five
  fingerprints **validated** — no new status, no re-validation (M6 changes no parser).
- **M6 — M5 coverage line backfilled (house-style regression, Operator-flagged).** The
  M1 "coverage declaration adopted" house style requires a per-package `Coverage:` line
  per milestone entry; M5's entry lacked it. Backfilled from a fresh `go test -cover`
  (`notes` 84.0%, `internal/applenotes` 85.7%; the rest unchanged). No code change —
  a documentation correction to a completed milestone.
- **M6 — tag is Operator-gated; version `v0.1.0`.** The charter forbids tagging or
  pushing unasked, so M6 prepares the release (CHANGELOG, docs, examples) and the
  Operator applies the annotated `v0.1.0` tag after the privacy gate + commit. The
  version string follows the charter's "M6 — v0.1"; SemVer pre-1.0 semantics
  (minor releases may break API until v1.0.0) are stated in the README and CHANGELOG.
- **M6 — pins.** No new Go module dependencies (examples, docs, and a changelog only;
  no non-test Go changed); `go.mod`/`go.sum` unchanged. Same toolchain pins as M1–M5.
- **M7 — a domain spanning two databases (in-milestone gap decision).** Unlike M1–M5
  (one domain = one database), safari reads `Bookmarks.db` (bookmarks + reading list)
  **and** `History.db` (history). One `Reader` holds both handles and exposes three
  streams (`Bookmarks`, `ReadingList`, `History`). Only `Bookmarks.db` determines the
  `safari.1` fingerprint; `History.db` is an **optional cross-file unit** — introspected
  separately (`historySpec`), and any failure to open/recognize it degrades `History()`
  to `ErrUnavailable` (`history` in `Missing`) rather than failing `Open`. `history` is
  therefore added to `Capability.Missing` at Open (not by the `Bookmarks.db` introspect
  pass, which cannot see it). No public-API shape change: it is the same
  `{Domain, Supported, Schema, Missing}` capability and the same two-stream-plus-optional
  pattern as calendar (Events/Calendars), generalized to three streams.
- **M7 — the two-epoch trap: Bookmarks is Unix, History is Cocoa (pinned by
  introspection, the wrong-but-plausible bug this domain must not ship).** Read-only
  introspection of scratch copies showed `bookmarks.last_modified` (REAL) lands in
  2012–2021 as Unix seconds (2043–2052 as Cocoa — an impossible future for a modified
  date) and equals the reading-list plist `DateAdded` exactly when read as Unix, while
  `history_visits.visit_time` (REAL) lands in 2026 as Cocoa (1995 as Unix — before
  Safari existed) and matches iLEAPP's `datetime(visit_time + 978307200, 'unixepoch')`.
  So the two Safari stores use **different epochs**. History uses `cocoa.FromSecondsFloat`;
  bookmarks uses a safari-local `unixFromFloat` — deliberately **not** added to
  `internal/cocoa`, whose charter (per its package doc) is Cocoa-epoch only. Documented
  prominently in `docs/schemas/safari.md` and the cross-domain timestamp table.
- **M7 — reading-list discriminator is `read IS NOT NULL`; the two streams partition
  the table.** A reading-list item is a leaf (`type = 0`) hanging off the `special_id = 3`
  `com.apple.ReadingList` folder; the per-row marker is a non-NULL `bookmarks.read`
  column (0 unread / 1 read), which on the observed schema coincides exactly with
  folder membership. `Bookmarks()` streams `read IS NULL`, `ReadingList()` streams
  `read IS NOT NULL`; their union is every row, matching iLEAPP's flat
  `SELECT title, url, hidden FROM bookmarks` export (which does not distinguish reading
  list) for the both-directions set check. Absent the `read` column (`reading_list` in
  `Missing`), `ReadingList()` is `ErrUnavailable` and `Bookmarks()` emits every row.
- **M7 — reading-list plist metadata deferred; no FileRef.** Reading-list `DateAdded` /
  `PreviewText` / `DateLastViewed` live in the `extra_attributes` **binary plist** BLOB;
  decoding it needs a binary-plist reader — deferred (a forward note, like calendar's
  `ExceptionDate` and messages' rich runs). The column `last_modified` (which equals a
  freshly-added item's `DateAdded`) is surfaced as the reading-list timestamp. Inline
  favicon BLOBs (`bookmarks.icon`) and the separate favicon store are out of scope, and
  no bookmark/visit references a backup file, so the domain emits **no** `backup.FileRef`
  (never-fabricate).
- **M7 — differential ±1s tolerance (SQLite round vs parser truncate; the calls
  precedent).** iLEAPP renders `visit_time` via SQLite `datetime(…,'unixepoch')`, which
  **rounds** the fractional second, while the parser keeps the precise sub-second value
  and truncates on display — so a visit whose fractional part is ≥ 0.5 renders one
  second apart (2 of the study backup's visits, verified against the raw values). The
  parser holds the exact value; `diff_safari.py` phase 1 tolerates ±1s (matching the
  calls domain's Julian-day rounding tolerance) and phase 2 — truncation-identical on
  both sides, keyed by `history_visits.id` — is the exact gate. Harness corrected;
  parser untouched. (Escalation stayed on-charter: raw-value inspection, never GPL
  source.)
- **M7 — pins.** No new Go module dependencies (`safari` uses stdlib + the internal
  packages; the binary-plist decode that would need a dependency is deferred);
  `go.mod`/`go.sum` unchanged. Same toolchain + iLEAPP oracle pins as M1–M6
  (`safariBookmarks.py` / `safariHistory.py` ship in the same iLEAPP v2026.1.0 image).
- **M7 — safari lands on `main` post-v0.1; the next release tag is Operator-gated.**
  Per the M6 precedent (never tag/push unasked), safari is recorded in `CHANGELOG.md`
  under `[Unreleased]`; the version bump and annotated tag for the next release are the
  Operator's, applied after the privacy gate + commit from the main checkout.
- **M8 — reminders store located by spike: multi-file CoreData, NOT `Calendar.sqlitedb`.**
  Read-only introspection of the study backup found the reminder data in
  `AppDomainGroup-group.com.apple.reminders/Container_v1/Stores/Data-*.sqlite` — a
  CloudKit-mirrored CoreData store per account plus a fixed-name `Data-local.sqlite`
  (the calendar store's reminder columns stay unused, confirming the ~iOS-13 move). The
  data lives in `ZREMCDREMINDER` (reminders), `ZREMCDBASELIST` (lists) and the shared
  `ZREMCDOBJECT` (accounts/recurrence/assignments/sharees), mixed CoreData inheritance.
- **M8 — new additive core capability `backup.ReadDirFS` (Operator-approved 2026-07-20).**
  The reminder data store is `Data-<UUID>.sqlite` with a UUID recorded in no manifest
  (verified: no peer-refs in any store's `Z_METADATA`, and the fixed-name
  `Data-local.sqlite` holds zero reminders on the study backup). The base `FS`
  (`Materialize`+`Exists`) cannot enumerate a directory, so reading reminders
  correctly requires listing `Container_v1/Stores` — a public-API question beyond the
  reminders package. Rather than read the fixed-name store only (a wrong-but-plausible
  *empty* result — the exact failure the charter forbids), the Operator approved an
  **additive optional** interface `backup.ReadDirFS{ FS; ReadDir(domain, relDir)
  ([]string, error) }` mirroring `io/fs.ReadDirFS`: the base `FS` contract is
  UNCHANGED (no host breaks), `DirFS` implements it (read-only — listing never
  materializes or mutates), and a host that has not adopted it degrades honestly
  (`reminders` reads only `Data-local.sqlite` and reports `cloudkit_stores` in
  `Capability.Missing`). This is the first multi-store domain whose file *names* are
  unknowable in advance (safari's two stores are fixed-name); the capability is the
  general mechanism for that.
- **M8 — identity is (Store, Z_PK); a stream-scoped error terminates the whole
  multi-store iteration.** Because each store has its own `Z_PK` sequence, `Reminder`
  and `List` carry `Store` (the store's base filename) and the differential keys on
  (store, id). Row-scoped scan errors are yielded and the stream continues (within and
  across stores); a stream-scoped error (e.g. a closed/broken store) yielded from any
  store ENDS the whole `Reminders()`/`Lists()` iteration — a stream-scoped failure
  terminates iteration by contract, so it must not silently roll on to the next store.
- **M8 — Z_ENT resolved per store (the M5 rule, generalized) — proven on real data.**
  The study's `Data-local.sqlite` renumbers `REMCDAccount`/`REMCDRecurrenceRule`/
  `REMCDAssignment`/`REMCDSharee` vs the CloudKit stores (a
  `REMCDGroceryOperationQueueItem` inserted mid-`Z_PRIMARYKEY`). The parser resolves
  each store's ordinals by name at Open; a hard-coded or store-shared ordinal would
  mis-read a store. The committed fixture uses two stores with different non-standard
  ordinals and a reused cross-store `Z_PK` to lock this in.
- **M8 — timestamps are Cocoa-2001 seconds, undated is NULL (no sentinel).** All six
  `ZREMCDREMINDER` date columns pinned by magnitude to Cocoa seconds (2012–2026;
  1981–1995 as Unix — impossible), REAL, via `cocoa.FromSecondsFloat`; iLEAPP's
  `+ 978307200` confirms. Unlike calendar's floating/all-day sentinels, an undated
  reminder is a plain NULL; an all-day reminder carries a date-only `ZDUEDATE` flagged
  by `ZALLDAY`. Titles/notes are plain columns — no blob decode.
- **M8 — split-oracle differential (iLEAPP reminders export stale, like notes).**
  iLEAPP `reminders.py` queries `ZREMCDOBJECT.ZTITLE1` and guards on
  `ZREMCDOBJECT.ZLASTMODIFIEDDATE`; on the iOS-18 schema reminders are in
  `ZREMCDREMINDER` (title `ZTITLE`) and `ZREMCDOBJECT` has no `ZLASTMODIFIEDDATE`, so
  it returns zero reminders (confirmed by source + an empirical run). The oracle is
  therefore split: iLEAPP's confirmed store glob and Cocoa epoch (MIT, attributed) plus
  the authoritative `deploy/diff_reminders.py` — each store's own SQL, per-store
  ordinal resolution, keyed by (store, `ZREMCDREMINDER.Z_PK`), both-directions set
  check. All clean on first run after the pre-coding introspection (every reminder and
  list across every store). Recurrence-constant and assignee interpretations are
  surfaced raw and documented-to-validate (no MIT oracle interprets them; the raw-code
  discipline of calls' `Handle.Type` and calendar's `Recurrence.frequency`).
- **M8 — pins.** No new Go module dependencies (`reminders` uses stdlib + the internal
  packages; `ReadDirFS`/`DirFS.ReadDir` are stdlib-only); `go.mod`/`go.sum` unchanged.
  Same toolchain + iLEAPP oracle pins as M1–M7 (`reminders.py` ships in the same iLEAPP
  v2026.1.0 image).
