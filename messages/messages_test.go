package messages_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/messages"
)

// rowErrorID is the fixture message whose handle reference is dangling (see
// BuildFixture): it must surface as a row-scoped error.
const rowErrorID = 8

func fixtureFS(t *testing.T, opt messages.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, messages.Domain, filepath.FromSlash(messages.RelativePath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	messages.BuildFixture(t, dbPath, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt messages.FixtureOptions) *messages.Messages {
	t.Helper()
	m, err := messages.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func assertFixtureParse(t *testing.T, m *messages.Messages) {
	t.Helper()
	expected := messages.ExpectedMessages()
	i := 0
	for msg, err := range m.Messages() {
		if i >= len(expected) {
			t.Fatalf("more messages than expected: %+v, %v", msg, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("message %d: got (%+v, %v), want a *backup.RowError", i, msg, err)
			}
			if rowErr.Domain != "messages" || rowErr.Table != "message" || rowErr.RowID != rowErrorID {
				t.Errorf("row error = %+v, want message rowid %d", rowErr, rowErrorID)
			}
		} else {
			if err != nil {
				t.Fatalf("message %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(msg, *want) {
				t.Errorf("message %d:\n got %+v\nwant %+v", i, msg, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("stream ended after %d messages, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func TestCapability(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	capability := m.Capability()
	want := backup.Capability{Domain: "messages", Supported: true, Schema: "messages.1"}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	assertFixtureParse(t, openFixture(t, messages.FixtureOptions{}))
}

// TestCommittedFixture parses the COMMITTED rung-1 artifact — proving the
// checked-in fixture, not just the in-memory builder, matches the parser.
func TestCommittedFixture(t *testing.T) {
	data, err := os.ReadFile(messages.CommittedFixturePath)
	if err != nil {
		t.Fatalf("committed fixture missing (%v) — run `make fixtures` and commit the result", err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, messages.Domain, filepath.FromSlash(messages.RelativePath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	m, err := messages.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	assertFixtureParse(t, m)
}

func TestChats(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	want := messages.ExpectedChats()
	i := 0
	for chat, err := range m.Chats() {
		if err != nil {
			t.Fatalf("chat %d: unexpected error %v", i, err)
		}
		if i >= len(want) {
			t.Fatalf("more chats than expected: %+v", chat)
		}
		if !reflect.DeepEqual(chat, want[i]) {
			t.Errorf("chat %d:\n got %+v\nwant %+v", i, chat, want[i])
		}
		i++
	}
	if i != len(want) {
		t.Errorf("got %d chats, want %d", i, len(want))
	}
}

func TestTapbackAndGroupHelpers(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	byID := map[int64]messages.Message{}
	for msg, err := range m.Messages() {
		if err != nil {
			continue
		}
		byID[msg.ID] = msg
	}
	if !byID[7].IsTapback() {
		t.Errorf("message 7 IsTapback() = false, want true (associated_message_type=%d)", byID[7].AssociatedType)
	}
	if byID[7].TapbackRemoved() {
		t.Errorf("message 7 TapbackRemoved() = true, want false (2000 range = added)")
	}
	if byID[1].IsTapback() {
		t.Errorf("message 1 IsTapback() = true, want false (ordinary message)")
	}
	for chat, err := range m.Chats() {
		if err != nil {
			t.Fatal(err)
		}
		if chat.ID == 2 && !chat.IsGroup() {
			t.Errorf("chat 2 IsGroup() = false, want true (style=%d)", chat.Style)
		}
		if chat.ID == 1 && chat.IsGroup() {
			t.Errorf("chat 1 IsGroup() = true, want false (style=%d)", chat.Style)
		}
	}
}

// TestBodyUndecodedSurfaces pins the Operator amendment: a message whose SOLE
// body source (attributedBody) fails to decode is yielded with BodyUndecoded
// set and Text=="" — never dropped, and never mistaken for an empty message.
func TestBodyUndecodedSurfaces(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	var found bool
	for msg, err := range m.Messages() {
		if err != nil {
			continue
		}
		if msg.ID == 9 {
			found = true
			if !msg.BodyUndecoded {
				t.Errorf("message 9 BodyUndecoded = false, want true")
			}
			if msg.Text != "" {
				t.Errorf("message 9 Text = %q, want empty", msg.Text)
			}
		}
		// No ordinarily-decoded message should carry the undecoded marker.
		if msg.ID != 9 && msg.BodyUndecoded {
			t.Errorf("message %d unexpectedly BodyUndecoded", msg.ID)
		}
	}
	if !found {
		t.Fatal("message 9 (undecodable body) was dropped from the stream")
	}
}

func TestDegradedSchema(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{
		DropTables: []string{"attachment", "message_attachment_join"},
		DropColumns: []string{
			"message.associated_message_type", "message.associated_message_guid",
			"message.date_edited", "message.date_retracted",
		},
	})
	capability := m.Capability()
	wantMissing := []string{"attachments", "edits", "tapbacks"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "messages.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}

	byID := map[int64]messages.Message{}
	for msg, err := range m.Messages() {
		if err != nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("degraded stream yielded a stream-scoped error %v", err)
			}
			continue
		}
		byID[msg.ID] = msg
	}
	if len(byID) != 9 { // 10 messages, the dangling-handle row withheld
		t.Fatalf("degraded stream yielded %d messages, want 9", len(byID))
	}
	if byID[1].Text != "Hello from the fixture" {
		t.Errorf("message 1 Text = %q", byID[1].Text)
	}
	if byID[7].AssociatedType != 0 {
		t.Errorf("message 7 AssociatedType = %d, want 0 (tapback columns dropped)", byID[7].AssociatedType)
	}
	if byID[5].Attachments != nil {
		t.Errorf("message 5 Attachments = %v, want nil (attachment tables dropped)", byID[5].Attachments)
	}
}

func TestChatsUnavailable(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{
		DropTables: []string{"chat", "chat_message_join", "chat_handle_join"},
	})
	if !slices.Contains(m.Capability().Missing, "chats") {
		t.Fatalf("Missing = %v, want it to contain \"chats\"", m.Capability().Missing)
	}
	// Chats() yields ErrUnavailable rather than a misleading empty stream.
	var got error
	for _, err := range m.Chats() {
		got = err
		break
	}
	if !errors.Is(got, backup.ErrUnavailable) {
		t.Errorf("Chats() first error = %v, want ErrUnavailable", got)
	}
	// Messages still stream; they simply carry no ChatIDs.
	for msg, err := range m.Messages() {
		if err != nil {
			continue
		}
		if msg.ChatIDs != nil {
			t.Errorf("message %d ChatIDs = %v, want nil (chat tables dropped)", msg.ID, msg.ChatIDs)
		}
	}
}

func TestUnsupportedSchema(t *testing.T) {
	// A required column absent (here message.date) is a different, unsupported
	// fingerprint — never a silent degradation.
	_, err := messages.Open(fixtureFS(t, messages.FixtureOptions{
		DropColumns: []string{"message.date"},
	}))
	if !errors.Is(err, backup.ErrUnsupportedSchema) {
		t.Fatalf("err = %v, want ErrUnsupportedSchema", err)
	}
	var unsupported *backup.UnsupportedSchemaError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T, want *UnsupportedSchemaError", err)
	}
	if unsupported.Fingerprint == "" {
		t.Error("observed fingerprint missing from the error")
	}
}

func TestOpenWithoutDatabase(t *testing.T) {
	fsys, err := backup.NewDirFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	if _, err := messages.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, err := range m.Messages() {
		count++
		if err == nil {
			t.Fatal("iteration over a closed domain yielded a record")
		}
		var rowErr *backup.RowError
		if errors.As(err, &rowErr) {
			t.Fatalf("closed-domain error is row-scoped: %v", err)
		}
	}
	if count != 1 {
		t.Errorf("closed-domain stream yielded %d times, want exactly 1 terminal error", count)
	}
}
