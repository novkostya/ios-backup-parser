// Package introspect implements schema fingerprint detection.
//
// Support is decided by the OBSERVED table/column structure of the database at
// hand — never by an iOS version string (charter hard rule). A fingerprint's
// identity is that structure; its label ("contacts.1") is a human alias in
// discovery order.
//
// Match semantics: a database matches a Fingerprint when every Required table
// and column is present. Extra, unknown columns never disqualify a match (new
// iOS releases add columns constantly); an absent Optional unit degrades the
// capability report (Capability.Missing) instead of failing. When no
// fingerprint matches, Detect returns *backup.UnsupportedSchemaError carrying
// the observed structure.
package introspect

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	backup "github.com/novkostya/ios-backup-parser"
)

// Tables maps a table name to the column names required of it.
type Tables map[string][]string

// Unit is an optional capability unit: a record field name (as it appears in
// Capability.Missing) together with the tables/columns that provide it. The
// unit is available only when EVERY listed table and column is present.
type Unit struct {
	Name   string
	Tables Tables
}

// Fingerprint is one supported schema shape for a domain.
type Fingerprint struct {
	// Label is the discovery-order alias, e.g. "contacts.1".
	Label string
	// Required tables/columns; all must be present for the fingerprint to
	// match. Keep this to what extraction genuinely cannot do without —
	// anything that can degrade honestly belongs in Optional.
	Required Tables
	// Optional units; an absent unit lands its Name in Capability.Missing.
	Optional []Unit
	// AlwaysMissing lists intended record fields this fingerprint can never
	// provide (e.g. data that lives in an out-of-scope sibling database).
	// They are reported in Capability.Missing unconditionally.
	AlwaysMissing []string
}

// Spec is a domain's set of supported fingerprints, tried in order.
type Spec struct {
	// Domain is the domain package name, e.g. "contacts".
	Domain string
	// Fingerprints in discovery order; the first match wins.
	Fingerprints []Fingerprint
}

// Result is a successful detection.
type Result struct {
	Capability backup.Capability
	// Unavailable holds the names of the matched fingerprint's Optional units
	// that this database lacks (a subset of Capability.Missing).
	Unavailable map[string]bool
}

// Detect introspects db against spec.
func Detect(db *sql.DB, spec Spec) (*Result, error) {
	observed, err := observe(db, universe(spec))
	if err != nil {
		return nil, fmt.Errorf("%s: introspect: %w", spec.Domain, err)
	}

	for _, fp := range spec.Fingerprints {
		if len(unmet(fp.Required, observed)) > 0 {
			continue
		}
		result := &Result{Unavailable: map[string]bool{}}
		missing := slices.Clone(fp.AlwaysMissing)
		for _, unit := range fp.Optional {
			if len(unmet(unit.Tables, observed)) > 0 {
				result.Unavailable[unit.Name] = true
				missing = append(missing, unit.Name)
			}
		}
		slices.Sort(missing)
		result.Capability = backup.Capability{
			Domain:    spec.Domain,
			Supported: true,
			Schema:    fp.Label,
			Missing:   slices.Compact(missing),
		}
		return result, nil
	}

	reason := "no fingerprints defined"
	if len(spec.Fingerprints) > 0 {
		base := spec.Fingerprints[0]
		reason = fmt.Sprintf("missing %s (vs %s)",
			strings.Join(unmet(base.Required, observed), ", "), base.Label)
	}
	return nil, &backup.UnsupportedSchemaError{
		Domain:      spec.Domain,
		Fingerprint: render(observed),
		Reason:      reason,
	}
}

// universe collects every table name any fingerprint mentions — the tables
// "relevant to the domain" that constitute fingerprint identity.
func universe(spec Spec) []string {
	set := map[string]bool{}
	for _, fp := range spec.Fingerprints {
		for table := range fp.Required {
			set[table] = true
		}
		for _, unit := range fp.Optional {
			for table := range unit.Tables {
				set[table] = true
			}
		}
	}
	tables := make([]string, 0, len(set))
	for table := range set {
		tables = append(tables, table)
	}
	slices.Sort(tables)
	return tables
}

// observe reads the column set of each named table that exists. A table
// absent from the database is absent from the map.
func observe(db *sql.DB, tables []string) (map[string][]string, error) {
	observed := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return nil, fmt.Errorf("table_info %s: %w", table, err)
		}
		var cols []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("table_info %s: %w", table, err)
			}
			cols = append(cols, name)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("table_info %s: %w", table, err)
		}
		_ = rows.Close()
		if len(cols) > 0 {
			slices.Sort(cols)
			observed[table] = cols
		}
	}
	return observed, nil
}

// unmet returns "Table" / "Table.column" names required by want but absent
// from observed, sorted.
//
// "ROWID" is special-cased as always present: every non-WITHOUT-ROWID SQLite
// table has an implicit rowid even when no column declares it (e.g.
// ABMultiValueLabel), and pragma table_info never lists the implicit one.
func unmet(want Tables, observed map[string][]string) []string {
	var out []string
	for table, cols := range want {
		have, ok := observed[table]
		if !ok {
			out = append(out, table)
			continue
		}
		for _, col := range cols {
			if col == "ROWID" {
				continue
			}
			if !slices.Contains(have, col) {
				out = append(out, table+"."+col)
			}
		}
	}
	slices.Sort(out)
	return out
}

// render prints an observed structure compactly: "A(x,y); B(z)".
func render(observed map[string][]string) string {
	if len(observed) == 0 {
		return "(none of the domain's tables present)"
	}
	tables := make([]string, 0, len(observed))
	for table := range observed {
		tables = append(tables, table)
	}
	slices.Sort(tables)
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		parts = append(parts, table+"("+strings.Join(observed[table], ",")+")")
	}
	return strings.Join(parts, "; ")
}
