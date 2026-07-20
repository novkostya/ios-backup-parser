# ios-backup-parser — project charter

> **Human contributors:** this is the internal charter followed by the AI coding agents
> that build this project — you do not need it. Your guide is
> [CONTRIBUTING.md](CONTRIBUTING.md). This file is kept in the open as honest provenance
> of how the code was written.

> Agent entry point. Read this whole file before touching anything. This is a fresh
> repo: the charter is canon-so-far, the milestone you're on defines your boundary,
> and gaps are expected — handle them per "Process" below.

## Mission

A pure-Go, dependency-light library that turns an **already-decrypted** iOS/iPadOS
backup into **typed, streamable records** for the personal-data domains people
actually want out of a backup: **messages, contacts, call history, calendar, notes**.
MIT-licensed, useful standalone to anyone in the Go ecosystem. Module path:
`github.com/novkostya/ios-backup-parser`.

Sibling of [`ios-backup-crypt`](https://github.com/novkostya/ios-backup-crypt)
(decryption). The two never depend on each other: both plug into a host application
through small interfaces. The first consumer is quince (a self-hosted backup
manager), whose vault will glue them together — but nothing in this repo may know
or assume quince specifics.

## Non-goals

- **No decryption, no keybags, no Manifest.db file-ID resolution** — that's the
  sibling's job. This library starts from readable domain files.
- **No photos pipeline** (parked by the consumer; media referenced by messages is
  surfaced as file references, not rendered).
- **No report generation, no CLI product** (a tiny debug CLI for development is
  fine; it is not the deliverable).
- **No persistence** — the library reads lazily and holds no state between calls.

## Input boundary

Domain databases are SQLite, and SQLite needs **real on-disk files** (including
`-wal`/`-shm` siblings when present). So the host supplies an accessor:

```go
// backup.FS is how the library reads a backup's files (the "BackupFS"
// contract). Hosts back it with a decrypted directory tree, a vault's
// session scratch, or anything else.
type FS interface {
    // Materialize guarantees a real filesystem path for domain/relativePath,
    // including sidecar files (-wal/-shm/-journal) when they exist in the backup.
    Materialize(domain, relativePath string) (path string, err error)
    Exists(domain, relativePath string) (bool, error)
}
```

Plus the built-in `backup.DirFS` over a reconstructed
`<root>/<Domain>/<relativePath>` directory tree (what extraction tools produce).
Final shape settled at M1: the root package is `backup` (the sibling's root
package took `iosbackup`, and `backup.BackupFS` would stutter — hosts import
both without aliases), the interface is `backup.FS`, and hot `-journal`
sidecars are materialized alongside `-wal`/`-shm` for the same
mutation-safety reason (see the PROGRESS decisions log, M1).

Two contract points, ruled at M0 review (2026-07-20):

- **`Materialize` returns a private, mutation-safe copy.** Opening a SQLite DB with a
  live `-wal` replays/checkpoints it — mutation. So the library may open what
  `Materialize` returns with normal SQLite semantics, and implementations must never
  hand out paths into the original backup: the built-in directory FS **copies to
  scratch** (db + sidecars) and returns the copy. The originals are sacred (see the
  never-mutate hard rule).
- **Un-hashing a raw Apple backup (Manifest.db fileID → file) is the host's / the
  decryption sibling's job, never this library's.** The built-in FS reads an
  already-reconstructed domain tree; do not build a Manifest reader here (it is a
  named non-goal).

## Output model

- One package per domain: `contacts`, `calls`, `messages`, `calendar`, `notes`.
- Records stream (`iter.Seq2[T, error]` — Go 1.23+ iterators); nothing loads a
  whole domain into memory. Callers paginate naturally.
- Every domain opens with **schema introspection** and returns a **capability
  report**: what schema fingerprint was detected, whether it's supported, and
  which fields are unavailable in this backup's schema.
- **Validation is eager; row errors are scoped** (ruled 2026-07-20). Schema
  introspection and `ErrUnsupportedSchema` happen at domain *open*, before any
  iterator exists. Mid-stream, a **row-scoped** defect yields `(zero, err)` and the
  stream continues (one corrupt row must not hide a hundred thousand good ones); a
  **stream-scoped** failure (the DB itself stops reading) terminates iteration with
  the error. The two are never conflated. (M1 concretes: row-scoped =
  `*backup.RowError`, anything else ends the stream; a stream whose data the
  schema cannot provide yields `backup.ErrUnavailable` rather than reading empty.)
- **Attachment/media references are structured** (ruled 2026-07-20): a
  `FileRef{Domain, RelativePath string}` that round-trips into
  `BackupFS.Materialize` — never a bare path string that lost its domain.

```go
type Capability struct {
    Domain    string   // "messages"
    Supported bool
    Schema    string   // fingerprint LABEL, e.g. "sms.1" — see below
    Missing   []string // fields this schema cannot provide
}
```

**Fingerprint identity is the introspected structure, never a version claim** (ruled
2026-07-20): a fingerprint IS the observed table/column set relevant to the domain;
the `Schema` string is a human alias for it — project-internal ordinals in order of
discovery (`sms.1`, `sms.2`, …), NEVER an iOS-version-shaped name. Which iOS versions
a fingerprint was *observed on* is recorded as evidence in `docs/schemas/`, as
metadata — it is not the identity, and detection never consults a version string.

## Hard rules

- **State honesty.** Unknown schema → explicit `ErrUnsupportedSchema` with the
  fingerprint; a missing column degrades the *capability report*, never silently
  yields wrong or empty records. Wrong-but-plausible output is the worst failure
  mode a library in this space can have.
- **Never mutate the input backup** (ruled 2026-07-20). Domain DBs are opened only
  as private scratch copies (see Input boundary — `Materialize` semantics); the
  source tree is never opened in a mode that could replay/checkpoint a WAL or write
  a `-shm`. The operator's study backup is an irreplaceable personal artifact and is
  mounted read-only besides — treat any write attempt against it as a bug, not an
  inconvenience.
- **Schema drift is detected, never assumed.** Detect by introspection (table and
  column presence), never by trusting an iOS version string. Each supported
  fingerprint is documented in `docs/schemas/` with the evidence it came from.
- **License hygiene (this repo is MIT).**
  - [iLEAPP](https://github.com/abrignoni/ILEAPP) is MIT: reading and translating
    its parsing logic is allowed **with attribution** (comment + NOTICE entry).
  - [imessage-exporter](https://github.com/ReagentX/imessage-exporter) and any
    other GPL source: **black-box oracle only** — run it, compare outputs, read
    its author's published reverse-engineering *write-ups* (facts are free), but
    NEVER read-and-port its code. When in doubt, don't open the file.
  - **The operational form** (ruled 2026-07-20, because the natural debugging
    reflex is exactly what's forbidden): when a differential run disagrees with the
    oracle, the escalation path is write-ups → format docs → your own instrumented
    dumps — never the oracle's source. The harness invokes the oracle as an
    installed binary/container; no vendored or cloned GPL checkout may live
    anywhere readable-by-habit in this repo or its scratch.
- **Privacy is a commit-time gate.** Real backups, their derived outputs, real
  names/numbers/UDIDs, and Operator-infrastructure facts (hostnames, LAN addresses,
  topology, hardware) never enter committed files, **commit messages**, branch names,
  or fixtures. Committed fixtures are synthetic-only, generated by builders in this
  repo. Before EVERY commit run `make privacy-check` — it greps the staged diff
  against the Operator-private pattern list when the quince checkout is present
  (no-op for contributors/CI). **Know the gate's edges** (ruled 2026-07-20): the
  target sees only staged *content* — commit-message and branch-name hygiene are
  MANUAL discipline, checked by you before every commit; and the grep deliberately
  also matches diff headers (`+++ b/…`), so a private token in a *filename* trips
  it — that is a feature, keep it.
- **Version pins are looked up, never remembered.** LLM training data is stale by
  construction. When pinning anything (Go version, modules, linter), query the
  live source at pin time and prefer newest stable with support runway. SQLite
  driver: CGO-free `modernc.org/sqlite` (matches the consumer's constraint —
  cross-compiled static binaries) unless M1 finds a disqualifying limitation.
- **Docs are part of the diff.** A milestone that changes behavior updates this
  charter and `docs/` in the same change.

## The known hard part: typedstream

Modern `sms.db` stores message text not in the `text` column but in
`attributedBody` — a serialized `NSAttributedString` in Apple's ancient
**typedstream** format. This is M3's core difficulty. The plan: implement from
public format documentation (the imessage-exporter author's write-ups, archived
GNUstep/NeXT sources describing the format) and validate differentially against
imessage-exporter as a black box on operator-local data. Budget real time for it;
it is the reason messages is a late milestone despite being the headline domain.

## Testing ladder

1. **Synthetic fixtures** — builder-generated domain DBs per supported schema
   fingerprint, committed; data invented (see Privacy). Every parser bug found
   later becomes a fixture before it's fixed.
2. **Property/round-trip tests** where a builder exists (build → parse → compare).
3. **Differential vs iLEAPP** on an operator-local real backup — run both, diff
   record-by-record. Never committed; outputs stay on the operator's machines.
4. **Operator spot-check vs iMazing** rendering of the same data.

Gates for every milestone: `gofmt -l` empty, `go vet`, `golangci-lint run`,
`go test -race ./...`, fixtures green.

**Fixture-green is NOT validated** (ruled 2026-07-20). Committed tests prove only
"parses fixtures built from our own schema belief" — a circle. Every fingerprint
therefore carries an explicit status: **`fixture-only`** until the Operator-local
differential (iLEAPP and/or iMazing spot-check) passes on a real backup, then
**`validated`** — recorded per fingerprint in `docs/schemas/` and in the progress
doc. The differential is a required manual gate per fingerprint, not a nicety; a
contributor must never mistake green CI for correctness.

## Milestones

- **M0 — schema spike (no API design before this lands).** Against an
  operator-local decrypted real backup (+ iLEAPP output as cross-reference),
  document the *actual* schemas of the five domains for the iOS versions at hand:
  tables, joins, the attributedBody situation, attachment references, the notes
  encryption wrinkle. Deliverable: `docs/schemas/*.md` (scrubbed — structure,
  never data) + `docs/PROGRESS.md` + the capability-report design validated
  against reality. Ruled at M0 review (2026-07-20):
  - **Docs-only, scratch tooling only.** M0 commits zero Go — no `tools/`, no
    `go.mod`; `.schema`/query dumps run via sqlite3 in a toolchain container, any
    throwaway script lives in session scratch and dies there.
  - **Every timestamp column documents its epoch + unit** (Cocoa 2001 seconds,
    iMessage 2001 *nanoseconds*, Unix, …) — the off-by-31-years /
    off-by-10⁹ trap is exactly the wrong-but-plausible failure this library
    exists to prevent, and the spike is the cheapest place to nail it.
  - **Every domain doc names its storage idiom** — plain app SQLite vs CoreData
    (`Z`-tables, `Z_PK`/`Z_ENT` indirection, Cocoa epoch) vs blob-encoded
    (typedstream, gzip+protobuf) — so M2–M5 inherit the join/PK/epoch strategy
    instead of re-discovering it per milestone.
  - **Single-version honesty.** One study backup = one iOS version = one baseline
    fingerprint per domain. That IS "M0 done": a drift table is not required to
    close the spike, but the doc structure is per-fingerprint so a second
    observed version *appends* rather than forces a rewrite; fingerprints from
    versions we haven't observed are never invented.
- **M1 — core + contacts.** `BackupFS` + directory implementation, schema
  introspection helpers, capability report, and the `contacts` domain
  (`AddressBook.sqlitedb`) — the easiest domain proves the whole shape end to end.
- **M2 — call history** (`CallHistory.storedata`).
- **M3 — messages.** Chats, messages, attachments join, typedstream text;
  tapbacks/edits/replies as capability-gated extras, not blockers.
- **M4 — calendar** (`Calendar.sqlitedb`).
- **M5 — notes** (`NoteStore.sqlite`). Two distinct wrinkles, never conflated
  (ruled 2026-07-20): (a) **per-note password protection** — out of scope, such
  notes are *reported* (present, locked), never decrypted in v0.1; (b) the
  **routine gzip+protobuf encoding of every ordinary note body** — fully in
  scope, it must be decoded or no note has text at all. M0's notes doc keeps
  them separate so M5 doesn't treat every body as "encrypted/opaque".
- **M6 — v0.1.** Docs, examples, schema-coverage table, tag.

## Backlog — post-v0.1 domain candidates (Operator-acked 2026-07-20)

Recorded so absence reads as a decision, not an oversight (parity review against
iMazing's domain list). Every future domain enters through the M0 pattern — schema
spike first, fingerprint `observed` by introspection of a real backup, differential to
`validated` — never by assumption. **One domain per session** (agents collide on the
README support table, `cmd/ibp-dump`'s switch, PROGRESS, NOTICE); a domain lands, the
next starts — the milestone-worktree rhythm. These run fine **in parallel with quince**
(separate repo, separate dev CT) but **not in parallel with each other**.

**Ordering (ruled 2026-07-20, momentum > raw size):** **Safari first** — `Bookmarks.db`
is plain SQLite, near-certainly populated, and iLEAPP has strong coverage → a clean
two-oracle differential that reliably reaches `validated`. **Voicemail is NOT first
despite being smallest:** visual-voicemail storage is carrier-dependent, so the study
backup may hold zero VVM rows — a domain that can only reach `observed`, not
`validated`, is a poor opener. **Scope revised 2026-07-20 (Operator):** safari (M7) and
reminders (M8) done. **voicemail DE-PRIORITIZED, whatsapp DE-SCOPED** (see their entries);
**photos under active reconsideration as the one remaining feasible domain — as METADATA
only.** Post-v0.1 domains are M7+; each is its own milestone, CHANGELOG entry, and
fingerprint.

- **safari** — bookmarks + reading list (`Bookmarks.db`, plain SQLite; the reading
  list lives inside it) and history. Spike caveat: which Safari artifacts are
  actually PRESENT in a backup varies across iOS versions (history has moved and
  may be protection-class-gated) — presence is verified against a real backup,
  never assumed.
- **reminders** — its own store since ~iOS 13 (the M0 calendar doc recorded
  `CalendarItem`'s reminder columns as present-but-unused for exactly this
  reason); location + idiom (expect CoreData) established by its spike.
- **voicemail** — **DE-PRIORITIZED (Operator, 2026-07-20):** the Operator's device
  holds no voicemails to introspect or differential-validate against, so it could
  only ever reach `observed`/`fixture-only` here, never `validated`. Not scheduled;
  a contributor with real visual-voicemail data could carry it (metadata DB + audio
  `FileRef`s — small, and it fits the boundary).
- **whatsapp** — **DE-SCOPED (Operator, 2026-07-20):** modern WhatsApp encrypts its
  own local data, which puts `ChatStorage.sqlite` **outside this library's boundary
  by definition** — the parser reads *already-decrypted* domain databases, and
  app-level encryption is a WhatsApp-specific *decryption* problem (a different
  project), not a parsing one. (iMazing can't do it either; adversarial schema churn
  on top.) Removed from the planned backlog; only becomes viable if someone first
  solves WhatsApp decryption upstream.
- **photos** — **UNDER RECONSIDERATION (Operator, 2026-07-20) — as METADATA only.**
  The original park was a *quince-viewer* decision (icloudpd + Immich render the
  Operator's photos, so quince won't). But the standalone library's job here is
  different: parse `Photos.sqlite` into typed **metadata records** — asset filename,
  capture date, geolocation, album membership, favorite/hidden — with a `FileRef` to
  the media file, exactly like message attachments. **No thumbnails, no rendering**
  (that stays out — the README "no photos libraries" line means no *rendering*).
  Feasibility: it's the ONE remaining domain that both fits the decrypted-input
  boundary (Photos.sqlite is a normal iOS DB, not app-encrypted) AND has real data to
  validate against. Caveats: it is the **largest and most schema-churned** iOS DB
  (huge CoreData `ZASSET` model) — a serious spike, and the fingerprint/capability
  model earns its keep hard here; and **quince won't consume it**, so building it is
  standalone-library ambition, not consumer-driven. Pending an explicit Operator go.

## Where work runs (read this BEFORE your first command)

- **The driving workstation is a thin client** — no language toolchains, no container
  runtime get installed on it, ever. Editing and driving from it are fine; *executing*
  is not. If a gate seems to need a local tool, you're in the wrong place — don't
  install anything.
- Gates and builds run on the Operator's dev host, which is a **pure container host**:
  no toolchains on its rootfs either — every gate runs inside a pinned toolchain
  container (`nerdctl`/`docker`, autodetected by the Makefile). Same rules as the
  quince project's "Where work runs" (its `docs/program/quince.program.md`).
- **This project has its own dev host — never run gates on another project's box**
  (one project, one dev host; sharing was tried once, 2026-07-20, and the contention
  forced an emergency second box mid-rung). Its identity and provisioning live in the
  Operator-private environment doc's sibling section, along with the rule that the
  privacy pattern list must be present on any box that commits (the Makefile also
  probes `../quince-local/privacy-patterns.txt` for exactly this case).
- **Concrete hosts, addresses, paths, and the exact workflow are Operator-private and
  live OUTSIDE this repo.** On the Operator's machines the quince checkout sits next
  to this repo (directory named `quince` or `iphone-backup-app`); read its
  `local/environment.md` — section **"Sibling library repos"** — before starting.
  Never quote that file's contents into anything committed here.
- M0's study material (a decrypted real backup) and all differential-test data are
  Operator-local; locations are recorded in that same file.
- Contributors without that file need only `make` + a container runtime.

## Process

- One milestone per session. Start by reading this charter and `docs/PROGRESS.md`
  (create it in M0: milestone states + a decisions log).
- **Gaps**: this charter is decided-so-far. A gap inside your milestone's boundary
  that changes no public API, license posture, or privacy posture → decide it,
  record it in the decisions log. Anything else → write the smallest complete
  proposal into the affected doc marked `PROPOSED (gap):`, report, and stop that
  thread. Never silently deviate.
- A story is proven by running it, never by reading the code.
- Commit when the Operator asks; never push or tag unasked.
