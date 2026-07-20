package contacts_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/contacts"
)

// rowErrorID is the fixture person whose multi-values carry a dangling label
// reference (see BuildFixture): it must surface as a row-scoped error.
const rowErrorID = 2

// fixtureFS builds a synthetic backup tree holding a contacts database built
// with opt and returns a DirFS over it.
func fixtureFS(t *testing.T, opt contacts.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, contacts.Domain, filepath.FromSlash(contacts.RelativePath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contacts.BuildFixture(t, dbPath, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt contacts.FixtureOptions) *contacts.Contacts {
	t.Helper()
	c, err := contacts.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func assertFixtureParse(t *testing.T, c *contacts.Contacts) {
	t.Helper()
	expected := contacts.ExpectedPeople()
	i := 0
	for person, err := range c.People() {
		if i >= len(expected) {
			t.Fatalf("more people than expected: %+v, %v", person, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("person %d: got (%+v, %v), want a *backup.RowError", i, person, err)
			}
			if rowErr.Domain != "contacts" || rowErr.Table != "ABPerson" || rowErr.RowID != rowErrorID {
				t.Errorf("row error = %+v, want ABPerson rowid %d", rowErr, rowErrorID)
			}
		} else {
			if err != nil {
				t.Fatalf("person %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(person, *want) {
				t.Errorf("person %d:\n got %+v\nwant %+v", i, person, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("stream ended after %d people, want %d (row-scoped errors must not end it)", i, len(expected))
	}

	expectedGroups := contacts.ExpectedGroups()
	j := 0
	for group, err := range c.Groups() {
		if err != nil {
			t.Fatalf("group %d: unexpected error %v", j, err)
		}
		if j < len(expectedGroups) && !reflect.DeepEqual(group, expectedGroups[j]) {
			t.Errorf("group %d:\n got %+v\nwant %+v", j, group, expectedGroups[j])
		}
		j++
	}
	if j != len(expectedGroups) {
		t.Errorf("got %d groups, want %d", j, len(expectedGroups))
	}
}

func TestCapability(t *testing.T) {
	c := openFixture(t, contacts.FixtureOptions{})
	capability := c.Capability()
	want := backup.Capability{
		Domain:    "contacts",
		Supported: true,
		Schema:    "contacts.1",
		Missing:   []string{"photo"},
	}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestPeopleRoundTrip(t *testing.T) {
	assertFixtureParse(t, openFixture(t, contacts.FixtureOptions{}))
}

// TestCommittedFixture parses the COMMITTED rung-1 artifact — proving the
// checked-in fixture, not just the in-memory builder, matches the parser.
func TestCommittedFixture(t *testing.T) {
	data, err := os.ReadFile(contacts.CommittedFixturePath)
	if err != nil {
		t.Fatalf("committed fixture missing (%v) — run `make fixtures` and commit the result", err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, contacts.Domain, filepath.FromSlash(contacts.RelativePath))
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
	c, err := contacts.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	assertFixtureParse(t, c)
}

func TestDegradedSchema(t *testing.T) {
	c := openFixture(t, contacts.FixtureOptions{
		DropTables:  []string{"ABGroup", "ABGroupMembers"},
		DropColumns: []string{"ABPerson.Nickname"},
	})
	capability := c.Capability()
	wantMissing := []string{"groups", "nickname", "photo"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "contacts.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}

	// People still parse; the nickname field stays zero (and is declared in
	// Missing — never silently guessed).
	var first *contacts.Person
	for person, err := range c.People() {
		if err == nil {
			first = &person
			break
		}
	}
	if first == nil {
		t.Fatal("no people parsed")
	}
	if first.Nickname != "" {
		t.Errorf("Nickname = %q, want empty (column dropped)", first.Nickname)
	}
	if first.First != "Alex" {
		t.Errorf("First = %q", first.First)
	}

	// Groups must yield ErrUnavailable — not a misleading empty stream.
	sawUnavailable := false
	for _, err := range c.Groups() {
		if errors.Is(err, backup.ErrUnavailable) {
			sawUnavailable = true
			continue
		}
		t.Fatalf("Groups yielded %v, want ErrUnavailable", err)
	}
	if !sawUnavailable {
		t.Error("Groups() yielded nothing; want ErrUnavailable")
	}
}

func TestUnsupportedSchema(t *testing.T) {
	_, err := contacts.Open(fixtureFS(t, contacts.FixtureOptions{
		DropTables: []string{"ABMultiValue"},
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
	if _, err := contacts.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	c := openFixture(t, contacts.FixtureOptions{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, err := range c.People() {
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

func TestCanonicalLabel(t *testing.T) {
	for in, want := range map[string]string{
		"_$!<Home>!$_":   "Home",
		"_$!<Mobile>!$_": "Mobile",
		"fixture custom": "fixture custom",
		"":               "",
		"_$!<Broken":     "_$!<Broken",
	} {
		if got := contacts.CanonicalLabel(in); got != want {
			t.Errorf("CanonicalLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
