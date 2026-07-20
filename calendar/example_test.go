package calendar_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/calendar"
)

// Example streams calendar events. The open handle is calendar.Reader (the
// record type Calendar is one calendar in the list, embedded as Event.Calendar).
// EventKit uses far-past/negative sentinel dates for floating and all-day items
// — Event.Floating reports the timezone sentinel, so consumers do not treat
// those dates as ordinary.
func Example() {
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	r, err := calendar.Open(fsys)
	if err != nil {
		log.Fatal(err) // eager: an unsupported schema fails here
	}
	defer func() { _ = r.Close() }()

	for event, err := range r.Events() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			fmt.Printf("%s allday=%t %s\n",
				event.StartDate.Format("2006-01-02"), event.AllDay, event.Summary)
		case errors.As(err, &rowErr):
			log.Printf("skipped row: %v", rowErr) // one bad row; stream continues
		default:
			log.Fatal(err) // stream-scoped: iteration ends
		}
	}
}
