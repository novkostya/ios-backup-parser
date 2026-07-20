package contacts

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint contacts.1
// (docs/schemas/contacts.md): table and column sets only — indexes, triggers,
// table-level UNIQUE constraints, FTS mirrors and sync bookkeeping tables are
// not fingerprint-relevant and are omitted. Every inserted row is invented;
// nothing here derives from a real backup (charter privacy gate).
//
// TestWriteCommittedFixture regenerates the committed fixture
// (testdata/contacts.1.AddressBook.sqlitedb) when FIXTURE_WRITE is set — via
// `make fixtures` — so the committed artifact and the round-trip tests are
// built from the same schema belief. Green fixtures do NOT prove correctness:
// the operator-local differential (rung 3) is what moves a fingerprint from
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

// contacts1DDL: column definitions per table, first token = column name.
var contacts1DDL = map[string][]string{
	"ABPerson": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT",
		"First TEXT", "Last TEXT", "Middle TEXT",
		"FirstPhonetic TEXT", "MiddlePhonetic TEXT", "LastPhonetic TEXT",
		"Organization TEXT", "Department TEXT", "Note TEXT",
		"Kind INTEGER", "Birthday TEXT", "JobTitle TEXT", "Nickname TEXT",
		"Prefix TEXT", "Suffix TEXT", "FirstSort TEXT", "LastSort TEXT",
		"CreationDate INTEGER", "ModificationDate INTEGER",
		"CompositeNameFallback TEXT", "ExternalIdentifier TEXT",
		"ExternalModificationTag TEXT", "ExternalUUID TEXT", "StoreID INTEGER",
		"DisplayName TEXT", "ExternalRepresentation BLOB",
		"FirstSortSection TEXT", "LastSortSection TEXT",
		"FirstSortLanguageIndex INTEGER DEFAULT 2147483647",
		"LastSortLanguageIndex INTEGER DEFAULT 2147483647",
		"PersonLink INTEGER DEFAULT -1", "ImageURI TEXT",
		"IsPreferredName INTEGER DEFAULT 1",
		"guid TEXT NOT NULL DEFAULT (ab_generate_guid())",
		"PhonemeData TEXT", "AlternateBirthday TEXT", "MapsData TEXT",
		"FirstPronunciation TEXT", "MiddlePronunciation TEXT",
		"LastPronunciation TEXT", "OrganizationPhonetic TEXT",
		"OrganizationPronunciation TEXT", "PreviousFamilyName TEXT",
		"PreferredLikenessSource TEXT", "PreferredPersonaIdentifier TEXT",
		"PreferredChannel TEXT", "DowntimeWhitelist TEXT", "ImageType TEXT",
		"ImageHash BLOB", "MemojiMetadata BLOB", "Wallpaper BLOB",
		"DisplayFlags INTEGER", "WatchWallpaperImageData BLOB",
		"WallpaperMetadata BLOB", "ImageBackgroundColorsData BLOB",
		"SensitiveContentConfiguration BLOB", "WallpaperURI TEXT",
		"ImageSyncFailedTime TEXT", "WallpaperSyncFailedTime TEXT",
	},
	"ABMultiValue": {
		"UID INTEGER PRIMARY KEY", "record_id INTEGER", "property INTEGER",
		"identifier INTEGER", "label INTEGER", "value TEXT",
		"guid TEXT NOT NULL DEFAULT (ab_generate_guid())",
	},
	"ABMultiValueLabel":    {"value TEXT"},
	"ABMultiValueEntry":    {"parent_id INTEGER", "key INTEGER", "value TEXT"},
	"ABMultiValueEntryKey": {"value TEXT"},
	"ABStore": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "Name TEXT",
		"ExternalIdentifier TEXT", "Type INTEGER", "ConstraintsPath TEXT",
		"ExternalModificationTag TEXT", "ExternalSyncTag TEXT",
		"StoreInternalIdentifier TEXT", "AccountID INTEGER DEFAULT -1",
		"Enabled INTEGER DEFAULT 1", "SyncData BLOB",
		"MeIdentifier INTEGER DEFAULT -1", "Capabilities INTEGER DEFAULT 0",
		"guid TEXT NOT NULL DEFAULT (ab_generate_guid())", "LastSyncDate TEXT",
		"ProviderIdentifier TEXT", "ProviderMetadata BLOB",
	},
	"ABAccount": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "AccountIdentifier TEXT",
		"Flags INTEGER", "DefaultSourceID INTEGER",
		"guid TEXT NOT NULL DEFAULT (ab_generate_guid())",
	},
	"ABGroup": {
		"ROWID INTEGER PRIMARY KEY AUTOINCREMENT", "Name TEXT",
		"ExternalIdentifier TEXT", "StoreID INTEGER",
		"ExternalModificationTag TEXT", "ExternalRepresentation BLOB",
		"ExternalUUID TEXT", "guid TEXT NOT NULL DEFAULT (ab_generate_guid())",
	},
	"ABGroupMembers": {
		"UID INTEGER PRIMARY KEY", "group_id INTEGER", "member_type INTEGER",
		"member_id INTEGER",
	},
}

