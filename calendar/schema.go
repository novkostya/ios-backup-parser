package calendar

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint calendar.1 — first observed on the iOS 18.x study backup; the full
// observed structure and its evidence live in docs/schemas/calendar.md. Identity
// is the introspected structure, never a version claim; detection checks
// table/column PRESENCE and unknown extra columns never disqualify (CalendarItem
// carries ~90 of them — travel time, junk status, structured data, …).
//
// The store is plain app SQLite (EventKit's own schema), so relations are by id
// convention rather than CoreData Z_PK indirection. Required is deliberately
// minimal: the CalendarItem anchor plus the columns without which an event would
// be misleading — its title, span, all-day flag, owning calendar, and the
// calendar_scale that separates ordinary events from the "gregorian" birthday
// items (excluded from the stream). Everything that can degrade honestly is an
// Optional unit whose absence lands its name in Capability.Missing.
var spec = introspect.Spec{
	Domain: "calendar",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "calendar.1",
			Required: introspect.Tables{
				"CalendarItem": {
					"ROWID", "summary", "start_date", "end_date",
					"all_day", "calendar_id", "calendar_scale",
				},
			},
			Optional: []introspect.Unit{
				{Name: "notes", Tables: introspect.Tables{"CalendarItem": {"description"}}},
				{Name: "entity_type", Tables: introspect.Tables{"CalendarItem": {"entity_type"}}},
				{Name: "timezone", Tables: introspect.Tables{"CalendarItem": {"start_tz", "end_tz"}}},
				{Name: "status", Tables: introspect.Tables{"CalendarItem": {"status"}}},
				{Name: "availability", Tables: introspect.Tables{"CalendarItem": {"availability"}}},
				{Name: "privacy", Tables: introspect.Tables{"CalendarItem": {"privacy_level"}}},
				{Name: "url", Tables: introspect.Tables{"CalendarItem": {"url"}}},
				{Name: "created", Tables: introspect.Tables{"CalendarItem": {"creation_date"}}},
				{Name: "modified", Tables: introspect.Tables{"CalendarItem": {"last_modified"}}},
				// conference_url_detected is the modern column iLEAPP prefers;
				// conference_url is read as a fallback when the value is NULL.
				{Name: "conference", Tables: introspect.Tables{"CalendarItem": {"conference_url_detected"}}},
				// Calendar + its account/store (Calendar.store_id → Store.ROWID).
				{Name: "calendar", Tables: introspect.Tables{
					"Calendar": {"ROWID", "store_id", "title", "color", "type", "sharing_status"},
					"Store":    {"ROWID", "name", "type"},
				}},
				// Primary location (CalendarItem.location_id → Location.ROWID).
				{Name: "location", Tables: introspect.Tables{
					"CalendarItem": {"location_id"},
					"Location":     {"ROWID", "title", "address", "latitude", "longitude"},
				}},
				// Organizer (CalendarItem.organizer_id → Participant, entity_type
				// 8) and invitees (Participant.owner_id → CalendarItem, entity_type
				// 7), each resolved against Identity (keyed by its implicit rowid).
				{Name: "attendees", Tables: introspect.Tables{
					"CalendarItem": {"organizer_id"},
					"Participant": {
						"ROWID", "entity_type", "owner_id", "email", "phone_number",
						"status", "role", "type", "is_self", "identity_id",
					},
					"Identity": {"display_name"},
				}},
				// Recurrence rules (Recurrence.owner_id → CalendarItem.ROWID).
				{Name: "recurrence", Tables: introspect.Tables{
					"Recurrence": {"ROWID", "frequency", "interval", "count", "end_date", "specifier", "owner_id"},
				}},
				// Alarms (Alarm.calendaritem_owner_id → CalendarItem.ROWID).
				{Name: "alarms", Tables: introspect.Tables{
					"Alarm": {"ROWID", "trigger_date", "trigger_interval", "type", "proximity", "calendaritem_owner_id"},
				}},
				// Attachments (Attachment.owner_id → CalendarItem.ROWID;
				// Attachment.file_id → AttachmentFile.ROWID).
				{Name: "attachments", Tables: introspect.Tables{
					"Attachment":     {"ROWID", "owner_id", "file_id"},
					"AttachmentFile": {"ROWID", "filename", "file_size", "url", "UUID"},
				}},
			},
		},
	},
}

// participantEntityAttendee is the Participant.entity_type of an event invitee.
// Cross-referenced from iLEAPP calendarAll.py (MIT — see NOTICE), which selects
// invitees WHERE entity_type = 7. The organizer is a separate participant
// (entity_type 8) reached directly by CalendarItem.organizer_id, so it needs no
// entity_type filter here.
const participantEntityAttendee = 7

// birthdayCalendarScale is the CalendarItem.calendar_scale value that marks a
// birthday item — a distinct kind (special date encoding) reported by iLEAPP's
// separate calendarBirthdays artifact and excluded from Events (see the package
// doc). Ordinary events carry NULL / any other scale.
const birthdayCalendarScale = "gregorian"
