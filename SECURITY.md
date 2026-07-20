# Security Policy

## Scope

ios-backup-parser reads deeply personal data — messages, contacts, call history,
calendar, and notes — out of an iOS/iPadOS backup. It is designed so that using it is
safe for the data and the machine it runs on.

**Security properties the library guarantees:**

- **It never mutates the input backup.** Domain databases are opened only as private,
  throwaway scratch copies (including their `-wal`/`-shm`/`-journal` sidecars); the
  original tree is never opened by SQLite and may be mounted read-only.
- **It makes no network connections.** It is a pure parsing library — no telemetry, no
  update checks, no outbound requests of any kind.
- **It persists nothing.** Records stream and are not cached or indexed; scratch copies
  are removed on `Close`.
- **It is CGO-free** (`modernc.org/sqlite`), so there is no native SQLite attack
  surface and builds are static and reproducible.

It does **not** decrypt backups — decryption is a separate concern handled by
[`ios-backup-crypt`](https://github.com/novkostya/ios-backup-crypt) or another tool.

## Reporting a vulnerability

If you find a security issue — for example a path-traversal escape from the scratch
directory, a way to make the library write outside its scratch area, or a
malformed-input crash that could be weaponized — please report it **privately** rather
than opening a public issue:

- Use GitHub's **[private vulnerability reporting](https://github.com/novkostya/ios-backup-parser/security/advisories/new)**
  ("Report a vulnerability" on the Security tab), or
- open a minimal public issue asking for a private channel **without** the details.

Please include the smallest input that reproduces the problem. **Do not attach real
backup data** — a synthetic or scrubbed reproducer is always sufficient and keeps your
personal data out of the report.

You can expect an acknowledgement within a few days. As a pre-1.0 solo-maintained
project there is no formal SLA, but security reports are prioritized over features.

## Supported versions

Only the latest release line receives fixes while the project is pre-1.0. Malformed
input should degrade gracefully (a row- or stream-scoped error), never a panic — a
panic on attacker-influenced input is treated as a security bug.
