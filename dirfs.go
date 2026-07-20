package backup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// sidecarSuffixes are the SQLite companion files Materialize copies alongside
// a database when they exist: a live WAL and its shared-memory index, or a hot
// rollback journal. Opening a copy without them would show a stale or corrupt
// view; opening the ORIGINAL with them present would mutate the backup.
var sidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// DirFS is the built-in FS over a reconstructed <root>/<Domain>/<relativePath>
// directory tree — what extraction tools (e.g. ios-backup-crypt) produce.
//
// Materialize copies the requested file and its sidecars into a private
// scratch directory and returns the copy, so the original tree is never opened
// by SQLite (the never-mutate-input rule; the tree may even be mounted
// read-only). Close removes the scratch directory and every path Materialize
// has returned.
type DirFS struct {
	root string

	mu      sync.Mutex
	scratch string // lazily created; removed by Close
	seq     int
	closed  bool
}

// NewDirFS opens a DirFS over root, which must be an existing directory.
func NewDirFS(root string) (*DirFS, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("backup: open directory tree: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("backup: open directory tree: %s is not a directory", root)
	}
	return &DirFS{root: root}, nil
}

// source validates domain and relativePath and returns the path of the
// original file inside the tree. Both components must stay inside root: the
// domain is a single path element, and the (slash-separated) relative path
// must be local in the io/fs sense.
func (d *DirFS) source(domain, relativePath string) (string, error) {
	if domain == "" || domain == "." || domain == ".." || strings.ContainsAny(domain, `/\`) {
		return "", fmt.Errorf("backup: invalid domain %q", domain)
	}
	rel := filepath.FromSlash(relativePath)
	if relativePath == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("backup: invalid relative path %q", relativePath)
	}
	return filepath.Join(d.root, domain, rel), nil
}

// Exists implements FS.
func (d *DirFS) Exists(domain, relativePath string) (bool, error) {
	src, err := d.source(domain, relativePath)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("backup: stat %s/%s: %w", domain, relativePath, err)
	}
	return true, nil
}

// ReadDir implements ReadDirFS: it lists the entry names directly inside
// domain/relativeDir in the ORIGINAL tree. Listing is read-only — it never
// copies, opens, or mutates anything (only Materialize copies), so it is safe
// against a read-only backup tree. A missing directory surfaces as
// fs.ErrNotExist (wrapped). Names are returned sorted (os.ReadDir sorts), so
// callers get a deterministic order.
func (d *DirFS) ReadDir(domain, relativeDir string) ([]string, error) {
	src, err := d.source(domain, relativeDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, fmt.Errorf("backup: read dir %s/%s: %w", domain, relativeDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// Materialize implements FS: it copies domain/relativePath — and any
// -wal/-shm/-journal sidecars present — into a private scratch directory and
// returns the copied file's path. Each call materializes a fresh copy.
func (d *DirFS) Materialize(domain, relativePath string) (string, error) {
	src, err := d.source(domain, relativePath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("backup: materialize %s/%s: %w", domain, relativePath, err)
	}

	dir, err := d.newScratchDir()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := copyFile(src, dst); err != nil {
		return "", fmt.Errorf("backup: materialize %s/%s: %w", domain, relativePath, err)
	}
	for _, suffix := range sidecarSuffixes {
		if _, err := os.Stat(src + suffix); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("backup: materialize %s/%s: sidecar: %w", domain, relativePath, err)
		}
		if err := copyFile(src+suffix, dst+suffix); err != nil {
			return "", fmt.Errorf("backup: materialize %s/%s: sidecar: %w", domain, relativePath, err)
		}
	}
	return dst, nil
}

// Close removes the scratch directory holding every materialized copy. The
// DirFS is unusable afterwards.
func (d *DirFS) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.scratch == "" {
		return nil
	}
	scratch := d.scratch
	d.scratch = ""
	return os.RemoveAll(scratch)
}

// newScratchDir returns a fresh subdirectory of the (lazily created) scratch
// root, so repeated materializations of same-named files never collide.
func (d *DirFS) newScratchDir() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return "", errors.New("backup: DirFS is closed")
	}
	if d.scratch == "" {
		scratch, err := os.MkdirTemp("", "ios-backup-parser-")
		if err != nil {
			return "", fmt.Errorf("backup: create scratch: %w", err)
		}
		d.scratch = scratch
	}
	d.seq++
	dir := filepath.Join(d.scratch, strconv.Itoa(d.seq))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("backup: create scratch: %w", err)
	}
	return dir, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
