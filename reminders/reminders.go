// Package reminders streams typed reminder records out of an iOS backup's
// Reminders database (AppDomainGroup-group.com.apple.reminders).
//
// Reminders moved to their own store around iOS 13 (before that they shared the
// calendar database — the M0 calendar doc recorded CalendarItem's reminder
// columns as present-but-unused for exactly this reason). The modern store is
// CoreData + CloudKit, and it SPANS MULTIPLE FILES: Container_v1/Stores holds
// one Data-<UUID>.sqlite per account plus the on-device Data-local.sqlite. Every
// store shares one model — reminders in ZREMCDREMINDER, lists in ZREMCDBASELIST,
// and accounts/recurrence/assignments/sharees as REMCDObject subclasses sharing
// ZREMCDOBJECT, all discriminated by Z_ENT ordinals resolved from Z_PRIMARYKEY
// at Open (never hard-coded; the stores do not agree on the ordinals).
//
// Reminders() flattens each ZREMCDREMINDER row across every store into a
// Reminder (its list, account, recurrence and assignee resolved); Lists()
// streams the list containers. Titles and notes are plain columns — no blob
// decode (unlike messages/notes).
//
// Enumerating the UUID-named stores needs the optional backup.ReadDirFS
// capability. The built-in DirFS provides it; a host that does not is served
// best-effort — only the fixed-name Data-local.sqlite is read and
// "cloudkit_stores" is added to Capability.Missing, rather than failing or
// silently under-reading.
//
// Open validates the schema eagerly: a store whose structure matches no
// supported fingerprint fails with backup.ErrUnsupportedSchema before any
// iterator exists, and absent optional columns degrade the Capability report
// instead of silently yielding empty fields. Iteration follows the shared error
// contract (see the backup package doc): a *backup.RowError is row-scoped and
// the stream continues; any other yielded error is stream-scoped and ends it.
// Descriptive references (list, account, recurrence, assignee) resolve with
// LEFT-JOIN semantics — nil/empty when unresolved — and never withhold a
// reminder; the only row-scoped defect is a row that fails to scan.
package reminders

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"path"
	"slices"
	"strings"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Domain locates the Reminders group container inside a backup. StoresDir is the
// directory holding the per-account stores; LocalStore is the one store whose
// name is fixed (the on-device account), used as the fallback when a host cannot
// enumerate the directory.
const (
	Domain     = "AppDomainGroup-group.com.apple.reminders"
	StoresDir  = "Container_v1/Stores"
	LocalStore = "Data-local.sqlite"
)

// Reader is an open reminders domain. It holds open handles to the materialized
// scratch copies of every reminder store; Close releases them all.
type Reader struct {
	stores      []*storeHandle
	capability  backup.Capability
	unavailable map[string]bool
}

// storeHandle is one opened store file with its per-store entity ordinals.
type storeHandle struct {
	name string // base filename, e.g. "Data-local.sqlite"
	db   *sql.DB
	ent  entities
}

// entities holds the per-store Z_ENT ordinals for the entities the parser reads,
// resolved from that store's Z_PRIMARYKEY map by name. Resolving them (rather
// than hard-coding an observed ordinal) keeps the parser correct across stores
// that renumber entities. REMCDReminder is mandatory; the rest degrade to 0
// (their resolution is then skipped) if absent from a store's map.
type entities struct {
	reminder, account, recurrence, assignment, sharee int64
}

