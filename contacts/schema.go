package contacts

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint contacts.1 — first observed on the iOS 18.x study backup; the
// full observed structure and its evidence live in docs/schemas/contacts.md.
// Identity is the introspected structure, never a version claim; detection
// checks table/column PRESENCE and unknown extra columns never disqualify.
//
// Required is deliberately minimal: the anchor table and the multi-value
// machinery, without which contact extraction would be misleading. Everything
// that can degrade honestly is an Optional unit whose absence lands in
// Capability.Missing under the unit's name.
var spec = introspect.Spec{
	Domain: "contacts",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "contacts.1",
			Required: introspect.Tables{
				"ABPerson":          {"ROWID", "First", "Last"},
				"ABMultiValue":      {"UID", "record_id", "property", "label", "value"},
				"ABMultiValueLabel": {"value"}, // keyed by implicit rowid
			},
			Optional: []introspect.Unit{
				{Name: "middle_name", Tables: introspect.Tables{"ABPerson": {"Middle"}}},
				{Name: "prefix", Tables: introspect.Tables{"ABPerson": {"Prefix"}}},
				{Name: "suffix", Tables: introspect.Tables{"ABPerson": {"Suffix"}}},
				{Name: "nickname", Tables: introspect.Tables{"ABPerson": {"Nickname"}}},
				{Name: "organization", Tables: introspect.Tables{"ABPerson": {"Organization"}}},
				{Name: "department", Tables: introspect.Tables{"ABPerson": {"Department"}}},
				{Name: "job_title", Tables: introspect.Tables{"ABPerson": {"JobTitle"}}},
				{Name: "note", Tables: introspect.Tables{"ABPerson": {"Note"}}},
				{Name: "kind", Tables: introspect.Tables{"ABPerson": {"Kind"}}},
				{Name: "birthday", Tables: introspect.Tables{"ABPerson": {"Birthday"}}},
				{Name: "created", Tables: introspect.Tables{"ABPerson": {"CreationDate"}}},
				{Name: "modified", Tables: introspect.Tables{"ABPerson": {"ModificationDate"}}},
				{Name: "addresses", Tables: introspect.Tables{
					// Composite values fan out into entries; parent_id joins
					// ABMultiValue.UID, key joins ABMultiValueEntryKey's
					// implicit rowid.
					"ABMultiValueEntry":    {"parent_id", "key", "value"},
					"ABMultiValueEntryKey": {"value"},
				}},
				{Name: "account", Tables: introspect.Tables{
					"ABPerson":  {"StoreID"},
					"ABStore":   {"ROWID", "Name", "Type", "AccountID"},
					"ABAccount": {"ROWID", "AccountIdentifier"},
				}},
				{Name: "groups", Tables: introspect.Tables{
					"ABGroup":        {"ROWID", "Name", "StoreID"},
					"ABGroupMembers": {"group_id", "member_type", "member_id"},
				}},
			},
			// The contact photo lives in AddressBookImages.sqlitedb — a
			// charter non-goal — so no fingerprint of this domain provides it.
			AlwaysMissing: []string{"photo"},
		},
	},
}

// Classic AddressBook multi-value property constants. Interpretation
// cross-referenced from iLEAPP's addressBook artifact (MIT, see NOTICE) and
// validated differentially on real data (testing ladder rung 3). Kinds not
// listed (12 dates, 13 instant messages, 23 related names, 46 profiles) are
// ignored by this milestone.
const (
	propPhone   = 3
	propEmail   = 4
	propAddress = 5
	propURL     = 22
)
