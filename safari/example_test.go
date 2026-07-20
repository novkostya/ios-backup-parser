package safari_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/safari"
)

// Example streams Safari bookmarks, then the reading list and history. The domain
// spans two databases (Bookmarks.db + History.db); ReadingList and History yield
// backup.ErrUnavailable when their backing schema is absent, so a consumer tells
// "none present" from "cannot know". Note the two epochs: Bookmark.LastModified is
// Unix-based while Visit.Time is Cocoa-based (see the package doc).
func Example() {
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	r, err := safari.Open(fsys)
	if err != nil {
		log.Fatal(err) // eager: an unsupported schema fails here
	}
	defer func() { _ = r.Close() }()

	fmt.Println(r.Capability()) // {safari true safari.1 []}

	for bm, err := range r.Bookmarks() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			if !bm.IsFolder() {
				fmt.Println(bm.Title, bm.URL)
			}
		case errors.As(err, &rowErr):
			log.Printf("skipped row: %v", rowErr) // one bad row; stream continues
		default:
			log.Fatal(err) // stream-scoped: iteration ends
		}
	}

	for item, err := range r.ReadingList() {
		if errors.Is(err, backup.ErrUnavailable) {
			break // this backup's schema has no reading-list discriminator
		}
		if err == nil {
			fmt.Printf("[reading list] read=%t %s\n", item.Read, item.URL)
		}
	}

	for visit, err := range r.History() {
		if errors.Is(err, backup.ErrUnavailable) {
			break // no History.db in this backup
		}
		if err == nil {
			fmt.Printf("%s %s\n", visit.Time.Format("2006-01-02"), visit.URL)
		}
	}
}
