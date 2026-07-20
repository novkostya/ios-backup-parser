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
| M2 | `calls` (`CallHistory.storedata`) | Pending |
| M3 | `messages` — chats / messages / attachments join, typedstream text | Pending |
| M4 | `calendar` (`Calendar.sqlitedb`) | Pending |
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
