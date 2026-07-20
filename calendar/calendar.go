// Package calendar streams typed calendar-event records out of an iOS backup's
// Calendar database (HomeDomain, Library/Calendar/Calendar.sqlitedb).
//
// The store is plain app SQLite (EventKit's own schema). Events() flattens each
// CalendarItem into an Event and resolves its calendar/account, location,
// organizer, invitees, recurrence rules, alarms and attachments through the
// surrounding tables; Calendars() streams the calendar list. Two kinds of
// CalendarItem are deliberately NOT events and are excluded from Events():
// reminders (kept in a separate store on modern iOS, absent here) and birthday
// items (CalendarItem.calendar_scale == "gregorian", a distinct kind with a
// special date encoding — iLEAPP reports them as a separate artifact). The
// events/birthday split follows iLEAPP's calendarAll.py (MIT, see NOTICE):
// events are the rows whose calendar_scale is not "gregorian".
//
// Open validates the schema eagerly: an unrecognized structure fails with
// backup.ErrUnsupportedSchema before any iterator exists, and absent optional
// columns/tables degrade the Capability report instead of silently yielding
// empty fields. Iteration follows the shared error contract (see the backup
// package doc): a *backup.RowError is row-scoped and the stream continues; any
// other yielded error is stream-scoped and ends it. Descriptive references
// (calendar, location, organizer) resolve with LEFT-JOIN semantics — nil when a
// reference does not resolve — matching the oracle; they never withhold an event.
package calendar

