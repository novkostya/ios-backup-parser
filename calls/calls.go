// Package calls streams typed call-history records out of an iOS backup's
// CallHistory database (HomeDomain,
// Library/CallHistoryDB/CallHistory.storedata).
//
// The store is CoreData; calls flattens ZCALLRECORD into Call records and, for
// multi-party calls, resolves the ZHANDLE participants joined through
// Z_2REMOTEPARTICIPANTHANDLES. Only the canonical store is read;
// CallHistoryTemp.storedata (a short-lived buffer of not-yet-migrated recent
// calls) is out of scope for this milestone — see docs/schemas/calls.md.
//
// Open validates the schema eagerly: an unrecognized structure fails with
// backup.ErrUnsupportedSchema before any iterator exists, and absent optional
// columns degrade the Capability report instead of silently yielding empty
// fields. Iteration follows the shared error contract (see the backup package
// doc): a *backup.RowError is row-scoped and the stream continues; any other
// yielded error is stream-scoped and ends it.
package calls

import (
	"database/sql"
	"fmt"
	"io/fs"
	"iter"
	"slices"
	"strings"
	"time"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Domain and RelativePath locate the call-history database inside a backup; as
// a FileRef: backup.FileRef{Domain: Domain, RelativePath: RelativePath}.
const (
	Domain       = "HomeDomain"
	RelativePath = "Library/CallHistoryDB/CallHistory.storedata"
)

// Calls is an open call-history domain. It holds an open handle to the
// materialized scratch copy of the database; Close releases it.
type Calls struct {
	db          *sql.DB
	capability  backup.Capability
	unavailable map[string]bool
}

// Open materializes the CallHistory database out of fsys, introspects its
// schema and — when a supported fingerprint matches — returns the open domain.
// An unrecognized structure fails with backup.ErrUnsupportedSchema (wrapped in
// *backup.UnsupportedSchemaError carrying the observed fingerprint); a backup
// without the database fails with fs.ErrNotExist.
func Open(fsys backup.FS) (*Calls, error) {
	ok, err := fsys.Exists(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("calls: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("calls: backup has no %s/%s: %w", Domain, RelativePath, fs.ErrNotExist)
	}
	path, err := fsys.Materialize(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("calls: %w", err)
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("calls: %w", err)
	}
	result, err := introspect.Detect(db, spec)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Calls{
		db:          db,
		capability:  result.Capability,
		unavailable: result.Unavailable,
	}, nil
}

// Capability returns the capability report produced at Open.
func (c *Calls) Capability() backup.Capability {
	capability := c.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes the underlying database handle. (The scratch copy itself
// belongs to the FS that materialized it.)
func (c *Calls) Close() error {
	return c.db.Close()
}

// Calls streams every ZCALLRECORD row in Z_PK order. See the package doc for
// the row-scoped vs stream-scoped error contract.
func (c *Calls) Calls() iter.Seq2[Call, error] {
	return func(yield func(Call, error) bool) {
		// Participant handles are a small reference table, preloaded once per
		// iteration (nil when the schema lacks the participants join).
		var handles map[int64]Handle
		if !c.unavailable["participants"] {
			var err error
			handles, err = c.loadHandles()
			if err != nil {
				yield(Call{}, fmt.Errorf("calls: %w", err))
				return
			}
		}

		row := &callRow{}
		sel := []string{"Z_PK", "ZDATE", "ZDURATION", "ZORIGINATED", "ZANSWERED", "ZCALLTYPE", "ZADDRESS"}
		dest := []any{&row.id, &row.date, &row.duration, &row.originated, &row.answered, &row.callType, &row.address}
		include := func(unit, col string, target any) {
			if !c.unavailable[unit] {
				sel = append(sel, col)
				dest = append(dest, target)
			}
		}
		include("name", "ZNAME", &row.name)
		include("service_provider", "ZSERVICE_PROVIDER", &row.serviceProvider)
		include("iso_country_code", "ZISO_COUNTRY_CODE", &row.isoCountryCode)
		include("unique_id", "ZUNIQUE_ID", &row.uniqueID)
		include("read", "ZREAD", &row.read)
		if !c.unavailable["spam"] {
			sel = append(sel, "ZJUNKCONFIDENCE", "ZJUNKIDENTIFICATIONCATEGORY")
			dest = append(dest, &row.junkConfidence, &row.junkCategory)
		}

		rows, err := c.db.Query("SELECT " + strings.Join(sel, ", ") + " FROM ZCALLRECORD ORDER BY Z_PK")
		if err != nil {
			yield(Call{}, fmt.Errorf("calls: query calls: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = callRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Call{}, &backup.RowError{
					Domain: "calls", Table: "ZCALLRECORD", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			call := row.call()
			if handles != nil {
				if err, rowScoped := c.fillParticipants(&call, handles); err != nil {
					if !rowScoped {
						yield(Call{}, fmt.Errorf("calls: %w", err))
						return
					}
					if !yield(Call{}, &backup.RowError{
						Domain: "calls", Table: "ZCALLRECORD", RowID: call.ID, Err: err,
					}) {
						return
					}
					continue
				}
			}
			if !yield(call, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Call{}, fmt.Errorf("calls: read calls: %w", err))
		}
	}
}

// callRow holds one scanned ZCALLRECORD row; only the columns selected for this
// database's capability are filled.
type callRow struct {
	id              sql.NullInt64
	date            sql.NullFloat64 // ZDATE — Cocoa seconds, REAL
	duration        sql.NullFloat64 // ZDURATION — seconds, FLOAT
	originated      sql.NullInt64
	answered        sql.NullInt64
	callType        sql.NullInt64
	address         sql.NullString
	name            sql.NullString
	serviceProvider sql.NullString
	isoCountryCode  sql.NullString
	uniqueID        sql.NullString
	read            sql.NullInt64
	junkConfidence  sql.NullInt64
	junkCategory    sql.NullString // ZJUNKIDENTIFICATIONCATEGORY — VARCHAR
}

func (r *callRow) call() Call {
	c := Call{
		ID:              r.id.Int64,
		Direction:       r.originated.Int64,
		Answered:        r.answered.Valid && r.answered.Int64 != 0,
		CallType:        r.callType.Int64,
		Address:         r.address.String,
		Name:            r.name.String,
		ServiceProvider: r.serviceProvider.String,
		ISOCountryCode:  r.isoCountryCode.String,
		UniqueID:        r.uniqueID.String,
		Read:            r.read.Valid && r.read.Int64 != 0,
		JunkConfidence:  r.junkConfidence.Int64,
		JunkCategory:    r.junkCategory.String,
	}
	if r.date.Valid {
		c.Time = cocoa.FromSecondsFloat(r.date.Float64)
	}
	if r.duration.Valid {
		c.Duration = time.Duration(r.duration.Float64 * float64(time.Second))
	}
	return c
}

// loadHandles preloads the ZHANDLE reference table for participant resolution.
func (c *Calls) loadHandles() (map[int64]Handle, error) {
	rows, err := c.db.Query("SELECT Z_PK, ZVALUE, ZNORMALIZEDVALUE, ZTYPE FROM ZHANDLE")
	if err != nil {
		return nil, fmt.Errorf("load handles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Handle{}
	for rows.Next() {
		var id, typ sql.NullInt64
		var value, normalized sql.NullString
		if err := rows.Scan(&id, &value, &normalized, &typ); err != nil {
			return nil, fmt.Errorf("load handles: %w", err)
		}
		out[id.Int64] = Handle{
			ID:              id.Int64,
			Value:           value.String,
			NormalizedValue: normalized.String,
			Type:            typ.Int64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load handles: %w", err)
	}
	return out, nil
}

// fillParticipants resolves a call's ZHANDLE participants through the CoreData
// many-to-many join. The bool result classifies the error: true = row-scoped
// defect (this call only), false = stream-scoped.
func (c *Calls) fillParticipants(call *Call, handles map[int64]Handle) (error, bool) {
	rows, err := c.db.Query(
		"SELECT Z_4REMOTEPARTICIPANTHANDLES FROM Z_2REMOTEPARTICIPANTHANDLES"+
			" WHERE Z_2REMOTEPARTICIPANTCALLS = ? ORDER BY Z_4REMOTEPARTICIPANTHANDLES", call.ID)
	if err != nil {
		return fmt.Errorf("query participants: %w", err), false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var handleID sql.NullInt64
		if err := rows.Scan(&handleID); err != nil {
			return fmt.Errorf("participant: %w", err), true
		}
		handle, ok := handles[handleID.Int64]
		if !ok {
			// A dangling handle reference would drop a participant silently;
			// withhold the whole call instead (row-scoped), never emit a
			// partial participant set as if it were complete.
			return fmt.Errorf("participant: dangling handle reference %d", handleID.Int64), true
		}
		call.Participants = append(call.Participants, handle)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read participants: %w", err), false
	}
	return nil, false
}