// Open discovers the reminder stores under fsys, materializes and introspects
// each, and — when every store matches a supported fingerprint — returns the
// open domain. A store matching no fingerprint fails with
// backup.ErrUnsupportedSchema (wrapped in *backup.UnsupportedSchemaError); a
// backup with no reminder store fails with fs.ErrNotExist.
func Open(fsys backup.FS) (*Reader, error) {
	names, enumerated, err := storeNames(fsys)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("reminders: backup has no %s/%s/Data-*.sqlite: %w", Domain, StoresDir, fs.ErrNotExist)
	}

	r := &Reader{unavailable: map[string]bool{}}
	capSet := false
	for _, name := range names {
		rel := path.Join(StoresDir, name)
		dbPath, err := fsys.Materialize(Domain, rel)
		if err != nil {
			_ = r.closeAll()
			return nil, fmt.Errorf("reminders: %w", err)
		}
		db, err := sqlitedb.Open(dbPath)
		if err != nil {
			_ = r.closeAll()
			return nil, fmt.Errorf("reminders: %w", err)
		}
		result, err := introspect.Detect(db, spec)
		if err != nil {
			_ = db.Close()
			_ = r.closeAll()
			return nil, err
		}
		ent, err := loadEntities(db)
		if err != nil {
			_ = db.Close()
			_ = r.closeAll()
			return nil, fmt.Errorf("reminders: %s: %w", name, err)
		}
		r.stores = append(r.stores, &storeHandle{name: name, db: db, ent: ent})
		if !capSet {
			// Every store shares the model, so the first store's capability
			// describes them all.
			r.capability = result.Capability
			r.unavailable = result.Unavailable
			capSet = true
		}
	}
	if !enumerated {
		// A host without ReadDirFS saw only the fixed-name store; the
		// per-account CloudKit stores could not be discovered.
		r.markMissing("cloudkit_stores")
	}
	return r, nil
}

// storeNames returns the reminder store filenames to open. With a ReadDirFS host
// it enumerates every Data-*.sqlite in the Stores directory (enumerated=true);
// without one it falls back to the fixed-name Data-local.sqlite when present
// (enumerated=false), which the caller records as a capability shortfall.
func storeNames(fsys backup.FS) (names []string, enumerated bool, err error) {
	if rd, ok := fsys.(backup.ReadDirFS); ok {
		entries, err := rd.ReadDir(Domain, StoresDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, true, nil // the store directory is absent
			}
			return nil, true, fmt.Errorf("reminders: %w", err)
		}
		for _, e := range entries {
			// A store is Data-*.sqlite; skip -wal/-shm/-journal sidecars and the
			// unrelated MLModels databases (which live in a sibling directory
			// anyway). Materialize copies any sidecars automatically.
			if strings.HasPrefix(e, "Data-") && strings.HasSuffix(e, ".sqlite") {
				names = append(names, e)
			}
		}
		slices.Sort(names) // deterministic order regardless of the FS's listing order
		return names, true, nil
	}
	ok, err := fsys.Exists(Domain, path.Join(StoresDir, LocalStore))
	if err != nil {
		return nil, false, fmt.Errorf("reminders: %w", err)
	}
	if ok {
		names = append(names, LocalStore)
	}
	return names, false, nil
}

