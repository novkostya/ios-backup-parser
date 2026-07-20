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
| M5 | `notes` (`NoteStore.sqlite`) — locked notes reported, not decrypted | Pending |
| M6 | v0.1 — docs, examples, schema-coverage table, tag | Pending |

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
