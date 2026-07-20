package notes

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint notes.1
// (docs/schemas/notes.md): the CoreData single-table-inheritance schema — one wide
// ZICCLOUDSYNCINGOBJECT holding every entity, plus the Z_PRIMARYKEY entity map and
// the ZICNOTEDATA body table — and the column set the parser reads, plus a few of
// the table's ~200 other columns so the fixture proves "unknown extra columns never
// disqualify". Note bodies are REAL gzip+protobuf blobs built by
// internal/applenotes' encoder, so the committed fixture exercises the actual
// decoder path, not hand-waved bytes.
//
// The Z_ENT ordinals here are deliberately NON-standard (not the observed
// 12/15/14/5/11): the parser must resolve each entity from the Z_PRIMARYKEY map by
// name, never hard-code the ordinal, so a fixture that renumbers them proves the
// resolution works. Every inserted row is invented; nothing here derives from a
// real backup (charter privacy gate).
//
// TestWriteCommittedFixture regenerates the committed fixture
// (testdata/notes.1.NoteStore.sqlite) when FIXTURE_WRITE is set — via
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

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/applenotes"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Non-standard entity ordinals — see the file header. Resolution is by Z_NAME.
const (
	entNote       = 42
	entFolder     = 45
	entAccount    = 44
	entAttachment = 35
	entMedia      = 41
)

// Fixture bodies (round-tripped through the real gzip+protobuf encoder) and dates
// (Cocoa seconds, REAL — fractional on purpose, to exercise cocoa.FromSecondsFloat).
const (
	noteABody = "Shopping list\nmilk\neggs\nbread"
	noteEBody = "Remember to call the bank"

	fxCreatedA  = 700000000.5
	fxModifiedA = 700100000.25
	fxCreatedB  = 700200000.0
	fxCreatedC  = 700300000.0
	fxCreatedD  = 700400000.0
	fxCreatedE  = 700500000.0
)

// rowErrorNoteID is the note whose ZCREATIONDATE3 is a corrupt (non-numeric)
// value, forcing a scan error — the row-scoped defect.
const rowErrorNoteID = 15

// notes1DDL: column definitions per table, first token = column name.
var notes1DDL = map[string][]string{
	"ZICCLOUDSYNCINGOBJECT": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "Z_OPT INTEGER",
		"ZIDENTIFIER VARCHAR", "ZTITLE VARCHAR", "ZTITLE1 VARCHAR", "ZTITLE2 VARCHAR",
		"ZSNIPPET VARCHAR", "ZFOLDER INTEGER", "ZACCOUNT7 INTEGER",
		"ZCREATIONDATE3 TIMESTAMP", "ZMODIFICATIONDATE1 TIMESTAMP",
		"ZISPASSWORDPROTECTED INTEGER", "ZPASSWORDHINT VARCHAR",
		"ZISPINNED INTEGER", "ZMARKEDFORDELETION INTEGER",
		"ZNAME VARCHAR", "ZACCOUNTTYPE INTEGER", "ZFOLDERTYPE INTEGER",
		"ZNOTE INTEGER", "ZTYPEUTI VARCHAR", "ZATTACHMENT1 INTEGER",
		"ZGENERATION1 VARCHAR", "ZFILENAME VARCHAR", "ZNOTEDATA INTEGER",
		// Realistic extras — never read; prove they do not disqualify.
		"ZHASCHECKLIST INTEGER", "ZWIDGETSNIPPET VARCHAR", "ZLASTVIEWEDMODIFICATIONDATE TIMESTAMP",
	},
	"Z_PRIMARYKEY": {
		"Z_ENT INTEGER PRIMARY KEY", "Z_NAME VARCHAR", "Z_SUPER INTEGER", "Z_MAX INTEGER",
	},
	"ZICNOTEDATA": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "ZNOTE INTEGER", "ZDATA BLOB",
		"ZCRYPTOINITIALIZATIONVECTOR BLOB", "ZCRYPTOTAG BLOB",
	},
}

// FixtureOptions degrade the built database for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables, e.g. ZICNOTEDATA.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "ZICCLOUDSYNCINGOBJECT.ZFOLDER".
	DropColumns []string
}

