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
)

type line struct {
	Type       string             `json:"type"` // capability | person | group | call | message | chat | event | calendar | row_error | note
	Capability *backup.Capability `json:"capability,omitempty"`
	Person     *contacts.Person   `json:"person,omitempty"`
	Group      *contacts.Group    `json:"group,omitempty"`
	Call       *calls.Call        `json:"call,omitempty"`
	Message    *messages.Message  `json:"message,omitempty"`
	Chat       *messages.Chat     `json:"chat,omitempty"`
	Event      *calendar.Event    `json:"event,omitempty"`
	Calendar   *calendar.Calendar `json:"calendar,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func main() {
	root := flag.String("root", "", "path to a decrypted <Domain>/<relativePath> backup tree")
	domain := flag.String("domain", "contacts", "domain to dump (contacts, calls, messages, calendar)")
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

func isRowError(err error) bool {
	var rowErr *backup.RowError
	return errors.As(err, &rowErr)
}
