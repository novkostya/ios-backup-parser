package backup

import (
	"errors"
	"fmt"
)

// ErrUnsupportedSchema reports that a domain database's introspected structure
// matched no supported schema fingerprint. Domain Open functions return it
// eagerly — before any iterator exists — always wrapped in an
// *UnsupportedSchemaError carrying the observed fingerprint. Test with
// errors.Is; retrieve the fingerprint with errors.As.
var ErrUnsupportedSchema = errors.New("unsupported schema")

// ErrUnavailable reports that a requested stream or field is listed in the
// domain's Capability.Missing — this backup's schema cannot provide it.
// Iterators for unavailable data yield it instead of an empty stream, so
// "none present" is never conflated with "cannot know".
var ErrUnavailable = errors.New("not available in this backup's schema")

// UnsupportedSchemaError carries the evidence behind ErrUnsupportedSchema.
type UnsupportedSchemaError struct {
	// Domain is the domain that rejected the database, e.g. "contacts".
	Domain string

	// Fingerprint is the observed structure — the tables and columns relevant
	// to the domain that were actually present — rendered compactly as
	// "Table(col,col,…); Table(…)". This is the fingerprint's identity; report
	// it when filing a schema-support issue.
	Fingerprint string

	// Reason names the first requirements the baseline fingerprint did not
	// find, e.g. "missing ABMultiValue.record_id".
	Reason string
}

func (e *UnsupportedSchemaError) Error() string {
	return fmt.Sprintf("%s: unsupported schema: %s; observed fingerprint: %s",
		e.Domain, e.Reason, e.Fingerprint)
}

// Is makes errors.Is(err, ErrUnsupportedSchema) true.
func (e *UnsupportedSchemaError) Is(target error) bool {
	return target == ErrUnsupportedSchema
}

// RowError is a row-scoped defect: one database row that could not be turned
// into a record (corrupt content, a dangling reference). Iterators yield
// (zero, *RowError) and CONTINUE with the next row. Any yielded error that is
// not a *RowError is stream-scoped and ends the iteration.
type RowError struct {
	// Domain is the domain package name, e.g. "contacts".
	Domain string
	// Table is the anchor table of the defective row, e.g. "ABPerson".
	Table string
	// RowID identifies the defective row within Table.
	RowID int64
	// Err is the underlying defect.
	Err error
}

func (e *RowError) Error() string {
	return fmt.Sprintf("%s: %s rowid %d: %v", e.Domain, e.Table, e.RowID, e.Err)
}

func (e *RowError) Unwrap() error { return e.Err }
