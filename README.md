# ios-backup-parser

Typed Go readers for the personal data inside an iOS/iPadOS backup — **messages,
contacts, call history, calendar, notes**.

[![Go Reference](https://pkg.go.dev/badge/github.com/novkostya/ios-backup-parser.svg)](https://pkg.go.dev/github.com/novkostya/ios-backup-parser)
[![CI](https://github.com/novkostya/ios-backup-parser/actions/workflows/gates.yml/badge.svg)](https://github.com/novkostya/ios-backup-parser/actions/workflows/gates.yml)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8.svg)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> **Status: v0.1 — first release.** All five domains are validated against a real
> decrypted backup. Pre-1.0, so the API may still change before v1.

> ⚠️ **Intended use.** This library exposes a person's entire private life. Use it only
> on **your own** backups, or data you are **explicitly authorized** to access. See
> [Intended use](#intended-use) below — this is a boundary, not boilerplate.

## What it is

A pure-Go library that takes an **already-decrypted** backup and streams typed
records out of its domain databases. No CGO, no report generator, no GUI — just
data structures and iterators you can build on.

- **Streaming** — records come as Go 1.23 iterators; nothing loads a whole
  domain into memory.
- **Honest** — every domain opens with schema introspection and returns a
  capability report: which schema was detected, whether it's supported, and
  which fields this particular backup can't provide. Unsupported schemas fail
  loudly; the library never guesses.
- **Schema-aware** — iOS database schemas drift between versions; support is
  detected by introspection, never assumed from a version string.
- **Decodes what Apple hides** — modern message bodies live in an `attributedBody`
  typedstream blob, not the `text` column, and note bodies live in a gzip+protobuf
  blob with no text column at all; the `messages` and `notes` domains decode them
  (from-scratch, dependency-free readers) so a body is never silently dropped. A blob
  that can't be decoded is flagged, never returned as empty.

## Domains

Every domain is **validated** — not merely green against synthetic fixtures, but
diffed record-by-record against an independent oracle on a real decrypted backup
(the study device is on the iOS 18.x line). Schema support is detected by
introspecting the actual tables and columns, never inferred from an iOS version
string; the `Schema` label is a project-internal, discovery-order ordinal.

| Domain | Package | Database | Storage idiom | Schema | Status |
|---|---|---|---|---|---|
| Contacts | [`contacts`](contacts) | `AddressBook.sqlitedb` | plain SQLite | `contacts.1` | validated |
| Call history | [`calls`](calls) | `CallHistory.storedata` | CoreData | `calls.1` | validated |
| Messages | [`messages`](messages) | `sms.db` | plain SQLite + typedstream | `messages.1` | validated |
| Calendar | [`calendar`](calendar) | `Calendar.sqlitedb` | plain SQLite | `calendar.1` | validated |
| Notes | [`notes`](notes) | `NoteStore.sqlite` | CoreData + gzip/protobuf | `notes.1` | validated |

The per-domain schema reference — tables, joins, every timestamp column's epoch and
unit, and the evidence behind each fingerprint — is in [`docs/schemas/`](docs/schemas).
A backup whose schema does not match a known fingerprint fails loudly at `Open`
(`ErrUnsupportedSchema`, carrying the observed fingerprint), never silently.

## Install

```sh
go get github.com/novkostya/ios-backup-parser@latest
```

Requires Go 1.25+. Pure Go, CGO-free (`modernc.org/sqlite`) — cross-compiles to a static
binary like any other Go dependency.

## Use

```go
import (
    backup "github.com/novkostya/ios-backup-parser"
    "github.com/novkostya/ios-backup-parser/contacts"
)

// A decrypted backup, laid out as <root>/<Domain>/<relativePath>.
fsys, err := backup.NewDirFS(root)
defer fsys.Close() // removes the private scratch copies

c, err := contacts.Open(fsys) // eager: unsupported schemas fail HERE
defer c.Close()

fmt.Println(c.Capability()) // {contacts true contacts.1 [photo]}
for person, err := range c.People() {
    var rowErr *backup.RowError
    switch {
    case err == nil:
        fmt.Println(person.First, person.Last, person.Phones)
    case errors.As(err, &rowErr):
        // one defective row; the stream continues
    default:
        return err // stream-scoped: the database stopped reading
    }
}
```

Every domain follows this exact shape — `Open`, `Capability`, `Close`, and streaming
iterators. Runnable examples live in each package's `example_test.go` (rendered on
pkg.go.dev): [contacts](contacts/example_test.go), [calls](calls/example_test.go),
[messages](messages/example_test.go), [calendar](calendar/example_test.go),
[notes](notes/example_test.go).

The original backup is never opened by SQLite: `Materialize` hands the parser a
private, mutation-safe copy (including `-wal`/`-shm`/`-journal` sidecars), so a
live WAL can never be replayed into your only copy of a backup.

## What it is not

- It does **not** decrypt backups. Use its sibling,
  [`ios-backup-crypt`](https://github.com/novkostya/ios-backup-crypt), or any
  other tool that yields readable domain files.
- It does **not** parse photos libraries (use dedicated tools for that).

## Intended use

This library reads messages, contacts, call history, calendar, and notes — effectively
someone's whole private life. It exists for people who want to **own and inspect their
own data**: personal backup browsers, archival tools, migration helpers, and lawful
digital-forensics work.

Use it **only** on backups of devices you own, or data you are **explicitly and lawfully
authorized** to access. Accessing another person's data without consent is very likely
illegal where you live and is not a use this project supports. You are responsible for
using it lawfully.

The library is built to respect the data: it never mutates the input, makes no network
connections, and persists nothing (see [SECURITY.md](SECURITY.md)).

## Contributing

Contributions are welcome — new domains especially (there's a backlog: Safari,
Reminders, voicemail, WhatsApp). Please read [CONTRIBUTING.md](CONTRIBUTING.md); it
covers the containerized build (`make gates`, no local Go needed), the correctness rules
that keep the library trustworthy, and the license-hygiene boundary. By participating
you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

Found a backup that fails at `Open` with `ErrUnsupportedSchema`? That's a schema this
release hasn't seen yet — please [open an issue](https://github.com/novkostya/ios-backup-parser/issues/new?template=unsupported-schema.md)
with the observed fingerprint (it's structural, no personal data).

## Security

Security properties and private vulnerability reporting are in
[SECURITY.md](SECURITY.md). Please report suspected issues privately, with a synthetic
reproducer — never real backup data.

## Acknowledgments

This library was written from public documentation and validated against independent,
appropriately-licensed tools. Full attribution — including which format facts came from
where, and the strict black-box boundary around GPL tools — is in
[`NOTICE`](NOTICE). In particular:

- **[iLEAPP](https://github.com/abrignoni/iLEAPP)** (MIT) — the primary differential
  oracle for validation, and a cross-reference for schema interpretation across every
  domain.
- The published **typedstream** reverse-engineering write-ups and the
  **[python-typedstream](https://github.com/dgelessus/python-typedstream)** format
  documentation, which informed the from-scratch `attributedBody` decoder (facts only —
  no code ported).
- **Apple Notes** protobuf format documentation, for the from-scratch note-body decoder.
- **[modernc.org/sqlite](https://gitlab.com/cznic/sqlite)** — the CGO-free SQLite driver
  that keeps this library pure Go.

Not affiliated with or endorsed by Apple.

## License

Released under the [MIT License](LICENSE) — Copyright (c) 2026 Konstantin Novikov.
Third-party attributions are in [`NOTICE`](NOTICE). Changes per release are in
[`CHANGELOG.md`](CHANGELOG.md).

MIT. Not affiliated with or endorsed by Apple.