// BuildFixture writes a synthetic NoteStore database to path.
func BuildFixture(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	present := map[string]map[string]bool{}
	for table, defs := range notes1DDL {
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

	// Entity map (Z_PRIMARYKEY) — the NON-standard ordinals the parser must resolve.
	for _, e := range []struct {
		ent  int
		name string
	}{
		{entNote, "ICNote"}, {entFolder, "ICFolder"}, {entAccount, "ICAccount"},
		{entAttachment, "ICAttachment"}, {entMedia, "ICMedia"},
		{3, "ICCloudSyncingObject"}, {19, "ICNoteData"}, // extras, never looked up
	} {
		insert("Z_PRIMARYKEY", map[string]any{"Z_ENT": e.ent, "Z_NAME": e.name, "Z_SUPER": 0})
	}

	// Account + folders.
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 1, "Z_ENT": entAccount, "ZIDENTIFIER": "ACCT-UUID-0001",
		"ZNAME": "On My iPhone", "ZACCOUNTTYPE": 1})
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 2, "Z_ENT": entFolder, "ZIDENTIFIER": "FLDR-UUID-0002",
		"ZTITLE2": "Notes", "ZFOLDERTYPE": 1, "ZACCOUNT7": 1})
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 3, "Z_ENT": entFolder, "ZIDENTIFIER": "FLDR-UUID-0003",
		"ZTITLE2": "Recipes", "ZFOLDERTYPE": 0, "ZACCOUNT7": 1})

	// Note A (Z_PK 10) — fully populated, pinned, with two attachments.
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 10, "Z_ENT": entNote, "ZIDENTIFIER": "NOTE-UUID-0010",
		"ZTITLE1": "Shopping", "ZSNIPPET": "Shopping list", "ZFOLDER": 2, "ZACCOUNT7": 1,
		"ZCREATIONDATE3": fxCreatedA, "ZMODIFICATIONDATE1": fxModifiedA,
		"ZISPASSWORDPROTECTED": 0, "ZISPINNED": 1, "ZMARKEDFORDELETION": 0, "ZNOTEDATA": 110})
	insert("ZICNOTEDATA", map[string]any{"Z_PK": 110, "Z_ENT": 19, "ZNOTE": 10, "ZDATA": applenotes.EncodeBody(noteABody)})
	// A media-backed attachment (image) and a non-media one (table).
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 20, "Z_ENT": entAttachment, "ZNOTE": 10, "ZTYPEUTI": "public.jpeg",
		"ZIDENTIFIER": "ATT-UUID-0020"})
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 30, "Z_ENT": entMedia, "ZATTACHMENT1": 20, "ZIDENTIFIER": "MEDIA-UUID-0030",
		"ZGENERATION1": "1_GEN-0030", "ZFILENAME": "photo.jpeg"})
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 21, "Z_ENT": entAttachment, "ZNOTE": 10, "ZTYPEUTI": "com.apple.notes.table",
		"ZIDENTIFIER": "ATT-UUID-0021", "ZTITLE": "Budget table"})

	// Note B (Z_PK 11) — LOCKED: body must NOT be decoded (ZDATA is ciphertext).
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 11, "Z_ENT": entNote, "ZIDENTIFIER": "NOTE-UUID-0011",
		"ZTITLE1": "Locked", "ZFOLDER": 2, "ZACCOUNT7": 1, "ZCREATIONDATE3": fxCreatedB,
		"ZISPASSWORDPROTECTED": 1, "ZPASSWORDHINT": "pet name", "ZNOTEDATA": 111})
	insert("ZICNOTEDATA", map[string]any{"Z_PK": 111, "Z_ENT": 19, "ZNOTE": 11,
		"ZDATA":                       []byte{0x00, 0x11, 0x22, 0x33}, // not gzip; must be left undecoded (locked)
		"ZCRYPTOINITIALIZATIONVECTOR": []byte{0xAA, 0xBB}, "ZCRYPTOTAG": []byte{0xCC, 0xDD}})

	// Note C (Z_PK 12) — BLANK: ZICNOTEDATA row with NULL ZDATA.
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 12, "Z_ENT": entNote, "ZIDENTIFIER": "NOTE-UUID-0012",
		"ZTITLE1": "Empty", "ZFOLDER": 3, "ZACCOUNT7": 1, "ZCREATIONDATE3": fxCreatedC, "ZNOTEDATA": 112})
	insert("ZICNOTEDATA", map[string]any{"Z_PK": 112, "Z_ENT": 19, "ZNOTE": 12}) // ZDATA NULL

	// Note D (Z_PK 13) — UNDECODED: ZDATA present but not a note body.
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 13, "Z_ENT": entNote, "ZIDENTIFIER": "NOTE-UUID-0013",
		"ZTITLE1": "Corrupt body", "ZFOLDER": 3, "ZACCOUNT7": 1, "ZCREATIONDATE3": fxCreatedD, "ZNOTEDATA": 113})
	insert("ZICNOTEDATA", map[string]any{"Z_PK": 113, "Z_ENT": 19, "ZNOTE": 13, "ZDATA": []byte("this is not gzip")})

	// Note E (Z_PK 14) — no folder (soft-nil) + marked for deletion.
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": 14, "Z_ENT": entNote, "ZIDENTIFIER": "NOTE-UUID-0014",
		"ZTITLE1": "Trash note", "ZACCOUNT7": 1, "ZCREATIONDATE3": fxCreatedE,
		"ZMARKEDFORDELETION": 1, "ZNOTEDATA": 114})
	insert("ZICNOTEDATA", map[string]any{"Z_PK": 114, "Z_ENT": 19, "ZNOTE": 14, "ZDATA": applenotes.EncodeBody(noteEBody)})

	// Note F (Z_PK 15) — row-scoped defect: a non-numeric ZCREATIONDATE3 scan error.
	insert("ZICCLOUDSYNCINGOBJECT", map[string]any{
		"Z_PK": rowErrorNoteID, "Z_ENT": entNote, "ZIDENTIFIER": "NOTE-UUID-0015",
		"ZTITLE1": "Corrupt date", "ZFOLDER": 2, "ZACCOUNT7": 1,
		"ZCREATIONDATE3": "not-a-date", "ZNOTEDATA": 115})
	insert("ZICNOTEDATA", map[string]any{"Z_PK": 115, "Z_ENT": 19, "ZNOTE": 15, "ZDATA": applenotes.EncodeBody("unreached")})
}

