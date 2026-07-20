# ios-backup-parser

Typed Go readers for the personal data inside an iOS/iPadOS backup — **messages,
contacts, call history, calendar, notes**.

> **Status: pre-v0.1.** Under active development; the API is not stable yet.

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

The original backup is never opened by SQLite: `Materialize` hands the parser a
private, mutation-safe copy (including `-wal`/`-shm`/`-journal` sidecars), so a
live WAL can never be replayed into your only copy of a backup.

## What it is not

- It does **not** decrypt backups. Use its sibling,
  [`ios-backup-crypt`](https://github.com/novkostya/ios-backup-crypt), or any
  other tool that yields readable domain files.
- It does **not** parse photos libraries (use dedicated tools for that).

## License

MIT. Not affiliated with or endorsed by Apple.
