package backup_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDirFSMaterializeIsAMutationSafeCopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "HomeDomain", "Library", "db.sqlite")
	writeFile(t, src, "original-db")
	writeFile(t, src+"-wal", "original-wal")
	writeFile(t, src+"-shm", "original-shm")
	writeFile(t, src+"-journal", "original-journal")

	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	path, err := fsys.Materialize("HomeDomain", "Library/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if path == src {
		t.Fatalf("Materialize returned the original path %s", path)
	}
	for suffix, want := range map[string]string{
		"": "original-db", "-wal": "original-wal", "-shm": "original-shm", "-journal": "original-journal",
	} {
		if got := readFile(t, path+suffix); got != want {
			t.Errorf("copy%s = %q, want %q", suffix, got, want)
		}
	}

	// Mutating the copy must not touch the originals.
	if err := os.WriteFile(path, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, src); got != "original-db" {
		t.Errorf("original mutated: %q", got)
	}
	if got := readFile(t, src+"-wal"); got != "original-wal" {
		t.Errorf("original wal mutated: %q", got)
	}

	// A second materialization is a fresh, unmutated copy.
	path2, err := fsys.Materialize("HomeDomain", "Library/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if path2 == path {
		t.Fatalf("second Materialize returned the same path")
	}
	if got := readFile(t, path2); got != "original-db" {
		t.Errorf("second copy = %q", got)
	}
}

func TestDirFSMaterializeWithoutSidecars(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "HomeDomain", "solo.db"), "solo")
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	path, err := fsys.Materialize("HomeDomain", "solo.db")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "solo" {
		t.Errorf("copy = %q", got)
	}
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("phantom -wal sidecar materialized")
	}
}

func TestDirFSExists(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "HomeDomain", "Library", "db.sqlite"), "x")
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	ok, err := fsys.Exists("HomeDomain", "Library/db.sqlite")
	if err != nil || !ok {
		t.Errorf("Exists(present) = %v, %v; want true, nil", ok, err)
	}
	ok, err = fsys.Exists("HomeDomain", "Library/nope.sqlite")
	if err != nil || ok {
		t.Errorf("Exists(absent) = %v, %v; want false, nil", ok, err)
	}
	ok, err = fsys.Exists("MediaDomain", "Library/db.sqlite")
	if err != nil || ok {
		t.Errorf("Exists(absent domain) = %v, %v; want false, nil", ok, err)
	}
}

func TestDirFSMissingFile(t *testing.T) {
	fsys, err := backup.NewDirFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	_, err = fsys.Materialize("HomeDomain", "Library/db.sqlite")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Materialize(absent) = %v, want fs.ErrNotExist", err)
	}
}

func TestDirFSRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "HomeDomain", "ok.txt"), "x")
	writeFile(t, filepath.Join(root, "secret.txt"), "top")
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	for _, tc := range []struct{ domain, rel string }{
		{"", "ok.txt"},
		{".", "ok.txt"},
		{"..", "ok.txt"},
		{"a/b", "ok.txt"},
		{`a\b`, "ok.txt"},
		{"HomeDomain", ""},
		{"HomeDomain", "../secret.txt"},
		{"HomeDomain", "a/../../secret.txt"},
		{"HomeDomain", "/etc/hosts"},
	} {
		if _, err := fsys.Materialize(tc.domain, tc.rel); err == nil {
			t.Errorf("Materialize(%q, %q) accepted an escaping path", tc.domain, tc.rel)
		}
		if _, err := fsys.Exists(tc.domain, tc.rel); err == nil {
			t.Errorf("Exists(%q, %q) accepted an escaping path", tc.domain, tc.rel)
		}
	}
}

func TestDirFSCloseRemovesScratch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "HomeDomain", "db.sqlite"), "x")
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := fsys.Materialize("HomeDomain", "db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("scratch copy survived Close: %v", err)
	}
	if _, err := fsys.Materialize("HomeDomain", "db.sqlite"); err == nil {
		t.Errorf("Materialize after Close succeeded")
	}
	if err := fsys.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestNewDirFSValidatesRoot(t *testing.T) {
	if _, err := backup.NewDirFS(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Errorf("NewDirFS(missing) succeeded")
	}
	file := filepath.Join(t.TempDir(), "f")
	writeFile(t, file, "x")
	if _, err := backup.NewDirFS(file); err == nil {
		t.Errorf("NewDirFS(file) succeeded")
	}
}
