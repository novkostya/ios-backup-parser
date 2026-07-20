package safari

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint safari.1
// (docs/schemas/safari.md): the Bookmarks.db `bookmarks` tree table and the two
// History.db tables the parser reads, plus a few columns the parser never reads
// (fetched_icon, dav_generation, load_successful) so the fixture proves "unknown extra
// columns never disqualify". Every inserted row is invented; nothing here derives from
// a real backup (charter privacy gate). The two epochs are exercised deliberately:
// bookmarks.last_modified holds UNIX seconds, history_visits.visit_time holds COCOA
// seconds — the two-epoch trap.
//
// TestWriteCommittedFixture regenerates the committed fixtures
// (testdata/safari.1.Bookmarks.db and testdata/safari.1.History.db) when FIXTURE_WRITE
// is set — via `make fixtures` — so the committed artifacts and the round-trip tests
// are built from the same schema belief. Green fixtures do NOT prove correctness: the
// operator-local differential (rung 3) is what moves a fingerprint from fixture-only to
// validated.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// bookmarksDDL / historyDDL: column definitions per table, first token = column name.
var bookmarksDDL = map[string][]string{
	"bookmarks": {
		"id INTEGER PRIMARY KEY AUTOINCREMENT", "special_id INTEGER DEFAULT 0",
		"parent INTEGER", "type INTEGER", "title TEXT", "url TEXT COLLATE NOCASE",
		"num_children INTEGER DEFAULT 0", "hidden INTEGER DEFAULT 0",
		"order_index INTEGER NOT NULL", "external_uuid TEXT UNIQUE",
		"read INTEGER DEFAULT NULL", "last_modified REAL DEFAULT NULL",
		"added INTEGER DEFAULT 1", "deleted INTEGER DEFAULT 0",
		"extra_attributes BLOB DEFAULT NULL",
		// Realistic extras — never read; prove they do not disqualify.
		"fetched_icon BOOL DEFAULT 0", "dav_generation INTEGER DEFAULT 0",
	},
	// Folder closure — present in the real schema, not parsed by v0.1.
	"folder_ancestors": {
		"id INTEGER PRIMARY KEY AUTOINCREMENT", "folder_id INTEGER NOT NULL",
		"ancestor_id INTEGER NOT NULL",
	},
}

var historyDDL = map[string][]string{
	"history_items": {
		"id INTEGER PRIMARY KEY AUTOINCREMENT", "url TEXT NOT NULL UNIQUE",
		"visit_count INTEGER NOT NULL", "domain_expansion TEXT",
		"visit_count_score INTEGER NOT NULL DEFAULT 0", // extra — not read
	},
	"history_visits": {
		"id INTEGER PRIMARY KEY AUTOINCREMENT", "history_item INTEGER NOT NULL",
		"visit_time REAL NOT NULL", "title TEXT",
		"redirect_source INTEGER", "redirect_destination INTEGER",
		"origin INTEGER NOT NULL DEFAULT 0",
		"load_successful BOOLEAN NOT NULL DEFAULT 1", // extra — not read
	},
}

// FixtureOptions degrade the built databases for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables, e.g. history_items.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "bookmarks.read".
	DropColumns []string
	// OmitHistory writes no History.db at all (→ History() unavailable).
	OmitHistory bool
}

// Fixture timestamps. Bookmarks are UNIX seconds (REAL); history is COCOA seconds
// (REAL). Fractional on purpose, to exercise the float converters.
const (
	fxUnixMod4 = 1600000000.5  // bookmark id 4 last_modified (Unix)
	fxUnixMod5 = 1610000000.25 // bookmark id 5
	fxUnixRL6  = 1566302073.5  // reading-list id 6
	fxUnixRL7  = 1566388888.75 // reading-list id 7
	fxCocoa1   = 700000000.5   // history visit 1 (Cocoa)
	fxCocoa2   = 700003600.5   // history visit 2
	fxCocoa3   = 700007200.0   // history visit 3
)

// rowErrorBookmarkID / rowErrorVisitID are the rows whose timestamp is a corrupt
// (non-numeric) value, forcing a scan error — the row-scoped defect.
const (
	rowErrorBookmarkID = 8
	rowErrorVisitID    = 4
)

// buildDB creates ddl's tables in a fresh database at path and returns an insert
// helper plus the present-columns map (so callers only insert existing columns).
func buildDB(t *testing.T, path string, ddl map[string][]string, opt FixtureOptions) (func(string, map[string]any), func()) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]map[string]bool{}
	for table, defs := range ddl {
		if slices.Contains(opt.DropTables, table) {
			continue
		}
		var kept []string
		cols := map[string]bool{}
		for _, def := range defs {
			name := strings.Fields(def)[0]
			if slices.Contains(opt.DropColumns, table+"."+name) {
				continue
			}
			kept = append(kept, def)
			cols[name] = true
		}
		if _, err := db.Exec("CREATE TABLE " + table + " (" + strings.Join(kept, ", ") + ")"); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		present[table] = cols
	}
	insert := func(table string, row map[string]any) {
		t.Helper()
		cols, ok := present[table]
		if !ok {
			return
		}
		names := make([]string, 0, len(row))
		for name := range row {
			if cols[name] {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		values := make([]any, 0, len(names))
		marks := make([]string, 0, len(names))
		for _, name := range names {
			values = append(values, row[name])
			marks = append(marks, "?")
		}
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			table, strings.Join(names, ", "), strings.Join(marks, ", "))
		if _, err := db.Exec(query, values...); err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}
	return insert, func() { _ = db.Close() }
}

