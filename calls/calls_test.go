package calls_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/calls"
)

// rowErrorID is the fixture call whose participant join carries a dangling
// handle reference (see BuildFixture): it must surface as a row-scoped error.
const rowErrorID = 4

// fixtureFS builds a synthetic backup tree holding a call-history database built
// with opt and returns a DirFS over it.
func fixtureFS(t *testing.T, opt calls.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, calls.Domain, filepath.FromSlash(calls.RelativePath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	calls.BuildFixture(t, dbPath, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt calls.FixtureOptions) *calls.Calls {
	t.Helper()
	c, err := calls.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func assertFixtureParse(t *testing.T, c *calls.Calls) {
	t.Helper()
	expected := calls.ExpectedCalls()
	i := 0
	for call, err := range c.Calls() {
		if i >= len(expected) {
			t.Fatalf("more calls than expected: %+v, %v", call, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("call %d: got (%+v, %v), want a *backup.RowError", i, call, err)
			}
			if rowErr.Domain != "calls" || rowErr.Table != "ZCALLRECORD" || rowErr.RowID != rowErrorID {
				t.Errorf("row error = %+v, want ZCALLRECORD rowid %d", rowErr, rowErrorID)
			}
		} else {
			if err != nil {
				t.Fatalf("call %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(call, *want) {
				t.Errorf("call %d:\n got %+v\nwant %+v", i, call, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("stream ended after %d calls, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func TestCapability(t *testing.T) {
	c := openFixture(t, calls.FixtureOptions{})
	capability := c.Capability()
	want := backup.Capability{
		Domain:    "calls",
		Supported: true,
		Schema:    "calls.1",
	}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestCallsRoundTrip(t *testing.T) {
	assertFixtureParse(t, openFixture(t, calls.FixtureOptions{}))
}

// TestCommittedFixture parses the COMMITTED rung-1 artifact — proving the
// checked-in fixture, not just the in-memory builder, matches the parser.
func TestCommittedFixture(t *testing.T) {
	data, err := os.ReadFile(calls.CommittedFixturePath)
	if err != nil {
		t.Fatalf("committed fixture missing (%v) — run `make fixtures` and commit the result", err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, calls.Domain, filepath.FromSlash(calls.RelativePath))
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
	c, err := calls.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	assertFixtureParse(t, c)
}

func TestMissed(t *testing.T) {
	c := openFixture(t, calls.FixtureOptions{})
	got := map[int64]bool{}
	for call, err := range c.Calls() {
		if err != nil {
			continue // the dangling-handle row
		}
		got[call.ID] = call.Missed()
	}
	// Call 2 is incoming + unanswered; the rest are answered or outgoing.
	for id, wantMissed := range map[int64]bool{1: false, 2: true, 3: false, 5: false} {
		if got[id] != wantMissed {
			t.Errorf("call %d Missed() = %v, want %v", id, got[id], wantMissed)
		}
	}
}

func TestDegradedSchema(t *testing.T) {
	c := openFixture(t, calls.FixtureOptions{
		DropTables:  []string{"Z_2REMOTEPARTICIPANTHANDLES", "ZHANDLE"},
		DropColumns: []string{"ZCALLRECORD.ZNAME", "ZCALLRECORD.ZJUNKCONFIDENCE", "ZCALLRECORD.ZJUNKIDENTIFICATIONCATEGORY"},
	})
	capability := c.Capability()
	wantMissing := []string{"name", "participants", "spam"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "calls.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}

	// Calls still parse; the dropped fields stay zero (and are declared in
	// Missing — never silently guessed), and the once-dangling call now parses
	// cleanly because the participant join is gone entirely.
	var byID = map[int64]calls.Call{}
	for call, err := range c.Calls() {
		if err != nil {
			t.Fatalf("degraded stream yielded error %v", err)
		}
		byID[call.ID] = call
	}
	if len(byID) != 5 {
		t.Fatalf("degraded stream yielded %d calls, want 5", len(byID))
	}
	first := byID[1]
	if first.Name != "" {
		t.Errorf("Name = %q, want empty (column dropped)", first.Name)
	}
	if first.Address != "+1 555 0100" {
		t.Errorf("Address = %q", first.Address)
	}
	if group := byID[3]; group.Participants != nil {
		t.Errorf("Participants = %v, want nil (join dropped)", group.Participants)
	}
	if spam := byID[2]; spam.JunkConfidence != 0 || spam.JunkCategory != "" {
		t.Errorf("spam fields = (%d, %q), want zero (columns dropped)", spam.JunkConfidence, spam.JunkCategory)
	}
}

func TestUnsupportedSchema(t *testing.T) {
	// A required column absent (here ZDATE) is a different, unsupported
	// fingerprint — never a silent degradation.
	_, err := calls.Open(fixtureFS(t, calls.FixtureOptions{
		DropColumns: []string{"ZCALLRECORD.ZDATE"},
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
	if _, err := calls.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	c := openFixture(t, calls.FixtureOptions{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, err := range c.Calls() {
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
