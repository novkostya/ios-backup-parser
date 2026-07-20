package calls

// Synthetic fixture builder (testing ladder rung 1).
//
// The DDL below mirrors the OBSERVED structure of fingerprint calls.1
// (docs/schemas/calls.md): the CoreData tables and column sets the parser reads,
// plus a realistic superset of ZCALLRECORD's other columns (trust score,
// emergency, screen-sharing, …) so the fixture proves "unknown extra columns
// never disqualify". CoreData bookkeeping tables (Z_PRIMARYKEY, Z_METADATA,
// Z_MODELCACHE), indexes and triggers are not fingerprint-relevant and are
// omitted. Every inserted row is invented; nothing here derives from a real
// backup (charter privacy gate).
//
// TestWriteCommittedFixture regenerates the committed fixture
// (testdata/calls.1.CallHistory.storedata) when FIXTURE_WRITE is set — via
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
	"time"

	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// calls1DDL: column definitions per table, first token = column name.
var calls1DDL = map[string][]string{
	"ZCALLRECORD": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "Z_OPT INTEGER",
		"ZANSWERED INTEGER", "ZCALL_CATEGORY INTEGER", "ZCALLTYPE INTEGER",
		"ZDISCONNECTED_CAUSE INTEGER", "ZFACE_TIME_DATA INTEGER",
		"ZFILTERED_OUT_REASON INTEGER", "ZHANDLE_TYPE INTEGER",
		"ZHASMESSAGE INTEGER", "ZJUNKCONFIDENCE INTEGER", "ZORIGINATED INTEGER",
		"ZREAD INTEGER", "ZWASEMERGENCYCALL INTEGER",
		"ZCOMMUNICATIONTRUSTSCORE INTEGER", "ZDATE TIMESTAMP", "ZDURATION FLOAT",
		"ZADDRESS VARCHAR", "ZISO_COUNTRY_CODE VARCHAR",
		"ZJUNKIDENTIFICATIONCATEGORY VARCHAR", "ZLOCATION VARCHAR",
		"ZNAME VARCHAR", "ZSERVICE_PROVIDER VARCHAR", "ZUNIQUE_ID VARCHAR",
	},
	"ZHANDLE": {
		"Z_PK INTEGER PRIMARY KEY", "Z_ENT INTEGER", "Z_OPT INTEGER",
		"ZTYPE INTEGER", "ZNORMALIZEDVALUE VARCHAR", "ZVALUE VARCHAR",
	},
	"Z_2REMOTEPARTICIPANTHANDLES": {
		"Z_2REMOTEPARTICIPANTCALLS INTEGER", "Z_4REMOTEPARTICIPANTHANDLES INTEGER",
	},
}

// FixtureOptions degrade the built database for negative tests.
type FixtureOptions struct {
	// DropTables omits whole tables, e.g. ZHANDLE.
	DropTables []string
	// DropColumns omits "Table.Column" definitions, e.g. "ZCALLRECORD.ZNAME".
	DropColumns []string
}

// Fixture timestamps (Cocoa seconds, REAL — fractional on purpose, to exercise
// cocoa.FromSecondsFloat) and durations (seconds), shared with the expectations.
const (
	fixtureDate1 = 700000000.5
	fixtureDate2 = 700086400.25
	fixtureDate3 = 700172800.0
	fixtureDate4 = 700259200.0
	fixtureDate5 = 700345600.0

	fixtureDuration1 = 65.5
	fixtureDuration3 = 120.0
)

// danglingHandlePK is a participant handle reference with no ZHANDLE row.
const danglingHandlePK = 999

