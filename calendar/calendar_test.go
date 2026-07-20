package calendar_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/calendar"
)

// fixtureFS builds a synthetic backup tree holding a calendar database built with
// opt and returns a DirFS over it.
func fixtureFS(t *testing.T, opt calendar.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, calendar.Domain, filepath.FromSlash(calendar.RelativePath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	calendar.BuildFixture(t, dbPath, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt calendar.FixtureOptions) *calendar.Reader {
	t.Helper()
	r, err := calendar.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// rowErrorEventID is the fixture event whose corrupt start_date must surface as a
// row-scoped error (mirrors the fixture builder's constant).
const rowErrorEventID = 4

func assertFixtureParse(t *testing.T, r *calendar.Reader) {
	t.Helper()
	expected := calendar.ExpectedEvents()
	i := 0
	for event, err := range r.Events() {
		if i >= len(expected) {
			t.Fatalf("more events than expected: %+v, %v", event, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("event %d: got (%+v, %v), want a *backup.RowError", i, event, err)
			}
			if rowErr.Domain != "calendar" || rowErr.Table != "CalendarItem" || rowErr.RowID != rowErrorEventID {
				t.Errorf("row error = %+v, want CalendarItem rowid %d", rowErr, rowErrorEventID)
			}
		} else {
			if err != nil {
				t.Fatalf("event %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(event, *want) {
				t.Errorf("event %d:\n got %+v\nwant %+v", i, event, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("stream ended after %d events, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func TestCapability(t *testing.T) {
	r := openFixture(t, calendar.FixtureOptions{})
	capability := r.Capability()
	want := backup.Capability{
		Domain:    "calendar",
		Supported: true,
		Schema:    "calendar.1",
	}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestEventsRoundTrip(t *testing.T) {
	assertFixtureParse(t, openFixture(t, calendar.FixtureOptions{}))
}

// TestCommittedFixture parses the COMMITTED rung-1 artifact — proving the
// checked-in fixture, not just the in-memory builder, matches the parser.
func TestCommittedFixture(t *testing.T) {
	data, err := os.ReadFile(calendar.CommittedFixturePath)
	if err != nil {
		t.Fatalf("committed fixture missing (%v) — run `make fixtures` and commit the result", err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, calendar.Domain, filepath.FromSlash(calendar.RelativePath))
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
	r, err := calendar.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	assertFixtureParse(t, r)
}

func TestCalendars(t *testing.T) {
	r := openFixture(t, calendar.FixtureOptions{})
	var got []calendar.Calendar
	for cal, err := range r.Calendars() {
		if err != nil {
			t.Fatalf("calendars stream error: %v", err)
		}
		got = append(got, cal)
	}
	want := calendar.ExpectedCalendars()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calendars:\n got %+v\nwant %+v", got, want)
	}
}

// TestBirthdayExcludedAndFloating checks the two events-stream invariants: the
// gregorian birthday item is filtered out, and the floating event is flagged.
func TestBirthdayExcludedAndFloating(t *testing.T) {
	r := openFixture(t, calendar.FixtureOptions{})
	seen := map[int64]calendar.Event{}
	for event, err := range r.Events() {
		if err != nil {
			continue // the corrupt row
		}
		seen[event.ID] = event
	}
	if _, ok := seen[3]; ok {
		t.Error("birthday item (ROWID 3, calendar_scale gregorian) leaked into Events()")
	}
	if ev, ok := seen[2]; !ok || !ev.Floating() {
		t.Errorf("event 2 Floating() = %v (present=%v), want floating", ok && ev.Floating(), ok)
	}
	if ev := seen[1]; ev.Floating() {
		t.Error("event 1 (real timezone) reported as floating")
	}
	if ev := seen[1]; !ev.Location.HasCoordinates() {
		t.Error("event 1 location should report coordinates")
	}
}

func TestDegradedSchema(t *testing.T) {
	r := openFixture(t, calendar.FixtureOptions{
		DropTables:  []string{"Location", "Recurrence", "Alarm", "Attachment"},
		DropColumns: []string{"CalendarItem.status", "CalendarItem.url"},
	})
	capability := r.Capability()
	wantMissing := []string{"alarms", "attachments", "location", "recurrence", "status", "url"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "calendar.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}

	byID := map[int64]calendar.Event{}
	for event, err := range r.Events() {
		if err != nil {
			continue // the corrupt row still errors independently
		}
		byID[event.ID] = event
	}
	if len(byID) != 3 { // events 1, 2, 5 (3 excluded, 4 errored)
		t.Fatalf("degraded stream yielded %d events, want 3", len(byID))
	}
	first := byID[1]
	if first.Location != nil || first.Recurrences != nil || first.Alarms != nil || first.Attachments != nil {
		t.Errorf("dropped units should be nil, got location=%v recur=%v alarms=%v attach=%v",
			first.Location, first.Recurrences, first.Alarms, first.Attachments)
	}
	if first.Status != 0 || first.URL != "" {
		t.Errorf("dropped columns should be zero, got status=%d url=%q", first.Status, first.URL)
	}
	// Units NOT dropped remain populated — never silently guessed away.
	if first.Calendar == nil || first.Organizer == nil || len(first.Attendees) != 2 {
		t.Errorf("surviving units degraded unexpectedly: %+v", first)
	}
}

func TestCalendarsUnavailable(t *testing.T) {
	r := openFixture(t, calendar.FixtureOptions{DropTables: []string{"Calendar", "Store"}})
	if !slices.Contains(r.Capability().Missing, "calendar") {
		t.Errorf("Missing = %v, want it to contain \"calendar\"", r.Capability().Missing)
	}
	// Calendars() yields ErrUnavailable rather than an empty stream.
	gotUnavailable := false
	for _, err := range r.Calendars() {
		if errors.Is(err, backup.ErrUnavailable) {
			gotUnavailable = true
			break
		}
		t.Fatalf("calendars over a schema without Calendar tables: unexpected %v", err)
	}
	if !gotUnavailable {
		t.Error("Calendars() did not yield ErrUnavailable")
	}
	// Events still stream, with Calendar left nil (never fabricated).
	for event, err := range r.Events() {
		if err != nil {
			continue
		}
		if event.Calendar != nil {
			t.Errorf("event %d has a Calendar despite the tables being absent", event.ID)
		}
	}
}

func TestUnsupportedSchema(t *testing.T) {
	// A required column absent (here start_date) is a different, unsupported
	// fingerprint — never a silent degradation.
	_, err := calendar.Open(fixtureFS(t, calendar.FixtureOptions{
		DropColumns: []string{"CalendarItem.start_date"},
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
	if _, err := calendar.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	r := openFixture(t, calendar.FixtureOptions{})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, err := range r.Events() {
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
