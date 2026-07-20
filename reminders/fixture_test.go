package reminders

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint reminders.1
// (docs/schemas/reminders.md): the reminder table (ZREMCDREMINDER), the list
// table (ZREMCDBASELIST), the shared object table (ZREMCDOBJECT, holding
// accounts / recurrence rules / assignments / sharees) and the Z_PRIMARYKEY
// entity map, plus a few columns the parser never reads (ZSPOTLIGHTINDEXCOUNT,
// ZBADGEEMBLEM, ZLISTTYPERAWVALUE) so the fixture proves "unknown extra columns
// never disqualify". Titles/notes are plain columns — no blob decode. Every
// inserted row is invented; nothing here derives from a real backup (charter
// privacy gate).
//
// The fixture writes TWO stores, exercising the multi-store domain:
//   - a UUID-named CloudKit store (cloudStore) with most of the data, and
//   - the fixed-name on-device store (LocalStore).
//
// Two things are proven deliberately:
//  1. Z_ENT ordinals are resolved per store from Z_PRIMARYKEY by name. The two
//     stores use DIFFERENT, non-standard ordinals (as the real Data-local and
//     CloudKit stores do), so a hard-coded ordinal — or one global map shared
//     across stores — would mis-read one of them.
//  2. Identity is (Store, Z_PK): BOTH stores contain a reminder with Z_PK 10,
//     and they must surface as two distinct reminders keyed by Store.
//
// TestWriteCommittedFixture regenerates the committed fixtures when FIXTURE_WRITE
// is set — via `make fixtures` — so the committed artifacts and the round-trip
// tests are built from the same schema belief. Green fixtures do NOT prove
// correctness: the operator-local differential (rung 3) is what moves a
// fingerprint from fixture-only to validated.

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

// cloudStore is the synthetic UUID-named store; LocalStore (from the package) is
// the fixed-name one. cloudStore sorts before LocalStore ('A' < 'l'), so the
// stream order is cloud reminders then the local reminder.
const cloudStore = "Data-A1B2C3D4-1111-2222-3333-444455556666.sqlite"

// CloudStoreName exposes cloudStore to the external test package (which places
// the committed cloud fixture at this name when reconstructing the tree).
func CloudStoreName() string { return cloudStore }

// Per-store entity ordinals — deliberately NON-standard AND different between the
// two stores (see the file header). Resolution is by Z_NAME from Z_PRIMARYKEY.
const (
	cReminder   = 91
	cList       = 96
	cAccount    = 92
	cRecurrence = 93
	cAssignment = 94
	cSharee     = 95

	lReminder = 81
	lList     = 86
	lAccount  = 82
)

// Fixture timestamps (Cocoa seconds, REAL — fractional on purpose, to exercise
// cocoa.FromSecondsFloat).
const (
	fxCreated  = 700000000.5
	fxModified = 700100000.25
	fxDue      = 700200000.0
	fxDue2     = 700250000.0
	fxComplete = 700300000.75
)

// rowErrorReminderID is the cloud-store reminder whose ZCREATIONDATE is a corrupt
// (non-numeric) value, forcing a scan error — the row-scoped defect.
const rowErrorReminderID = 14

