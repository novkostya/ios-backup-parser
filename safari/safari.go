// Package safari streams typed Safari records out of an iOS backup: bookmarks and
// the reading list (HomeDomain, Library/Safari/Bookmarks.db) and browsing history
// (Library/Safari/History.db).
//
// Both stores are plain app SQLite (Safari's own schema). The domain spans two
// databases: Open reads Bookmarks.db (the primary store, which alone determines the
// safari.1 fingerprint) and, when present, History.db (an optional second store).
// Three streams:
//
//   - Bookmarks() — every bookmark and folder (the tree is self-referential via
//     Bookmark.Parent; folders vs leaves by Bookmark.IsFolder). Reading-list items are
//     excluded (they go to ReadingList).
//   - ReadingList() — the Reading List: leaf rows hanging off the com.apple.ReadingList
//     folder, discriminated by a non-NULL bookmarks.read column. Yields
//     backup.ErrUnavailable when the schema lacks that column ("reading_list" in
//     Capability.Missing) — Bookmarks() then emits every row instead.
//   - History() — one record per visit (history_visits) with its page URL joined from
//     history_items. Yields backup.ErrUnavailable when History.db is absent or its
//     schema is unrecognized ("history" in Capability.Missing).
//
// TWO EPOCHS, ONE DOMAIN. Bookmarks.db's last_modified is UNIX-epoch seconds while
// History.db's visit_time is COCOA 2001-epoch seconds — the wrong-but-plausible
// off-by-31-years trap this domain must not ship (docs/schemas/safari.md).
//
// Open validates the schema eagerly: an unrecognized Bookmarks.db fails with
// backup.ErrUnsupportedSchema before any iterator exists, and absent optional
// columns/tables degrade the Capability report instead of silently yielding empty
// fields. Iteration follows the shared error contract (see the backup package doc): a
// *backup.RowError is row-scoped and the stream continues; any other yielded error is
// stream-scoped and ends it.
package safari

