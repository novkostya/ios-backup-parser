// ibp-dump is the tiny development CLI (a debug tool, per the charter NOT a
// deliverable): it opens a reconstructed <Domain>/<relativePath> backup tree
// and streams a domain's records as JSON lines, for eyeballing and for the
// operator-local differential runs (testing ladder rung 3).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/calendar"
	"github.com/novkostya/ios-backup-parser/calls"
	"github.com/novkostya/ios-backup-parser/contacts"
	"github.com/novkostya/ios-backup-parser/messages"
	"github.com/novkostya/ios-backup-parser/notes"
	"github.com/novkostya/ios-backup-parser/safari"
)

type line struct {
	Type        string                  `json:"type"` // capability | person | group | call | message | chat | event | calendar | note | folder | bookmark | reading_list | visit | row_error | unavailable
	Capability  *backup.Capability      `json:"capability,omitempty"`
	Person      *contacts.Person        `json:"person,omitempty"`
	Group       *contacts.Group         `json:"group,omitempty"`
	Call        *calls.Call             `json:"call,omitempty"`
	Message     *messages.Message       `json:"message,omitempty"`
	Chat        *messages.Chat          `json:"chat,omitempty"`
	Event       *calendar.Event         `json:"event,omitempty"`
	Calendar    *calendar.Calendar      `json:"calendar,omitempty"`
	Note        *notes.Note             `json:"note,omitempty"`
	Folder      *notes.Folder           `json:"folder,omitempty"`
	Bookmark    *safari.Bookmark        `json:"bookmark,omitempty"`
	ReadingList *safari.ReadingListItem `json:"reading_list,omitempty"`
	Visit       *safari.Visit           `json:"visit,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

func main() {
	root := flag.String("root", "", "path to a decrypted <Domain>/<relativePath> backup tree")
	domain := flag.String("domain", "contacts", "domain to dump (contacts, calls, messages, calendar, notes, safari)")
	flag.Parse()
	if *root == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*root, *domain); err != nil {
		fmt.Fprintln(os.Stderr, "ibp-dump:", err)
		os.Exit(1)
	}
}

func run(root, domain string) error {
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		return err
	}
	defer func() { _ = fsys.Close() }()

	enc := json.NewEncoder(os.Stdout)
	switch domain {
	case "contacts":
		return dumpContacts(fsys, enc)
	case "calls":
		return dumpCalls(fsys, enc)
	case "messages":
		return dumpMessages(fsys, enc)
	case "calendar":
		return dumpCalendar(fsys, enc)
	case "notes":
		return dumpNotes(fsys, enc)
	case "safari":
		return dumpSafari(fsys, enc)
	default:
		return fmt.Errorf("unknown domain %q", domain)
	}
}

func dumpContacts(fsys backup.FS, enc *json.Encoder) error {
	c, err := contacts.Open(fsys)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	capability := c.Capability()
	if err := enc.Encode(line{Type: "capability", Capability: &capability}); err != nil {
		return err
	}
	for person, err := range c.People() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "person", Person: &person}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	for group, err := range c.Groups() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "group", Group: &group}); err != nil {
				return err
			}
		case errors.Is(err, backup.ErrUnavailable):
			if err := enc.Encode(line{Type: "note", Error: err.Error()}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func dumpCalls(fsys backup.FS, enc *json.Encoder) error {
	c, err := calls.Open(fsys)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	capability := c.Capability()
	if err := enc.Encode(line{Type: "capability", Capability: &capability}); err != nil {
		return err
	}
	for call, err := range c.Calls() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "call", Call: &call}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func dumpMessages(fsys backup.FS, enc *json.Encoder) error {
	m, err := messages.Open(fsys)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	capability := m.Capability()
	if err := enc.Encode(line{Type: "capability", Capability: &capability}); err != nil {
		return err
	}
	for message, err := range m.Messages() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "message", Message: &message}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	for chat, err := range m.Chats() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "chat", Chat: &chat}); err != nil {
				return err
			}
		case errors.Is(err, backup.ErrUnavailable):
			if err := enc.Encode(line{Type: "note", Error: err.Error()}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func dumpCalendar(fsys backup.FS, enc *json.Encoder) error {
	r, err := calendar.Open(fsys)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()

	capability := r.Capability()
	if err := enc.Encode(line{Type: "capability", Capability: &capability}); err != nil {
		return err
	}
	for event, err := range r.Events() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "event", Event: &event}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	for cal, err := range r.Calendars() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "calendar", Calendar: &cal}); err != nil {
				return err
			}
		case errors.Is(err, backup.ErrUnavailable):
			if err := enc.Encode(line{Type: "note", Error: err.Error()}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func dumpNotes(fsys backup.FS, enc *json.Encoder) error {
	n, err := notes.Open(fsys)
	if err != nil {
		return err
	}
	defer func() { _ = n.Close() }()

	capability := n.Capability()
	if err := enc.Encode(line{Type: "capability", Capability: &capability}); err != nil {
		return err
	}
	for note, err := range n.Notes() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "note", Note: &note}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	for folder, err := range n.Folders() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "folder", Folder: &folder}); err != nil {
				return err
			}
		case errors.Is(err, backup.ErrUnavailable):
			if err := enc.Encode(line{Type: "unavailable", Error: err.Error()}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func dumpSafari(fsys backup.FS, enc *json.Encoder) error {
	s, err := safari.Open(fsys)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	capability := s.Capability()
	if err := enc.Encode(line{Type: "capability", Capability: &capability}); err != nil {
		return err
	}
	for bm, err := range s.Bookmarks() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "bookmark", Bookmark: &bm}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	for item, err := range s.ReadingList() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "reading_list", ReadingList: &item}); err != nil {
				return err
			}
		case errors.Is(err, backup.ErrUnavailable):
			if err := enc.Encode(line{Type: "unavailable", Error: err.Error()}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	for visit, err := range s.History() {
		switch {
		case err == nil:
			if err := enc.Encode(line{Type: "visit", Visit: &visit}); err != nil {
				return err
			}
		case errors.Is(err, backup.ErrUnavailable):
			if err := enc.Encode(line{Type: "unavailable", Error: err.Error()}); err != nil {
				return err
			}
		case isRowError(err):
			if err := enc.Encode(line{Type: "row_error", Error: err.Error()}); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func isRowError(err error) bool {
	var rowErr *backup.RowError
	return errors.As(err, &rowErr)
}