// remindersDDL: column definitions per table, first token = column name. Both
// stores share this schema.
var remindersDDL = map[string][]string{
	"Z_PRIMARYKEY": {
		"Z_ENT INTEGER PRIMARY KEY", "Z_NAME VARCHAR", "Z_SUPER INTEGER", "Z_MAX INTEGER",
	},
	"ZREMCDREMINDER": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "Z_OPT INTEGER",
		"ZIDENTIFIER BLOB", "ZTITLE VARCHAR", "ZNOTES VARCHAR",
		"ZCOMPLETED INTEGER", "ZCOMPLETIONDATE TIMESTAMP", "ZFLAGGED INTEGER",
		"ZPRIORITY INTEGER", "ZALLDAY INTEGER",
		"ZCREATIONDATE TIMESTAMP", "ZLASTMODIFIEDDATE TIMESTAMP",
		"ZDUEDATE TIMESTAMP", "ZSTARTDATE TIMESTAMP",
		"ZMARKEDFORDELETION INTEGER", "ZPARENTREMINDER INTEGER",
		"ZLIST INTEGER", "ZACCOUNT INTEGER",
		// Realistic extras — never read; prove they do not disqualify.
		"ZDISPLAYDATEDATE TIMESTAMP", "ZSPOTLIGHTINDEXCOUNT INTEGER",
	},
	"ZREMCDBASELIST": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "Z_OPT INTEGER",
		"ZIDENTIFIER BLOB", "ZNAME VARCHAR", "ZISGROUP INTEGER",
		"ZSHARINGSTATUS INTEGER", "ZACCOUNT INTEGER",
		"ZBADGEEMBLEM VARCHAR", // extra
	},
	"ZREMCDOBJECT": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "Z_OPT INTEGER",
		"ZNAME VARCHAR",
		"ZREMINDER4 INTEGER", "ZFREQUENCY INTEGER", "ZINTERVAL INTEGER",
		"ZOCCURRENCECOUNT INTEGER", "ZENDDATE TIMESTAMP",
		"ZREMINDER1 INTEGER", "ZASSIGNEE INTEGER",
		"ZFIRSTNAME VARCHAR", "ZLASTNAME VARCHAR", "ZADDRESS1 VARCHAR",
		"ZLISTTYPERAWVALUE INTEGER", // extra
	},
}

// FixtureOptions degrade the built stores for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables from every store, e.g. ZREMCDBASELIST.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "ZREMCDREMINDER.ZNOTES".
	DropColumns []string
}

// uuidBytes returns a 16-byte identifier BLOB filled with tag (a distinct
// per-record UUID whose formatted form the parser must produce).
func uuidBytes(tag byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = tag
	}
	return b
}

// buildDB creates ddl's tables in a fresh database at path and returns an insert
// helper plus a close func (mirrors the safari builder).
func buildDB(t *testing.T, path string, opt FixtureOptions) (func(string, map[string]any), func()) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]map[string]bool{}
	for table, defs := range remindersDDL {
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
	return insert, func() { _ = db.Close() }
}

// entityMap inserts the Z_PRIMARYKEY rows for one store's ordinals.
func entityMap(insert func(string, map[string]any), ord map[string]int) {
	for name, ent := range ord {
		insert("Z_PRIMARYKEY", map[string]any{"Z_ENT": ent, "Z_NAME": name, "Z_SUPER": 0})
	}
	// Extra entities the parser never looks up — realistic noise.
	insert("Z_PRIMARYKEY", map[string]any{"Z_ENT": 1, "Z_NAME": "REMCDAccountListData", "Z_SUPER": 0})
}

