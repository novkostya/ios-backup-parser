package messages_test

import (
	"errors"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/messages"
)

// ChatMessages is the per-chat read path (quince#1531). What these cover is the paging
// contract — order, boundary, limit — because that is where an off-by-one drops or repeats a
// message, and both look like a rendering bug much later.

// rowScoped reports whether an error is the row-scoped kind the package contract says to skip
// and continue past. The fixture carries one deliberately — message 8 has a dangling handle
// reference — so a test that treats every error as fatal is testing its own harness rather than
// the reader.
func rowScoped(err error) bool {
	var re *backup.RowError
	return errors.As(err, &re)
}

func chatIDsOf(t *testing.T, m *messages.Messages, chat int64, before messages.ChatCursor, limit int) []int64 {
	t.Helper()
	var out []int64
	for msg, err := range m.ChatMessages(chat, before, limit) {
		if err != nil {
			if rowScoped(err) {
				continue
			}
			t.Fatalf("ChatMessages: %v", err)
		}
		out = append(out, msg.ID)
	}
	return out
}

func TestChatMessagesNewestFirst(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	var prev int64 = -1
	for msg, err := range m.ChatMessages(1, messages.ChatCursor{}, 0) {
		if err != nil {
			if rowScoped(err) {
				continue
			}
			t.Fatalf("ChatMessages: %v", err)
		}
		if prev != -1 && msg.Time.UnixNano() > prev {
			t.Errorf("message %d is newer than the one before it — not newest-first", msg.ID)
		}
		prev = msg.Time.UnixNano()
	}
}

// THE PAGING PROPERTY THAT MATTERS: walking in pages must visit exactly the same messages, in
// the same order, as reading the conversation in one go. An off-by-one at the boundary either
// repeats a message or drops one.
func TestChatMessagesPagingMatchesOneShot(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})

	whole := chatIDsOf(t, m, 1, messages.ChatCursor{}, 0)
	if len(whole) < 3 {
		t.Fatalf("fixture chat 1 has %d messages, need at least 3 to page", len(whole))
	}

	var paged []int64
	cursor := messages.ChatCursor{}
	for range 100 { // bounded: a cursor that fails to advance must not spin forever
		page := []messages.Message{}
		for msg, err := range m.ChatMessages(1, cursor, 2) {
			if err != nil {
				if rowScoped(err) {
					continue
				}
				t.Fatalf("ChatMessages: %v", err)
			}
			page = append(page, msg)
		}
		if len(page) == 0 {
			break
		}
		for _, msg := range page {
			paged = append(paged, msg.ID)
		}
		last := page[len(page)-1]
		cursor = messages.ChatCursor{At: last.Time, ID: last.ID}
	}

	if len(paged) != len(whole) {
		t.Fatalf("paged %d messages, one-shot returned %d", len(paged), len(whole))
	}
	for i := range whole {
		if paged[i] != whole[i] {
			t.Fatalf("page walk diverges at %d: got %d, want %d", i, paged[i], whole[i])
		}
	}
}

func TestChatMessagesLimitIsRespected(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	if got := len(chatIDsOf(t, m, 1, messages.ChatCursor{}, 2)); got != 2 {
		t.Errorf("limit 2 returned %d messages", got)
	}
	// limit 0 means no limit, which is what the one-shot read above relies on.
	if got := len(chatIDsOf(t, m, 1, messages.ChatCursor{}, 0)); got <= 2 {
		t.Errorf("limit 0 returned %d — it must not cap", got)
	}
}

// A conversation returns ITS OWN messages. The join is many-to-many, so a message in two chats
// must appear under both — and a message in neither must appear under nothing.
func TestChatMessagesIsScopedToItsConversation(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	one := chatIDsOf(t, m, 1, messages.ChatCursor{}, 0)
	two := chatIDsOf(t, m, 2, messages.ChatCursor{}, 0)

	if len(one) == 0 || len(two) == 0 {
		t.Fatalf("chat 1 has %d and chat 2 has %d messages; both must be non-empty", len(one), len(two))
	}
	// Fixture message 10 is deliberately in both (see fixture_test.go).
	inBoth := func(ids []int64) bool {
		for _, id := range ids {
			if id == 10 {
				return true
			}
		}
		return false
	}
	if !inBoth(one) || !inBoth(two) {
		t.Errorf("message 10 is in chats 1 and 2 but reached chat1=%v chat2=%v", inBoth(one), inBoth(two))
	}
}

// The memberships come back on the page path too — through the per-message query rather than
// the scan's prefetch, which is the branch quince#1531 added.
func TestChatMessagesCarriesMemberships(t *testing.T) {
	m := openFixture(t, messages.FixtureOptions{})
	for msg, err := range m.ChatMessages(2, messages.ChatCursor{}, 0) {
		if err != nil {
			if rowScoped(err) {
				continue
			}
			t.Fatalf("ChatMessages: %v", err)
		}
		if msg.ID == 10 {
			if len(msg.ChatIDs) != 2 {
				t.Errorf("message 10 ChatIDs = %v, want both memberships on the page path", msg.ChatIDs)
			}
			return
		}
	}
	t.Fatal("message 10 not reached through chat 2")
}
