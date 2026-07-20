// Package notes streams typed note records out of an iOS backup's Notes database
// (AppDomainGroup-group.com.apple.notes, NoteStore.sqlite).
//
// The store is CoreData with single-table inheritance: notes, folders, accounts,
// attachments and media are all rows of ZICCLOUDSYNCINGOBJECT discriminated by
// Z_ENT (whose per-model ordinals are resolved from the Z_PRIMARYKEY entity map
// at Open, not assumed). A note's plain text is not in any column — it lives in a
// gzip+protobuf blob in ZICNOTEDATA.ZDATA, which notes decodes via
// internal/applenotes. Notes() flattens each ICNote into a Note; Folders() streams
// the folder list. Media attachments surface as backup.FileRefs into this group
// domain; other attachment kinds surface metadata only.
//
// Password-protected notes are REPORTED, never decrypted (charter v0.1 scope): a
// locked note is yielded with Locked set and an empty Body, its metadata intact.
//
// Open validates the schema eagerly: an unrecognized structure fails with
// backup.ErrUnsupportedSchema before any iterator exists, and absent optional
// columns degrade the Capability report instead of silently yielding empty fields.
// Iteration follows the shared error contract (see the backup package doc): a
// *backup.RowError is row-scoped and the stream continues; any other yielded error
// is stream-scoped and ends it. Descriptive references (folder, account) resolve
// with LEFT-JOIN semantics — nil when unresolved — and never withhold a note; the
// only row-scoped defect is a row that fails to scan. A note whose body blob
// cannot be decoded is NOT dropped — it is yielded with BodyUndecoded set.
package notes

import (
	"database/sql"
	"fmt"
	"io/fs"
	"iter"
	"path"
	"slices"
	"strings"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/applenotes"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
)

// Domain and RelativePath locate the Notes database inside a backup; as a
// FileRef: backup.FileRef{Domain: Domain, RelativePath: RelativePath}. Domain is
// also where a note's media attachments resolve (Accounts/<account>/Media/…).
const (
	Domain       = "AppDomainGroup-group.com.apple.notes"
	RelativePath = "NoteStore.sqlite"
)

// Notes is an open notes domain. It holds an open handle to the materialized
// scratch copy of the database; Close releases it.
type Notes struct {
	db          *sql.DB
	capability  backup.Capability
	unavailable map[string]bool
	ent         entities // Z_ENT ordinals resolved from Z_PRIMARYKEY at Open
}

// entities holds the per-model Z_ENT ordinals for the note-relevant entities,
// read from the Z_PRIMARYKEY map. Resolving them (rather than hard-coding the
// observed 12/15/14/5/11) keeps the parser correct if a model renumbers entities.
type entities struct {
	note, folder, account, attachment, media int64
}

// Open materializes the NoteStore database out of fsys, introspects its schema
// and — when a supported fingerprint matches — returns the open domain. An
// unrecognized structure fails with backup.ErrUnsupportedSchema (wrapped in
// *backup.UnsupportedSchemaError carrying the observed fingerprint); a backup
// without the database fails with fs.ErrNotExist.
func Open(fsys backup.FS) (*Notes, error) {
	ok, err := fsys.Exists(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("notes: backup has no %s/%s: %w", Domain, RelativePath, fs.ErrNotExist)
	}
	dbPath, err := fsys.Materialize(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}
	db, err := sqlitedb.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}
	result, err := introspect.Detect(db, spec)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	ent, err := loadEntities(db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("notes: %w", err)
	}
	return &Notes{
		db:          db,
		capability:  result.Capability,
		unavailable: result.Unavailable,
		ent:         ent,
	}, nil
}

// loadEntities reads the Z_PRIMARYKEY entity map and resolves the note-relevant
// Z_ENT ordinals. ICNote is mandatory (without it there are no notes to read);
// the others degrade to 0 (their resolution is then skipped) if absent.
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
		note:       byName["ICNote"],
		folder:     byName["ICFolder"],
		account:    byName["ICAccount"],
		attachment: byName["ICAttachment"],
		media:      byName["ICMedia"],
	}
	if ent.note == 0 {
		return entities{}, fmt.Errorf("entity map lacks ICNote")
	}
	return ent, nil
}