// buildCloudStore writes the UUID-named store: two lists, an account, four
// reminders (recurrence, assignment, subtask, all-day), and a row-error row.
func buildCloudStore(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	insert, closeDB := buildDB(t, path, opt)
	defer closeDB()

	entityMap(insert, map[string]int{
		"REMCDReminder": cReminder, "REMCDList": cList, "REMCDBaseList": 2,
		"REMCDAccount": cAccount, "REMCDRecurrenceRule": cRecurrence,
		"REMCDAssignment": cAssignment, "REMCDSharee": cSharee,
	})

	// Shared-object rows: account, sharee, recurrence rule, assignment.
	insert("ZREMCDOBJECT", map[string]any{"Z_PK": 1, "Z_ENT": cAccount, "ZNAME": "iCloud"})
	insert("ZREMCDOBJECT", map[string]any{"Z_PK": 5, "Z_ENT": cSharee,
		"ZFIRSTNAME": "Sam", "ZLASTNAME": "Rivera", "ZADDRESS1": "sam@example.invalid"})
	insert("ZREMCDOBJECT", map[string]any{"Z_PK": 100, "Z_ENT": cRecurrence,
		"ZREMINDER4": 10, "ZFREQUENCY": 2, "ZINTERVAL": 1, "ZOCCURRENCECOUNT": 0})
	insert("ZREMCDOBJECT", map[string]any{"Z_PK": 101, "Z_ENT": cAssignment,
		"ZREMINDER1": 11, "ZASSIGNEE": 5})

	// Lists.
	insert("ZREMCDBASELIST", map[string]any{"Z_PK": 1, "Z_ENT": cList, "ZIDENTIFIER": uuidBytes(0xA1),
		"ZNAME": "Groceries", "ZISGROUP": 0, "ZSHARINGSTATUS": 1, "ZACCOUNT": 1})
	insert("ZREMCDBASELIST", map[string]any{"Z_PK": 2, "Z_ENT": cList, "ZIDENTIFIER": uuidBytes(0xA2),
		"ZNAME": "Work", "ZISGROUP": 0, "ZSHARINGSTATUS": 0, "ZACCOUNT": 1})

	// Reminders.
	insert("ZREMCDREMINDER", map[string]any{"Z_PK": 10, "Z_ENT": cReminder, "ZIDENTIFIER": uuidBytes(0x10),
		"ZTITLE": "Buy milk", "ZNOTES": "2% organic", "ZCOMPLETED": 0, "ZFLAGGED": 1, "ZPRIORITY": 1,
		"ZALLDAY": 0, "ZCREATIONDATE": fxCreated, "ZLASTMODIFIEDDATE": fxModified, "ZDUEDATE": fxDue,
		"ZLIST": 1, "ZACCOUNT": 1})
	insert("ZREMCDREMINDER", map[string]any{"Z_PK": 11, "Z_ENT": cReminder, "ZIDENTIFIER": uuidBytes(0x11),
		"ZTITLE": "Submit report", "ZCOMPLETED": 1, "ZCOMPLETIONDATE": fxComplete, "ZPRIORITY": 5,
		"ZLIST": 2, "ZACCOUNT": 1})
	insert("ZREMCDREMINDER", map[string]any{"Z_PK": 12, "Z_ENT": cReminder, "ZIDENTIFIER": uuidBytes(0x12),
		"ZTITLE": "Call plumber", "ZCOMPLETED": 0, "ZALLDAY": 1, "ZDUEDATE": fxDue2,
		"ZMARKEDFORDELETION": 1, "ZLIST": 2, "ZACCOUNT": 1})
	insert("ZREMCDREMINDER", map[string]any{"Z_PK": 13, "Z_ENT": cReminder, "ZIDENTIFIER": uuidBytes(0x13),
		"ZTITLE": "Buy oat milk", "ZCOMPLETED": 0, "ZPARENTREMINDER": 10, "ZLIST": 1, "ZACCOUNT": 1})
	// Row-scoped defect: a non-numeric ZCREATIONDATE forces a scan error.
	insert("ZREMCDREMINDER", map[string]any{"Z_PK": rowErrorReminderID, "Z_ENT": cReminder,
		"ZTITLE": "Corrupt date", "ZCOMPLETED": 0, "ZCREATIONDATE": "not-a-date", "ZLIST": 1, "ZACCOUNT": 1})
}

// buildLocalStore writes the fixed-name on-device store: one account, one list,
// one reminder whose Z_PK (10) COLLIDES with a cloud-store reminder — proving
// (Store, Z_PK) identity — using its own distinct ordinals.
func buildLocalStore(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	insert, closeDB := buildDB(t, path, opt)
	defer closeDB()

	entityMap(insert, map[string]int{
		"REMCDReminder": lReminder, "REMCDList": lList, "REMCDBaseList": 2,
		"REMCDAccount": lAccount,
	})
	insert("ZREMCDOBJECT", map[string]any{"Z_PK": 1, "Z_ENT": lAccount, "ZNAME": "On My iPhone"})
	insert("ZREMCDBASELIST", map[string]any{"Z_PK": 1, "Z_ENT": lList, "ZIDENTIFIER": uuidBytes(0xB1),
		"ZNAME": "Personal", "ZISGROUP": 0, "ZSHARINGSTATUS": 0, "ZACCOUNT": 1})
	insert("ZREMCDREMINDER", map[string]any{"Z_PK": 10, "Z_ENT": lReminder, "ZIDENTIFIER": uuidBytes(0x20),
		"ZTITLE": "Water plants", "ZCOMPLETED": 0, "ZLIST": 1, "ZACCOUNT": 1})
}

