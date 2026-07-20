// Package backup is the core of ios-backup-parser: typed, streamable readers
// for the personal-data domains inside an already-decrypted iOS/iPadOS backup.
//
// This package holds what every domain package (contacts, calls, messages,
// calendar, notes) shares:
//
//   - FS — the accessor a host implements to hand the library its backup files
//     (the project charter's "BackupFS" contract), plus DirFS, a built-in
//     implementation over a reconstructed <root>/<Domain>/<relativePath>
//     directory tree.
//   - FileRef — a structured reference to a file inside the backup that
//     round-trips into FS.Materialize.
//   - Capability — the per-domain capability report: which schema fingerprint
//     was detected, whether it is supported, and which fields this particular
//     backup cannot provide.
//   - The error taxonomy: ErrUnsupportedSchema (eager, at domain open),
//     ErrUnavailable (a requested field/stream is in Capability.Missing), and
//     RowError (a row-scoped defect mid-stream).
//
// # Streaming and errors
//
// Domain packages stream records as Go iterators (iter.Seq2[T, error]);
// nothing loads a whole domain into memory. Two error scopes are never
// conflated:
//
//   - A row-scoped defect yields (zero, *RowError) and the stream CONTINUES —
//     one corrupt row must not hide a hundred thousand good ones.
//   - Any other yielded error is stream-scoped and ends the iteration.
//
// Schema validation is eager: unsupported schemas fail at domain open with
// ErrUnsupportedSchema (carrying the observed fingerprint), before any
// iterator exists. A missing column degrades Capability.Missing — it never
// silently yields wrong or empty records.
//
// # Lifetimes
//
// Close open domains BEFORE closing the FS that materialized their databases:
// a domain reads lazily from the materialized copy for as long as it is open,
// and DirFS.Close removes every copy it has handed out (a domain iterated
// after that fails with a stream-scoped error). The usual
//
//	fsys, _ := backup.NewDirFS(root)
//	defer fsys.Close()
//	c, _ := contacts.Open(fsys)
//	defer c.Close()
//
// defer order is correct: LIFO runs c.Close first.
package backup