// buildBookmarks writes a synthetic Bookmarks.db to path.
func buildBookmarks(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	insert, closeDB := buildDB(t, path, bookmarksDDL, opt)
	defer closeDB()

	// Tree: Root(0) ← BookmarksBar(1), ReadingList(2); Dev(3) under BookmarksBar.
	insert("bookmarks", map[string]any{"id": 0, "special_id": SpecialNone, "parent": nil, "type": bookmarkTypeFolder, "title": "Root", "order_index": 0, "num_children": 2, "external_uuid": "uuid-0"})
	insert("bookmarks", map[string]any{"id": 1, "special_id": SpecialBookmarksBar, "parent": 0, "type": bookmarkTypeFolder, "title": "BookmarksBar", "order_index": 0, "num_children": 3, "external_uuid": "uuid-1"})
	insert("bookmarks", map[string]any{"id": 2, "special_id": SpecialReadingList, "parent": 0, "type": bookmarkTypeFolder, "title": "com.apple.ReadingList", "order_index": 1, "num_children": 2, "external_uuid": "uuid-2"})
	insert("bookmarks", map[string]any{"id": 3, "special_id": SpecialNone, "parent": 1, "type": bookmarkTypeFolder, "title": "Dev", "order_index": 0, "num_children": 1, "external_uuid": "uuid-3"})

	// Ordinary bookmarks (read IS NULL).
	insert("bookmarks", map[string]any{"id": 4, "parent": 1, "type": bookmarkTypeLeaf, "title": "Example Bookmark", "url": "https://example.invalid/a", "order_index": 1, "external_uuid": "uuid-4", "last_modified": fxUnixMod4})
	insert("bookmarks", map[string]any{"id": 5, "parent": 3, "type": bookmarkTypeLeaf, "title": "Nested Bookmark", "url": "https://example.invalid/b", "order_index": 0, "hidden": 1, "external_uuid": "uuid-5", "last_modified": fxUnixMod5})

	// Reading-list items (read IS NOT NULL): one unread, one read.
	insert("bookmarks", map[string]any{"id": 6, "parent": 2, "type": bookmarkTypeLeaf, "title": "RL Unread", "url": "https://example.invalid/r1", "order_index": 0, "read": 0, "external_uuid": "uuid-6", "last_modified": fxUnixRL6, "extra_attributes": []byte("bplist00-invented")})
	insert("bookmarks", map[string]any{"id": 7, "parent": 2, "type": bookmarkTypeLeaf, "title": "RL Read", "url": "https://example.invalid/r2", "order_index": 1, "read": 1, "external_uuid": "uuid-7", "last_modified": fxUnixRL7, "extra_attributes": []byte("bplist00-invented")})

	// Row-scoped defect: a non-numeric last_modified forces a scan error.
	insert("bookmarks", map[string]any{"id": rowErrorBookmarkID, "parent": 1, "type": bookmarkTypeLeaf, "title": "Corrupt", "url": "https://example.invalid/c", "order_index": 2, "external_uuid": "uuid-8", "last_modified": "not-a-date"})
}

// buildHistory writes a synthetic History.db to path.
func buildHistory(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	insert, closeDB := buildDB(t, path, historyDDL, opt)
	defer closeDB()

	insert("history_items", map[string]any{"id": 1, "url": "https://example.invalid/a", "visit_count": 3})
	insert("history_items", map[string]any{"id": 2, "url": "https://example.invalid/b", "visit_count": 1})

	// Visit 1 → redirected TO visit 2; visit 2 ← redirected FROM visit 1.
	insert("history_visits", map[string]any{"id": 1, "history_item": 1, "visit_time": fxCocoa1, "title": "Visit A1", "redirect_destination": 2, "origin": OriginLocalDevice})
	insert("history_visits", map[string]any{"id": 2, "history_item": 1, "visit_time": fxCocoa2, "title": "Visit A2", "redirect_source": 1, "origin": OriginLocalDevice})
	insert("history_visits", map[string]any{"id": 3, "history_item": 2, "visit_time": fxCocoa3, "title": "Visit B1", "origin": OriginLocalDevice})
	// Row-scoped defect: a non-numeric visit_time forces a scan error.
	insert("history_visits", map[string]any{"id": rowErrorVisitID, "history_item": 2, "visit_time": "not-a-time", "title": "Corrupt", "origin": OriginLocalDevice})
}

