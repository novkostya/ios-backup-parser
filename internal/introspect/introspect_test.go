package introspect

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

func testDB(t *testing.T, ddl ...string) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range ddl {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return db
}

func TestDetect(t *testing.T) {
	db := testDB(t,
		"CREATE TABLE A (x TEXT, y INTEGER)",
		"CREATE TABLE B (z TEXT)",
	)
	spec := Spec{
		Domain: "testdomain",
		Fingerprints: []Fingerprint{{
			Label:    "testdomain.1",
			Required: Tables{"A": {"ROWID", "x"}}, // ROWID is implicit and must count as present
			Optional: []Unit{
				{Name: "with_y", Tables: Tables{"A": {"y"}}},
				{Name: "with_b", Tables: Tables{"B": {"z"}}},
				{Name: "gone_column", Tables: Tables{"A": {"q"}}},
				{Name: "gone_table", Tables: Tables{"C": {"r"}}},
			},
			AlwaysMissing: []string{"photo"},
		}},
	}
	result, err := Detect(db, spec)
	if err != nil {
		t.Fatal(err)
	}
	c := result.Capability
	if !c.Supported || c.Domain != "testdomain" || c.Schema != "testdomain.1" {
		t.Errorf("capability = %+v", c)
	}
	if want := []string{"gone_column", "gone_table", "photo"}; !equal(c.Missing, want) {
		t.Errorf("Missing = %v, want %v", c.Missing, want)
	}
	if !result.Unavailable["gone_column"] || !result.Unavailable["gone_table"] ||
		result.Unavailable["with_y"] || result.Unavailable["with_b"] {
		t.Errorf("Unavailable = %v", result.Unavailable)
	}
}

func TestDetectUnsupported(t *testing.T) {
	db := testDB(t, "CREATE TABLE A (x TEXT, y INTEGER)")
	spec := Spec{
		Domain: "testdomain",
		Fingerprints: []Fingerprint{{
			Label:    "testdomain.1",
			Required: Tables{"A": {"x", "missing_col"}, "D": {"d"}},
		}},
	}
	_, err := Detect(db, spec)
	if !errors.Is(err, backup.ErrUnsupportedSchema) {
		t.Fatalf("err = %v, want ErrUnsupportedSchema", err)
	}
	var unsupported *backup.UnsupportedSchemaError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T, want *UnsupportedSchemaError", err)
	}
	if unsupported.Domain != "testdomain" {
		t.Errorf("Domain = %q", unsupported.Domain)
	}
	if !strings.Contains(unsupported.Fingerprint, "A(x,y)") {
		t.Errorf("Fingerprint = %q, want it to render the observed structure", unsupported.Fingerprint)
	}
	for _, want := range []string{"A.missing_col", "D", "testdomain.1"} {
		if !strings.Contains(unsupported.Reason, want) {
			t.Errorf("Reason = %q, want it to mention %s", unsupported.Reason, want)
		}
	}
}

func TestDetectSecondFingerprintMatches(t *testing.T) {
	db := testDB(t, "CREATE TABLE Old (a TEXT)")
	spec := Spec{
		Domain: "testdomain",
		Fingerprints: []Fingerprint{
			{Label: "testdomain.1", Required: Tables{"New": {"n"}}},
			{Label: "testdomain.2", Required: Tables{"Old": {"a"}}},
		},
	}
	result, err := Detect(db, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Capability.Schema != "testdomain.2" {
		t.Errorf("Schema = %q, want testdomain.2", result.Capability.Schema)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
