package messages

import (
	"time"

	backup "github.com/novkostya/ios-backup-parser"
)

// Message is one row of the sms.db `message` table — a single message.
//
// The body follows the modern-iOS rule: message.text when non-empty, otherwise
// the plain text decoded from the typedstream `attributedBody` blob (see the
// internal/typedstream package). Fields backed by an absent optional column
// stay at their zero value AND the domain's Capability.Missing names them —
// check the capability report to tell "empty" from "cannot know".
type Message struct {
	// ID is message.ROWID; GUID is message.guid (the stable identity that
	// tapbacks, replies and threads reference).
	ID   int64  `json:"id"`
	GUID string `json:"guid"`

	// ChatIDs are the chat.ROWIDs this message belongs to (chat_message_join is
	// many-to-many — a message can appear in more than one chat). Empty when the
	// "chats" unit is unavailable (in Capability.Missing).
	ChatIDs []int64 `json:"chat_ids,omitempty"`

	// Time is the message time (message.date), a Cocoa-epoch NANOseconds column
	// (INTEGER). Messages is the ONLY domain in nanoseconds — the cross-domain
	// unit trap (docs/schemas/README.md); the parser converts with
	// cocoa.FromNanoseconds, never the seconds helpers.
	Time time.Time `json:"time,omitzero"`

	// Text is the message body: message.text when non-empty, else the decoded
	// attributedBody. Empty is legitimate for an attachment-only or system
	// message — distinguish it from an UNKNOWN body via BodyUndecoded.
	Text string `json:"text,omitempty"`

	// BodyUndecoded is true when text was the sole source (message.text
	// NULL/empty) AND its attributedBody blob could not be decoded: the body is
	// UNKNOWN, not empty. The message is still yielded (its metadata is intact),
	// but a caller must not treat Text=="" here as a genuinely empty message —
	// that would be the wrong-but-plausible failure the charter forbids. On a
	// correct parser + supported schema this is expected to be zero across a
	// backup (the differential asserts it).
	BodyUndecoded bool `json:"body_undecoded,omitempty"`

	// Service is message.service ("iMessage", "SMS"; "RCS" is schema-supported);
	// "" when the schema lacks the column ("service" in Capability.Missing).
	Service string `json:"service,omitempty"`

	// IsFromMe reports message.is_from_me == 1 (a sent message). For a sent
	// message Handle is normally nil (handle_id is 0 — the counterpart set lives
	// on the chat, not the message).
	IsFromMe bool `json:"is_from_me"`

	// Handle is the remote party (message.handle_id → handle) for a received
	// message; nil when the message is from me, when handle_id is 0, or when the
	// "handles" unit is unavailable.
	Handle *Handle `json:"handle,omitempty"`

	// DateRead and DateDelivered are Cocoa-NANOsecond delivery timestamps; zero
	// when unset or the "delivery" unit is absent.
	DateRead      time.Time `json:"date_read,omitzero"`
	DateDelivered time.Time `json:"date_delivered,omitzero"`

	// Attachments are the message's files (message_attachment_join →
	// attachment); empty for a text-only message and when the "attachments"
	// unit is unavailable.
	Attachments []Attachment `json:"attachments,omitempty"`

	// AssociatedType is message.associated_message_type verbatim: 0 for an
	// ordinary message, the 2000–2007 range for a tapback (reaction) added and
	// 3000–3007 for one removed (see IsTapback/TapbackRemoved and the constants
	// below — interpretation cross-referenced and validated differentially).
	// AssociatedGUID is the guid of the message this one reacts to
	// (message.associated_message_guid); AssociatedEmoji is the custom-emoji
	// reaction (message.associated_message_emoji, iOS 18).
	AssociatedType  int64  `json:"associated_type,omitempty"`
	AssociatedGUID  string `json:"associated_guid,omitempty"`
	AssociatedEmoji string `json:"associated_emoji,omitempty"`

	// DateEdited and DateRetracted (Cocoa NANOseconds) are set when a message
	// was edited or unsent; zero otherwise or when the "edits" unit is absent.
	DateEdited    time.Time `json:"date_edited,omitzero"`
	DateRetracted time.Time `json:"date_retracted,omitzero"`

	// ThreadOriginatorGUID / ReplyToGUID place the message in a reply thread;
	// "" when not a threaded reply or the "threads" unit is absent.
	ThreadOriginatorGUID string `json:"thread_originator_guid,omitempty"`
	ReplyToGUID          string `json:"reply_to_guid,omitempty"`

	// BalloonBundleID is message.balloon_bundle_id — set for an app / rich
	// "balloon" message (Apple Pay, polls, stickers, …) whose real content is in
	// the payload_data blob. HasPayload reports whether that blob is present.
	// The payload itself is not decoded in v0.1 (a message with a balloon may
	// carry no plain Text) — a caller reads these to recognize an app message.
	BalloonBundleID string `json:"balloon_bundle_id,omitempty"`
	HasPayload      bool   `json:"has_payload,omitempty"`

	// ItemType is message.item_type: 0 for an ordinary message, non-zero for a
	// system / group event (participant add/remove, name change, …). GroupTitle
	// and GroupActionType accompany group events. Surfaced raw — the non-zero
	// mapping is validated differentially, not guessed. Zero and in
	// Capability.Missing ("group_events") when the schema lacks the columns.
	ItemType        int64  `json:"item_type,omitempty"`
	GroupTitle      string `json:"group_title,omitempty"`
	GroupActionType int64  `json:"group_action_type,omitempty"`
}

