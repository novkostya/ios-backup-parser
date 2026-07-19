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

## What it is not

- It does **not** decrypt backups. Use its sibling,
  [`ios-backup-crypt`](https://github.com/novkostya/ios-backup-crypt), or any
  other tool that yields readable domain files.
- It does **not** parse photos libraries (use dedicated tools for that).

## License

MIT. Not affiliated with or endorsed by Apple.
