package calendar

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint calendar.1
// (docs/schemas/calendar.md): the EventKit tables and column sets the parser
// reads, plus a few of CalendarItem's many other columns (travel_time,
// junk_status, has_attendees, …) so the fixture proves "unknown extra columns
// never disqualify". Identity is declared WITHOUT an explicit ROWID, mirroring
// the real schema — its rows rely on the implicit rowid, exactly as the parser's
// join does. Every inserted row is invented; nothing here derives from a real
// backup (charter privacy gate).
//
// TestWriteCommittedFixture regenerates the committed fixture
// (testdata/calendar.1.Calendar.sqlitedb) when FIXTURE_WRITE is set — via
// `make fixtures` — so the committed artifact and the round-trip tests are built
// from the same schema belief. Green fixtures do NOT prove correctness: the
// operator-local differential (rung 3) is what moves a fingerprint from
// fixture-only to validated.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// calendar1DDL: column definitions per table, first token = column name.
var calendar1DDL = map[string][]string{
	"CalendarItem": {
		"ROWID INTEGER PRIMARY KEY", "summary TEXT", "description TEXT",
		"start_date REAL", "start_tz TEXT", "end_date REAL", "end_tz TEXT",
		"all_day INTEGER", "calendar_id INTEGER", "location_id INTEGER",
		"organizer_id INTEGER", "status INTEGER", "availability INTEGER",
		"privacy_level INTEGER", "url TEXT", "creation_date REAL",
		"last_modified REAL", "conference_url TEXT", "conference_url_detected TEXT",
		"entity_type INTEGER", "calendar_scale TEXT",
		// Realistic extras — never read; prove they do not disqualify.
		"travel_time INTEGER", "junk_status INTEGER", "has_attendees INTEGER",
	},
	"Calendar": {
		"ROWID INTEGER PRIMARY KEY", "store_id INTEGER", "title TEXT", "color TEXT",
		"type TEXT", "sharing_status INTEGER", "external_id TEXT",
	},
	"Store": {
		"ROWID INTEGER PRIMARY KEY", "name TEXT", "type INTEGER", "persistent_id TEXT",
	},
	"Location": {
		// latitude/longitude declared INTEGER but hold REAL coordinates (SQLite
		// type affinity), exactly as observed — the parser reads them as floats.
		"ROWID INTEGER PRIMARY KEY", "title TEXT", "address TEXT",
		"latitude INTEGER", "longitude INTEGER", "item_owner_id INTEGER",
	},
	"Participant": {
		"ROWID INTEGER PRIMARY KEY", "entity_type INTEGER", "type INTEGER",
		"status INTEGER", "pending_status INTEGER", "role INTEGER",
		"identity_id INTEGER", "owner_id INTEGER", "email TEXT",
		"phone_number TEXT", "is_self INTEGER",
	},
	// Identity: NO explicit ROWID — implicit rowid, as observed.
	"Identity": {
		"display_name TEXT", "address TEXT", "first_name TEXT", "last_name TEXT",
	},
	"Recurrence": {
		"ROWID INTEGER PRIMARY KEY", "frequency INTEGER", "interval INTEGER",
		"count INTEGER", "end_date REAL", "specifier TEXT", "owner_id INTEGER",
	},
	"Alarm": {
		"ROWID INTEGER PRIMARY KEY", "trigger_date REAL", "trigger_interval INTEGER",
		"type INTEGER", "proximity INTEGER", "calendaritem_owner_id INTEGER",
	},
	"Attachment": {
		"ROWID INTEGER PRIMARY KEY", "owner_id INTEGER", "file_id INTEGER",
	},
	"AttachmentFile": {
		"ROWID INTEGER PRIMARY KEY", "filename TEXT", "file_size INTEGER",
		"url TEXT", "UUID TEXT", "local_path TEXT",
	},
}

// FixtureOptions degrade the built database for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables, e.g. Location.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "CalendarItem.status".
	DropColumns []string
}

// Fixture timestamps (Cocoa seconds, REAL — fractional on purpose, to exercise
// cocoa.FromSecondsFloat), shared with the expectations.
const (
	fxStart1    = 700000000.5
	fxEnd1      = 700003600.5
	fxCreated1  = 699900000.25
	fxModified1 = 699950000.75
	fxAlarmAbs1 = 699999100.0
	fxStart2    = 700100000.0
	fxEnd2      = 700186400.0
	fxStart5    = 700200000.0
	fxEnd5      = 700203600.0
)

// danglingCalendarID is an event's calendar_id with no Calendar row (soft-nil).
const danglingCalendarID = 99

// rowErrorEventID is the event whose start_date is a corrupt (non-numeric) value,
// forcing a scan error — the row-scoped defect.
const rowErrorEventID = 4

