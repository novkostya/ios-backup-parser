package notes

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint notes.1 — first observed on the iOS 18.x study backup; the full
// observed structure and its evidence live in docs/schemas/notes.md. Identity is
// the introspected structure, never a version claim; detection checks
// table/column PRESENCE and unknown extra columns never disqualify
// (ZICCLOUDSYNCINGOBJECT carries ~200 columns spanning every Notes entity).
//
// The store is CoreData with SINGLE-TABLE INHERITANCE: notes, folders, accounts,
// attachments and media are all rows of ZICCLOUDSYNCINGOBJECT discriminated by
// Z_ENT, whose per-model ordinals are read from the Z_PRIMARYKEY entity map at
// Open (see Open) rather than assumed — so a model that renumbers entities is
// still handled, and the fingerprint keys on column presence, not on an ordinal.
//
// The column suffixes are the fingerprint's discriminator. Under single-table
// inheritance CoreData disambiguates colliding attributes with numeric suffixes,
// and which suffix a note uses is version-specific (confirmed by introspection,
// correcting M0's guesses): on notes.1 a note's creation date is ZCREATIONDATE3,
// its account pointer ZACCOUNT7, its title ZTITLE1, and a folder's title ZTITLE2.
// A backup whose notes use a different suffix set (e.g. ZCREATIONDATE1/ZACCOUNT3
// on older iOS — the layout iLEAPP's notes.py branches on) is a DIFFERENT
// fingerprint (notes.2), not a silent degradation; it is not invented here
// (single-version honesty) but appends cleanly when observed.
//
// Required is deliberately minimal: the shared-object table + primary key, the
// entity map needed to find notes, and the note-body table (ZICNOTEDATA) without
// which no note has any text at all — the domain's headline deliverable.
// Everything that can degrade honestly is an Optional unit whose absence lands in
// Capability.Missing under its name.
var spec = introspect.Spec{
	Domain: "notes",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "notes.1",
			Required: introspect.Tables{
				// ZIDENTIFIER (the per-object UUID) is Required, not optional: it is
				// a core Notes column present across all CloudKit-era schemas, shared
				// by every entity (note, folder, account, media), and the account's
				// value is the Accounts/<identifier> media directory — so it is
				// selected unconditionally for all of them.
				"ZICCLOUDSYNCINGOBJECT": {"Z_PK", "Z_ENT", "ZIDENTIFIER"},
				"Z_PRIMARYKEY":          {"Z_ENT", "Z_NAME"},
				"ZICNOTEDATA":           {"ZNOTE", "ZDATA"},
			},
			Optional: []introspect.Unit{
				{Name: "title", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZTITLE1"}}},
				{Name: "snippet", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZSNIPPET"}}},
				{Name: "created", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZCREATIONDATE3"}}},
				{Name: "modified", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZMODIFICATIONDATE1"}}},
				// Folder resolution: the note→folder pointer and the folder's title
				// and type columns (ZTITLE2 under single-table inheritance). Every
				// column the folder queries read is listed here, so an available unit
				// guarantees the SELECT never references an absent column.
				{Name: "folders", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZFOLDER", "ZTITLE2", "ZFOLDERTYPE"}}},
				// Account resolution: the note→account pointer, the account name and
				// type (the account's ZIDENTIFIER — Required — is the on-disk media dir).
				{Name: "account", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZACCOUNT7", "ZNAME", "ZACCOUNTTYPE"}}},
				// Password protection (reported, not decrypted).
				{Name: "locked", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZISPASSWORDPROTECTED", "ZPASSWORDHINT"}}},
				{Name: "pinned", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZISPINNED"}}},
				{Name: "deletion", Tables: introspect.Tables{"ZICCLOUDSYNCINGOBJECT": {"ZMARKEDFORDELETION"}}},
				// Embedded attachments: the ICAttachment→note and ICMedia→attachment
				// pointers plus the media-file naming columns. ZGENERATION1 is the
				// on-disk generation directory needed to form a resolvable media
				// FileRef; without the whole set attachments degrade to absent.
				{Name: "attachments", Tables: introspect.Tables{
					"ZICCLOUDSYNCINGOBJECT": {"ZNOTE", "ZTYPEUTI", "ZTITLE", "ZATTACHMENT1", "ZFILENAME", "ZGENERATION1"},
				}},
			},
		},
	},
}
