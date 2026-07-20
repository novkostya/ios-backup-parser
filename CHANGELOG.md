# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project is pre-1.0,
so minor releases may make breaking API changes until v1.0.0.

## [0.1.0] — 2026-07-20

First release: typed, streaming readers for five personal-data domains inside an
already-decrypted iOS/iPadOS backup. Every domain was validated record-by-record
against an independent oracle on a real decrypted backup, not merely against
synthetic fixtures.

### Core

- `backup.FS` — the accessor contract a host implements (the charter's "BackupFS"),
  with `FileRef` for structured file references that round-trip into `Materialize`.
- `backup.DirFS` — built-in `FS` over a reconstructed `<Domain>/<relativePath>` tree.
  `Materialize` returns a private, mutation-safe copy (including `-wal`/`-shm`/
  `-journal` sidecars); the original backup is never opened by SQLite. `Close`
  removes the scratch copies.
- Schema introspection with a per-domain `Capability` report (detected fingerprint,
  supported flag, and the `Missing` fields this backup's schema cannot provide).
- Error taxonomy: eager `ErrUnsupportedSchema` (with the observed fingerprint) at
  `Open`; row-scoped `RowError` (the stream continues) versus stream-scoped errors
  (iteration ends); `ErrUnavailable` for a stream the schema cannot provide.
- Streaming records as Go 1.23 iterators (`iter.Seq2[T, error]`); no domain is ever
  loaded whole into memory. Pure Go, CGO-free (`modernc.org/sqlite`).

### Domains

- `contacts` — people and groups over `AddressBook.sqlitedb` (`contacts.1`).
- `calls` — call history over `CallHistory.storedata`, the first CoreData domain
  (`calls.1`).
- `messages` — chats, messages and attachments over `sms.db`, including a
  from-scratch typedstream decoder for `attributedBody` message bodies
  (`messages.1`).
- `calendar` — events and calendars over `Calendar.sqlitedb` (`calendar.1`).
- `notes` — notes and folders over `NoteStore.sqlite`, including a from-scratch
  gzip+protobuf decoder for note bodies; locked notes are reported, never decrypted
  (`notes.1`).

[0.1.0]: https://github.com/novkostya/ios-backup-parser/releases/tag/v0.1.0