// BuildFixture writes a synthetic call-history database to path.
func BuildFixture(t *testing.T, path string, opt FixtureOptions) {
	t.Helper()
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	present := map[string]map[string]bool{}
	for table, defs := range calls1DDL {
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

	// Participant handles (entity Handle = Z_ENT 4).
	insert("ZHANDLE", map[string]any{"Z_PK": 1, "Z_ENT": 4, "Z_OPT": 1, "ZTYPE": 0, "ZVALUE": "+1 555 0300", "ZNORMALIZEDVALUE": "+15550300"})
	insert("ZHANDLE", map[string]any{"Z_PK": 2, "Z_ENT": 4, "Z_OPT": 1, "ZTYPE": 0, "ZVALUE": "+1 555 0301", "ZNORMALIZEDVALUE": "+15550301"})

	// Call 1 — fully populated 1:1 outgoing answered phone call.
	insert("ZCALLRECORD", map[string]any{
		"Z_PK": 1, "Z_ENT": 2, "Z_OPT": 1,
		"ZDATE": fixtureDate1, "ZDURATION": fixtureDuration1,
		"ZORIGINATED": DirectionOutgoing, "ZANSWERED": 1, "ZCALLTYPE": CallTypePhone,
		"ZADDRESS": "+1 555 0100", "ZNAME": "Alex Fixture",
		"ZSERVICE_PROVIDER": "com.apple.Telephony", "ZISO_COUNTRY_CODE": "us",
		"ZUNIQUE_ID": "fixture-uid-1", "ZREAD": 1, "ZJUNKCONFIDENCE": 0,
	})

	// Call 2 — missed incoming call, flagged as spam/junk.
	insert("ZCALLRECORD", map[string]any{
		"Z_PK": 2, "Z_ENT": 2, "Z_OPT": 1,
		"ZDATE": fixtureDate2, "ZDURATION": 0.0,
		"ZORIGINATED": DirectionIncoming, "ZANSWERED": 0, "ZCALLTYPE": CallTypePhone,
		"ZADDRESS": "+1 555 0142", "ZREAD": 0,
		"ZJUNKCONFIDENCE": 2, "ZJUNKIDENTIFICATIONCATEGORY": "fixture-spam",
	})

	// Call 3 — FaceTime video group call; the counterparts are participants,
	// not ZADDRESS.
	insert("ZCALLRECORD", map[string]any{
		"Z_PK": 3, "Z_ENT": 2, "Z_OPT": 1,
		"ZDATE": fixtureDate3, "ZDURATION": fixtureDuration3,
		"ZORIGINATED": DirectionOutgoing, "ZANSWERED": 1, "ZCALLTYPE": CallTypeFaceTimeVideo,
		"ZADDRESS": nil,
	})
	insert("Z_2REMOTEPARTICIPANTHANDLES", map[string]any{"Z_2REMOTEPARTICIPANTCALLS": 3, "Z_4REMOTEPARTICIPANTHANDLES": 1})
	insert("Z_2REMOTEPARTICIPANTHANDLES", map[string]any{"Z_2REMOTEPARTICIPANTCALLS": 3, "Z_4REMOTEPARTICIPANTHANDLES": 2})

	// Call 4 — row-scoped defect: a participant handle that points nowhere.
	insert("ZCALLRECORD", map[string]any{
		"Z_PK": 4, "Z_ENT": 2, "Z_OPT": 1,
		"ZDATE": fixtureDate4, "ZDURATION": 0.0,
		"ZORIGINATED": DirectionIncoming, "ZANSWERED": 1, "ZCALLTYPE": CallTypePhone,
		"ZADDRESS": "+1 555 0166",
	})
	insert("Z_2REMOTEPARTICIPANTHANDLES", map[string]any{"Z_2REMOTEPARTICIPANTCALLS": 4, "Z_4REMOTEPARTICIPANTHANDLES": danglingHandlePK})

	// Call 5 — minimal: only required columns non-NULL, no optional data.
	insert("ZCALLRECORD", map[string]any{
		"Z_PK": 5, "Z_ENT": 2, "Z_OPT": 1,
		"ZDATE": fixtureDate5, "ZDURATION": 0.0,
		"ZORIGINATED": DirectionIncoming, "ZANSWERED": 1, "ZCALLTYPE": CallTypePhone,
		"ZADDRESS": "+1 555 0199",
	})
}

// ExpectedCalls returns what parsing the default fixture must yield, in stream
// order (by Z_PK). The entry for Z_PK 4 is nil: that row yields a
// *backup.RowError (dangling handle) and the stream continues.
func ExpectedCalls() []*Call {
	return []*Call{
		{
			ID: 1, Time: cocoa.FromSecondsFloat(fixtureDate1),
			Duration:  time.Duration(fixtureDuration1 * float64(time.Second)),
			Direction: DirectionOutgoing, Answered: true, CallType: CallTypePhone,
			Address: "+1 555 0100", Name: "Alex Fixture",
			ServiceProvider: "com.apple.Telephony", ISOCountryCode: "us",
			UniqueID: "fixture-uid-1", Read: true,
		},
		{
			ID: 2, Time: cocoa.FromSecondsFloat(fixtureDate2),
			Direction: DirectionIncoming, Answered: false, CallType: CallTypePhone,
			Address: "+1 555 0142", JunkConfidence: 2, JunkCategory: "fixture-spam",
		},
		{
			ID: 3, Time: cocoa.FromSecondsFloat(fixtureDate3),
			Duration:  time.Duration(fixtureDuration3 * float64(time.Second)),
			Direction: DirectionOutgoing, Answered: true, CallType: CallTypeFaceTimeVideo,
			Participants: []Handle{
				{ID: 1, Value: "+1 555 0300", NormalizedValue: "+15550300", Type: 0},
				{ID: 2, Value: "+1 555 0301", NormalizedValue: "+15550301", Type: 0},
			},
		},
		nil, // Z_PK 4: *backup.RowError — dangling handle reference
		{
			ID: 5, Time: cocoa.FromSecondsFloat(fixtureDate5),
			Direction: DirectionIncoming, Answered: true, CallType: CallTypePhone,
			Address: "+1 555 0199",
		},
	}
}

// CommittedFixturePath is where `make fixtures` writes the rung-1 artifact.
const CommittedFixturePath = "testdata/calls.1.CallHistory.storedata"

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