// loadEntities reads a store's Z_PRIMARYKEY entity map and resolves its Z_ENT
// ordinals by name. REMCDReminder is mandatory (without it there are no
// reminders to read); the others degrade to 0 (skipped) if absent.
func loadEntities(db *sql.DB) (entities, error) {
	rows, err := db.Query("SELECT Z_ENT, Z_NAME FROM Z_PRIMARYKEY")
	if err != nil {
		return entities{}, fmt.Errorf("read entity map: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byName := map[string]int64{}
	for rows.Next() {
		var ent sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&ent, &name); err != nil {
			return entities{}, fmt.Errorf("read entity map: %w", err)
		}
		if name.Valid {
			byName[name.String] = ent.Int64
		}
	}
	if err := rows.Err(); err != nil {
		return entities{}, fmt.Errorf("read entity map: %w", err)
	}
	ent := entities{
		reminder:   byName["REMCDReminder"],
		account:    byName["REMCDAccount"],
		recurrence: byName["REMCDRecurrenceRule"],
		assignment: byName["REMCDAssignment"],
		sharee:     byName["REMCDSharee"],
	}
	if ent.reminder == 0 {
		return entities{}, errors.New("entity map lacks REMCDReminder")
	}
	return ent, nil
}

// markMissing records unit as absent in both the unavailable set and the
// capability report (kept sorted and deduplicated). Used for the cross-file
// "cloudkit_stores" unit, which no single store's introspection can see.
func (r *Reader) markMissing(unit string) {
	r.unavailable[unit] = true
	missing := append(slices.Clone(r.capability.Missing), unit)
	slices.Sort(missing)
	r.capability.Missing = slices.Compact(missing)
}

// Capability returns the capability report produced at Open.
func (r *Reader) Capability() backup.Capability {
	capability := r.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes every underlying database handle. (The scratch copies themselves
// belong to the FS that materialized them.)
func (r *Reader) Close() error {
	return r.closeAll()
}

func (r *Reader) closeAll() error {
	var err error
	for _, s := range r.stores {
		if s.db != nil {
			if cerr := s.db.Close(); err == nil {
				err = cerr
			}
		}
	}
	return err
}

// Reminders streams every reminder across every store, in (store, Z_PK) order,
// with its list, account, recurrence and assignee resolved. See the package doc
// for the row-scoped vs stream-scoped error contract.
func (r *Reader) Reminders() iter.Seq2[Reminder, error] {
	return func(yield func(Reminder, error) bool) {
		for _, s := range r.stores {
			if !r.streamStore(s, yield) {
				return
			}
		}
	}
}

// streamStore streams one store's reminders. It returns true to continue with
// the next store, false to stop the whole iteration — either because the
// consumer asked to stop (yield returned false) or because a STREAM-scoped error
// was yielded (a stream-scoped failure terminates iteration; row-scoped scan
// errors are yielded and the stream continues).
func (r *Reader) streamStore(s *storeHandle, yield func(Reminder, error) bool) bool {
	children, err := r.loadChildren(s)
	if err != nil {
		yield(Reminder{}, fmt.Errorf("reminders: %s: %w", s.name, err))
		return false
	}

	row := &reminderRow{}
	sel := []string{"Z_PK", "ZTITLE"}
	dest := []any{&row.id, &row.title}
	col := func(unit, expr string, target any) {
		if !r.unavailable[unit] {
			sel = append(sel, expr)
			dest = append(dest, target)
		}
	}
	col("identifier", "ZIDENTIFIER", &row.identifier)
	col("notes", "ZNOTES", &row.notes)
	col("flagged", "ZFLAGGED", &row.flagged)
	col("priority", "ZPRIORITY", &row.priority)
	col("all_day", "ZALLDAY", &row.allDay)
	col("created", "ZCREATIONDATE", &row.created)
	col("modified", "ZLASTMODIFIEDDATE", &row.modified)
	col("due", "ZDUEDATE", &row.due)
	col("start", "ZSTARTDATE", &row.start)
	col("deletion", "ZMARKEDFORDELETION", &row.markedForDeletion)
	col("parent", "ZPARENTREMINDER", &row.parentID)
	col("list_link", "ZLIST", &row.listID)
	// Completion is a two-column unit (flag + date).
	if !r.unavailable["completion"] {
		sel = append(sel, "ZCOMPLETED", "ZCOMPLETIONDATE")
		dest = append(dest, &row.completed, &row.completion)
	}
	if !r.unavailable["account"] {
		sel = append(sel, "ZACCOUNT")
		dest = append(dest, &row.accountID)
	}

	query := "SELECT " + strings.Join(sel, ", ") +
		" FROM ZREMCDREMINDER WHERE Z_ENT = ? ORDER BY Z_PK"
	rows, err := s.db.Query(query, s.ent.reminder)
	if err != nil {
		yield(Reminder{}, fmt.Errorf("reminders: %s: query reminders: %w", s.name, err))
		return false
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		*row = reminderRow{}
		if err := rows.Scan(dest...); err != nil {
			if !yield(Reminder{}, &backup.RowError{
				Domain: "reminders", Table: "ZREMCDREMINDER", RowID: row.id.Int64, Err: err,
			}) {
				return false
			}
			continue
		}
		rem := row.reminder(s.name)
		children.attach(&rem, row)
		if !yield(rem, nil) {
			return false
		}
	}
	if err := rows.Err(); err != nil {
		yield(Reminder{}, fmt.Errorf("reminders: %s: read reminders: %w", s.name, err))
		return false
	}
	return true
}

// Lists streams every reminder list across every store, in (store, Z_PK) order,
// with its account resolved. When the schema lacks the list columns ("lists" in
// Capability.Missing) the iterator yields backup.ErrUnavailable instead of a
// misleading empty stream.
func (r *Reader) Lists() iter.Seq2[List, error] {
	return func(yield func(List, error) bool) {
		if r.unavailable["lists"] {
			yield(List{}, fmt.Errorf("reminders: lists: %w", backup.ErrUnavailable))
			return
		}
		for _, s := range r.stores {
			accounts, err := r.loadAccounts(s)
			if err != nil {
				yield(List{}, fmt.Errorf("reminders: %s: %w", s.name, err))
				return // stream-scoped: terminate iteration
			}
			if !r.streamLists(s, accounts, yield) {
				return
			}
		}
	}
}

// streamLists streams one store's lists; like streamStore it returns false to
// stop the whole iteration (consumer stop or a stream-scoped error).
func (r *Reader) streamLists(s *storeHandle, accounts map[int64]Account, yield func(List, error) bool) bool {
	rows, err := s.db.Query(
		"SELECT Z_PK, ZIDENTIFIER, ZNAME, ZISGROUP, ZSHARINGSTATUS, ZACCOUNT " +
			"FROM ZREMCDBASELIST ORDER BY Z_PK")
	if err != nil {
		yield(List{}, fmt.Errorf("reminders: %s: query lists: %w", s.name, err))
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		l, err := scanList(rows, s.name, accounts)
		if err != nil {
			if !yield(List{}, &backup.RowError{
				Domain: "reminders", Table: "ZREMCDBASELIST", RowID: l.ID, Err: err,
			}) {
				return false
			}
			continue
		}
		if !yield(l, nil) {
			return false
		}
	}
	if err := rows.Err(); err != nil {
		yield(List{}, fmt.Errorf("reminders: %s: read lists: %w", s.name, err))
		return false
	}
	return true
}

// scanList scans one ZREMCDBASELIST row, resolving its account from the
// preloaded map (LEFT-JOIN semantics).
func scanList(rows *sql.Rows, store string, accounts map[int64]Account) (List, error) {
	var id, isGroup, sharing, accountID sql.NullInt64
	var identifier []byte
	var name sql.NullString
	if err := rows.Scan(&id, &identifier, &name, &isGroup, &sharing, &accountID); err != nil {
		return List{ID: id.Int64}, err
	}
	l := List{
		Store:         store,
		ID:            id.Int64,
		Identifier:    formatUUID(identifier),
		Name:          name.String,
		IsGroup:       isGroup.Valid && isGroup.Int64 != 0,
		SharingStatus: sharing.Int64,
	}
	if accounts != nil && accountID.Valid {
		if a, ok := accounts[accountID.Int64]; ok {
			l.Account = &a
		}
	}
	return l, nil
}

// reminderRow holds one scanned ZREMCDREMINDER row; only the columns selected
// for this database's capability are filled.
type reminderRow struct {
	id                sql.NullInt64
	identifier        []byte
	title             sql.NullString
	notes             sql.NullString
	completed         sql.NullInt64
	flagged           sql.NullInt64
	priority          sql.NullInt64
	allDay            sql.NullInt64
	created           sql.NullFloat64 // Cocoa seconds, REAL
	modified          sql.NullFloat64
	due               sql.NullFloat64
	completion        sql.NullFloat64
	start             sql.NullFloat64
	markedForDeletion sql.NullInt64
	parentID          sql.NullInt64
	listID            sql.NullInt64
	accountID         sql.NullInt64
}

func (r *reminderRow) reminder(store string) Reminder {
	rem := Reminder{
		Store:             store,
		ID:                r.id.Int64,
		Identifier:        formatUUID(r.identifier),
		Title:             r.title.String,
		Notes:             r.notes.String,
		Completed:         r.completed.Valid && r.completed.Int64 != 0,
		Flagged:           r.flagged.Valid && r.flagged.Int64 != 0,
		Priority:          r.priority.Int64,
		AllDay:            r.allDay.Valid && r.allDay.Int64 != 0,
		MarkedForDeletion: r.markedForDeletion.Valid && r.markedForDeletion.Int64 != 0,
		ParentID:          r.parentID.Int64,
	}
	if r.created.Valid {
		rem.Created = cocoa.FromSecondsFloat(r.created.Float64)
	}
	if r.modified.Valid {
		rem.Modified = cocoa.FromSecondsFloat(r.modified.Float64)
	}
	if r.due.Valid {
		rem.Due = cocoa.FromSecondsFloat(r.due.Float64)
	}
	if r.completion.Valid {
		rem.Completion = cocoa.FromSecondsFloat(r.completion.Float64)
	}
	if r.start.Valid {
		rem.Start = cocoa.FromSecondsFloat(r.start.Float64)
	}
	return rem
}

// children holds one store's preloaded reference tables — bounded, metadata-only
// lookups fetched once so the reminder stream issues no per-row child query. A
// nil map means the corresponding optional unit is unavailable. Nothing here
// outlives the iterator (the library holds no state between calls).
type children struct {
	accounts    map[int64]Account     // by Z_PK
	lists       map[int64]List        // by Z_PK (account resolved)
	recurrences map[int64]*Recurrence // by reminder Z_PK (ZREMINDER4)
	assignees   map[int64]string      // by reminder Z_PK (ZREMINDER1 → sharee name)
}

func (r *Reader) loadChildren(s *storeHandle) (*children, error) {
	c := &children{}
	var err error
	if !r.unavailable["account"] && s.ent.account != 0 {
		if c.accounts, err = r.loadAccounts(s); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["lists"] {
		if c.lists, err = r.loadLists(s, c.accounts); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["recurrence"] && s.ent.recurrence != 0 {
		if c.recurrences, err = r.loadRecurrences(s); err != nil {
			return nil, err
		}
	}
	if !r.unavailable["assignment"] && s.ent.assignment != 0 {
		if c.assignees, err = r.loadAssignees(s); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// attach wires a reminder's resolved list, account, recurrence and assignee in
// from the preloaded tables.
func (c *children) attach(rem *Reminder, row *reminderRow) {
	if c.accounts != nil && row.accountID.Valid {
		if a, ok := c.accounts[row.accountID.Int64]; ok {
			rem.Account = &a
		}
	}
	if c.lists != nil && row.listID.Valid {
		if l, ok := c.lists[row.listID.Int64]; ok {
			rem.List = &l
		}
	}
	if c.recurrences != nil {
		if rec, ok := c.recurrences[rem.ID]; ok {
			rem.Recurrence = rec
		}
	}
	if c.assignees != nil {
		rem.Assignee = c.assignees[rem.ID]
	}
}

// loadAccounts preloads the REMCDAccount rows (from the shared ZREMCDOBJECT
// table) keyed by Z_PK.
func (r *Reader) loadAccounts(s *storeHandle) (map[int64]Account, error) {
	if r.unavailable["account"] || s.ent.account == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(
		"SELECT Z_PK, ZNAME FROM ZREMCDOBJECT WHERE Z_ENT = ?", s.ent.account)
	if err != nil {
		return nil, fmt.Errorf("load accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Account{}
	for rows.Next() {
		var id sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("load accounts: %w", err)
		}
		out[id.Int64] = Account{Store: s.name, ID: id.Int64, Name: name.String}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load accounts: %w", err)
	}
	return out, nil
}

// loadLists preloads the ZREMCDBASELIST rows (with account) keyed by Z_PK.
func (r *Reader) loadLists(s *storeHandle, accounts map[int64]Account) (map[int64]List, error) {
	rows, err := s.db.Query(
		"SELECT Z_PK, ZIDENTIFIER, ZNAME, ZISGROUP, ZSHARINGSTATUS, ZACCOUNT " +
			"FROM ZREMCDBASELIST")
	if err != nil {
		return nil, fmt.Errorf("load lists: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]List{}
	for rows.Next() {
		l, err := scanList(rows, s.name, accounts)
		if err != nil {
			return nil, fmt.Errorf("load lists: %w", err)
		}
		out[l.ID] = l
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load lists: %w", err)
	}
	return out, nil
}

// loadRecurrences preloads REMCDRecurrenceRule rows keyed by the reminder they
// point at (ZREMINDER4). A rule with no reminder link is skipped.
func (r *Reader) loadRecurrences(s *storeHandle) (map[int64]*Recurrence, error) {
	rows, err := s.db.Query(
		"SELECT ZREMINDER4, ZFREQUENCY, ZINTERVAL, ZOCCURRENCECOUNT, ZENDDATE "+
			"FROM ZREMCDOBJECT WHERE Z_ENT = ?", s.ent.recurrence)
	if err != nil {
		return nil, fmt.Errorf("load recurrences: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]*Recurrence{}
	for rows.Next() {
		var reminderID, freq, interval, count sql.NullInt64
		var end sql.NullFloat64
		if err := rows.Scan(&reminderID, &freq, &interval, &count, &end); err != nil {
			return nil, fmt.Errorf("load recurrences: %w", err)
		}
		if !reminderID.Valid {
			continue
		}
		rec := &Recurrence{
			Frequency:       freq.Int64,
			Interval:        interval.Int64,
			OccurrenceCount: count.Int64,
		}
		if end.Valid {
			rec.End = cocoa.FromSecondsFloat(end.Float64)
		}
		out[reminderID.Int64] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load recurrences: %w", err)
	}
	return out, nil
}

// loadAssignees preloads, per reminder (ZREMINDER1), the display name of the
// sharee an assignment targets (ZASSIGNEE → REMCDSharee). Best-effort: a sharee
// name is "First Last" when present, else the address.
func (r *Reader) loadAssignees(s *storeHandle) (map[int64]string, error) {
	sharees, err := r.loadSharees(s)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		"SELECT ZREMINDER1, ZASSIGNEE FROM ZREMCDOBJECT WHERE Z_ENT = ?", s.ent.assignment)
	if err != nil {
		return nil, fmt.Errorf("load assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var reminderID, assigneeID sql.NullInt64
		if err := rows.Scan(&reminderID, &assigneeID); err != nil {
			return nil, fmt.Errorf("load assignments: %w", err)
		}
		if !reminderID.Valid {
			continue
		}
		if name, ok := sharees[assigneeID.Int64]; ok && name != "" {
			out[reminderID.Int64] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load assignments: %w", err)
	}
	return out, nil
}

// loadSharees preloads REMCDSharee display names keyed by Z_PK.
func (r *Reader) loadSharees(s *storeHandle) (map[int64]string, error) {
	if s.ent.sharee == 0 {
		return map[int64]string{}, nil
	}
	rows, err := s.db.Query(
		"SELECT Z_PK, ZFIRSTNAME, ZLASTNAME, ZADDRESS1 FROM ZREMCDOBJECT WHERE Z_ENT = ?", s.ent.sharee)
	if err != nil {
		return nil, fmt.Errorf("load sharees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id sql.NullInt64
		var first, last, address sql.NullString
		if err := rows.Scan(&id, &first, &last, &address); err != nil {
			return nil, fmt.Errorf("load sharees: %w", err)
		}
		out[id.Int64] = shareeName(first.String, last.String, address.String)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load sharees: %w", err)
	}
	return out, nil
}

// shareeName joins a first/last name, falling back to the address.
func shareeName(first, last, address string) string {
	name := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	if name != "" {
		return name
	}
	return address
}

// formatUUID renders a 16-byte identifier BLOB as a canonical lowercase UUID; a
// differently sized blob falls back to hex, and an empty one to "".
func formatUUID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) != 16 {
		return hex.EncodeToString(b)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
