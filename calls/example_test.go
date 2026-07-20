package calls_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/calls"
)

// Example streams the call history. Time is a Cocoa-epoch value in SECONDS
// (unlike messages, which is in nanoseconds); Missed() derives from direction
// and answered.
func Example() {
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	c, err := calls.Open(fsys)
	if err != nil {
		log.Fatal(err) // eager: an unsupported schema fails here
	}
	defer func() { _ = c.Close() }()

	for call, err := range c.Calls() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			fmt.Printf("%s %s missed=%t %s\n",
				call.Time.Format("2006-01-02 15:04"), call.Address, call.Missed(), call.Duration)
		case errors.As(err, &rowErr):
			log.Printf("skipped row: %v", rowErr) // one bad row; stream continues
		default:
			log.Fatal(err) // stream-scoped: iteration ends
		}
	}
}
