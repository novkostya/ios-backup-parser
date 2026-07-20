package reminders

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint reminders.1 — first observed on the iOS 18.x study backup; the
// full observed structure and its evidence live in docs/schemas/reminders.md.
// Identity is the introspected structure, never a version claim; detection
// checks table/column PRESENCE and unknown extra columns never disqualify
// (ZREMCDOBJECT alone carries ~140 columns spanning every REMCDObject subclass).
//
// The store is CoreData with MIXED inheritance: reminders have their own table
// (ZREMCDREMINDER), lists share ZREMCDBASELIST (REMCDList/REMCDSmartList), and
// accounts, recurrence rules, assignments and sharees are subclasses of
// REMCDObject sharing ZREMCDOBJECT. The per-model Z_ENT ordinals are read from
// the Z_PRIMARYKEY entity map at Open (see Open), not assumed — a store that
// renumbers entities is still handled, and the fingerprint keys on column
// presence, not on an ordinal. This is not academic: on the study backup the
// on-device Data-local.sqlite store renumbers REMCDAccount/REMCDRecurrenceRule/
// REMCDAssignment/REMCDSharee relative to the CloudKit stores (a grocery entity
// inserted mid-map), so a hard-coded ordinal would silently corrupt one store.
//
// The domain SPANS MULTIPLE stores: Container_v1/Stores/Data-<UUID>.sqlite (one
// per account) plus the fixed-name Data-local.sqlite. Each store is introspected
// against this same fingerprint (they share the model). Enumerating the
// UUID-named stores needs backup.ReadDirFS; a host lacking it reads only
// Data-local.sqlite and reports "cloudkit_stores" in Capability.Missing (see
// Open) — honest degradation, never a silent partial read.
//
// Required is deliberately minimal: the entity map needed to resolve ordinals,
// and the reminder table with its Z_PK/Z_ENT and title (the headline
// deliverable — reminders store their title in a plain column). Everything that
// can degrade honestly is an Optional unit whose absence lands in
// Capability.Missing under its name; each unit lists EVERY column the parser
// reads for that field, so an available unit guarantees the SELECT never
// references an absent column.
var spec = introspect.Spec{
	Domain: "reminders",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "reminders.1",
			Required: introspect.Tables{
				"Z_PRIMARYKEY":   {"Z_ENT", "Z_NAME"},
				"ZREMCDREMINDER": {"Z_PK", "Z_ENT", "ZTITLE"},
			},
			Optional: []introspect.Unit{
				{Name: "notes", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZNOTES"}}},
				{Name: "completion", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZCOMPLETED", "ZCOMPLETIONDATE"}}},
				{Name: "flagged", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZFLAGGED"}}},
				{Name: "priority", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZPRIORITY"}}},
				{Name: "all_day", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZALLDAY"}}},
				{Name: "created", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZCREATIONDATE"}}},
				{Name: "modified", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZLASTMODIFIEDDATE"}}},
				{Name: "due", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZDUEDATE"}}},
				{Name: "start", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZSTARTDATE"}}},
				{Name: "deletion", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZMARKEDFORDELETION"}}},
				{Name: "parent", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZPARENTREMINDER"}}},
				{Name: "identifier", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZIDENTIFIER"}}},
				// The reminder→list pointer (in ZREMCDREMINDER) and the list table
				// itself are separate units: a store could carry the pointer but not
				// the list rows, or vice versa. Both must be present to attach a List.
				{Name: "list_link", Tables: introspect.Tables{"ZREMCDREMINDER": {"ZLIST"}}},
				{Name: "lists", Tables: introspect.Tables{
					"ZREMCDBASELIST": {"Z_PK", "Z_ENT", "ZIDENTIFIER", "ZNAME", "ZISGROUP", "ZSHARINGSTATUS", "ZACCOUNT"},
				}},
				// Account: the reminder→account pointer plus the account name column
				// on the shared ZREMCDOBJECT table.
				{Name: "account", Tables: introspect.Tables{
					"ZREMCDREMINDER": {"ZACCOUNT"},
					"ZREMCDOBJECT":   {"Z_PK", "Z_ENT", "ZNAME"},
				}},
				// Recurrence: the REMCDRecurrenceRule columns on ZREMCDOBJECT and its
				// back-pointer to the reminder (ZREMINDER4).
				{Name: "recurrence", Tables: introspect.Tables{
					"ZREMCDOBJECT": {"ZREMINDER4", "ZFREQUENCY", "ZINTERVAL", "ZOCCURRENCECOUNT", "ZENDDATE"},
				}},
				// Assignment: the REMCDAssignment back-pointer (ZREMINDER1) and its
				// assignee link (ZASSIGNEE), plus the REMCDSharee name columns.
				{Name: "assignment", Tables: introspect.Tables{
					"ZREMCDOBJECT": {"ZREMINDER1", "ZASSIGNEE", "ZFIRSTNAME", "ZLASTNAME", "ZADDRESS1"},
				}},
			},
		},
	},
}
