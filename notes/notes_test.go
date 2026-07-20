package notes_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/notes"
)

// rowErrorNoteID is the fixture note whose ZCREATIONDATE3 is corrupt (see
// BuildFixture): it must surface as a row-scoped error, not end the stream.
const rowErrorNoteID = 15

// fixtureFS builds a synthetic backup tree holding a NoteStore database built with
// opt and returns a DirFS over it.
func fixtureFS(t *testing.T, opt notes.FixtureOptions) *backup.DirFS {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, notes.Domain, filepath.FromSlash(notes.RelativePath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	notes.BuildFixture(t, dbPath, opt)
	fsys, err := backup.NewDirFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

func openFixture(t *testing.T, opt notes.FixtureOptions) *notes.Notes {
	t.Helper()
	n, err := notes.Open(fixtureFS(t, opt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// assertFixtureParse iterates Notes() and checks each record against ExpectedNotes.
// A nil expectation marks the row-scoped defect. Passing this also proves the
// Z_ENT resolution works: the fixture uses non-standard ordinals, so a hard-coded
// ICNote=12 would find zero notes.
func assertFixtureParse(t *testing.T, n *notes.Notes) {
	t.Helper()
	expected := notes.ExpectedNotes()
	i := 0
	for note, err := range n.Notes() {
		if i >= len(expected) {
			t.Fatalf("more notes than expected: %+v, %v", note, err)
		}
		want := expected[i]
		if want == nil {
			var rowErr *backup.RowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("note %d: got (%+v, %v), want a *backup.RowError", i, note, err)
			}
			if rowErr.Domain != "notes" || rowErr.Table != "ZICCLOUDSYNCINGOBJECT" || rowErr.RowID != rowErrorNoteID {
				t.Errorf("row error = %+v, want ZICCLOUDSYNCINGOBJECT rowid %d", rowErr, rowErrorNoteID)
			}
		} else {
			if err != nil {
				t.Fatalf("note %d: unexpected error %v", i, err)
			}
			if !reflect.DeepEqual(note, *want) {
				t.Errorf("note %d:\n got %+v\nwant %+v", i, note, *want)
			}
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("stream ended after %d notes, want %d (row-scoped errors must not end it)", i, len(expected))
	}
}

func TestCapability(t *testing.T) {
	n := openFixture(t, notes.FixtureOptions{})
	capability := n.Capability()
	want := backup.Capability{Domain: "notes", Supported: true, Schema: "notes.1"}
	if !reflect.DeepEqual(capability, want) {
		t.Errorf("capability = %+v, want %+v", capability, want)
	}
}

func TestNotesRoundTrip(t *testing.T) {
	assertFixtureParse(t, openFixture(t, notes.FixtureOptions{}))
}

// TestCommittedFixture parses the COMMITTED rung-1 artifact — proving the
// checked-in fixture, not just the in-memory builder, matches the parser.
func TestCommittedFixture(t *testing.T) {
	data, err := os.ReadFile(notes.CommittedFixturePath)
	if err != nil {
		t.Fatalf("committed fixture missing (%v) — run `make fixtures` and commit the result", err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, notes.Domain, filepath.FromSlash(notes.RelativePath))
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
	n, err := notes.Open(fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	assertFixtureParse(t, n)
}

func TestFolders(t *testing.T) {
	n := openFixture(t, notes.FixtureOptions{})
	var got []notes.Folder
	for folder, err := range n.Folders() {
		if err != nil {
			t.Fatalf("folders yielded error %v", err)
		}
		got = append(got, folder)
	}
	if want := notes.ExpectedFolders(); !reflect.DeepEqual(got, want) {
		t.Errorf("folders:\n got %+v\nwant %+v", got, want)
	}
}

// TestLockedNoteReported: a password-protected note is reported (Locked + hint),
// never decrypted — its body stays empty and BodyUndecoded is false (the body is
// intentionally not decoded, which is not a decode failure).
func TestLockedNoteReported(t *testing.T) {
	locked := noteByID(t, openFixture(t, notes.FixtureOptions{}), 11)
	if !locked.Locked || locked.PasswordHint != "pet name" {
		t.Errorf("locked note = %+v, want Locked with hint", locked)
	}
	if locked.Body != "" || locked.BodyUndecoded {
		t.Errorf("locked note body = (%q, undecoded=%v), want empty and NOT undecoded", locked.Body, locked.BodyUndecoded)
	}
}

// TestBodyUndecoded: a note whose ZDATA is present but not a decodable body is
// yielded with BodyUndecoded set (body unknown), never as a silently-empty note.
func TestBodyUndecoded(t *testing.T) {
	n := openFixture(t, notes.FixtureOptions{})
	corrupt := noteByID(t, n, 13)
	if !corrupt.BodyUndecoded || corrupt.Body != "" {
		t.Errorf("corrupt-body note = (%q, undecoded=%v), want empty + undecoded", corrupt.Body, corrupt.BodyUndecoded)
	}
	blank := noteByID(t, n, 12)
	if blank.BodyUndecoded || blank.Body != "" {
		t.Errorf("blank note = (%q, undecoded=%v), want empty + NOT undecoded", blank.Body, blank.BodyUndecoded)
	}
}

// TestMediaFileRef: a media-backed attachment carries a resolvable FileRef into
// the notes group domain; a non-media attachment (a table) carries metadata only.
func TestMediaFileRef(t *testing.T) {
	note := noteByID(t, openFixture(t, notes.FixtureOptions{}), 10)
	if len(note.Attachments) != 2 {
		t.Fatalf("note 10 has %d attachments, want 2", len(note.Attachments))
	}
	media := note.Attachments[0]
	wantRef := &backup.FileRef{Domain: notes.Domain, RelativePath: "Accounts/ACCT-UUID-0001/Media/MEDIA-UUID-0030/1_GEN-0030/photo.jpeg"}
	if !reflect.DeepEqual(media.File, wantRef) {
		t.Errorf("media FileRef = %+v, want %+v", media.File, wantRef)
	}
	if table := note.Attachments[1]; table.File != nil {
		t.Errorf("non-media attachment File = %+v, want nil", table.File)
	}
}

func TestDegradedSchema(t *testing.T) {
	n := openFixture(t, notes.FixtureOptions{
		DropColumns: []string{
			"ZICCLOUDSYNCINGOBJECT.ZSNIPPET",
			"ZICCLOUDSYNCINGOBJECT.ZMARKEDFORDELETION",
			"ZICCLOUDSYNCINGOBJECT.ZFOLDER",
			"ZICCLOUDSYNCINGOBJECT.ZGENERATION1",
		},
	})
	capability := n.Capability()
	wantMissing := []string{"attachments", "deletion", "folders", "snippet"}
	if !reflect.DeepEqual(capability.Missing, wantMissing) {
		t.Errorf("Missing = %v, want %v", capability.Missing, wantMissing)
	}
	if capability.Schema != "notes.1" || !capability.Supported {
		t.Errorf("capability = %+v", capability)
	}

	// Notes still parse; dropped fields stay zero and are declared in Missing.
	byID := map[int64]notes.Note{}
	for note, err := range n.Notes() {
		if err != nil {
			continue // the corrupt-date row
		}
		byID[note.ID] = note
	}
	if len(byID) != 5 {
		t.Fatalf("degraded stream yielded %d notes, want 5", len(byID))
	}
	if a := byID[10]; a.Snippet != "" || a.Folder != nil || a.Attachments != nil {
		t.Errorf("note 10 = %+v, want empty snippet, nil folder, nil attachments", a)
	}
	if a := byID[10]; a.Body != "Shopping list\nmilk\neggs\nbread" {
		t.Errorf("note 10 body = %q (body must survive schema degradation)", a.Body)
	}
	if e := byID[14]; e.MarkedForDeletion {
		t.Errorf("note 14 MarkedForDeletion = true, want false (column dropped)")
	}

	// Folders() is unavailable when the folders unit is gone.
	for _, err := range n.Folders() {
		if !errors.Is(err, backup.ErrUnavailable) {
			t.Errorf("Folders() error = %v, want ErrUnavailable", err)
		}
		break
	}
}

func TestUnsupportedSchema(t *testing.T) {
	// A required column absent (here ZICNOTEDATA.ZDATA, the body source) is a
	// different, unsupported fingerprint — never a silent degradation.
	_, err := notes.Open(fixtureFS(t, notes.FixtureOptions{
		DropColumns: []string{"ZICNOTEDATA.ZDATA"},
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
	if _, err := notes.Open(fsys); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(empty tree) = %v, want fs.ErrNotExist", err)
	}
}

func TestIterateAfterCloseIsStreamScoped(t *testing.T) {
	n := openFixture(t, notes.FixtureOptions{})
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, err := range n.Notes() {
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

// noteByID drains Notes() and returns the note with the given Z_PK (skipping
// row-scoped errors).
func noteByID(t *testing.T, n *notes.Notes, id int64) notes.Note {
	t.Helper()
	for note, err := range n.Notes() {
		if err != nil {
			continue
		}
		if note.ID == id {
			return note
		}
	}
	t.Fatalf("note %d not found", id)
	return notes.Note{}
}