// BuildFixture writes a synthetic calendar database to path.
func BuildFixture(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	present := map[string]map[string]bool{}
	for table, defs := range calendar1DDL {
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

	// Accounts/stores.
	insert("Store", map[string]any{"ROWID": 1, "name": "iCloud", "type": 2})
	insert("Store", map[string]any{"ROWID": 2, "name": "On My iPhone", "type": 0})

	// Calendars.
	insert("Calendar", map[string]any{"ROWID": 1, "store_id": 1, "title": "Work", "color": "#FF0000", "sharing_status": SharingSharedByMe})
	insert("Calendar", map[string]any{"ROWID": 2, "store_id": 2, "title": "Personal", "color": "#00FF00", "sharing_status": SharingNotShared})

	// Identities (implicit rowid → 1,2,3 in insertion order).
	insert("Identity", map[string]any{"display_name": "Alex Organizer", "address": "mailto:alex@example.invalid"})
	insert("Identity", map[string]any{"display_name": "Blair Invitee", "address": "mailto:blair@example.invalid"})
	insert("Identity", map[string]any{"display_name": "Casey Invitee", "address": "mailto:casey@example.invalid"})

	// Location for event 1.
	insert("Location", map[string]any{"ROWID": 1, "title": "HQ", "address": "1 Example Way", "latitude": 37.33, "longitude": -122.03, "item_owner_id": 1})

	// Participants: organizer (entity_type 8) + two invitees (entity_type 7).
	insert("Participant", map[string]any{"ROWID": 10, "entity_type": 8, "type": 0, "status": AttendeeStatusAccepted, "role": 0, "identity_id": 1, "owner_id": 1, "email": "alex@example.invalid", "is_self": 1})
	insert("Participant", map[string]any{"ROWID": 11, "entity_type": participantEntityAttendee, "type": 1, "status": AttendeeStatusAccepted, "role": 1, "identity_id": 2, "owner_id": 1, "email": "blair@example.invalid"})
	insert("Participant", map[string]any{"ROWID": 12, "entity_type": participantEntityAttendee, "type": 1, "status": AttendeeStatusDeclined, "role": 1, "identity_id": 3, "owner_id": 1, "email": "casey@example.invalid"})

	// Recurrence for event 1 (count-limited, no end date).
	insert("Recurrence", map[string]any{"ROWID": 1, "frequency": 2, "interval": 1, "count": 10, "specifier": "FREQ=WEEKLY", "owner_id": 1})

	// Alarms for event 1: a relative one then an absolute one.
	insert("Alarm", map[string]any{"ROWID": 1, "trigger_interval": -900, "type": 0, "proximity": 0, "calendaritem_owner_id": 1})
	insert("Alarm", map[string]any{"ROWID": 2, "trigger_date": fxAlarmAbs1, "type": 1, "proximity": 0, "calendaritem_owner_id": 1})

	// Attachment for event 1 (server-side: local_path NULL).
	insert("AttachmentFile", map[string]any{"ROWID": 1, "filename": "agenda.pdf", "file_size": 1024, "url": "cid:agenda", "UUID": "uuid-att-1"})
	insert("Attachment", map[string]any{"ROWID": 1, "owner_id": 1, "file_id": 1})

	// Event 1 — fully populated.
	insert("CalendarItem", map[string]any{
		"ROWID": 1, "summary": "Team Sync", "description": "Quarterly planning",
		"start_date": fxStart1, "start_tz": "America/Los_Angeles", "end_date": fxEnd1, "end_tz": "America/Los_Angeles",
		"all_day": 0, "calendar_id": 1, "location_id": 1, "organizer_id": 10,
		"status": 1, "availability": 1, "privacy_level": 1, "url": "https://example.invalid/e1",
		"creation_date": fxCreated1, "last_modified": fxModified1,
		"conference_url": "https://old.example.invalid/x", "conference_url_detected": "https://meet.example.invalid/abc",
		"entity_type": 2, "has_attendees": 1,
	})

	// Event 2 — floating all-day event, minimal else.
	insert("CalendarItem", map[string]any{
		"ROWID": 2, "summary": "Holiday",
		"start_date": fxStart2, "start_tz": "_float", "end_date": fxEnd2, "end_tz": "_float",
		"all_day": 1, "calendar_id": 2, "entity_type": 2,
	})

	// Event 3 — birthday item (calendar_scale 'gregorian'): MUST be excluded.
	insert("CalendarItem", map[string]any{
		"ROWID": 3, "summary": "Birthday of Somebody",
		"start_date": fxStart2, "all_day": 1, "calendar_id": 2,
		"entity_type": 2, "calendar_scale": "gregorian",
	})

	// Event 4 — row-scoped defect: a non-numeric start_date forces a scan error.
	insert("CalendarItem", map[string]any{
		"ROWID": rowErrorEventID, "summary": "Corrupt", "start_date": "not-a-date",
		"end_date": fxEnd2, "all_day": 0, "calendar_id": 2, "entity_type": 2,
	})

	// Event 5 — minimal: required columns + a dangling calendar_id (soft-nil).
	insert("CalendarItem", map[string]any{
		"ROWID": 5, "summary": "Quick note",
		"start_date": fxStart5, "end_date": fxEnd5, "all_day": 0,
		"calendar_id": danglingCalendarID, "entity_type": 2,
	})
}

// storeICloud / storeLocal are the fixture accounts, reused across expectations.
func storeICloud() *Store { return &Store{ID: 1, Name: "iCloud", Type: 2} }
func storeLocal() *Store  { return &Store{ID: 2, Name: "On My iPhone", Type: 0} }

func calendarWork() *Calendar {
	return &Calendar{ID: 1, Title: "Work", Color: "#FF0000", SharingStatus: SharingSharedByMe, Store: storeICloud()}
}
func calendarPersonal() *Calendar {
	return &Calendar{ID: 2, Title: "Personal", Color: "#00FF00", SharingStatus: SharingNotShared, Store: storeLocal()}
}

// ExpectedEvents returns what parsing the default fixture must yield, in stream
// order (by ROWID). The entry for ROWID 4 is nil: that row yields a
// *backup.RowError (corrupt start_date) and the stream continues. The birthday
// (ROWID 3) never appears — it is filtered out by calendar_scale.
func ExpectedEvents() []*Event {
	return []*Event{
		{
			ID: 1, Summary: "Team Sync", Notes: "Quarterly planning",
			StartDate: cocoa.FromSecondsFloat(fxStart1), EndDate: cocoa.FromSecondsFloat(fxEnd1),
			StartTZ: "America/Los_Angeles", EndTZ: "America/Los_Angeles",
			URL: "https://example.invalid/e1", ConferenceURL: "https://meet.example.invalid/abc",
			Status: 1, Availability: 1, PrivacyLevel: 1, EntityType: 2,
			Created: cocoa.FromSecondsFloat(fxCreated1), LastModified: cocoa.FromSecondsFloat(fxModified1),
			Calendar: calendarWork(),
			Location: &Location{Title: "HQ", Address: "1 Example Way", Latitude: 37.33, Longitude: -122.03},
			Organizer: &Attendee{
				Name: "Alex Organizer", Email: "alex@example.invalid",
				Status: AttendeeStatusAccepted, Role: 0, Type: 0, IsSelf: true,
			},
			Attendees: []Attendee{
				{Name: "Blair Invitee", Email: "blair@example.invalid", Status: AttendeeStatusAccepted, Role: 1, Type: 1},
				{Name: "Casey Invitee", Email: "casey@example.invalid", Status: AttendeeStatusDeclined, Role: 1, Type: 1},
			},
			Recurrences: []Recurrence{{Frequency: 2, Interval: 1, Count: 10, Specifier: "FREQ=WEEKLY"}},
			Alarms: []Alarm{
				{TriggerInterval: -900, Type: 0, Proximity: 0},
				{TriggerDate: cocoa.FromSecondsFloat(fxAlarmAbs1), Type: 1, Proximity: 0},
			},
			Attachments: []Attachment{{Filename: "agenda.pdf", FileSize: 1024, UUID: "uuid-att-1", URL: "cid:agenda"}},
		},
		{
			ID: 2, Summary: "Holiday",
			StartDate: cocoa.FromSecondsFloat(fxStart2), EndDate: cocoa.FromSecondsFloat(fxEnd2),
			StartTZ: "_float", EndTZ: "_float", AllDay: true, EntityType: 2,
			Calendar: calendarPersonal(),
		},
		nil, // ROWID 4: *backup.RowError — corrupt start_date
		{
			ID: 5, Summary: "Quick note",
			StartDate: cocoa.FromSecondsFloat(fxStart5), EndDate: cocoa.FromSecondsFloat(fxEnd5),
			EntityType: 2, // calendar_id 99 does not resolve → Calendar stays nil
		},
	}
}

// ExpectedCalendars returns what Calendars() must yield from the default fixture,
// in ROWID order.
func ExpectedCalendars() []Calendar {
	return []Calendar{*calendarWork(), *calendarPersonal()}
}

// CommittedFixturePath is where `make fixtures` writes the rung-1 artifact.
const CommittedFixturePath = "testdata/calendar.1.Calendar.sqlitedb"

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
