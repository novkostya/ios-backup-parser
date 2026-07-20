package safari_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/safari"
)

// fixtureFS builds a synthetic backup tree holding Safari databases built with opt and
// returns a DirFS over it.
func fixtureFS(t *testing.T, opt safari.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	safari.BuildFixture(t, root, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt safari.FixtureOptions) *safari.Reader {
	t.Helper()
	r, err := safari.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// rowErrorBookmarkID / rowErrorVisitID mirror the fixture builder's constants.
const (
	rowErrorBookmarkID = 8
	rowErrorVisitID    = 4
)

func assertBookmarks(t *testing.T, r *safari.Reader) {
	t.Helper()
	expected := safari.ExpectedBookmarks()
	i := 0
	for bm, err := range r.Bookmarks() {
		if i >= len(expected) {
			t.Fatalf("more bookmarks than expected: %+v, %v", bm, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("bookmark %d: got (%+v, %v), want a *backup.RowError", i, bm, err)
			}
			if rowErr.Domain != "safari" || rowErr.Table != "bookmarks" || rowErr.RowID != rowErrorBookmarkID {
				t.Errorf("row error = %+v, want bookmarks rowid %d", rowErr, rowErrorBookmarkID)
			}
		} else {
			if err != nil {
				t.Fatalf("bookmark %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(bm, *want) {
				t.Errorf("bookmark %d:\n got %+v\nwant %+v", i, bm, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("bookmarks stream ended after %d, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func assertReadingList(t *testing.T, r *safari.Reader) {
	t.Helper()
	var got []safari.ReadingListItem
	for item, err := range r.ReadingList() {
		if err != nil {
			t.Fatalf("reading list stream error: %v", err)
		}
		got = append(got, item)
	}
	if want := safari.ExpectedReadingList(); !reflect.DeepEqual(got, want) {
		t.Errorf("reading list:\n got %+v\nwant %+v", got, want)
	}
}

func assertHistory(t *testing.T, r *safari.Reader) {
	t.Helper()
	expected := safari.ExpectedHistory()
	i := 0
	for v, err := range r.History() {
		if i >= len(expected) {
			t.Fatalf("more visits than expected: %+v, %v", v, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("visit %d: got (%+v, %v), want a *backup.RowError", i, v, err)
			}
			if rowErr.Domain != "safari" || rowErr.Table != "history_visits" || rowErr.RowID != rowErrorVisitID {
				t.Errorf("row error = %+v, want history_visits rowid %d", rowErr, rowErrorVisitID)
			}
		} else {
			if err != nil {
				t.Fatalf("visit %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(v, *want) {
				t.Errorf("visit %d:\n got %+v\nwant %+v", i, v, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("history stream ended after %d, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func TestCapability(t *testing.T) {
	r := openFixture(t, safari.FixtureOptions{})
	capability := r.Capability()
	want := backup.Capability{Domain: "safari", Supported: true, Schema: "safari.1"}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestRoundTrip(t *testing.T) {
	r := openFixture(t, safari.FixtureOptions{})
	assertBookmarks(t, r)
	assertReadingList(t, r)
	assertHistory(t, r)
}

// TestCommittedFixture parses the COMMITTED rung-1 artifacts — proving the checked-in
// fixtures, not just the in-memory builder, match the parser.
func TestCommittedFixture(t *testing.T) {
	root := t.TempDir()
	safariDir := filepath.Join(root, safari.Domain, "Library", "Safari")
	if err := os.MkdirAll(safariDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for committed, dest := range map[string]string{
		safari.CommittedBookmarksFixture: filepath.Join(safariDir, "Bookmarks.db"),
		safari.CommittedHistoryFixture:   filepath.Join(safariDir, "History.db"),
	} {
		data, err := os.ReadFile(committed)
		if err != nil {
			t.Fatalf("committed fixture missing (%v) — run `make fixtures` and commit the result", err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	r, err := safari.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	assertBookmarks(t, r)
	assertReadingList(t, r)
	assertHistory(t, r)
}

func TestDegradedSchema(t *testing.T) {
	r := openFixture(t, safari.FixtureOptions{
		DropColumns: []string{"bookmarks.special_id", "bookmarks.hidden", "bookmarks.last_modified", "bookmarks.num_children"},
	})
	capability := r.Capability()
	wantMissing := []string{"hidden", "modified", "num_children", "special"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "safari.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}
	// The dropped columns degrade to zero values; the corrupt last_modified row no
	// longer errors (the column is not selected), so all non-reading-list rows parse.
	byID := map[int64]safari.Bookmark{}
	for bm, err := range r.Bookmarks() {
		if err != nil {
			t.Fatalf("degraded bookmarks: unexpected error %v", err)
		}
		byID[bm.ID] = bm
	}
	if len(byID) != 7 { // ids 0,1,2,3,4,5,8 (reading list 6,7 excluded)
		t.Fatalf("degraded stream yielded %d bookmarks, want 7", len(byID))
	}
	if b := byID[2]; b.SpecialID != 0 { // special_id dropped
		t.Errorf("dropped special_id should be zero, got %d", b.SpecialID)
	}
	if b := byID[5]; b.Hidden || !b.LastModified.IsZero() {
		t.Errorf("dropped hidden/last_modified should be zero, got hidden=%v lm=%v", b.Hidden, b.LastModified)
	}
	if b := byID[4]; b.URL == "" || b.Title == "" { // surviving columns intact
		t.Errorf("surviving columns degraded unexpectedly: %+v", b)
	}
}

func TestReadingListUnavailable(t *testing.T) {
	// Dropping bookmarks.read removes the reading-list discriminator.
	r := openFixture(t, safari.FixtureOptions{DropColumns: []string{"bookmarks.read"}})
	if !slices.Contains(r.Capability().Missing, "reading_list") {
		t.Errorf("Missing = %v, want it to contain \"reading_list\"", r.Capability().Missing)
	}
	gotUnavailable := false
	for _, err := range r.ReadingList() {
		if errors.Is(err, backup.ErrUnavailable) {
			gotUnavailable = true
			break
		}
		t.Fatalf("reading list without the read column: unexpected %v", err)
	}
	if !gotUnavailable {
		t.Error("ReadingList() did not yield ErrUnavailable")
	}
	// Without the discriminator, Bookmarks() emits every row (including the two
	// reading-list items) — 9 total (7 + 2), still with the corrupt row erroring.
	seen, rowErrs := 0, 0
	for _, err := range r.Bookmarks() {
		if err != nil {
			rowErrs++
			continue
		}
		seen++
	}
	if seen != 8 || rowErrs != 1 { // ids 0..7 minus the corrupt id 8 = 8 good + 1 error
		t.Errorf("Bookmarks() without discriminator yielded %d good + %d errors, want 8 + 1", seen, rowErrs)
	}
}

func TestHistoryUnavailable(t *testing.T) {
	// No History.db at all.
	r := openFixture(t, safari.FixtureOptions{OmitHistory: true})
	if !slices.Contains(r.Capability().Missing, "history") {
		t.Errorf("Missing = %v, want it to contain \"history\"", r.Capability().Missing)
	}
	gotUnavailable := false
	for _, err := range r.History() {
		if errors.Is(err, backup.ErrUnavailable) {
			gotUnavailable = true
			break
		}
		t.Fatalf("history without History.db: unexpected %v", err)
	}
	if !gotUnavailable {
		t.Error("History() did not yield ErrUnavailable")
	}
	// Bookmarks still stream normally.
	assertBookmarks(t, r)
}

// TestHistoryUnsupportedSchemaDegrades checks that a present-but-unrecognized
// History.db degrades History() to ErrUnavailable rather than failing Open.
func TestHistoryUnsupportedSchemaDegrades(t *testing.T) {
	r := openFixture(t, safari.FixtureOptions{DropTables: []string{"history_items"}})
	if !slices.Contains(r.Capability().Missing, "history") {
		t.Errorf("Missing = %v, want it to contain \"history\"", r.Capability().Missing)
	}
	for _, err := range r.History() {
		if !errors.Is(err, backup.ErrUnavailable) {
			t.Fatalf("history over an unrecognized History.db: want ErrUnavailable, got %v", err)
		}
		break
	}
}

func TestUnsupportedSchema(t *testing.T) {
	// A required column absent (here bookmarks.url) is a different, unsupported
	// fingerprint — never a silent degradation.
	_, err := safari.Open(fixtureFS(t, safari.FixtureOptions{DropColumns: []string{"bookmarks.url"}}))
	if !errors.Is(err, backup.ErrUnsupportedSchema) {
		t.Fatalf("err = %v, want ErrUnsupportedSchema", err)
	}
	var unsupported *backup.UnsupportedSchemaError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T, want *UnsupportedSchemaError", err)
	}
	if unsupported.Fingerprint == "" {
		t.Error("observed fingerprint missing from the error")
	}
}

func TestOpenWithoutDatabase(t *testing.T) {
	fsys, err := backup.NewDirFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	if _, err := safari.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	r := openFixture(t, safari.FixtureOptions{})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Bookmarks() and History() both read closed handles; each must terminate with a
	// single stream-scoped error, never a record and never a row-scoped RowError.
	assertClosedStream(t, "bookmarks", func(yield func(error) bool) {
		for _, err := range r.Bookmarks() {
			if !yield(err) {
				return
			}
		}
	})
	assertClosedStream(t, "history", func(yield func(error) bool) {
		for _, err := range r.History() {
			if !yield(err) {
				return
			}
		}
	})
}

func assertClosedStream(t *testing.T, name string, stream func(func(error) bool)) {
	t.Helper()
	count := 0
	for err := range stream {
		count++
		if err == nil {
			t.Fatalf("%s: iteration over a closed domain yielded a record", name)
		}
		var rowErr *backup.RowError
		if errors.As(err, &rowErr) {
			t.Fatalf("%s: closed-domain error is row-scoped: %v", name, err)
		}
	}
	if count != 1 {
		t.Errorf("%s: closed-domain stream yielded %d times, want exactly 1 terminal error", name, count)
	}
}
