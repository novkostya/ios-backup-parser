package messages

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint messages.1
// (docs/schemas/messages.md): the message/handle/chat/attachment tables and the
// three joins, plus a realistic superset of the message table's other columns
// (is_read, satellite/key-transparency flags) so the fixture proves "unknown
// extra columns never disqualify". The attributedBody blobs are REAL typedstream
// archives produced by internal/typedstream.EncodeAttributedString, so a
// build→encode→parse→compare round-trip exercises the decoder. Every inserted
// row is invented; nothing here derives from a real backup (charter privacy
// gate). Green fixtures do NOT prove correctness: the operator-local differential
// (rung 3) is what moves messages.1 from fixture-only to validated.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
	"github.com/novkostya/ios-backup-parser/internal/typedstream"
)

// messages1DDL: column definitions per table, first token = column name.
var messages1DDL = map[string][]string{
	"message": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "guid TEXT UNIQUE NOT NULL",
		"text TEXT", "attributedBody BLOB", "handle_id INTEGER DEFAULT 0",
		"service TEXT", "date INTEGER", "date_read INTEGER", "date_delivered INTEGER",
		"is_from_me INTEGER DEFAULT 0", "is_read INTEGER DEFAULT 0",
		"associated_message_type INTEGER DEFAULT 0", "associated_message_guid TEXT",
		"associated_message_emoji TEXT", "date_edited INTEGER", "date_retracted INTEGER",
		"thread_originator_guid TEXT", "reply_to_guid TEXT", "balloon_bundle_id TEXT",
		"payload_data BLOB", "item_type INTEGER DEFAULT 0", "group_title TEXT",
		"group_action_type INTEGER DEFAULT 0", "cache_has_attachments INTEGER DEFAULT 0",
		"is_pending_satellite_send INTEGER DEFAULT 0", "is_kt_verified INTEGER DEFAULT 0",
	},
	"handle": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "id TEXT NOT NULL", "service TEXT",
		"country TEXT", "uncanonicalized_id TEXT", "person_centric_id TEXT",
	},
	"chat": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "guid TEXT UNIQUE NOT NULL",
		"style INTEGER", "chat_identifier TEXT", "service_name TEXT",
		"display_name TEXT", "room_name TEXT", "group_id TEXT", "account_id TEXT",
	},
	"attachment": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "guid TEXT UNIQUE NOT NULL",
		"filename TEXT", "uti TEXT", "mime_type TEXT", "transfer_name TEXT",
		"total_bytes INTEGER DEFAULT 0", "is_sticker INTEGER DEFAULT 0",
		"hide_attachment INTEGER DEFAULT 0",
	},
	"chat_message_join":       {"chat_id INTEGER", "message_id INTEGER", "message_date INTEGER DEFAULT 0"},
	"chat_handle_join":        {"chat_id INTEGER", "handle_id INTEGER"},
	"message_attachment_join": {"message_id INTEGER", "attachment_id INTEGER"},
}

// FixtureOptions degrade the built database for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables, e.g. attachment.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "message.attributedBody".
	DropColumns []string
}

// Fixture timestamps (Cocoa NANOseconds — the messages-domain unit). date(i)
// increases with i so stream order (date, ROWID) equals insertion order.
const fixtureDateBase = 700_000_000_000_000_000

func fixtureDate(i int) int64 { return fixtureDateBase + int64(i)*1_000_000_000 }

const (
	fixtureRead      = fixtureDateBase + 2_500_000_000
	fixtureDelivered = fixtureDateBase + 2_200_000_000
	fixtureEdited    = fixtureDateBase + 2_800_000_000
)

// danglingHandlePK is a handle reference with no `handle` row.
const danglingHandlePK = 999

// longBody exceeds 127 bytes, exercising the typedstream multi-byte length tag.
var longBody = strings.Repeat("The quick brown fox jumped. ", 8)