import (
	"database/sql"
	"fmt"
	"io/fs"
	"iter"
	"slices"
	"strings"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Domain and RelativePath locate the calendar database inside a backup; as a
// FileRef: backup.FileRef{Domain: Domain, RelativePath: RelativePath}.
const (
	Domain       = "HomeDomain"
	RelativePath = "Library/Calendar/Calendar.sqlitedb"
)

// Reader is an open calendar domain. It holds an open handle to the materialized
// scratch copy of the database; Close releases it. (It is named Reader rather
// than Calendar because Calendar is the record type for one calendar in the
// list — see Calendars.)
type Reader struct {
	db          *sql.DB
	capability  backup.Capability
	unavailable map[string]bool
}

// Open materializes the Calendar database out of fsys, introspects its schema
// and — when a supported fingerprint matches — returns the open domain. An
// unrecognized structure fails with backup.ErrUnsupportedSchema (wrapped in
// *backup.UnsupportedSchemaError carrying the observed fingerprint); a backup
// without the database fails with fs.ErrNotExist.
func Open(fsys backup.FS) (*Reader, error) {
	ok, err := fsys.Exists(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("calendar: backup has no %s/%s: %w", Domain, RelativePath, fs.ErrNotExist)
	}
	path, err := fsys.Materialize(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	result, err := introspect.Detect(db, spec)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Reader{
		db:          db,
		capability:  result.Capability,
		unavailable: result.Unavailable,
	}, nil
}

// Capability returns the capability report produced at Open.
func (r *Reader) Capability() backup.Capability {
	capability := r.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes the underlying database handle. (The scratch copy itself belongs
// to the FS that materialized it.)
func (r *Reader) Close() error {
	return r.db.Close()
}

// Events streams every ordinary event (CalendarItem whose calendar_scale is not
// "gregorian") in ROWID order, with its children resolved. See the package doc
// for the row-scoped vs stream-scoped error contract.
func (r *Reader) Events() iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		children, err := r.loadChildren()
		if err != nil {
			yield(Event{}, fmt.Errorf("calendar: %w", err))
			return
		}

		row := &eventRow{}
		sel := []string{"ROWID", "summary", "start_date", "end_date", "all_day", "calendar_id", "calendar_scale"}
		dest := []any{&row.id, &row.summary, &row.startDate, &row.endDate, &row.allDay, &row.calendarID, &row.calendarScale}
		col := func(unit, expr string, target any) {
			if !r.unavailable[unit] {
				sel = append(sel, expr)
				dest = append(dest, target)
			}
		}
		col("notes", "description", &row.notes)
		col("entity_type", "entity_type", &row.entityType)
		col("timezone", "start_tz", &row.startTZ)
		col("timezone", "end_tz", &row.endTZ)
		col("status", "status", &row.status)
		col("availability", "availability", &row.availability)
		col("privacy", "privacy_level", &row.privacyLevel)
		col("url", "url", &row.url)
		col("created", "creation_date", &row.created)
		col("modified", "last_modified", &row.lastModified)
		col("conference", "conference_url_detected", &row.conferenceDetected)
		col("conference", "conference_url", &row.conferenceURL)
		col("location", "location_id", &row.locationID)
		col("attendees", "organizer_id", &row.organizerID)

		// calendar_scale is a required column, so the events filter is always
		// available. IS NOT correctly includes rows whose scale is NULL.
		query := "SELECT " + strings.Join(sel, ", ") +
			" FROM CalendarItem WHERE calendar_scale IS NOT '" + birthdayCalendarScale + "' ORDER BY ROWID"
		rows, err := r.db.Query(query)
		if err != nil {
			yield(Event{}, fmt.Errorf("calendar: query events: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = eventRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Event{}, &backup.RowError{
					Domain: "calendar", Table: "CalendarItem", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			event := row.event()
			children.attach(&event, row)
			if !yield(event, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Event{}, fmt.Errorf("calendar: read events: %w", err))
		}
	}
}

// Calendars streams every Calendar row with its account/store resolved. When the
// schema lacks the calendar tables ("calendar" in Capability.Missing) the
// iterator yields backup.ErrUnavailable instead of a misleading empty stream.
func (r *Reader) Calendars() iter.Seq2[Calendar, error] {
	return func(yield func(Calendar, error) bool) {
		if r.unavailable["calendar"] {
			yield(Calendar{}, fmt.Errorf("calendar: calendars: %w", backup.ErrUnavailable))
			return
		}
		stores, err := r.loadStores()
		if err != nil {
			yield(Calendar{}, fmt.Errorf("calendar: %w", err))
			return
		}
		rows, err := r.db.Query(
			"SELECT ROWID, store_id, title, color, type, sharing_status FROM Calendar ORDER BY ROWID")
		if err != nil {
			yield(Calendar{}, fmt.Errorf("calendar: query calendars: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id, storeID, sharing sql.NullInt64
			var title, color, typ sql.NullString
			if err := rows.Scan(&id, &storeID, &title, &color, &typ, &sharing); err != nil {
				if !yield(Calendar{}, &backup.RowError{
					Domain: "calendar", Table: "Calendar", RowID: id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			cal := Calendar{
				ID:            id.Int64,
				Title:         title.String,
				Color:         color.String,
				Type:          typ.String,
				SharingStatus: sharing.Int64,
			}
			if storeID.Valid {
				if s, ok := stores[storeID.Int64]; ok {
					cal.Store = &s
				}
			}
			if !yield(cal, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Calendar{}, fmt.Errorf("calendar: read calendars: %w", err))
		}
	}
}

// eventRow holds one scanned CalendarItem row; only the columns selected for
// this database's capability are filled.
type eventRow struct {
	id            sql.NullInt64
	summary       sql.NullString
	startDate     sql.NullFloat64 // Cocoa seconds, REAL
	endDate       sql.NullFloat64
	allDay        sql.NullInt64
	calendarID    sql.NullInt64
	calendarScale sql.NullString

	notes              sql.NullString
	entityType         sql.NullInt64
	startTZ            sql.NullString
	endTZ              sql.NullString
	status             sql.NullInt64
	availability       sql.NullInt64
	privacyLevel       sql.NullInt64
	url                sql.NullString
	created            sql.NullFloat64
	lastModified       sql.NullFloat64
	conferenceDetected sql.NullString
	conferenceURL      sql.NullString
	locationID         sql.NullInt64
	organizerID        sql.NullInt64
}

func (r *eventRow) event() Event {
	e := Event{
		ID:            r.id.Int64,
		Summary:       r.summary.String,
		Notes:         r.notes.String,
		StartTZ:       r.startTZ.String,
		EndTZ:         r.endTZ.String,
		AllDay:        r.allDay.Valid && r.allDay.Int64 != 0,
		URL:           r.url.String,
		ConferenceURL: conferenceURL(r.conferenceDetected, r.conferenceURL),
		Status:        r.status.Int64,
		Availability:  r.availability.Int64,
		PrivacyLevel:  r.privacyLevel.Int64,
		EntityType:    r.entityType.Int64,
		CalendarScale: r.calendarScale.String,
	}
	if r.startDate.Valid {
		e.StartDate = cocoa.FromSecondsFloat(r.startDate.Float64)
	}
	if r.endDate.Valid {
		e.EndDate = cocoa.FromSecondsFloat(r.endDate.Float64)
	}
	if r.created.Valid {
		e.Created = cocoa.FromSecondsFloat(r.created.Float64)
	}
	if r.lastModified.Valid {
		e.LastModified = cocoa.FromSecondsFloat(r.lastModified.Float64)
	}
	return e
}

// conferenceURL prefers the detected link and falls back to the stored one.
func conferenceURL(detected, stored sql.NullString) string {
	if detected.Valid && detected.String != "" {
		return detected.String
	}
	return stored.String
}

// children holds the per-iteration reference tables — bounded lookups preloaded
// once so the stream never issues a per-event child query. A nil map means the
// corresponding optional unit is unavailable. Nothing here outlives the iterator
// (the library holds no state between calls).
type children struct {
	calendars   map[int64]Calendar     // by Calendar.ROWID
	locations   map[int64]Location     // by Location.ROWID
	organizers  map[int64]Attendee     // Participant by ROWID (for organizer_id)
	attendees   map[int64][]Attendee   // entity_type 7, by owner_id
	recurrences map[int64][]Recurrence // by owner_id
	alarms      map[int64][]Alarm      // by calendaritem_owner_id
	attachments map[int64][]Attachment // by owner_id
}

func (r *Reader) loadChildren() (*children, error) {
	c := &children{}
	var err error
	if !r.unavailable["calendar"] {
		if c.calendars, err = r.loadCalendars(); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["location"] {
		if c.locations, err = r.loadLocations(); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["attendees"] {
		if c.organizers, c.attendees, err = r.loadAttendees(); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["recurrence"] {
		if c.recurrences, err = r.loadRecurrences(); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["alarms"] {
		if c.alarms, err = r.loadAlarms(); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["attachments"] {
		if c.attachments, err = r.loadAttachments(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// attach wires an event's resolved children in from the preloaded tables.
func (c *children) attach(e *Event, row *eventRow) {
	if c.calendars != nil && row.calendarID.Valid {
		if cal, ok := c.calendars[row.calendarID.Int64]; ok {
			e.Calendar = &cal
		}
	}
	if c.locations != nil && row.locationID.Valid {
		if loc, ok := c.locations[row.locationID.Int64]; ok {
			e.Location = &loc
		}
	}
	if c.organizers != nil && row.organizerID.Valid && row.organizerID.Int64 != 0 {
		if org, ok := c.organizers[row.organizerID.Int64]; ok {
			e.Organizer = &org
		}
	}
	if c.attendees != nil {
		e.Attendees = c.attendees[e.ID]
	}
	if c.recurrences != nil {
		e.Recurrences = c.recurrences[e.ID]
	}
	if c.alarms != nil {
		e.Alarms = c.alarms[e.ID]
	}
	if c.attachments != nil {
		e.Attachments = c.attachments[e.ID]
	}
}

// loadStores preloads the Store table keyed by ROWID.
func (r *Reader) loadStores() (map[int64]Store, error) {
	rows, err := r.db.Query("SELECT ROWID, name, type FROM Store")
	if err != nil {
		return nil, fmt.Errorf("load stores: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Store{}
	for rows.Next() {
		var id, typ sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&id, &name, &typ); err != nil {
			return nil, fmt.Errorf("load stores: %w", err)
		}
		out[id.Int64] = Store{ID: id.Int64, Name: name.String, Type: typ.Int64}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load stores: %w", err)
	}
	return out, nil
}

// loadCalendars preloads the Calendar table (with its Store) keyed by ROWID.
func (r *Reader) loadCalendars() (map[int64]Calendar, error) {
	stores, err := r.loadStores()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query("SELECT ROWID, store_id, title, color, type, sharing_status FROM Calendar")
	if err != nil {
		return nil, fmt.Errorf("load calendars: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Calendar{}
	for rows.Next() {
		var id, storeID, sharing sql.NullInt64
		var title, color, typ sql.NullString
		if err := rows.Scan(&id, &storeID, &title, &color, &typ, &sharing); err != nil {
			return nil, fmt.Errorf("load calendars: %w", err)
		}
		cal := Calendar{
			ID:            id.Int64,
			Title:         title.String,
			Color:         color.String,
			Type:          typ.String,
			SharingStatus: sharing.Int64,
		}
		if storeID.Valid {
			if s, ok := stores[storeID.Int64]; ok {
				cal.Store = &s
			}
		}
		out[id.Int64] = cal
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load calendars: %w", err)
	}
	return out, nil
}

// loadLocations preloads the Location table keyed by ROWID.
func (r *Reader) loadLocations() (map[int64]Location, error) {
	rows, err := r.db.Query("SELECT ROWID, title, address, latitude, longitude FROM Location")
	if err != nil {
		return nil, fmt.Errorf("load locations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Location{}
	for rows.Next() {
		var id sql.NullInt64
		var title, address sql.NullString
		var lat, long sql.NullFloat64
		if err := rows.Scan(&id, &title, &address, &lat, &long); err != nil {
			return nil, fmt.Errorf("load locations: %w", err)
		}
		out[id.Int64] = Location{
			Title:     title.String,
			Address:   address.String,
			Latitude:  lat.Float64,
			Longitude: long.Float64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load locations: %w", err)
	}
	return out, nil
}

// loadAttendees preloads Participant rows resolved against Identity, returning
// both a by-ROWID map (for organizer_id lookup) and invitees grouped by owner_id
// (Participant.entity_type == participantEntityAttendee).
func (r *Reader) loadAttendees() (map[int64]Attendee, map[int64][]Attendee, error) {
	identities, err := r.loadIdentities()
	if err != nil {
		return nil, nil, err
	}
	rows, err := r.db.Query(
		"SELECT ROWID, entity_type, owner_id, email, phone_number, status, role, type, is_self, identity_id FROM Participant ORDER BY ROWID")
	if err != nil {
		return nil, nil, fmt.Errorf("load participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byID := map[int64]Attendee{}
	byOwner := map[int64][]Attendee{}
	for rows.Next() {
		var id, entityType, ownerID, status, role, typ, isSelf, identityID sql.NullInt64
		var email, phone sql.NullString
		if err := rows.Scan(&id, &entityType, &ownerID, &email, &phone,
			&status, &role, &typ, &isSelf, &identityID); err != nil {
			return nil, nil, fmt.Errorf("load participants: %w", err)
		}
		a := Attendee{
			Email:       email.String,
			PhoneNumber: phone.String,
			Status:      status.Int64,
			Role:        role.Int64,
			Type:        typ.Int64,
			IsSelf:      isSelf.Valid && isSelf.Int64 != 0,
		}
		if identityID.Valid {
			a.Name = identities[identityID.Int64]
		}
		byID[id.Int64] = a
		if entityType.Int64 == participantEntityAttendee && ownerID.Valid {
			byOwner[ownerID.Int64] = append(byOwner[ownerID.Int64], a)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("load participants: %w", err)
	}
	return byID, byOwner, nil
}

// loadIdentities preloads Identity.display_name keyed by its implicit ROWID
// (Participant.identity_id → Identity.ROWID).
func (r *Reader) loadIdentities() (map[int64]string, error) {
	rows, err := r.db.Query("SELECT ROWID, display_name FROM Identity")
	if err != nil {
		return nil, fmt.Errorf("load identities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("load identities: %w", err)
		}
		out[id.Int64] = name.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load identities: %w", err)
	}
	return out, nil
}

// loadRecurrences preloads Recurrence rows grouped by owner_id.
func (r *Reader) loadRecurrences() (map[int64][]Recurrence, error) {
	rows, err := r.db.Query(
		"SELECT owner_id, frequency, interval, count, end_date, specifier FROM Recurrence ORDER BY owner_id, ROWID")
	if err != nil {
		return nil, fmt.Errorf("load recurrences: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]Recurrence{}
	for rows.Next() {
		var ownerID, frequency, interval, count sql.NullInt64
		var endDate sql.NullFloat64
		var specifier sql.NullString
		if err := rows.Scan(&ownerID, &frequency, &interval, &count, &endDate, &specifier); err != nil {
			return nil, fmt.Errorf("load recurrences: %w", err)
		}
		rec := Recurrence{
			Frequency: frequency.Int64,
			Interval:  interval.Int64,
			Count:     count.Int64,
			Specifier: specifier.String,
		}
		if endDate.Valid && endDate.Float64 != 0 {
			rec.EndDate = cocoa.FromSecondsFloat(endDate.Float64)
		}
		out[ownerID.Int64] = append(out[ownerID.Int64], rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load recurrences: %w", err)
	}
	return out, nil
}

// loadAlarms preloads Alarm rows grouped by calendaritem_owner_id.
func (r *Reader) loadAlarms() (map[int64][]Alarm, error) {
	rows, err := r.db.Query(
		"SELECT calendaritem_owner_id, trigger_date, trigger_interval, type, proximity FROM Alarm ORDER BY calendaritem_owner_id, ROWID")
	if err != nil {
		return nil, fmt.Errorf("load alarms: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]Alarm{}
	for rows.Next() {
		var ownerID, interval, typ, proximity sql.NullInt64
		var triggerDate sql.NullFloat64
		if err := rows.Scan(&ownerID, &triggerDate, &interval, &typ, &proximity); err != nil {
			return nil, fmt.Errorf("load alarms: %w", err)
		}
		alarm := Alarm{
			TriggerInterval: interval.Int64,
			Type:            typ.Int64,
			Proximity:       proximity.Int64,
		}
		if triggerDate.Valid && triggerDate.Float64 != 0 {
			alarm.TriggerDate = cocoa.FromSecondsFloat(triggerDate.Float64)
		}
		out[ownerID.Int64] = append(out[ownerID.Int64], alarm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load alarms: %w", err)
	}
	return out, nil
}

// loadAttachments preloads Attachment rows (joined to AttachmentFile) grouped by
// owner_id. A dangling file_id yields empty file metadata (LEFT JOIN), never a
// dropped attachment.
func (r *Reader) loadAttachments() (map[int64][]Attachment, error) {
	rows, err := r.db.Query(
		`SELECT a.owner_id, f.filename, f.file_size, f.UUID, f.url, f.local_path
		FROM Attachment a LEFT JOIN AttachmentFile f ON f.ROWID = a.file_id
		ORDER BY a.owner_id, a.ROWID`)
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]Attachment{}
	for rows.Next() {
		var ownerID, fileSize sql.NullInt64
		var filename, uuid, url, localPath sql.NullString
		if err := rows.Scan(&ownerID, &filename, &fileSize, &uuid, &url, &localPath); err != nil {
			return nil, fmt.Errorf("load attachments: %w", err)
		}
		out[ownerID.Int64] = append(out[ownerID.Int64], Attachment{
			Filename:  filename.String,
			FileSize:  fileSize.Int64,
			UUID:      uuid.String,
			URL:       url.String,
			LocalPath: localPath.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	return out, nil
}