// Capability returns the capability report produced at Open.
func (n *Notes) Capability() backup.Capability {
	capability := n.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes the underlying database handle. (The scratch copy itself belongs
// to the FS that materialized it.)
func (n *Notes) Close() error {
	return n.db.Close()
}

// Notes streams every ICNote row in Z_PK order, its body decoded and its folder,
// account and attachments resolved. See the package doc for the row-scoped vs
// stream-scoped error contract.
func (n *Notes) Notes() iter.Seq2[Note, error] {
	return func(yield func(Note, error) bool) {
		children, err := n.loadChildren()
		if err != nil {
			yield(Note{}, fmt.Errorf("notes: %w", err))
			return
		}

		row := &noteRow{}
		sel := []string{"n.Z_PK", "n.ZIDENTIFIER"}
		dest := []any{&row.id, &row.identifier}
		col := func(unit, expr string, target any) {
			if !n.unavailable[unit] {
				sel = append(sel, expr)
				dest = append(dest, target)
			}
		}
		col("title", "n.ZTITLE1", &row.title)
		col("snippet", "n.ZSNIPPET", &row.snippet)
		col("created", "n.ZCREATIONDATE3", &row.created)
		col("modified", "n.ZMODIFICATIONDATE1", &row.modified)
		col("folders", "n.ZFOLDER", &row.folderID)
		col("account", "n.ZACCOUNT7", &row.accountID)
		if !n.unavailable["locked"] {
			sel = append(sel, "n.ZISPASSWORDPROTECTED", "n.ZPASSWORDHINT")
			dest = append(dest, &row.locked, &row.passwordHint)
		}
		col("pinned", "n.ZISPINNED", &row.pinned)
		col("deletion", "n.ZMARKEDFORDELETION", &row.markedForDeletion)
		// The body blob lives in a sibling table; a scalar subquery keeps one row
		// per note (a LEFT JOIN would duplicate a note with >1 ZICNOTEDATA row) and
		// streams the blob per row — nothing preloads every body into memory.
		sel = append(sel, "(SELECT d.ZDATA FROM ZICNOTEDATA d WHERE d.ZNOTE = n.Z_PK)")
		dest = append(dest, &row.data)

		query := "SELECT " + strings.Join(sel, ", ") +
			" FROM ZICCLOUDSYNCINGOBJECT n WHERE n.Z_ENT = ? ORDER BY n.Z_PK"
		rows, err := n.db.Query(query, n.ent.note)
		if err != nil {
			yield(Note{}, fmt.Errorf("notes: query notes: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = noteRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Note{}, &backup.RowError{
					Domain: "notes", Table: "ZICCLOUDSYNCINGOBJECT", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			note := row.note()
			children.attach(&note, row)
			if !yield(note, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Note{}, fmt.Errorf("notes: read notes: %w", err))
		}
	}
}

// Folders streams every ICFolder row with its account resolved. When the schema
// lacks the folder columns ("folders" in Capability.Missing) the iterator yields
// backup.ErrUnavailable instead of a misleading empty stream.
func (n *Notes) Folders() iter.Seq2[Folder, error] {
	return func(yield func(Folder, error) bool) {
		if n.unavailable["folders"] || n.ent.folder == 0 {
			yield(Folder{}, fmt.Errorf("notes: folders: %w", backup.ErrUnavailable))
			return
		}
		var accounts map[int64]Account
		if !n.unavailable["account"] && n.ent.account != 0 {
			var err error
			if accounts, err = n.loadAccounts(); err != nil {
				yield(Folder{}, fmt.Errorf("notes: %w", err))
				return
			}
		}
		rows, err := n.db.Query(
			"SELECT Z_PK, ZIDENTIFIER, ZTITLE2, ZFOLDERTYPE, "+folderAccountExpr(n.unavailable)+
				" FROM ZICCLOUDSYNCINGOBJECT WHERE Z_ENT = ? ORDER BY Z_PK", n.ent.folder)
		if err != nil {
			yield(Folder{}, fmt.Errorf("notes: query folders: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id, folderType, accountID sql.NullInt64
			var identifier, title sql.NullString
			if err := rows.Scan(&id, &identifier, &title, &folderType, &accountID); err != nil {
				if !yield(Folder{}, &backup.RowError{
					Domain: "notes", Table: "ZICCLOUDSYNCINGOBJECT", RowID: id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			folder := Folder{
				ID:         id.Int64,
				Identifier: identifier.String,
				Title:      title.String,
				Type:       folderType.Int64,
			}
			if accounts != nil && accountID.Valid {
				if a, ok := accounts[accountID.Int64]; ok {
					folder.Account = &a
				}
			}
			if !yield(folder, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Folder{}, fmt.Errorf("notes: read folders: %w", err))
		}
	}
}

// noteRow holds one scanned ICNote row; only the columns selected for this
// database's capability are filled.
type noteRow struct {
	id                sql.NullInt64
	identifier        sql.NullString
	title             sql.NullString
	snippet           sql.NullString
	created           sql.NullFloat64 // ZCREATIONDATE3 — Cocoa seconds, REAL
	modified          sql.NullFloat64 // ZMODIFICATIONDATE1
	folderID          sql.NullInt64
	accountID         sql.NullInt64
	locked            sql.NullInt64
	passwordHint      sql.NullString
	pinned            sql.NullInt64
	markedForDeletion sql.NullInt64
	data              []byte // ZICNOTEDATA.ZDATA (gzip+protobuf), streamed per row
}

func (r *noteRow) note() Note {
	n := Note{
		ID:                r.id.Int64,
		Identifier:        r.identifier.String,
		Title:             r.title.String,
		Snippet:           r.snippet.String,
		Locked:            r.locked.Valid && r.locked.Int64 != 0,
		PasswordHint:      r.passwordHint.String,
		Pinned:            r.pinned.Valid && r.pinned.Int64 != 0,
		MarkedForDeletion: r.markedForDeletion.Valid && r.markedForDeletion.Int64 != 0,
	}
	if r.created.Valid {
		n.Created = cocoa.FromSecondsFloat(r.created.Float64)
	}
	if r.modified.Valid {
		n.Modified = cocoa.FromSecondsFloat(r.modified.Float64)
	}
	n.Body, n.BodyUndecoded = decodeBody(n.Locked, r.data)
	return n
}

// decodeBody applies the note-body rule. A locked note is NOT decoded (its ZDATA
// is ciphertext, not gzip) — reported via Note.Locked, body left "". A NULL/empty
// blob is a blank note. Otherwise the gzip+protobuf blob is decoded; a decode
// failure yields BodyUndecoded (body unknown), never a silent "".
func decodeBody(locked bool, data []byte) (body string, undecoded bool) {
	if locked || len(data) == 0 {
		return "", false
	}
	text, err := applenotes.DecodeBody(data)
	if err != nil {
		return "", true
	}
	return text, false
}

// children holds the per-iteration reference tables — bounded, metadata-only
// lookups preloaded once so the stream issues no per-note child query. A nil map
// means the corresponding optional unit is unavailable. Nothing here outlives the
// iterator (the library holds no state between calls).
type children struct {
	accounts    map[int64]Account         // ICAccount by Z_PK
	folders     map[int64]Folder          // ICFolder by Z_PK (account resolved)
	attachments map[int64][]attachmentRow // by note Z_PK (ZNOTE)
}

func (n *Notes) loadChildren() (*children, error) {
	c := &children{}
	var err error
	if !n.unavailable["account"] && n.ent.account != 0 {
		if c.accounts, err = n.loadAccounts(); err != nil {
			return nil, err
		}
	}
	if !n.unavailable["folders"] && n.ent.folder != 0 {
		if c.folders, err = n.loadFolders(c.accounts); err != nil {
			return nil, err
		}
	}
	if !n.unavailable["attachments"] && n.ent.attachment != 0 {
		if c.attachments, err = n.loadAttachments(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// attach wires a note's resolved folder, account and attachments in from the
// preloaded tables. Media FileRefs need the note's account identifier (the
// on-disk Accounts/<identifier> dir), so attachments are built after the account.
func (c *children) attach(note *Note, row *noteRow) {
	if c.accounts != nil && row.accountID.Valid {
		if a, ok := c.accounts[row.accountID.Int64]; ok {
			note.Account = &a
		}
	}
	if c.folders != nil && row.folderID.Valid {
		if f, ok := c.folders[row.folderID.Int64]; ok {
			note.Folder = &f
		}
	}
	if c.attachments != nil {
		var accountID string
		if note.Account != nil {
			accountID = note.Account.Identifier
		}
		for _, ar := range c.attachments[note.ID] {
			note.Attachments = append(note.Attachments, ar.attachment(accountID))
		}
	}
}

// loadAccounts preloads the ICAccount rows keyed by Z_PK.
func (n *Notes) loadAccounts() (map[int64]Account, error) {
	rows, err := n.db.Query(
		"SELECT Z_PK, ZIDENTIFIER, ZNAME, ZACCOUNTTYPE FROM ZICCLOUDSYNCINGOBJECT WHERE Z_ENT = ?", n.ent.account)
	if err != nil {
		return nil, fmt.Errorf("load accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Account{}
	for rows.Next() {
		var id, typ sql.NullInt64
		var identifier, name sql.NullString
		if err := rows.Scan(&id, &identifier, &name, &typ); err != nil {
			return nil, fmt.Errorf("load accounts: %w", err)
		}
		out[id.Int64] = Account{
			ID:         id.Int64,
			Identifier: identifier.String,
			Name:       name.String,
			Type:       typ.Int64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load accounts: %w", err)
	}
	return out, nil
}

// folderAccountExpr selects the note→account column when the account unit is
// available, else a NULL placeholder — folders can be present while the account
// columns are not, and selecting an absent column would fail the whole stream.
func folderAccountExpr(unavailable map[string]bool) string {
	if unavailable["account"] {
		return "NULL"
	}
	return "ZACCOUNT7"
}

// loadFolders preloads the ICFolder rows (with account) keyed by Z_PK.
func (n *Notes) loadFolders(accounts map[int64]Account) (map[int64]Folder, error) {
	rows, err := n.db.Query(
		"SELECT Z_PK, ZIDENTIFIER, ZTITLE2, ZFOLDERTYPE, "+folderAccountExpr(n.unavailable)+
			" FROM ZICCLOUDSYNCINGOBJECT WHERE Z_ENT = ?", n.ent.folder)
	if err != nil {
		return nil, fmt.Errorf("load folders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Folder{}
	for rows.Next() {
		var id, folderType, accountID sql.NullInt64
		var identifier, title sql.NullString
		if err := rows.Scan(&id, &identifier, &title, &folderType, &accountID); err != nil {
			return nil, fmt.Errorf("load folders: %w", err)
		}
		folder := Folder{
			ID:         id.Int64,
			Identifier: identifier.String,
			Title:      title.String,
			Type:       folderType.Int64,
		}
		if accounts != nil && accountID.Valid {
			if a, ok := accounts[accountID.Int64]; ok {
				folder.Account = &a
			}
		}
		out[id.Int64] = folder
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load folders: %w", err)
	}
	return out, nil
}

// attachmentRow holds one preloaded ICAttachment (+ its ICMedia, when media-backed)
// awaiting the note's account identifier to form a FileRef.
type attachmentRow struct {
	id              int64
	identifier      string
	typeUTI         string
	title           string
	mediaIdentifier string // ICMedia.ZIDENTIFIER (the Media/<id> dir)
	generation      string // ICMedia.ZGENERATION1 (the generation subdir)
	filename        string // ICMedia.ZFILENAME (the leaf file)
}

// attachment builds the public Attachment, forming a media FileRef only when the
// account and all media path components are known — never fabricating a path.
func (ar attachmentRow) attachment(accountID string) Attachment {
	a := Attachment{
		ID:         ar.id,
		Identifier: ar.identifier,
		TypeUTI:    ar.typeUTI,
		Title:      ar.title,
		Filename:   ar.filename,
	}
	if accountID != "" && ar.mediaIdentifier != "" && ar.generation != "" && ar.filename != "" {
		a.File = &backup.FileRef{
			Domain:       Domain,
			RelativePath: path.Join("Accounts", accountID, "Media", ar.mediaIdentifier, ar.generation, ar.filename),
		}
	}
	return a
}

// loadAttachments preloads ICAttachment rows (joined to their ICMedia) grouped by
// the note they belong to (ZNOTE). A non-media attachment (no ICMedia) yields
// empty media fields — surfaced as metadata with no FileRef, never dropped.
func (n *Notes) loadAttachments() (map[int64][]attachmentRow, error) {
	rows, err := n.db.Query(
		`SELECT a.Z_PK, a.ZIDENTIFIER, a.ZTYPEUTI, a.ZTITLE, a.ZNOTE,
			m.ZIDENTIFIER, m.ZGENERATION1, m.ZFILENAME
		FROM ZICCLOUDSYNCINGOBJECT a
		LEFT JOIN ZICCLOUDSYNCINGOBJECT m ON m.ZATTACHMENT1 = a.Z_PK AND m.Z_ENT = ?
		WHERE a.Z_ENT = ? ORDER BY a.ZNOTE, a.Z_PK`, n.ent.media, n.ent.attachment)
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]attachmentRow{}
	for rows.Next() {
		var id, noteID sql.NullInt64
		var identifier, typeUTI, title, mediaID, generation, filename sql.NullString
		if err := rows.Scan(&id, &identifier, &typeUTI, &title, &noteID,
			&mediaID, &generation, &filename); err != nil {
			return nil, fmt.Errorf("load attachments: %w", err)
		}
		if !noteID.Valid {
			continue // an attachment with no owning note is not reachable from any note
		}
		out[noteID.Int64] = append(out[noteID.Int64], attachmentRow{
			id:              id.Int64,
			identifier:      identifier.String,
			typeUTI:         typeUTI.String,
			title:           title.String,
			mediaIdentifier: mediaID.String,
			generation:      generation.String,
			filename:        filename.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	return out, nil
}