// BuildFixture writes a synthetic sms.db to path.
func BuildFixture(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	present := map[string]map[string]bool{}
	for table, defs := range messages1DDL {
		if slices.Contains(opt.DropTables, table) {
			continue
		}
		var kept []string
		cols := map[string]bool{}
		for _, def := range defs {
			name := strings.Fields(def)[0]
			if slices.Contains(opt.DropColumns, table+"."+name) {
				continue
			}
			kept = append(kept, def)
			cols[name] = true
		}
		if _, err := db.Exec("CREATE TABLE " + table + " (" + strings.Join(kept, ", ") + ")"); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		present[table] = cols
	}

	insert := func(table string, row map[string]any) {
		t.Helper()
		cols, ok := present[table]
		if !ok {
			return
		}
		names := make([]string, 0, len(row))
		for name := range row {
			if cols[name] {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		values := make([]any, 0, len(names))
		marks := make([]string, 0, len(names))
		for _, name := range names {
			values = append(values, row[name])
			marks = append(marks, "?")
		}
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			table, strings.Join(names, ", "), strings.Join(marks, ", "))
		if _, err := db.Exec(query, values...); err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}

	// Handles.
	insert("handle", map[string]any{"ROWID": 1, "id": "+15550001", "service": "iMessage", "country": "us"})
	insert("handle", map[string]any{"ROWID": 2, "id": "+15550002", "service": "SMS", "country": "gb"})

	// Chats: chat 1 is 1:1 (style 45), chat 2 is a group (style 43).
	insert("chat", map[string]any{"ROWID": 1, "guid": "fx-chat-0001", "style": StyleDirect,
		"chat_identifier": "+15550001", "service_name": "iMessage"})
	insert("chat", map[string]any{"ROWID": 2, "guid": "fx-chat-0002", "style": StyleGroup,
		"chat_identifier": "chat-group-1", "service_name": "iMessage",
		"display_name": "Fixture Group", "room_name": "chat-group-1", "group_id": "grp-1"})
	insert("chat_handle_join", map[string]any{"chat_id": 1, "handle_id": 1})
	insert("chat_handle_join", map[string]any{"chat_id": 2, "handle_id": 1})
	insert("chat_handle_join", map[string]any{"chat_id": 2, "handle_id": 2})

	// Attachments: one with a filename (→ FileRef), one with filename NULL.
	insert("attachment", map[string]any{"ROWID": 1, "guid": "fx-att-0001",
		"filename": "~/Library/SMS/Attachments/aa/00/FX-GUID/photo.jpg",
		"uti":      "public.jpeg", "mime_type": "image/jpeg", "transfer_name": "photo.jpg",
		"total_bytes": 12345, "is_sticker": 0})
	insert("attachment", map[string]any{"ROWID": 2, "guid": "fx-att-0002",
		"filename": nil, "uti": "public.jpeg", "mime_type": "image/jpeg",
		"transfer_name": "clip.jpg", "total_bytes": 0, "is_sticker": 0})

	msg := func(row map[string]any) {
		insert("message", row)
	}
	link := func(chatID, messageID int) {
		insert("chat_message_join", map[string]any{"chat_id": chatID, "message_id": messageID})
	}

	// 1 — sent iMessage, body in the text column, 1:1.
	msg(map[string]any{"ROWID": 1, "guid": "fx-msg-0001", "text": "Hello from the fixture",
		"service": "iMessage", "date": fixtureDate(1), "is_from_me": 1, "handle_id": 0})
	link(1, 1)

	// 2 — received iMessage, body ONLY in attributedBody; edited + read/delivered.
	msg(map[string]any{"ROWID": 2, "guid": "fx-msg-0002",
		"attributedBody": typedstream.EncodeAttributedString("Reply via attributedBody"),
		"service":        "iMessage", "date": fixtureDate(2), "handle_id": 1,
		"date_read": fixtureRead, "date_delivered": fixtureDelivered, "date_edited": fixtureEdited})
	link(1, 2)

	// 3 — received, long body (multi-byte length), a threaded reply to msg 1.
	msg(map[string]any{"ROWID": 3, "guid": "fx-msg-0003",
		"attributedBody": typedstream.EncodeAttributedString(longBody),
		"service":        "iMessage", "date": fixtureDate(3), "handle_id": 1,
		"thread_originator_guid": "fx-msg-0001", "reply_to_guid": "fx-msg-0001"})
	link(1, 3)

	// 4 — group SMS, emoji body.
	msg(map[string]any{"ROWID": 4, "guid": "fx-msg-0004",
		"attributedBody": typedstream.EncodeAttributedString("Hi \U0001F44B group \U0001F30D"),
		"service":        "SMS", "date": fixtureDate(4), "handle_id": 2})
	link(2, 4)

	// 5 — group, attachment-only: body is just the U+FFFC placeholder (→ empty).
	msg(map[string]any{"ROWID": 5, "guid": "fx-msg-0005",
		"attributedBody": typedstream.EncodeAttributedString("￼"),
		"service":        "iMessage", "date": fixtureDate(5), "handle_id": 2, "cache_has_attachments": 1})
	link(2, 5)
	insert("message_attachment_join", map[string]any{"message_id": 5, "attachment_id": 1})

	// 6 — group, has a caption and an attachment whose filename is NULL.
	msg(map[string]any{"ROWID": 6, "guid": "fx-msg-0006", "text": "see attachment",
		"service": "iMessage", "date": fixtureDate(6), "handle_id": 2, "cache_has_attachments": 1})
	link(2, 6)
	insert("message_attachment_join", map[string]any{"message_id": 6, "attachment_id": 2})

	// 7 — a tapback (love, 2000) on msg 2.
	msg(map[string]any{"ROWID": 7, "guid": "fx-msg-0007", "text": "Loved a message",
		"service": "iMessage", "date": fixtureDate(7), "handle_id": 1,
		"associated_message_type": tapbackAddBase, "associated_message_guid": "fx-msg-0002"})
	link(1, 7)

	// 8 — row-scoped defect: a handle reference that points nowhere.
	msg(map[string]any{"ROWID": 8, "guid": "fx-msg-0008", "text": "orphan",
		"service": "iMessage", "date": fixtureDate(8), "handle_id": danglingHandlePK})
	link(1, 8)

	// 9 — sole-source attributedBody that fails to decode (truncated): the body
	// is UNKNOWN → BodyUndecoded, the message is still yielded.
	msg(map[string]any{"ROWID": 9, "guid": "fx-msg-0009",
		"attributedBody": typedstream.EncodeAttributedString("this will be truncated")[:20],
		"service":        "iMessage", "date": fixtureDate(9), "handle_id": 1})
	link(1, 9)

	// 10 — an app / balloon message: no plain text, payload present.
	msg(map[string]any{"ROWID": 10, "guid": "fx-msg-0010", "service": "iMessage",
		"date": fixtureDate(10), "is_from_me": 1,
		"balloon_bundle_id": "com.apple.messages.URLBalloonProvider",
		"payload_data":      []byte("fixture-payload")})
	link(1, 10)
}

var (
	fixtureHandle1 = Handle{ID: 1, Identifier: "+15550001", Service: "iMessage", Country: "us"}
	fixtureHandle2 = Handle{ID: 2, Identifier: "+15550002", Service: "SMS", Country: "gb"}
)

// ExpectedMessages returns what parsing the default fixture must yield, in stream
// order (date, ROWID). The entry for ROWID 8 is nil: that row yields a
// *backup.RowError (dangling handle) and the stream continues.
func ExpectedMessages() []*Message {
	h1, h2 := fixtureHandle1, fixtureHandle2
	return []*Message{
		{ID: 1, GUID: "fx-msg-0001", ChatIDs: []int64{1}, Time: cocoa.FromNanoseconds(fixtureDate(1)),
			Text: "Hello from the fixture", Service: "iMessage", IsFromMe: true},
		{ID: 2, GUID: "fx-msg-0002", ChatIDs: []int64{1}, Time: cocoa.FromNanoseconds(fixtureDate(2)),
			Text: "Reply via attributedBody", Service: "iMessage", Handle: &h1,
			DateRead: cocoa.FromNanoseconds(fixtureRead), DateDelivered: cocoa.FromNanoseconds(fixtureDelivered),
			DateEdited: cocoa.FromNanoseconds(fixtureEdited)},
		{ID: 3, GUID: "fx-msg-0003", ChatIDs: []int64{1}, Time: cocoa.FromNanoseconds(fixtureDate(3)),
			Text: longBody, Service: "iMessage", Handle: &h1,
			ThreadOriginatorGUID: "fx-msg-0001", ReplyToGUID: "fx-msg-0001"},
		{ID: 4, GUID: "fx-msg-0004", ChatIDs: []int64{2}, Time: cocoa.FromNanoseconds(fixtureDate(4)),
			Text: "Hi \U0001F44B group \U0001F30D", Service: "SMS", Handle: &h2},
		{ID: 5, GUID: "fx-msg-0005", ChatIDs: []int64{2}, Time: cocoa.FromNanoseconds(fixtureDate(5)),
			Text: "", Service: "iMessage", Handle: &h2, Attachments: []Attachment{{
				ID: 1, GUID: "fx-att-0001",
				File: &backup.FileRef{Domain: attachmentDomain, RelativePath: "Library/SMS/Attachments/aa/00/FX-GUID/photo.jpg"},
				UTI:  "public.jpeg", MIMEType: "image/jpeg", TransferName: "photo.jpg",
				TotalBytes: 12345,
			}}},
		{ID: 6, GUID: "fx-msg-0006", ChatIDs: []int64{2}, Time: cocoa.FromNanoseconds(fixtureDate(6)),
			Text: "see attachment", Service: "iMessage", Handle: &h2, Attachments: []Attachment{{
				ID: 2, GUID: "fx-att-0002", File: nil,
				UTI: "public.jpeg", MIMEType: "image/jpeg", TransferName: "clip.jpg",
			}}},
		{ID: 7, GUID: "fx-msg-0007", ChatIDs: []int64{1}, Time: cocoa.FromNanoseconds(fixtureDate(7)),
			Text: "Loved a message", Service: "iMessage", Handle: &h1,
			AssociatedType: tapbackAddBase, AssociatedGUID: "fx-msg-0002"},
		nil, // ROWID 8: *backup.RowError — dangling handle reference
		{ID: 9, GUID: "fx-msg-0009", ChatIDs: []int64{1}, Time: cocoa.FromNanoseconds(fixtureDate(9)),
			Text: "", BodyUndecoded: true, Service: "iMessage", Handle: &h1},
		{ID: 10, GUID: "fx-msg-0010", ChatIDs: []int64{1}, Time: cocoa.FromNanoseconds(fixtureDate(10)),
			Text: "", Service: "iMessage", IsFromMe: true,
			BalloonBundleID: "com.apple.messages.URLBalloonProvider", HasPayload: true},
	}
}

// ExpectedChats returns what parsing the default fixture's chats must yield.
func ExpectedChats() []Chat {
	h1, h2 := fixtureHandle1, fixtureHandle2
	return []Chat{
		{ID: 1, GUID: "fx-chat-0001", Identifier: "+15550001", ServiceName: "iMessage",
			Style: StyleDirect, Participants: []Handle{h1}},
		{ID: 2, GUID: "fx-chat-0002", Identifier: "chat-group-1", ServiceName: "iMessage",
			DisplayName: "Fixture Group", Style: StyleGroup, RoomName: "chat-group-1", GroupID: "grp-1",
			Participants: []Handle{h1, h2}},
	}
}

// CommittedFixturePath is where `make fixtures` writes the rung-1 artifact.
const CommittedFixturePath = "testdata/messages.1.sms.db"

func TestWriteCommittedFixture(t *testing.T) {
	if os.Getenv("FIXTURE_WRITE") == "" {
		t.Skip("set FIXTURE_WRITE=1 (make fixtures) to regenerate the committed fixture")
	}
	if err := os.MkdirAll(filepath.Dir(CommittedFixturePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(CommittedFixturePath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	BuildFixture(t, CommittedFixturePath, FixtureOptions{})
	t.Logf("wrote %s", CommittedFixturePath)
}