// IsTapback reports whether the message is a tapback (reaction) rather than an
// ordinary message, by message.associated_message_type. The 2000–2007 (added)
// and 3000–3007 (removed) ranges were confirmed present on the study backup and
// cross-referenced against iLEAPP's sms.py (MIT, see NOTICE); the exact
// per-code meaning is validated differentially.
func (m Message) IsTapback() bool {
	t := m.AssociatedType
	return (t >= tapbackAddBase && t <= tapbackAddBase+7) ||
		(t >= tapbackRemoveBase && t <= tapbackRemoveBase+7)
}

// TapbackRemoved reports whether the message removes a previously-added tapback
// (the 3000 range) rather than adding one.
func (m Message) TapbackRemoved() bool {
	return m.AssociatedType >= tapbackRemoveBase && m.AssociatedType <= tapbackRemoveBase+7
}

// chat.style values (1:1 vs group). Confirmed present as {45, 43} on the study
// backup and cross-referenced against iLEAPP's sms.py (MIT, see NOTICE);
// validated differentially.
const (
	StyleGroup  = 43
	StyleDirect = 45
)

// message.associated_message_type range bases (tapbacks). Add = 2000-range,
// remove = 3000-range. See IsTapback.
const (
	tapbackAddBase    = 2000
	tapbackRemoveBase = 3000
)

// Handle is one `handle` row: a correspondent's messaging identity.
type Handle struct {
	// ID is handle.ROWID.
	ID int64 `json:"id"`
	// Identifier is handle.id — a phone number, email or Apple ID.
	Identifier string `json:"identifier"`
	// Service is handle.service ("iMessage" / "SMS" / …); Country is
	// handle.country (an ISO region), when present.
	Service string `json:"service,omitempty"`
	Country string `json:"country,omitempty"`
}

// Attachment is one `attachment` row joined to a message.
type Attachment struct {
	// ID is attachment.ROWID; GUID is attachment.guid.
	ID   int64  `json:"id"`
	GUID string `json:"guid,omitempty"`

	// File is a structured reference to the attachment's bytes in the backup:
	// attachment.filename is a "~/Library/SMS/Attachments/…" path that resolves
	// to MediaDomain with the leading "~/" stripped. File is NIL when
	// attachment.filename is NULL (not downloaded / purged / iCloud-only) — the
	// reference is genuinely absent; the parser reports that, never a fabricated
	// path.
	File *backup.FileRef `json:"file,omitempty"`

	// UTI (attachment.uti), MIMEType (attachment.mime_type) and TransferName
	// (attachment.transfer_name) describe the file; TotalBytes is
	// attachment.total_bytes; IsSticker reports attachment.is_sticker == 1.
	UTI          string `json:"uti,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	TransferName string `json:"transfer_name,omitempty"`
	TotalBytes   int64  `json:"total_bytes,omitempty"`
	IsSticker    bool   `json:"is_sticker,omitempty"`
}

// Chat is one `chat` row — a conversation (1:1 or group) — with its resolved
// participants.
type Chat struct {
	// ID is chat.ROWID; GUID is chat.guid.
	ID   int64  `json:"id"`
	GUID string `json:"guid"`

	// Identifier is chat.chat_identifier (the address or group id); DisplayName
	// is chat.display_name (a user-set group name, often empty); ServiceName is
	// chat.service_name.
	Identifier  string `json:"identifier,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	ServiceName string `json:"service_name,omitempty"`

	// Style is chat.style verbatim; see StyleDirect / StyleGroup and IsGroup.
	Style int64 `json:"style"`

	// RoomName is chat.room_name and GroupID is chat.group_id (group chats).
	RoomName string `json:"room_name,omitempty"`
	GroupID  string `json:"group_id,omitempty"`

	// Participants are the resolved handles of the conversation (chat_handle_join
	// → handle). Empty when the "handles" unit is unavailable.
	Participants []Handle `json:"participants,omitempty"`
}

// IsGroup reports whether the chat is a group conversation (chat.style ==
// StyleGroup). Rests on the style interpretation — confirmed on the study
// backup and validated differentially, not guessed.
func (c Chat) IsGroup() bool {
	return c.Style == StyleGroup
}
