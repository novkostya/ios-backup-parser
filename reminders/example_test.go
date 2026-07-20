package reminders_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/reminders"
)

// Example streams reminders across every per-account store in the backup. Each
// Reminder names the Store it came from, so (Store, ID) is its identity — a bare
// ID repeats across stores. Titles and notes are plain columns (no blob decode);
// Completed/Flagged/Priority/Due surface the task state. A reminder's List and
// Account resolve with LEFT-JOIN semantics (nil when unavailable).
func Example() {
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	r, err := reminders.Open(fsys)
	if err != nil {
		log.Fatal(err) // eager: an unsupported schema fails here
	}
	defer func() { _ = r.Close() }()

	for rem, err := range r.Reminders() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			mark := " "
			if rem.Completed {
				mark = "x"
			}
			list := "(no list)"
			if rem.List != nil {
				list = rem.List.Name
			}
			fmt.Printf("[%s] %s — %s\n", mark, rem.Title, list)
		case errors.As(err, &rowErr):
			log.Printf("skipped row: %v", rowErr) // one bad row; stream continues
		default:
			log.Fatal(err) // stream-scoped: iteration ends
		}
	}
}
