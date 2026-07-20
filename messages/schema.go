package messages

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint messages.1 — first observed on the iOS 18.x study backup; the full
// observed structure and its evidence live in docs/schemas/messages.md.
// Identity is the introspected structure, never a version claim; detection
// checks table/column PRESENCE and unknown extra columns never disqualify (the
// message table carries ~100 columns — RCS, satellite, key-transparency,
// scheduled-send, … ).
//
// The store is plain app SQLite. Required is deliberately minimal: the message
// anchor and the columns without which a message record would be misleading (its
// identity, time, direction, remote party, and the two text sources). Everything
// that can degrade honestly is an Optional unit whose absence lands in
// Capability.Missing under its name — including whole sibling tables (handles,
// chats, attachments) and the iOS-18-era column groups.
//
// Note attributedBody is Optional, not Required: a schema without it degrades to
// text-only bodies (named in Missing as "attributed_text") rather than being
// rejected — honest degradation. On messages.1 it is present, so the study
// backup reports an empty Missing.
var spec = introspect.Spec{
	Domain: "messages",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "messages.1",
			Required: introspect.Tables{
				"message": {"ROWID", "guid", "text", "date", "is_from_me", "handle_id"},
			},
			Optional: []introspect.Unit{
				// The typedstream body blob — its absence means text-only bodies.
				{Name: "attributed_text", Tables: introspect.Tables{"message": {"attributedBody"}}},
				{Name: "service", Tables: introspect.Tables{"message": {"service"}}},
				{Name: "delivery", Tables: introspect.Tables{"message": {"date_read", "date_delivered"}}},
				// Tapbacks (reactions): the association columns.
				{Name: "tapbacks", Tables: introspect.Tables{
					"message": {"associated_message_type", "associated_message_guid"},
				}},
				// Custom-emoji tapbacks (iOS 18).
				{Name: "tapback_emoji", Tables: introspect.Tables{"message": {"associated_message_emoji"}}},
				// Edits / unsends.
				{Name: "edits", Tables: introspect.Tables{"message": {"date_edited", "date_retracted"}}},
				// Reply threads.
				{Name: "threads", Tables: introspect.Tables{"message": {"thread_originator_guid", "reply_to_guid"}}},
				// App / rich "balloon" messages.
				{Name: "app_messages", Tables: introspect.Tables{"message": {"balloon_bundle_id", "payload_data"}}},
				// System / group events.
				{Name: "group_events", Tables: introspect.Tables{
					"message": {"item_type", "group_title", "group_action_type"},
				}},
				// Sender/participant resolution: the handle reference table.
				{Name: "handles", Tables: introspect.Tables{
					"handle": {"ROWID", "id", "service", "country"},
				}},
				// Conversations: chat + the message and participant joins.
				{Name: "chats", Tables: introspect.Tables{
					"chat":              {"ROWID", "guid", "style", "chat_identifier", "service_name", "display_name", "room_name", "group_id"},
					"chat_message_join": {"chat_id", "message_id"},
					"chat_handle_join":  {"chat_id", "handle_id"},
				}},
				// Attachments: the attachment table, the join, and the message's
				// denormalized has-attachments cache (used to skip the join for
				// text-only messages).
				{Name: "attachments", Tables: introspect.Tables{
					"message":                 {"cache_has_attachments"},
					"attachment":              {"ROWID", "guid", "filename", "uti", "mime_type", "transfer_name", "total_bytes", "is_sticker"},
					"message_attachment_join": {"message_id", "attachment_id"},
				}},
			},
		},
	},
}
