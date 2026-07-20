package contacts_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/contacts"
)

// Example shows the shape every domain in this library shares: open a decrypted
// backup as a backup.FS, open the domain (schema validation is EAGER — an
// unsupported schema fails here, before any iterator exists), read the
// capability report, then stream records while keeping the two error scopes
// distinct. The other domains (calls, messages, calendar, notes) follow the
// same pattern.
func Example() {
	// A decrypted backup laid out as <root>/<Domain>/<relativePath>. The
	// original tree is never opened by SQLite: Open reads private, mutation-safe
	// copies that DirFS.Close removes.
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	c, err := contacts.Open(fsys)
	if err != nil {
		// An unsupported schema is reported here, eagerly. Distinguish it with
		// errors.Is(err, backup.ErrUnsupportedSchema); errors.As unwraps the
		// *backup.UnsupportedSchemaError to read the observed fingerprint.
		log.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// The capability report: which fingerprint was detected and which fields
	// this particular backup's schema cannot provide (e.g. ["photo"]).
	capability := c.Capability()
	fmt.Printf("schema %s, missing %v\n", capability.Schema, capability.Missing)

	for person, err := range c.People() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			fmt.Println(person.First, person.Last, person.Phones)
		case errors.As(err, &rowErr):
			// One defective row (a dangling reference, corrupt content): the
			// record is withheld and the stream CONTINUES — one bad row must
			// not hide a hundred thousand good ones.
			log.Printf("skipped %s rowid %d: %v", rowErr.Table, rowErr.RowID, rowErr.Err)
		default:
			// Stream-scoped: the database itself stopped reading. Iteration ends.
			log.Fatal(err)
		}
	}
}