// BuildFixture writes both synthetic stores into a reconstructed backup tree
// rooted at root: <root>/<Domain>/Container_v1/Stores/{cloudStore,LocalStore}.
func BuildFixture(t *testing.T, root string, opt FixtureOptions) {
	t.Helper()
	dir := filepath.Join(root, Domain, filepath.FromSlash(StoresDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	buildCloudStore(t, filepath.Join(dir, cloudStore), opt)
	buildLocalStore(t, filepath.Join(dir, LocalStore), opt)
}

// Expected-object builders (each carries its Store).
func cloudAccount() *Account { return &Account{Store: cloudStore, ID: 1, Name: "iCloud"} }
func localAccount() *Account { return &Account{Store: LocalStore, ID: 1, Name: "On My iPhone"} }

func listGroceries() *List {
	return &List{Store: cloudStore, ID: 1, Identifier: formatUUID(uuidBytes(0xA1)), Name: "Groceries", SharingStatus: 1, Account: cloudAccount()}
}
func listWork() *List {
	return &List{Store: cloudStore, ID: 2, Identifier: formatUUID(uuidBytes(0xA2)), Name: "Work", Account: cloudAccount()}
}
func listPersonal() *List {
	return &List{Store: LocalStore, ID: 1, Identifier: formatUUID(uuidBytes(0xB1)), Name: "Personal", Account: localAccount()}
}

// ExpectedReminders returns what Reminders() must yield from the default fixture,
// in stream order (stores sorted by name, then Z_PK). The entry for the cloud
// store's Z_PK 14 is nil: that row yields a *backup.RowError (corrupt
// ZCREATIONDATE) and the stream continues.
func ExpectedReminders() []*Reminder {
	return []*Reminder{
		{
			Store: cloudStore, ID: 10, Identifier: formatUUID(uuidBytes(0x10)),
			Title: "Buy milk", Notes: "2% organic", Flagged: true, Priority: 1,
			Created: cocoa.FromSecondsFloat(fxCreated), Modified: cocoa.FromSecondsFloat(fxModified),
			Due: cocoa.FromSecondsFloat(fxDue), List: listGroceries(), Account: cloudAccount(),
			Recurrence: &Recurrence{Frequency: 2, Interval: 1},
		},
		{
			Store: cloudStore, ID: 11, Identifier: formatUUID(uuidBytes(0x11)),
			Title: "Submit report", Completed: true, Completion: cocoa.FromSecondsFloat(fxComplete),
			Priority: 5, List: listWork(), Account: cloudAccount(), Assignee: "Sam Rivera",
		},
		{
			Store: cloudStore, ID: 12, Identifier: formatUUID(uuidBytes(0x12)),
			Title: "Call plumber", AllDay: true, Due: cocoa.FromSecondsFloat(fxDue2),
			MarkedForDeletion: true, List: listWork(), Account: cloudAccount(),
		},
		{
			Store: cloudStore, ID: 13, Identifier: formatUUID(uuidBytes(0x13)),
			Title: "Buy oat milk", ParentID: 10, List: listGroceries(), Account: cloudAccount(),
		},
		nil, // cloud Z_PK 14: *backup.RowError — corrupt ZCREATIONDATE
		{
			Store: LocalStore, ID: 10, Identifier: formatUUID(uuidBytes(0x20)),
			Title: "Water plants", List: listPersonal(), Account: localAccount(),
		},
	}
}

// ExpectedLists returns what Lists() must yield from the default fixture, in
// stream order (stores sorted by name, then Z_PK).
func ExpectedLists() []List {
	return []List{*listGroceries(), *listWork(), *listPersonal()}
}

// CommittedCloudFixture / CommittedLocalFixture are where `make fixtures` writes
// the rung-1 artifacts (one per store).
const (
	CommittedCloudFixture = "testdata/reminders.1.cloud.sqlite"
	CommittedLocalFixture = "testdata/reminders.1.local.sqlite"
)

func TestWriteCommittedFixture(t *testing.T) {
	if os.Getenv("FIXTURE_WRITE") == "" {
		t.Skip("set FIXTURE_WRITE=1 (make fixtures) to regenerate the committed fixture")
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{CommittedCloudFixture, CommittedLocalFixture} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	buildCloudStore(t, CommittedCloudFixture, FixtureOptions{})
	buildLocalStore(t, CommittedLocalFixture, FixtureOptions{})
	t.Logf("wrote %s and %s", CommittedCloudFixture, CommittedLocalFixture)
}