// FixtureOptions degrade the built database for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables, e.g. ABGroup.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "ABPerson.Nickname".
	DropColumns []string
}

// Fixture timestamps (Cocoa seconds), shared with the expectations.
const (
	fixtureCreated  = 700000000
	fixtureModified = 700086400
)

// BuildFixture writes a synthetic contacts database to path.
func BuildFixture(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	present := map[string]map[string]bool{}
	for table, defs := range contacts1DDL {
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
			// "ROWID" is insertable on any rowid table even when no column
			// declares it (the label/entry-key tables are keyed by their
			// implicit rowid, so fixture rows pin it explicitly).
			if name == "ROWID" || cols[name] {
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

	// Label / entry-key / store / account reference rows.
	insert("ABMultiValueLabel", map[string]any{"ROWID": 1, "value": "_$!<Home>!$_"})
	insert("ABMultiValueLabel", map[string]any{"ROWID": 2, "value": "_$!<Work>!$_"})
	insert("ABMultiValueLabel", map[string]any{"ROWID": 3, "value": "fixture custom"})
	for i, key := range []string{"street", "city", "state", "ZIP", "country"} {
		insert("ABMultiValueEntryKey", map[string]any{"ROWID": i + 1, "value": key})
	}
	insert("ABAccount", map[string]any{"ROWID": 1, "AccountIdentifier": "fixture-account-1", "guid": "fx-account-1"})
	insert("ABStore", map[string]any{"ROWID": 1, "Name": "Fixture Store", "Type": 0, "AccountID": 1, "guid": "fx-store-1"})
	insert("ABStore", map[string]any{"ROWID": 2, "Name": "Local Fixture", "Type": 0, "AccountID": -1, "guid": "fx-store-2"})

	// Person 1 — fully populated.
	insert("ABPerson", map[string]any{
		"ROWID": 1, "First": "Alex", "Middle": "Quinn", "Last": "Fixture",
		"Prefix": "Dr.", "Suffix": "Jr.", "Nickname": "Lexi",
		"Organization": "Fixture Works", "Department": "Synthesis",
		"JobTitle": "Fabricator", "Note": "Entirely invented.",
		"Kind": 0, "Birthday": "1990-04-01",
		"CreationDate": fixtureCreated, "ModificationDate": fixtureModified,
		"StoreID": 1, "guid": "fx-person-1",
	})
	insert("ABMultiValue", map[string]any{"UID": 101, "record_id": 1, "property": 3, "label": 1, "value": "+1 555 0100", "guid": "fx-mv-101"})
	insert("ABMultiValue", map[string]any{"UID": 102, "record_id": 1, "property": 3, "label": 2, "value": "+1 555 0101", "guid": "fx-mv-102"})
	insert("ABMultiValue", map[string]any{"UID": 103, "record_id": 1, "property": 4, "label": 1, "value": "alex@example.com", "guid": "fx-mv-103"})
	insert("ABMultiValue", map[string]any{"UID": 104, "record_id": 1, "property": 22, "label": 3, "value": "https://alex.example.com", "guid": "fx-mv-104"})
	insert("ABMultiValue", map[string]any{"UID": 105, "record_id": 1, "property": 5, "label": 1, "value": nil, "guid": "fx-mv-105"})
	for i, kv := range [][2]any{{1, "1 Synthetic Way"}, {2, "Faketown"}, {3, "FX"}, {4, "00001"}, {5, "Fixtureland"}} {
		insert("ABMultiValueEntry", map[string]any{"ROWID": i + 1, "parent_id": 105, "key": kv[0], "value": kv[1]})
	}
	// An out-of-scope multi-value kind (13 = instant message): must be skipped.
	insert("ABMultiValue", map[string]any{"UID": 106, "record_id": 1, "property": 13, "label": nil, "value": "fixture-im", "guid": "fx-mv-106"})
	// An unlabeled phone: label NULL is legitimate, not a defect.
	insert("ABMultiValue", map[string]any{"UID": 107, "record_id": 1, "property": 3, "label": nil, "value": "+1 555 0102", "guid": "fx-mv-107"})

	// Person 2 — row-scoped defect: a phone whose label points nowhere.
	insert("ABPerson", map[string]any{"ROWID": 2, "First": "Broken", "Last": "Label", "Kind": 0, "guid": "fx-person-2"})
	insert("ABMultiValue", map[string]any{"UID": 201, "record_id": 2, "property": 3, "label": 999, "value": "+1 555 0199", "guid": "fx-mv-201"})

	// Person 3 — an organization-kind contact.
	insert("ABPerson", map[string]any{"ROWID": 3, "Organization": "Acme Synthetics", "Kind": 1, "StoreID": 2, "guid": "fx-person-3"})
	insert("ABMultiValue", map[string]any{"UID": 301, "record_id": 3, "property": 3, "label": 2, "value": "+1 555 0142", "guid": "fx-mv-301"})

	// Person 4 — minimal: every optional column NULL, no multi-values.
	insert("ABPerson", map[string]any{"ROWID": 4, "First": "Solo", "guid": "fx-person-4"})

	// Groups.
	insert("ABGroup", map[string]any{"ROWID": 1, "Name": "Fixture Friends", "StoreID": 1, "guid": "fx-group-1"})
	insert("ABGroupMembers", map[string]any{"UID": 1, "group_id": 1, "member_type": 0, "member_id": 1})
	insert("ABGroupMembers", map[string]any{"UID": 2, "group_id": 1, "member_type": 0, "member_id": 4})
	insert("ABGroup", map[string]any{"ROWID": 2, "Name": "Empty Fixture", "StoreID": 1, "guid": "fx-group-2"})
}

// ExpectedPeople returns what parsing the default fixture must yield, in
// stream order. The entry for ROWID 2 is nil: that row yields a
// *backup.RowError (dangling label) and the stream continues.
func ExpectedPeople() []*Person {
	fixtureStore := &Store{ID: 1, Name: "Fixture Store", Type: 0, AccountID: 1, AccountIdentifier: "fixture-account-1"}
	localStore := &Store{ID: 2, Name: "Local Fixture", Type: 0, AccountID: -1}
	return []*Person{
		{
			ID: 1, First: "Alex", Middle: "Quinn", Last: "Fixture",
			Prefix: "Dr.", Suffix: "Jr.", Nickname: "Lexi",
			Organization: "Fixture Works", Department: "Synthesis",
			JobTitle: "Fabricator", Note: "Entirely invented.",
			Kind: KindPerson, Birthday: "1990-04-01",
			Created:  cocoa.FromSeconds(fixtureCreated),
			Modified: cocoa.FromSeconds(fixtureModified),
			Store:    fixtureStore,
			Phones: []Value{
				{Label: "_$!<Home>!$_", Value: "+1 555 0100"},
				{Label: "_$!<Work>!$_", Value: "+1 555 0101"},
				{Value: "+1 555 0102"},
			},
			Emails: []Value{{Label: "_$!<Home>!$_", Value: "alex@example.com"}},
			URLs:   []Value{{Label: "fixture custom", Value: "https://alex.example.com"}},
			Addresses: []StructuredValue{{
				Label: "_$!<Home>!$_",
				Components: map[string]string{
					"street": "1 Synthetic Way", "city": "Faketown",
					"state": "FX", "ZIP": "00001", "country": "Fixtureland",
				},
			}},
		},
		nil, // ROWID 2: *backup.RowError — dangling label reference
		{
			ID: 3, Organization: "Acme Synthetics", Kind: KindOrganization,
			Store:  localStore,
			Phones: []Value{{Label: "_$!<Work>!$_", Value: "+1 555 0142"}},
		},
		{ID: 4, First: "Solo", Kind: KindPerson},
	}
}

// ExpectedGroups returns what parsing the default fixture's groups must yield.
func ExpectedGroups() []Group {
	return []Group{
		{ID: 1, Name: "Fixture Friends", StoreID: 1, Members: []GroupMember{
			{Type: 0, MemberID: 1}, {Type: 0, MemberID: 4},
		}},
		{ID: 2, Name: "Empty Fixture", StoreID: 1},
	}
}

// CommittedFixturePath is where `make fixtures` writes the rung-1 artifact.
const CommittedFixturePath = "testdata/contacts.1.AddressBook.sqlitedb"

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