// fixtureAccount is the account every fixture note/folder resolves to.
func fixtureAccount() *Account {
	return &Account{ID: 1, Identifier: "ACCT-UUID-0001", Name: "On My iPhone", Type: 1}
}

func fixtureFolderNotes() *Folder {
	return &Folder{ID: 2, Identifier: "FLDR-UUID-0002", Title: "Notes", Type: 1, Account: fixtureAccount()}
}

func fixtureFolderRecipes() *Folder {
	return &Folder{ID: 3, Identifier: "FLDR-UUID-0003", Title: "Recipes", Type: 0, Account: fixtureAccount()}
}

// ExpectedNotes returns what parsing the default fixture must yield, in stream
// order (by Z_PK). The entry for Z_PK 15 is nil: that row yields a
// *backup.RowError (corrupt ZCREATIONDATE3) and the stream continues.
func ExpectedNotes() []*Note {
	return []*Note{
		{
			ID: 10, Identifier: "NOTE-UUID-0010", Title: "Shopping", Snippet: "Shopping list",
			Body: noteABody, Created: cocoa.FromSecondsFloat(fxCreatedA),
			Modified: cocoa.FromSecondsFloat(fxModifiedA), Pinned: true,
			Folder: fixtureFolderNotes(), Account: fixtureAccount(),
			Attachments: []Attachment{
				{
					ID: 20, Identifier: "ATT-UUID-0020", TypeUTI: "public.jpeg", Filename: "photo.jpeg",
					File: &backup.FileRef{Domain: Domain, RelativePath: "Accounts/ACCT-UUID-0001/Media/MEDIA-UUID-0030/1_GEN-0030/photo.jpeg"},
				},
				{ID: 21, Identifier: "ATT-UUID-0021", TypeUTI: "com.apple.notes.table", Title: "Budget table"},
			},
		},
		{
			ID: 11, Identifier: "NOTE-UUID-0011", Title: "Locked", Locked: true, PasswordHint: "pet name",
			Created: cocoa.FromSecondsFloat(fxCreatedB), Folder: fixtureFolderNotes(), Account: fixtureAccount(),
		},
		{
			ID: 12, Identifier: "NOTE-UUID-0012", Title: "Empty",
			Created: cocoa.FromSecondsFloat(fxCreatedC), Folder: fixtureFolderRecipes(), Account: fixtureAccount(),
		},
		{
			ID: 13, Identifier: "NOTE-UUID-0013", Title: "Corrupt body", BodyUndecoded: true,
			Created: cocoa.FromSecondsFloat(fxCreatedD), Folder: fixtureFolderRecipes(), Account: fixtureAccount(),
		},
		{
			ID: 14, Identifier: "NOTE-UUID-0014", Title: "Trash note", Body: noteEBody,
			Created: cocoa.FromSecondsFloat(fxCreatedE), MarkedForDeletion: true, Account: fixtureAccount(),
		},
		nil, // Z_PK 15: *backup.RowError — corrupt ZCREATIONDATE3
	}
}

// ExpectedFolders returns what Folders() must yield from the default fixture, in
// Z_PK order.
func ExpectedFolders() []Folder {
	return []Folder{*fixtureFolderNotes(), *fixtureFolderRecipes()}
}

// CommittedFixturePath is where `make fixtures` writes the rung-1 artifact.
const CommittedFixturePath = "testdata/notes.1.NoteStore.sqlite"

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
