package notes_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/notes"
)

// Example streams notes. Body is decoded from the gzip+protobuf ZICNOTEDATA
// blob (there is no plain text column). A locked note is REPORTED, never
// decrypted: it is yielded with Locked set and an empty Body (and is not
// flagged BodyUndecoded). A present-but-undecodable blob sets BodyUndecoded —
// the body is unknown, never a silent empty.
func Example() {
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	n, err := notes.Open(fsys)
	if err != nil {
		log.Fatal(err) // eager: an unsupported schema fails here
	}
	defer func() { _ = n.Close() }()

	for note, err := range n.Notes() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			switch {
			case note.Locked:
				fmt.Printf("%s [locked] hint=%q\n", note.Title, note.PasswordHint)
			case note.BodyUndecoded:
				fmt.Printf("%s <undecoded body>\n", note.Title)
			default:
				fmt.Printf("%s: %s\n", note.Title, note.Body)
			}
		case errors.As(err, &rowErr):
			log.Printf("skipped row: %v", rowErr) // one bad row; stream continues
		default:
			log.Fatal(err) // stream-scoped: iteration ends
		}
	}
}
