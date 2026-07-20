package notes

import (
	"time"

	backup "github.com/novkostya/ios-backup-parser"
)

// Note is one ICNote row — a single note in the store.
//
// The store is CoreData with single-table inheritance: notes, folders, accounts,
// attachments and media all live in ZICCLOUDSYNCINGOBJECT, discriminated by
// Z_ENT (resolved from the Z_PRIMARYKEY entity map at Open, never hard-coded).
// Note flattens the ICNote row, decodes its body out of the gzip+protobuf
// ZICNOTEDATA.ZDATA blob, and resolves its folder, account and attachments.
//
// Fields backed by an absent optional column stay at their zero value AND the
// domain's Capability.Missing names them — check the capability report to tell
// "empty" from "cannot know".
type Note struct {
	// ID is ZICCLOUDSYNCINGOBJECT.Z_PK — the CoreData primary key and a stable
	// per-backup identifier for the note.
	ID int64 `json:"id"`

	// Identifier is ZIDENTIFIER, the note's own UUID (stable across the note's
	// sync history).
	Identifier string `json:"identifier,omitempty"`

	// Title is ZTITLE1 (the note's display title, derived by Notes from the first
	// line of the body); "" when none or the schema lacks the column ("title" in
	// Capability.Missing).
	Title string `json:"title,omitempty"`

	// Snippet is ZSNIPPET, Apple's own stored plain-text preview of the body
	// (the first line or two). It is independent of the body blob.
	Snippet string `json:"snippet,omitempty"`

	// Body is the note's plain text, decoded from ZICNOTEDATA.ZDATA (gzip of an
	// Apple Notes protobuf) — see internal/applenotes. "" for a blank note and,
	// deliberately, for a locked note (whose body is not decrypted; see Locked).
	// Embedded-object placeholders (U+FFFC) are kept verbatim; the objects
	// themselves surface in Attachments. Rich formatting/attribute runs are
	// deferred for v0.1 (docs/schemas/notes.md).
	Body string `json:"body,omitempty"`

	// BodyUndecoded reports that ZDATA was present and non-empty but could not be
	// decoded (not gzip, or an unrecognized structure). The body is UNKNOWN, not
	// empty — the note is still yielded with its metadata intact, never as a
	// silently-empty note. Always false for a locked note (its body is encrypted,
	// intentionally not decoded, which is reported via Locked instead).
	BodyUndecoded bool `json:"body_undecoded,omitempty"`

	// Created is ZCREATIONDATE3 and Modified is ZMODIFICATIONDATE1 — Cocoa-epoch
	// SECONDS columns stored as REAL (NOT nanoseconds; that unit is the messages
	// domain's alone — docs/schemas/README.md). Zero when NULL or the schema lacks
	// the column ("created"/"modified" in Capability.Missing).
	Created  time.Time `json:"created,omitzero"`
	Modified time.Time `json:"modified,omitzero"`

	// Locked reports ZISPASSWORDPROTECTED == 1: a password-protected note. Per the
	// charter this is REPORTED, never decrypted in v0.1 — Body stays "" and the
	// note's metadata (title, dates, folder) is still surfaced. PasswordHint is
	// ZPASSWORDHINT when set.
	Locked       bool   `json:"locked,omitempty"`
	PasswordHint string `json:"password_hint,omitempty"`

	// Pinned reports ZISPINNED == 1; MarkedForDeletion reports
	// ZMARKEDFORDELETION == 1 (a note pending purge, still present in the store).
	Pinned            bool `json:"pinned,omitempty"`
	MarkedForDeletion bool `json:"marked_for_deletion,omitempty"`

	// Folder is the note's folder (via ZFOLDER), nil when unresolved or the schema
	// lacks the folder columns ("folders" in Capability.Missing). Account is the
	// owning account (via ZACCOUNT7), nil when unresolved or absent ("account" in
	// Capability.Missing).
	Folder  *Folder  `json:"folder,omitempty"`
	Account *Account `json:"account,omitempty"`

	// Attachments are the note's embedded attachments (ICAttachment rows pointing
	// at this note). A media-backed attachment (image/file) carries a resolvable
	// File reference; other kinds (tables, drawings, links) carry metadata only.
	// Empty when the note has none or the schema lacks the attachment columns
	// ("attachments" in Capability.Missing).
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Folder is one ICFolder row — a notes folder.
type Folder struct {
	// ID is Z_PK; Identifier is ZIDENTIFIER (the folder's UUID).
	ID         int64  `json:"id"`
	Identifier string `json:"identifier,omitempty"`

	// Title is the folder name (ZTITLE2 — the folder's title column under
	// single-table inheritance, distinct from a note's ZTITLE1).
	Title string `json:"title,omitempty"`

	// Type is ZFOLDERTYPE verbatim (e.g. the default "Notes" folder vs a
	// user-created one); exposed raw because its constant space is not interpreted
	// in this milestone.
	Type int64 `json:"type,omitempty"`

	// Account is the owning account when resolvable, else nil.
	Account *Account `json:"account,omitempty"`
}

// Account is one ICAccount row — the account a note or folder belongs to (the
// on-device "On My iPhone" account, or an iCloud/IMAP account).
type Account struct {
	// ID is Z_PK; Identifier is ZIDENTIFIER — the account UUID, which is also the
	// Accounts/<identifier>/ directory that holds the account's media.
	ID         int64  `json:"id"`
	Identifier string `json:"identifier,omitempty"`

	// Name is ZNAME (the account's display name). Type is ZACCOUNTTYPE verbatim.
	Name string `json:"name,omitempty"`
	Type int64  `json:"type,omitempty"`
}

// Attachment is one ICAttachment row embedded in a note. A media-backed
// attachment additionally resolves the ICMedia row that names the file on disk.
type Attachment struct {
	// ID is the ICAttachment Z_PK; Identifier is its ZIDENTIFIER.
	ID         int64  `json:"id"`
	Identifier string `json:"identifier,omitempty"`

	// TypeUTI is ZTYPEUTI — the attachment's uniform type identifier
	// (e.g. "public.jpeg", "com.apple.notes.table", "public.url"); exposed raw.
	TypeUTI string `json:"type_uti,omitempty"`

	// Title is ZTITLE when the attachment carries one (e.g. a link's title).
	Title string `json:"title,omitempty"`

	// Filename is the media file's name (ICMedia.ZFILENAME) for a media-backed
	// attachment; "" for a non-media attachment (table, drawing, …).
	Filename string `json:"filename,omitempty"`

	// File is a structured, backup-resolvable reference to the media file
	// (Accounts/<account>/Media/<media-id>/<generation>/<filename> in the notes
	// group domain), or nil for a non-media attachment or when the path cannot be
	// formed without fabrication (no media row, or the owning account is
	// unresolved). It round-trips into backup.FS.Materialize.
	File *backup.FileRef `json:"file,omitempty"`
}
