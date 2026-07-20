package backup

// FS is how the library reads a backup's files (the charter's "BackupFS"
// contract). Hosts back it with a decrypted directory tree (see DirFS), a
// vault's session scratch, or anything else.
//
// Materialize must return a private, MUTATION-SAFE copy: opening a SQLite
// database with a live -wal (or hot -journal) replays or checkpoints it, so
// the library opens returned paths with normal SQLite semantics, and
// implementations must never hand out paths into the original backup.
type FS interface {
	// Materialize guarantees a real filesystem path for domain/relativePath,
	// including sidecar files (-wal/-shm/-journal) when they exist in the
	// backup. The returned path is a private copy the caller may open — and
	// thereby mutate — freely; the original backup stays untouched.
	Materialize(domain, relativePath string) (path string, err error)

	// Exists reports whether domain/relativePath is present in the backup.
	Exists(domain, relativePath string) (bool, error)
}

// FileRef is a structured reference to a file inside the backup — an
// attachment, a media file, a sibling database. It round-trips into
// FS.Materialize; records never carry a bare path string that lost its
// domain.
type FileRef struct {
	Domain       string `json:"domain"`
	RelativePath string `json:"relative_path"`
}
