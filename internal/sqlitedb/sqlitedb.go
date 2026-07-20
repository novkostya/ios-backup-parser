// Package sqlitedb opens materialized domain databases with the CGO-free
// modernc.org/sqlite driver.
//
// FS.Materialize hands the library a private, mutation-safe copy, so databases
// are opened with normal SQLite semantics: a live WAL or hot journal is
// recovered onto the copy — never onto the original backup.
package sqlitedb

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Open opens the SQLite database at path (a path returned by FS.Materialize)
// and verifies it is readable. The error surfaces eagerly so a corrupt or
// non-database file fails at domain open, not mid-stream.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Force a real open + header read; sql.Open alone is lazy.
	if _, err := db.Exec("SELECT 1 FROM sqlite_master LIMIT 1"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}
