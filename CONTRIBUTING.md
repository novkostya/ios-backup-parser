# Contributing to ios-backup-parser

Thanks for your interest. This is a small, focused library with strong correctness
rules — contributions are welcome as long as they keep those rules intact.

## Building and testing

The only requirements are **`make`** and a **container runtime** (`nerdctl` or
`docker`). There is no need to install Go on your machine — every gate runs inside a
pinned toolchain container, so your build matches CI exactly.

```sh
make gates      # gofmt -l · go vet · golangci-lint · go test -race  (the full gate)
make test       # just the race tests, for a fast inner loop
make fixtures   # regenerate the committed synthetic fixtures after a builder change
```

All version pins live in `versions.env` (the single source of truth). The
`make privacy-check` target and the operator-local study targets (`diff-study-*`) are
used by the maintainer against a real backup; they no-op or are irrelevant without that
private material, and you never need them to contribute.

## The rules that make this library trustworthy

This is a forensic-adjacent library: wrong-but-plausible output is its worst possible
failure. A few non-negotiable principles keep it honest — please preserve them:

1. **Schema support is detected, never assumed.** A domain recognizes a backup by
   introspecting its actual tables and columns (a *fingerprint*), never by trusting an
   iOS version string. Fingerprint labels are project-internal, discovery-order
   ordinals (`contacts.1`), not version-shaped names.
2. **Fail loudly, never guess.** An unrecognized schema returns `ErrUnsupportedSchema`
   with the observed fingerprint at `Open`; a field the schema can't provide lands in
   `Capability.Missing`; a blob that can't be decoded is flagged, never returned as
   empty. The library never emits a value it isn't sure of.
3. **Never mutate the input.** Domain databases are opened only as private scratch
   copies (see `backup.FS` / `DirFS.Materialize`), including their `-wal`/`-shm`/
   `-journal` sidecars. The original backup is never opened by SQLite.
4. **License hygiene.** Interpretations may be cross-referenced against **MIT** tools
   (notably [iLEAPP](https://github.com/abrignoni/iLEAPP)) *with attribution* in
   `NOTICE`. **GPL** tools (e.g. `imessage-exporter`) are used **only as black-box
   differential oracles** — executed and compared against, never read or ported. When
   in doubt, don't open the file. Any format fact taken from a copyleft project's
   documentation (not its code) is attributed in `NOTICE`.

## Adding a new domain

New domains are welcome (see the backlog in `CLAUDE.md`). The established pattern:

1. **Schema spike first.** Document the real schema from an actual backup in
   `docs/schemas/<domain>.md` before writing parser code — every timestamp column's
   epoch and unit, the storage idiom (plain SQLite / CoreData / blob-encoded), and the
   joins. This step has caught a wrong-but-plausible bug in *every* domain so far.
2. Mirror an existing domain's shape: `Open` (eager validation), streaming
   `iter.Seq2[T, error]` iterators, a `Capability` report, a committed synthetic
   fixture and its builder, and the row-scoped vs stream-scoped error contract.
3. **Validate against a real backup**, not just fixtures — a record-by-record
   differential against an independent oracle. Green synthetic tests prove only "parses
   fixtures I built from my own belief"; the differential is what breaks that circle.
   A fingerprint stays `observed`/`fixture-only` until the differential passes, then
   `validated`.

## Reporting a backup that isn't recognized

If `Open` returns `ErrUnsupportedSchema`, that error carries the **observed
fingerprint** — the tables and columns it actually found. Open an issue (see the
schema-support template) and paste that fingerprint plus your device's iOS version.
The fingerprint is structural only; it contains no personal data.

## A note on how this project is built

ios-backup-parser is developed largely by AI coding agents (Claude) under a strict,
human-reviewed process; `CLAUDE.md` is that internal build charter, and commits are
co-authored accordingly. You do **not** need to read it to contribute — this file is
the human-facing guide. Ordinary pull requests are reviewed and merged the normal way.

## License

By contributing, you agree that your contributions are licensed under the project's
[MIT License](LICENSE).
