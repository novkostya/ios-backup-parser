package reminders_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/reminders"
)

// rowErrorReminderID is the cloud-store reminder whose ZCREATIONDATE is corrupt
// (see BuildFixture): it must surface as a row-scoped error, not end the stream.
const rowErrorReminderID = 14

// fixtureFS builds a synthetic backup tree holding the two reminder stores built
// with opt and returns a DirFS over it (DirFS implements backup.ReadDirFS, so the
// stores are enumerated).
func fixtureFS(t *testing.T, opt reminders.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	reminders.BuildFixture(t, root, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt reminders.FixtureOptions) *reminders.Reader {
	t.Helper()
	r, err := reminders.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// assertFixtureParse iterates Reminders() and checks each record against
// ExpectedReminders. A nil expectation marks the row-scoped defect. Passing this
// also proves the per-store Z_ENT resolution: the two stores use different
// non-standard ordinals, so a hard-coded (or store-shared) ordinal would drop a
// store's reminders.
func assertFixtureParse(t *testing.T, r *reminders.Reader) {
	t.Helper()
	expected := reminders.ExpectedReminders()
	i := 0
	for rem, err := range r.Reminders() {
		if i >= len(expected) {
			t.Fatalf("more reminders than expected: %+v, %v", rem, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("reminder %d: got (%+v, %v), want a *backup.RowError", i, rem, err)
			}
			if rowErr.Domain != "reminders" || rowErr.Table != "ZREMCDREMINDER" || rowErr.RowID != rowErrorReminderID {
				t.Errorf("row error = %+v, want ZREMCDREMINDER rowid %d", rowErr, rowErrorReminderID)
			}
		} else {
			if err != nil {
				t.Fatalf("reminder %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(rem, *want) {
				t.Errorf("reminder %d:\n got %+v\nwant %+v", i, rem, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("stream ended after %d reminders, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func TestCapability(t *testing.T) {
	r := openFixture(t, reminders.FixtureOptions{})
	capability := r.Capability()
	want := backup.Capability{Domain: "reminders", Supported: true, Schema: "reminders.1"}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestRemindersRoundTrip(t *testing.T) {
	assertFixtureParse(t, openFixture(t, reminders.FixtureOptions{}))
}

// TestCommittedFixture parses the COMMITTED rung-1 artifacts (both stores),
// reconstructing the tree — proving the checked-in fixture, not just the
// in-memory builder, matches the parser.
func TestCommittedFixture(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, reminders.Domain, filepath.FromSlash(reminders.StoresDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The committed files carry flat names; place them at the real store names.
	for committed, name := range map[string]string{
		reminders.CommittedCloudFixture: reminders.CloudStoreName(),
		reminders.CommittedLocalFixture: reminders.LocalStore,
	} {
		data, err := os.ReadFile(committed)
		if err != nil {
			t.Fatalf("committed fixture %s missing (%v) — run `make fixtures` and commit the result", committed, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	r, err := reminders.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	assertFixtureParse(t, r)
}

// TestStoreNamespacing: both stores hold a reminder with Z_PK 10; they must
// surface as two distinct reminders keyed by Store, never merged or shadowed.
func TestStoreNamespacing(t *testing.T) {
	r := openFixture(t, reminders.FixtureOptions{})
	var id10 []reminders.Reminder
	for rem, err := range r.Reminders() {
		if err != nil {
			continue
		}
		if rem.ID == 10 {
			id10 = append(id10, rem)
		}
	}
	if len(id10) != 2 {
		t.Fatalf("found %d reminders with ID 10, want 2 (one per store)", len(id10))
	}
	if id10[0].Store == id10[1].Store {
		t.Errorf("both ID-10 reminders share Store %q — namespacing failed", id10[0].Store)
	}
	if id10[0].Title == id10[1].Title {
		t.Errorf("ID-10 reminders have the same title %q — a store shadowed the other", id10[0].Title)
	}
}

func TestLists(t *testing.T) {
	r := openFixture(t, reminders.FixtureOptions{})
	var got []reminders.List
	for list, err := range r.Lists() {
		if err != nil {
			t.Fatalf("lists yielded error %v", err)
		}
		got = append(got, list)
	}
	if want := reminders.ExpectedLists(); !reflect.DeepEqual(got, want) {
		t.Errorf("lists:\n got %+v\nwant %+v", got, want)
	}
}

// TestRecurrenceAndAssignment spot-checks the raw, documented-to-validate fields.
func TestRecurrenceAndAssignment(t *testing.T) {
	r := openFixture(t, reminders.FixtureOptions{})
	byKey := map[int64]reminders.Reminder{}
	for rem, err := range r.Reminders() {
		if err != nil {
			continue
		}
		if rem.Store == reminders.CloudStoreName() {
			byKey[rem.ID] = rem
		}
	}
	if rec := byKey[10].Recurrence; rec == nil || rec.Frequency != 2 || rec.Interval != 1 {
		t.Errorf("reminder 10 recurrence = %+v, want frequency 2 interval 1", rec)
	}
	if a := byKey[11].Assignee; a != "Sam Rivera" {
		t.Errorf("reminder 11 assignee = %q, want %q", a, "Sam Rivera")
	}
	if p := byKey[13].ParentID; p != 10 {
		t.Errorf("reminder 13 parent = %d, want 10 (subtask)", p)
	}
}

// TestNoReadDirFSFallback: a host that does NOT implement backup.ReadDirFS is
// served best-effort — only the fixed-name Data-local.sqlite is read, and
// "cloudkit_stores" lands in Capability.Missing (never a silent partial read).
func TestNoReadDirFSFallback(t *testing.T) {
	dfs := fixtureFS(t, reminders.FixtureOptions{})
	r, err := reminders.Open(noReadDir{dfs})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	if got := r.Capability().Missing; !reflect.DeepEqual(got, []string{"cloudkit_stores"}) {
		t.Errorf("Missing = %v, want [cloudkit_stores]", got)
	}
	var titles []string
	for rem, err := range r.Reminders() {
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if rem.Store != reminders.LocalStore {
			t.Errorf("fallback read a non-local store: %q", rem.Store)
		}
		titles = append(titles, rem.Title)
	}
	if !reflect.DeepEqual(titles, []string{"Water plants"}) {
		t.Errorf("fallback reminders = %v, want just the local store's [Water plants]", titles)
	}
}

// noReadDir wraps a DirFS but exposes only the base FS methods, hiding ReadDir —
// modeling a host that has not adopted the optional ReadDirFS capability.
type noReadDir struct{ inner *backup.DirFS }

func (f noReadDir) Materialize(domain, rel string) (string, error) {
	return f.inner.Materialize(domain, rel)
}
func (f noReadDir) Exists(domain, rel string) (bool, error) { return f.inner.Exists(domain, rel) }

func TestDegradedSchema(t *testing.T) {
	r := openFixture(t, reminders.FixtureOptions{
		DropColumns: []string{
			"ZREMCDREMINDER.ZNOTES",
			"ZREMCDREMINDER.ZDUEDATE",
			"ZREMCDBASELIST.ZNAME", // drops the whole "lists" unit
			"ZREMCDOBJECT.ZFREQUENCY",
		},
	})
	capability := r.Capability()
	wantMissing := []string{"due", "lists", "notes", "recurrence"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "reminders.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}

	// Reminders still parse; dropped fields stay zero, lists no longer resolve.
	for rem, err := range r.Reminders() {
		if err != nil {
			continue // the corrupt-date row
		}
		if rem.Notes != "" || !rem.Due.IsZero() {
			t.Errorf("reminder %d carries a dropped field: %+v", rem.ID, rem)
		}
		if rem.List != nil {
			t.Errorf("reminder %d resolved a list though the lists unit is gone", rem.ID)
		}
		if rem.Title == "" {
			t.Errorf("reminder %d lost its title under degradation", rem.ID)
		}
	}

	// Lists() is unavailable when the lists unit is gone.
	for _, err := range r.Lists() {
		if !errors.Is(err, backup.ErrUnavailable) {
			t.Errorf("Lists() error = %v, want ErrUnavailable", err)
		}
		break
	}
}

func TestUnsupportedSchema(t *testing.T) {
	// A required column absent (here ZREMCDREMINDER.ZTITLE, the headline field)
	// is a different, unsupported fingerprint — never a silent degradation.
	_, err := reminders.Open(fixtureFS(t, reminders.FixtureOptions{
		DropColumns: []string{"ZREMCDREMINDER.ZTITLE"},
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
	if _, err := reminders.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	r := openFixture(t, reminders.FixtureOptions{})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, err := range r.Reminders() {
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
