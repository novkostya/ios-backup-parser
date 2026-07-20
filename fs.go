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

// ReadDirFS is an OPTIONAL FS capability: listing the entries of a directory
// within a domain. The base FS deliberately exposes only file access
// (Materialize/Exists) because almost every domain reads files whose relative
// paths are fixed and known ahead of time. A domain that instead must DISCOVER
// files whose names it cannot know in advance type-asserts for this — today
// only reminders, whose per-account CloudKit stores live at
// Container_v1/Stores/Data-<UUID>.sqlite with a UUID assigned at store-creation
// time and recorded in no manifest (the fixed-name Data-local.sqlite is only
// the on-device account's store).
//
// It mirrors io/fs.ReadDirFS: a host MAY implement it (the built-in DirFS
// does), and a host that does not is served best-effort — the domain reports
// the shortfall through its capability report rather than by failing. Listing
// is READ-ONLY: it reports names, never materializes or opens anything, so the
// never-mutate-input rule is not engaged (only Materialize ever copies).
type ReadDirFS interface {
	FS

	// ReadDir returns the names — the final path element only, not full
	// paths — of the entries directly inside domain/relativeDir, or an error
	// wrapping fs.ErrNotExist when that directory is absent. Join a returned
	// name onto relativeDir to form a relative path for Materialize/Exists.
	// The order is unspecified; callers that need determinism sort.
	ReadDir(domain, relativeDir string) (names []string, err error)
}

// FileRef is a structured reference to a file inside the backup — an
// attachment, a media file, a sibling database. It round-trips into
// FS.Materialize; records never carry a bare path string that lost its
// domain.
type FileRef struct {
	Domain       string `json:"domain"`
	RelativePath string `json:"relative_path"`
}