import (
	"database/sql"
	"fmt"
	"io/fs"
	"iter"
	"slices"
	"strings"
	"time"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Domain, RelativePath and HistoryRelativePath locate the two Safari databases inside
// a backup; as a FileRef: backup.FileRef{Domain: Domain, RelativePath: RelativePath}.
const (
	Domain              = "HomeDomain"
	RelativePath        = "Library/Safari/Bookmarks.db"
	HistoryRelativePath = "Library/Safari/History.db"
)

// Reader is an open safari domain. It holds open handles to the materialized scratch
// copies of Bookmarks.db and (when present) History.db; Close releases both. It is
// named Reader — not Safari — to match the other domains' handle naming.
type Reader struct {
	bookmarks   *sql.DB
	history     *sql.DB // nil when History.db is absent or its schema is unrecognized
	capability  backup.Capability
	unavailable map[string]bool
}

// Open materializes Bookmarks.db out of fsys, introspects its schema and — when a
// supported fingerprint matches — returns the open domain, additionally opening
// History.db when it is present and recognized. An unrecognized Bookmarks.db fails
// with backup.ErrUnsupportedSchema (wrapped in *backup.UnsupportedSchemaError carrying
// the observed fingerprint); a backup without Bookmarks.db fails with fs.ErrNotExist.
// A missing or unrecognized History.db never fails Open — it degrades History() to
// backup.ErrUnavailable.
func Open(fsys backup.FS) (*Reader, error) {
	ok, err := fsys.Exists(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("safari: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("safari: backup has no %s/%s: %w", Domain, RelativePath, fs.ErrNotExist)
	}
	path, err := fsys.Materialize(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("safari: %w", err)
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("safari: %w", err)
	}
	result, err := introspect.Detect(db, bookmarksSpec)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	r := &Reader{
		bookmarks:   db,
		capability:  result.Capability,
		unavailable: result.Unavailable,
	}
	r.openHistory(fsys)
	return r, nil
}

// openHistory opens the optional second store. Any failure to get a usable, recognized
// History.db (absent, unreadable, or an unknown schema) leaves it unavailable rather
// than failing the domain — History is optional; Bookmarks is the primary store.
func (r *Reader) openHistory(fsys backup.FS) {
	if ok, err := fsys.Exists(Domain, HistoryRelativePath); err == nil && ok {
		if path, err := fsys.Materialize(Domain, HistoryRelativePath); err == nil {
			if hdb, err := sqlitedb.Open(path); err == nil {
				if _, derr := introspect.Detect(hdb, historySpec); derr == nil {
					r.history = hdb
					return
				}
				_ = hdb.Close()
			}
		}
	}
	r.markUnavailable("history")
}

// markUnavailable records unit as absent in both the unavailable set and the
// capability report (kept sorted and deduplicated). Used for the cross-file "history"
// unit, which lives in History.db rather than the introspected Bookmarks.db.
func (r *Reader) markUnavailable(unit string) {
	if r.unavailable == nil {
		r.unavailable = map[string]bool{}
	}
	r.unavailable[unit] = true
	missing := append(slices.Clone(r.capability.Missing), unit)
	slices.Sort(missing)
	r.capability.Missing = slices.Compact(missing)
}

// Capability returns the capability report produced at Open.
func (r *Reader) Capability() backup.Capability {
	capability := r.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes both underlying database handles. (The scratch copies themselves belong
// to the FS that materialized them.)
func (r *Reader) Close() error {
	err := r.bookmarks.Close()
	if r.history != nil {
		if herr := r.history.Close(); err == nil {
			err = herr
		}
	}
	return err
}

// Bookmarks streams every bookmark and folder in id order. Reading-list items are
// excluded and streamed by ReadingList — UNLESS the schema lacks the bookmarks.read
// discriminator ("reading_list" in Capability.Missing), in which case they cannot be
// told apart and every row is emitted here. See the package doc for the row-scoped vs
// stream-scoped error contract.
func (r *Reader) Bookmarks() iter.Seq2[Bookmark, error] {
	return func(yield func(Bookmark, error) bool) {
		row := &bookmarkRow{}
		sel := []string{"id", "parent", "type", "title", "url"}
		dest := []any{&row.id, &row.parent, &row.typ, &row.title, &row.url}
		col := func(unit, expr string, target any) {
			if !r.unavailable[unit] {
				sel = append(sel, expr)
				dest = append(dest, target)
			}
		}
		col("special", "special_id", &row.specialID)
		col("order", "order_index", &row.orderIndex)
		col("hidden", "hidden", &row.hidden)
		col("num_children", "num_children", &row.numChildren)
		col("modified", "last_modified", &row.lastModified)
		col("uuid", "external_uuid", &row.uuid)
		col("deleted", "deleted", &row.deleted)

		query := "SELECT " + strings.Join(sel, ", ") + " FROM bookmarks"
		// Reading-list items (bookmarks.read IS NOT NULL) belong to ReadingList; keep
		// them out here when we can tell them apart.
		if !r.unavailable["reading_list"] {
			query += " WHERE read IS NULL"
		}
		query += " ORDER BY id"

		rows, err := r.bookmarks.Query(query)
		if err != nil {
			yield(Bookmark{}, fmt.Errorf("safari: query bookmarks: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = bookmarkRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Bookmark{}, &backup.RowError{
					Domain: "safari", Table: "bookmarks", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			if !yield(row.bookmark(), nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Bookmark{}, fmt.Errorf("safari: read bookmarks: %w", err))
		}
	}
}

// ReadingList streams the Safari Reading List (bookmarks rows with a non-NULL read
// column) in id order. When the schema lacks that column ("reading_list" in
// Capability.Missing) the iterator yields backup.ErrUnavailable instead of a
// misleading empty stream.
func (r *Reader) ReadingList() iter.Seq2[ReadingListItem, error] {
	return func(yield func(ReadingListItem, error) bool) {
		if r.unavailable["reading_list"] {
			yield(ReadingListItem{}, fmt.Errorf("safari: reading list: %w", backup.ErrUnavailable))
			return
		}
		sel := []string{"id", "parent", "title", "url", "read"}
		var id, parent, read sql.NullInt64
		var title, url sql.NullString
		var lastModified sql.NullFloat64
		dest := []any{&id, &parent, &title, &url, &read}
		haveModified := !r.unavailable["modified"]
		if haveModified {
			sel = append(sel, "last_modified")
			dest = append(dest, &lastModified)
		}

		rows, err := r.bookmarks.Query(
			"SELECT " + strings.Join(sel, ", ") + " FROM bookmarks WHERE read IS NOT NULL ORDER BY id")
		if err != nil {
			yield(ReadingListItem{}, fmt.Errorf("safari: query reading list: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			id, parent, read = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
			title, url, lastModified = sql.NullString{}, sql.NullString{}, sql.NullFloat64{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(ReadingListItem{}, &backup.RowError{
					Domain: "safari", Table: "bookmarks", RowID: id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			item := ReadingListItem{
				ID:     id.Int64,
				Parent: parent.Int64,
				Title:  title.String,
				URL:    url.String,
				Read:   read.Int64 != 0,
			}
			if lastModified.Valid {
				item.LastModified = unixFromFloat(lastModified.Float64)
			}
			if !yield(item, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(ReadingListItem{}, fmt.Errorf("safari: read reading list: %w", err))
		}
	}
}

// History streams every browsing-history visit (history_visits) in id order, each with
// its page URL and aggregate visit count joined from history_items (LEFT JOIN). When
// History.db is absent or its schema is unrecognized ("history" in Capability.Missing)
// the iterator yields backup.ErrUnavailable instead of a misleading empty stream.
func (r *Reader) History() iter.Seq2[Visit, error] {
	return func(yield func(Visit, error) bool) {
		if r.history == nil {
			yield(Visit{}, fmt.Errorf("safari: history: %w", backup.ErrUnavailable))
			return
		}
		rows, err := r.history.Query(`
			SELECT v.id, v.visit_time, v.title, i.url, i.visit_count,
			       v.redirect_source, v.redirect_destination, v.origin
			FROM history_visits v
			LEFT JOIN history_items i ON i.id = v.history_item
			ORDER BY v.id`)
		if err != nil {
			yield(Visit{}, fmt.Errorf("safari: query history: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id, visitCount, redirectSrc, redirectDst, origin sql.NullInt64
			var visitTime sql.NullFloat64
			var title, url sql.NullString
			if err := rows.Scan(&id, &visitTime, &title, &url, &visitCount,
				&redirectSrc, &redirectDst, &origin); err != nil {
				if !yield(Visit{}, &backup.RowError{
					Domain: "safari", Table: "history_visits", RowID: id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			visit := Visit{
				ID:                  id.Int64,
				Title:               title.String,
				URL:                 url.String,
				VisitCount:          visitCount.Int64,
				RedirectSource:      redirectSrc.Int64,
				RedirectDestination: redirectDst.Int64,
				Origin:              origin.Int64,
			}
			if visitTime.Valid {
				visit.Time = cocoa.FromSecondsFloat(visitTime.Float64)
			}
			if !yield(visit, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Visit{}, fmt.Errorf("safari: read history: %w", err))
		}
	}
}

// bookmarkRow holds one scanned bookmarks row; only the columns selected for this
// database's capability are filled.
type bookmarkRow struct {
	id           sql.NullInt64
	parent       sql.NullInt64
	typ          sql.NullInt64
	specialID    sql.NullInt64
	orderIndex   sql.NullInt64
	numChildren  sql.NullInt64
	hidden       sql.NullInt64
	deleted      sql.NullInt64
	title        sql.NullString
	url          sql.NullString
	uuid         sql.NullString
	lastModified sql.NullFloat64 // Unix seconds, REAL
}

func (r *bookmarkRow) bookmark() Bookmark {
	b := Bookmark{
		ID:          r.id.Int64,
		Parent:      r.parent.Int64,
		Type:        r.typ.Int64,
		SpecialID:   r.specialID.Int64,
		Title:       r.title.String,
		URL:         r.url.String,
		OrderIndex:  r.orderIndex.Int64,
		NumChildren: r.numChildren.Int64,
		Hidden:      r.hidden.Valid && r.hidden.Int64 != 0,
		Deleted:     r.deleted.Valid && r.deleted.Int64 != 0,
		UUID:        r.uuid.String,
	}
	if r.lastModified.Valid {
		b.LastModified = unixFromFloat(r.lastModified.Float64)
	}
	return b
}

// unixFromFloat converts a UNIX-epoch timestamp in fractional seconds (REAL) to
// time.Time. bookmarks.last_modified is Unix seconds — NOT Cocoa, unlike History.db's
// visit_time and every other domain (docs/schemas/safari.md, the two-epoch trap). Kept
// local here rather than in internal/cocoa, which is Cocoa-only by design.
func unixFromFloat(s float64) time.Time {
	sec := int64(s)
	nsec := int64((s - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}
