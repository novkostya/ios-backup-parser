// Package contacts streams typed contact records out of an iOS backup's
// AddressBook database (HomeDomain, Library/AddressBook/AddressBook.sqlitedb).
//
// Open validates the schema eagerly: an unrecognized structure fails with
// backup.ErrUnsupportedSchema before any iterator exists, and absent optional
// columns degrade the Capability report instead of silently yielding empty
// fields. Iteration follows the shared error contract (see the backup package
// doc): a *backup.RowError is row-scoped and the stream continues; any other
// yielded error is stream-scoped and ends it.
package contacts

import (
	"database/sql"
	"fmt"
	"io/fs"
	"iter"
	"slices"
	"strings"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Domain and RelativePath locate the contacts database inside a backup; as a
// FileRef: backup.FileRef{Domain: Domain, RelativePath: RelativePath}.
const (
	Domain       = "HomeDomain"
	RelativePath = "Library/AddressBook/AddressBook.sqlitedb"
)

// Contacts is an open contacts domain. It holds an open handle to the
// materialized scratch copy of the database; Close releases it.
type Contacts struct {
	db          *sql.DB
	capability  backup.Capability
	unavailable map[string]bool
}

// Open materializes the AddressBook database out of fsys, introspects its
// schema and — when a supported fingerprint matches — returns the open
// domain. An unrecognized structure fails with backup.ErrUnsupportedSchema
// (wrapped in *backup.UnsupportedSchemaError carrying the observed
// fingerprint); a backup without the database fails with fs.ErrNotExist.
func Open(fsys backup.FS) (*Contacts, error) {
	ok, err := fsys.Exists(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("contacts: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("contacts: backup has no %s/%s: %w", Domain, RelativePath, fs.ErrNotExist)
	}
	path, err := fsys.Materialize(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("contacts: %w", err)
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("contacts: %w", err)
	}
	result, err := introspect.Detect(db, spec)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Contacts{
		db:          db,
		capability:  result.Capability,
		unavailable: result.Unavailable,
	}, nil
}

// Capability returns the capability report produced at Open.
func (c *Contacts) Capability() backup.Capability {
	capability := c.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes the underlying database handle. (The scratch copy itself
// belongs to the FS that materialized it.)
func (c *Contacts) Close() error {
	return c.db.Close()
}

// People streams every ABPerson row in ROWID order. See the package doc for
// the row-scoped vs stream-scoped error contract.
func (c *Contacts) People() iter.Seq2[Person, error] {
	return func(yield func(Person, error) bool) {
		lk, err := c.loadLookups()
		if err != nil {
			yield(Person{}, fmt.Errorf("contacts: %w", err))
			return
		}

		row := &personRow{}
		sel := []string{"ROWID", "First", "Last"}
		dest := []any{&row.id, &row.first, &row.last}
		include := func(unit, col string, target any) {
			if !c.unavailable[unit] {
				sel = append(sel, col)
				dest = append(dest, target)
			}
		}
		include("middle_name", "Middle", &row.middle)
		include("prefix", "Prefix", &row.prefix)
		include("suffix", "Suffix", &row.suffix)
		include("nickname", "Nickname", &row.nickname)
		include("organization", "Organization", &row.organization)
		include("department", "Department", &row.department)
		include("job_title", "JobTitle", &row.jobTitle)
		include("note", "Note", &row.note)
		include("kind", "Kind", &row.kind)
		include("birthday", "Birthday", &row.birthday)
		include("created", "CreationDate", &row.created)
		include("modified", "ModificationDate", &row.modified)
		include("account", "StoreID", &row.storeID)

		rows, err := c.db.Query("SELECT " + strings.Join(sel, ", ") + " FROM ABPerson ORDER BY ROWID")
		if err != nil {
			yield(Person{}, fmt.Errorf("contacts: query people: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = personRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Person{}, &backup.RowError{
					Domain: "contacts", Table: "ABPerson", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			person := row.person(lk)
			if err, rowScoped := c.fillMultiValues(&person, lk); err != nil {
				if !rowScoped {
					yield(Person{}, fmt.Errorf("contacts: %w", err))
					return
				}
				if !yield(Person{}, &backup.RowError{
					Domain: "contacts", Table: "ABPerson", RowID: person.ID, Err: err,
				}) {
					return
				}
				continue
			}
			if !yield(person, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Person{}, fmt.Errorf("contacts: read people: %w", err))
		}
	}
}

// Groups streams every ABGroup row with its raw membership. When the schema
// lacks the group tables ("groups" in Capability.Missing) the iterator yields
// backup.ErrUnavailable instead of a misleading empty stream.
func (c *Contacts) Groups() iter.Seq2[Group, error] {
	return func(yield func(Group, error) bool) {
		if c.unavailable["groups"] {
			yield(Group{}, fmt.Errorf("contacts: groups: %w", backup.ErrUnavailable))
			return
		}
		rows, err := c.db.Query("SELECT ROWID, Name, StoreID FROM ABGroup ORDER BY ROWID")
		if err != nil {
			yield(Group{}, fmt.Errorf("contacts: query groups: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id sql.NullInt64
			var name sql.NullString
			var storeID sql.NullInt64
			if err := rows.Scan(&id, &name, &storeID); err != nil {
				if !yield(Group{}, &backup.RowError{
					Domain: "contacts", Table: "ABGroup", RowID: id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			group := Group{ID: id.Int64, Name: name.String, StoreID: storeID.Int64}
			if err, rowScoped := c.fillMembers(&group); err != nil {
				if !rowScoped {
					yield(Group{}, fmt.Errorf("contacts: %w", err))
					return
				}
				if !yield(Group{}, &backup.RowError{
					Domain: "contacts", Table: "ABGroup", RowID: group.ID, Err: err,
				}) {
					return
				}
				continue
			}
			if !yield(group, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Group{}, fmt.Errorf("contacts: read groups: %w", err))
		}
	}
}

// personRow holds one scanned ABPerson row; only the columns selected for
// this database's capability are filled.
type personRow struct {
	id           sql.NullInt64
	first        sql.NullString
	middle       sql.NullString
	last         sql.NullString
	prefix       sql.NullString
	suffix       sql.NullString
	nickname     sql.NullString
	organization sql.NullString
	department   sql.NullString
	jobTitle     sql.NullString
	note         sql.NullString
	kind         sql.NullInt64
	birthday     sql.NullString
	created      sql.NullInt64
	modified     sql.NullInt64
	storeID      sql.NullInt64
}

func (r *personRow) person(lk *lookups) Person {
	p := Person{
		ID:           r.id.Int64,
		First:        r.first.String,
		Middle:       r.middle.String,
		Last:         r.last.String,
		Prefix:       r.prefix.String,
		Suffix:       r.suffix.String,
		Nickname:     r.nickname.String,
		Organization: r.organization.String,
		Department:   r.department.String,
		JobTitle:     r.jobTitle.String,
		Note:         r.note.String,
		Kind:         r.kind.Int64,
		Birthday:     r.birthday.String,
	}
	if r.created.Valid {
		p.Created = cocoa.FromSeconds(r.created.Int64)
	}
	if r.modified.Valid {
		p.Modified = cocoa.FromSeconds(r.modified.Int64)
	}
	if lk.stores != nil && r.storeID.Valid {
		if store, ok := lk.stores[r.storeID.Int64]; ok {
			s := store
			p.Store = &s
		}
	}
	return p
}

// lookups are the small reference tables preloaded per iteration (labels and
// entry keys hold at most a few dozen rows; stores a handful). Nothing here
// outlives the iterator — the library holds no state between calls.
type lookups struct {
	labels    map[int64]string
	entryKeys map[int64]string // nil when the addresses unit is unavailable
	stores    map[int64]Store  // nil when the account unit is unavailable
}

func (c *Contacts) loadLookups() (*lookups, error) {
	lk := &lookups{}
	var err error
	if lk.labels, err = c.rowidText("ABMultiValueLabel"); err != nil {
		return nil, err
	}
	if !c.unavailable["addresses"] {
		if lk.entryKeys, err = c.rowidText("ABMultiValueEntryKey"); err != nil {
			return nil, err
		}
	}
	if !c.unavailable["account"] {
		if lk.stores, err = c.loadStores(); err != nil {
			return nil, err
		}
	}
	return lk, nil
}

func (c *Contacts) rowidText(table string) (map[int64]string, error) {
	rows, err := c.db.Query("SELECT ROWID, value FROM " + table)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var value sql.NullString
		if err := rows.Scan(&id, &value); err != nil {
			return nil, fmt.Errorf("load %s: %w", table, err)
		}
		out[id] = value.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load %s: %w", table, err)
	}
	return out, nil
}

func (c *Contacts) loadStores() (map[int64]Store, error) {
	rows, err := c.db.Query(`SELECT ABStore.ROWID, ABStore.Name, ABStore.Type, ABStore.AccountID,
		ABAccount.AccountIdentifier
		FROM ABStore LEFT JOIN ABAccount ON ABAccount.ROWID = ABStore.AccountID`)
	if err != nil {
		return nil, fmt.Errorf("load stores: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Store{}
	for rows.Next() {
		var id sql.NullInt64
		var name sql.NullString
		var typ, accountID sql.NullInt64
		var accountIdentifier sql.NullString
		if err := rows.Scan(&id, &name, &typ, &accountID, &accountIdentifier); err != nil {
			return nil, fmt.Errorf("load stores: %w", err)
		}
		out[id.Int64] = Store{
			ID:                id.Int64,
			Name:              name.String,
			Type:              typ.Int64,
			AccountID:         accountID.Int64,
			AccountIdentifier: accountIdentifier.String,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load stores: %w", err)
	}
	return out, nil
}

// fillMultiValues resolves a person's ABMultiValue rows into Phones, Emails,
// URLs and Addresses. The bool result classifies the error: true = row-scoped
// defect (this person only), false = stream-scoped.
func (c *Contacts) fillMultiValues(p *Person, lk *lookups) (error, bool) {
	rows, err := c.db.Query(
		"SELECT UID, property, label, value FROM ABMultiValue WHERE record_id = ? ORDER BY UID", p.ID)
	if err != nil {
		return fmt.Errorf("query multivalues: %w", err), false
	}
	defer func() { _ = rows.Close() }()

	type pendingAddress struct {
		uid   int64
		label string
	}
	var addresses []pendingAddress
	for rows.Next() {
		var uid, property, label sql.NullInt64
		var value sql.NullString
		if err := rows.Scan(&uid, &property, &label, &value); err != nil {
			return fmt.Errorf("multivalue: %w", err), true
		}
		labelText := ""
		if label.Valid {
			text, ok := lk.labels[label.Int64]
			if !ok {
				// A dangling label loses the home/work distinction; surfacing
				// the value without it would be silently degraded output.
				return fmt.Errorf("multivalue %d: dangling label reference %d", uid.Int64, label.Int64), true
			}
			labelText = text
		}
		switch property.Int64 {
		case propPhone:
			p.Phones = append(p.Phones, Value{Label: labelText, Value: value.String})
		case propEmail:
			p.Emails = append(p.Emails, Value{Label: labelText, Value: value.String})
		case propURL:
			p.URLs = append(p.URLs, Value{Label: labelText, Value: value.String})
		case propAddress:
			// Composite kind: the scalar value column is NULL for observed
			// composites; the data fans out into ABMultiValueEntry.
			if lk.entryKeys != nil {
				addresses = append(addresses, pendingAddress{uid: uid.Int64, label: labelText})
			}
		default:
			// Other kinds (dates, IM, related names, profiles, …) are out of
			// M1 scope — deliberately not surfaced yet.
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read multivalues: %w", err), false
	}

	for _, address := range addresses {
		components, err, rowScoped := c.addressComponents(address.uid, lk.entryKeys)
		if err != nil {
			return err, rowScoped
		}
		p.Addresses = append(p.Addresses, StructuredValue{Label: address.label, Components: components})
	}
	return nil, false
}

func (c *Contacts) addressComponents(uid int64, keys map[int64]string) (map[string]string, error, bool) {
	rows, err := c.db.Query(
		"SELECT key, value FROM ABMultiValueEntry WHERE parent_id = ? ORDER BY key", uid)
	if err != nil {
		return nil, fmt.Errorf("query address entries: %w", err), false
	}
	defer func() { _ = rows.Close() }()
	components := map[string]string{}
	for rows.Next() {
		var key sql.NullInt64
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("address entry: %w", err), true
		}
		keyText, ok := keys[key.Int64]
		if !ok {
			return nil, fmt.Errorf("address entry (multivalue %d): dangling key reference %d", uid, key.Int64), true
		}
		components[keyText] = value.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read address entries: %w", err), false
	}
	return components, nil, false
}

// fillMembers loads a group's raw membership rows. Same error classification
// as fillMultiValues.
func (c *Contacts) fillMembers(g *Group) (error, bool) {
	rows, err := c.db.Query(
		"SELECT member_type, member_id FROM ABGroupMembers WHERE group_id = ? ORDER BY member_type, member_id", g.ID)
	if err != nil {
		return fmt.Errorf("query group members: %w", err), false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var memberType, memberID sql.NullInt64
		if err := rows.Scan(&memberType, &memberID); err != nil {
			return fmt.Errorf("group member: %w", err), true
		}
		g.Members = append(g.Members, GroupMember{Type: memberType.Int64, MemberID: memberID.Int64})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read group members: %w", err), false
	}
	return nil, false
}
