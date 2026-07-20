package messages_test

import (
	"errors"
	"fmt"
	"log"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/messages"
)

// Example streams messages. Text is message.text when present, otherwise the
// text decoded from the typedstream attributedBody blob. An empty Text with
// BodyUndecoded set means the body is UNKNOWN (a blob that could not be
// decoded) — never conflate it with a genuinely empty (attachment-only or
// system) message.
func Example() {
	fsys, err := backup.NewDirFS("/path/to/decrypted-backup")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()

	m, err := messages.Open(fsys)
	if err != nil {
		log.Fatal(err) // eager: an unsupported schema fails here
	}
	defer func() { _ = m.Close() }()

	for msg, err := range m.Messages() {
		var rowErr *backup.RowError
		switch {
		case err == nil:
			switch {
			case msg.BodyUndecoded:
				fmt.Printf("[%s] <undecoded body>\n", msg.Service)
			case msg.IsFromMe:
				fmt.Printf("[%s] me: %s\n", msg.Service, msg.Text)
			default:
				fmt.Printf("[%s] %s\n", msg.Service, msg.Text)
			}
		case errors.As(err, &rowErr):
			log.Printf("skipped row: %v", rowErr) // one bad row; stream continues
		default:
			log.Fatal(err) // stream-scoped: iteration ends
		}
	}
}
