package calendar

import "time"

// Event is one CalendarItem row — a single calendar event — with its calendar,
// location, organizer, attendees, recurrence, alarms and attachments resolved.
//
// The store is plain app SQLite (EventKit's own schema). Reminders live in a
// separate store on modern iOS and are not read here; birthday items (a distinct
// kind with a special date encoding, marked by CalendarScale == "gregorian") are
// excluded from this stream — see the package doc. Fields backed by an absent
// optional column/table stay at their zero value AND the domain's
// Capability.Missing names them: check the capability report to tell "empty"
// from "cannot know".
type Event struct {
	// ID is CalendarItem.ROWID — the join anchor for attendees, alarms,
	// recurrence and attachments, and a stable per-backup identifier.
	ID int64 `json:"id"`

	// Summary is the event title (CalendarItem.summary); Notes is its
	// description (CalendarItem.description; "notes" in Capability.Missing when
	// the column is absent).
	Summary string `json:"summary,omitempty"`
	Notes   string `json:"notes,omitempty"`

	// StartDate / EndDate are the event span (CalendarItem.start_date /
	// end_date), Cocoa-epoch SECONDS columns stored as REAL. NOT nanoseconds —
	// that unit is the messages domain's alone (docs/schemas/README.md, the
	// cross-domain trap). EventKit uses far-past NEGATIVE and far-future sentinel
	// values for floating / all-day items, so a consumer must tolerate
	// out-of-range times rather than assume "recent".
	StartDate time.Time `json:"start_date,omitzero"`
	EndDate   time.Time `json:"end_date,omitzero"`

	// StartTZ / EndTZ are the paired time-zone columns (CalendarItem.start_tz /
	// end_tz), surfaced verbatim: an IANA name ("Europe/Moscow"), an offset form
	// ("GMT+0400"), or the sentinel "_float" for a floating (time-zone-less)
	// event — see Floating. Empty and "timezone" in Capability.Missing when the
	// schema lacks the columns. A naive UTC render ignores these and mis-places
	// all-day / floating events.
	StartTZ string `json:"start_tz,omitempty"`
	EndTZ   string `json:"end_tz,omitempty"`

	// AllDay reports CalendarItem.all_day == 1.
	AllDay bool `json:"all_day,omitempty"`

	// URL is CalendarItem.url (the event's own URL, e.g. an .ics source);
	// ConferenceURL is the detected video-conferencing link
	// (CalendarItem.conference_url_detected, falling back to conference_url).
	// Each "" and named in Capability.Missing ("url" / "conference") when absent.
	URL           string `json:"url,omitempty"`
	ConferenceURL string `json:"conference_url,omitempty"`

	// Status is CalendarItem.status verbatim; Availability is
	// CalendarItem.availability; PrivacyLevel is CalendarItem.privacy_level. Each
	// is surfaced RAW — their constant spaces are not interpreted in this
	// milestone (no MIT oracle covers them and the differential validates only
	// the raw code) — and named in Capability.Missing ("status" / "availability"
	// / "privacy") when the column is absent.
	Status       int64 `json:"status,omitempty"`
	Availability int64 `json:"availability,omitempty"`
	PrivacyLevel int64 `json:"privacy_level,omitempty"`

	// EntityType is CalendarItem.entity_type verbatim (observed as 2 for every
	// item on the calendar store) and CalendarScale is CalendarItem.calendar_scale
	// (empty for an ordinary event; the "gregorian" birthday items are excluded
	// from this stream). Both raw, to keep the events/birthdays split visible
	// rather than assumed.
	EntityType    int64  `json:"entity_type"`
	CalendarScale string `json:"calendar_scale,omitempty"`

	// Created / LastModified are Cocoa-SECONDS bookkeeping timestamps
	// (CalendarItem.creation_date / last_modified); zero when NULL or the schema
	// lacks the column ("created" / "modified" in Capability.Missing).
	Created      time.Time `json:"created,omitzero"`
	LastModified time.Time `json:"last_modified,omitzero"`

	// Calendar is the calendar (and its account/store) this event belongs to,
	// joined via CalendarItem.calendar_id; nil when the "calendar" unit is
	// unavailable or the reference does not resolve (matching iLEAPP's LEFT JOIN).
	Calendar *Calendar `json:"calendar,omitempty"`

	// Location is the event's primary location, joined via
	// CalendarItem.location_id; nil when absent, unresolved, or the "location"
	// unit is unavailable.
	Location *Location `json:"location,omitempty"`

	// Organizer is the meeting organizer, joined via CalendarItem.organizer_id
	// (a Participant with entity_type == 8) and its Identity; nil for an
	// unshared event or when the "attendees" unit is unavailable.
	Organizer *Attendee `json:"organizer,omitempty"`

	// Attendees are the invitees (Participant rows with entity_type == 7 whose
	// owner_id is this event), each resolved against Identity for a display name.
	// Empty for a solo event and when the "attendees" unit is unavailable.
	Attendees []Attendee `json:"attendees,omitempty"`

	// Recurrences are the event's recurrence rules (Recurrence rows via
	// owner_id); usually one, a slice because EventKit permits several. Empty for
	// a one-off event and when the "recurrence" unit is unavailable.
	Recurrences []Recurrence `json:"recurrences,omitempty"`

	// Alarms are the event's alarms (Alarm rows via calendaritem_owner_id).
	// Empty when none and when the "alarms" unit is unavailable.
	Alarms []Alarm `json:"alarms,omitempty"`

	// Attachments are the event's attachments (Attachment → AttachmentFile via
	// owner_id / file_id). Empty when none and when the "attachments" unit is
	// unavailable.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Floating reports whether the event is time-zone-less (a "floating" event),
// detected by the start_tz sentinel "_float" that EventKit stores for such
// items. Cross-referenced from iLEAPP's calendarAll.py (MIT, see NOTICE), which
// blanks the same sentinel.
func (e Event) Floating() bool {
	return e.StartTZ == floatTZSentinel
}

// floatTZSentinel is the start_tz / end_tz value EventKit uses for a floating
// (time-zone-less) event.
const floatTZSentinel = "_float"

// Calendar is one Calendar row — a calendar — with its account/store resolved.
type Calendar struct {
	// ID is Calendar.ROWID.
	ID int64 `json:"id"`

	// Title is Calendar.title; Color is Calendar.color (a hex-ish string).
	Title string `json:"title,omitempty"`
	Color string `json:"color,omitempty"`

	// Type is Calendar.type, a free TEXT kind that is frequently NULL; surfaced
	// verbatim, not interpreted.
	Type string `json:"type,omitempty"`

	// SharingStatus is Calendar.sharing_status verbatim. Cross-referenced from
	// iLEAPP's calendarAll.py (MIT, see NOTICE): 0 not shared, 1 shared by me,
	// 2 shared with me. Surfaced raw with the interpretation documented.
	SharingStatus int64 `json:"sharing_status,omitempty"`

	// Store is the account/source this calendar belongs to (Calendar.store_id →
	// Store); nil when unresolved.
	Store *Store `json:"store,omitempty"`
}

// Calendar.sharing_status interpretation (cross-referenced from iLEAPP
// calendarAll.py, MIT — see NOTICE).
const (
	SharingNotShared    = 0
	SharingSharedByMe   = 1
	SharingSharedWithMe = 2
)

// Store is the account/source of a calendar (Store row via Calendar.store_id).
type Store struct {
	// ID is Store.ROWID; Name is Store.name (the account name).
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`

	// Type is Store.type verbatim (the account kind — local, CalDAV,
	// subscribed, …); surfaced raw, its constant space not interpreted here.
	Type int64 `json:"type"`
}

// Location is one Location row attached to an event via CalendarItem.location_id.
type Location struct {
	// Title is Location.title (a place name); Address is Location.address.
	Title   string `json:"title,omitempty"`
	Address string `json:"address,omitempty"`

	// Latitude / Longitude are Location.latitude / longitude. The columns are
	// declared INTEGER but hold REAL coordinates (SQLite type affinity), so they
	// are read as floats. HasCoordinates reports whether a real fix is present
	// (both non-zero) — 0,0 is treated as "no coordinates", as iLEAPP does.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
}

// HasCoordinates reports whether the location carries a coordinate fix (both
// latitude and longitude non-zero). Mirrors iLEAPP's `if latitude and longitude`
// guard (0,0 is treated as absent).
func (l Location) HasCoordinates() bool {
	return l.Latitude != 0 && l.Longitude != 0
}

// Attendee is one Participant row — an event's organizer or invitee — resolved
// against Identity for a display name.
type Attendee struct {
	// Name is the resolved Identity.display_name; Email is Participant.email
	// (an address that may carry a "mailto:" prefix as stored); PhoneNumber is
	// Participant.phone_number. Empty fields are genuinely absent.
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`

	// Status is Participant.status verbatim (the invitation response); see the
	// AttendeeStatus constants, cross-referenced from iLEAPP calendarAll.py (MIT,
	// see NOTICE) and validated differentially. Role and Type are
	// Participant.role / type, surfaced RAW (their constant spaces are not
	// interpreted in this milestone).
	Status int64 `json:"status"`
	Role   int64 `json:"role,omitempty"`
	Type   int64 `json:"type,omitempty"`

	// IsSelf reports Participant.is_self == 1 (this attendee is the device
	// owner).
	IsSelf bool `json:"is_self,omitempty"`
}

// Participant.status interpretation (invitation response). Cross-referenced from
// iLEAPP's calendarAll.py (MIT, see NOTICE) and validated differentially. Note
// the schema uses two codes for "no response": 0 and 7.
const (
	AttendeeStatusNoResponse    = 0
	AttendeeStatusAccepted      = 1
	AttendeeStatusDeclined      = 2
	AttendeeStatusMaybe         = 3
	AttendeeStatusNoResponseAlt = 7
)

// Recurrence is one Recurrence row — a recurrence rule attached to an event via
// owner_id.
type Recurrence struct {
	// Frequency is Recurrence.frequency and Interval is Recurrence.interval,
	// surfaced RAW: the frequency constant space (the CalDAV/ICU enum) is not
	// interpreted in this milestone — no MIT oracle covers it and the
	// differential validates only the raw code. Count is Recurrence.count (0 =
	// unbounded / not count-limited).
	Frequency int64 `json:"frequency"`
	Interval  int64 `json:"interval,omitempty"`
	Count     int64 `json:"count,omitempty"`

	// EndDate is Recurrence.end_date (Cocoa SECONDS); zero when the rule has no
	// end date (unbounded or count-limited).
	EndDate time.Time `json:"end_date,omitzero"`

	// Specifier is Recurrence.specifier verbatim — an encoded rule detail string
	// EventKit stores; surfaced raw, not parsed.
	Specifier string `json:"specifier,omitempty"`
}

// Alarm is one Alarm row attached to an event via calendaritem_owner_id.
type Alarm struct {
	// TriggerDate is Alarm.trigger_date (Cocoa SECONDS) for an ABSOLUTE alarm;
	// zero for a relative one. TriggerInterval is Alarm.trigger_interval, the
	// offset in seconds relative to the event for a RELATIVE alarm (negative =
	// before the event).
	TriggerDate     time.Time `json:"trigger_date,omitzero"`
	TriggerInterval int64     `json:"trigger_interval,omitempty"`

	// Type is Alarm.type and Proximity is Alarm.proximity, surfaced RAW (display
	// vs email; none vs enter/leave region) — the constant spaces are not
	// interpreted in this milestone.
	Type      int64 `json:"type,omitempty"`
	Proximity int64 `json:"proximity,omitempty"`
}

// Attachment is one Attachment row joined to its AttachmentFile.
//
// No backup.FileRef is emitted: on the observed schema calendar attachments are
// server-side references (AttachmentFile.local_path is NULL; url is a mail
// content-id reference), so the file is not present in the backup and a FileRef
// would be a fabricated path — which the charter forbids. When a downloaded
// attachment carries an on-device path it is surfaced verbatim as LocalPath; a
// validated backup-path convention (and thus a FileRef) is deferred until a
// backup exercises one.
type Attachment struct {
	// Filename is AttachmentFile.filename; FileSize is AttachmentFile.file_size;
	// UUID is AttachmentFile.UUID; URL is AttachmentFile.url (a source/content
	// reference); LocalPath is AttachmentFile.local_path (the on-device path when
	// the file was downloaded, else empty).
	Filename  string `json:"filename,omitempty"`
	FileSize  int64  `json:"file_size,omitempty"`
	UUID      string `json:"uuid,omitempty"`
	URL       string `json:"url,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}