// BuildFixture writes the synthetic Safari databases into a reconstructed backup tree
// rooted at root: <root>/HomeDomain/Library/Safari/{Bookmarks.db,History.db}.
func BuildFixture(t *testing.T, root string, opt FixtureOptions) {
	t.Helper()
	bpath := filepath.Join(root, Domain, filepath.FromSlash(RelativePath))
	if err := os.MkdirAll(filepath.Dir(bpath), 0o755); err != nil {
		t.Fatal(err)
	}
	buildBookmarks(t, bpath, opt)
	if !opt.OmitHistory {
		hpath := filepath.Join(root, Domain, filepath.FromSlash(HistoryRelativePath))
		buildHistory(t, hpath, opt)
	}
}

// ExpectedBookmarks returns what Bookmarks() must yield from the default fixture, in id
// order. The entry for id 8 is nil: that row yields a *backup.RowError (corrupt
// last_modified) and the stream continues. Reading-list items (6, 7) never appear here.
func ExpectedBookmarks() []*Bookmark {
	return []*Bookmark{
		{ID: 0, Parent: 0, Type: bookmarkTypeFolder, SpecialID: SpecialNone, Title: "Root", NumChildren: 2, UUID: "uuid-0"},
		{ID: 1, Parent: 0, Type: bookmarkTypeFolder, SpecialID: SpecialBookmarksBar, Title: "BookmarksBar", NumChildren: 3, UUID: "uuid-1"},
		{ID: 2, Parent: 0, Type: bookmarkTypeFolder, SpecialID: SpecialReadingList, Title: "com.apple.ReadingList", OrderIndex: 1, NumChildren: 2, UUID: "uuid-2"},
		{ID: 3, Parent: 1, Type: bookmarkTypeFolder, SpecialID: SpecialNone, Title: "Dev", NumChildren: 1, UUID: "uuid-3"},
		{ID: 4, Parent: 1, Type: bookmarkTypeLeaf, Title: "Example Bookmark", URL: "https://example.invalid/a", OrderIndex: 1, UUID: "uuid-4", LastModified: unixFromFloat(fxUnixMod4)},
		{ID: 5, Parent: 3, Type: bookmarkTypeLeaf, Title: "Nested Bookmark", URL: "https://example.invalid/b", Hidden: true, UUID: "uuid-5", LastModified: unixFromFloat(fxUnixMod5)},
		nil, // id 8: *backup.RowError — corrupt last_modified
	}
}

// ExpectedReadingList returns what ReadingList() must yield from the default fixture,
// in id order.
func ExpectedReadingList() []ReadingListItem {
	return []ReadingListItem{
		{ID: 6, Parent: 2, Title: "RL Unread", URL: "https://example.invalid/r1", Read: false, LastModified: unixFromFloat(fxUnixRL6)},
		{ID: 7, Parent: 2, Title: "RL Read", URL: "https://example.invalid/r2", Read: true, LastModified: unixFromFloat(fxUnixRL7)},
	}
}

// ExpectedHistory returns what History() must yield from the default fixture, in id
// order. The entry for id 4 is nil: that row yields a *backup.RowError (corrupt
// visit_time) and the stream continues.
func ExpectedHistory() []*Visit {
	return []*Visit{
		{ID: 1, Time: cocoa.FromSecondsFloat(fxCocoa1), Title: "Visit A1", URL: "https://example.invalid/a", VisitCount: 3, RedirectDestination: 2, Origin: OriginLocalDevice},
		{ID: 2, Time: cocoa.FromSecondsFloat(fxCocoa2), Title: "Visit A2", URL: "https://example.invalid/a", VisitCount: 3, RedirectSource: 1, Origin: OriginLocalDevice},
		{ID: 3, Time: cocoa.FromSecondsFloat(fxCocoa3), Title: "Visit B1", URL: "https://example.invalid/b", VisitCount: 1, Origin: OriginLocalDevice},
		nil, // id 4: *backup.RowError — corrupt visit_time
	}
}

// CommittedBookmarksFixture / CommittedHistoryFixture are where `make fixtures` writes
// the rung-1 artifacts.
const (
	CommittedBookmarksFixture = "testdata/safari.1.Bookmarks.db"
	CommittedHistoryFixture   = "testdata/safari.1.History.db"
)

func TestWriteCommittedFixture(t *testing.T) {
	if os.Getenv("FIXTURE_WRITE") == "" {
		t.Skip("set FIXTURE_WRITE=1 (make fixtures) to regenerate the committed fixture")
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{CommittedBookmarksFixture, CommittedHistoryFixture} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	buildBookmarks(t, CommittedBookmarksFixture, FixtureOptions{})
	buildHistory(t, CommittedHistoryFixture, FixtureOptions{})
	t.Logf("wrote %s and %s", CommittedBookmarksFixture, CommittedHistoryFixture)
}
