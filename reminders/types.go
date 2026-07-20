package reminders

import "time"

// Reminder is one ZREMCDREMINDER row — a single reminder (a task).
//
// The store is CoreData. Reminders live in their own table (ZREMCDREMINDER),
// while lists, accounts, recurrence rules and assignments are subclasses of
// REMCDObject sharing ZREMCDOBJECT (discriminated by Z_ENT, resolved from the
// Z_PRIMARYKEY entity map at Open, never hard-coded). A backup can hold SEVERAL
// stores (one per account, plus the on-device Data-local.sqlite); Store names
// which one this reminder came from, so (Store, ID) is its identity — a bare ID
// is unique only within a store.
//
// Fields backed by an absent optional column stay at their zero value AND the
// domain's Capability.Missing names them — check the capability report to tell
// "empty" from "cannot know".
type Reminder struct {
	// Store is the base filename of the CloudKit store this reminder lives in
	// (e.g. "Data-local.sqlite" or "Data-<UUID>.sqlite"). It namespaces ID
	// across the backup's stores.
	Store string `json:"store"`

	// ID is ZREMCDREMINDER.Z_PK — the CoreData primary key, unique only WITHIN
	// Store (each store has its own Z_PK sequence).
	ID int64 `json:"id"`

	// Identifier is ZIDENTIFIER (a 16-byte UUID stored as a BLOB), formatted as
	// a canonical lowercase UUID string; the reminder's stable cross-sync id.
	Identifier string `json:"identifier,omitempty"`

	// Title is ZTITLE (plain text — reminders store the title in a real column,
	// unlike messages/notes which need a blob decode). Notes is ZNOTES, the
	// free-text body ("" when none).
	Title string `json:"title"`
	Notes string `json:"notes,omitempty"`

	// Completed is ZCOMPLETED == 1; Flagged is ZFLAGGED == 1. Priority is
	// ZPRIORITY verbatim (EKReminderPriority space: 0 none, 1 high, 5 medium,
	// 9 low — surfaced raw, not interpreted). AllDay is ZALLDAY == 1: the due
	// date is a date only, its time-of-day not meaningful.
	Completed bool  `json:"completed"`
	Flagged   bool  `json:"flagged,omitempty"`
	Priority  int64 `json:"priority,omitempty"`
	AllDay    bool  `json:"all_day,omitempty"`

	// Created (ZCREATIONDATE), Modified (ZLASTMODIFIEDDATE), Due (ZDUEDATE),
	// Completion (ZCOMPLETIONDATE) and Start (ZSTARTDATE) are Cocoa-epoch
	// SECONDS columns stored as REAL (all six reminder date columns share this
	// epoch/unit — no mixed epochs, unlike safari). Zero when NULL (an undated
	// reminder simply has no Due) or the schema lacks the column (the matching
	// name in Capability.Missing). There is no floating-date sentinel here
	// (unlike calendar): undated is NULL, all-day is the AllDay flag.
	Created    time.Time `json:"created,omitzero"`
	Modified   time.Time `json:"modified,omitzero"`
	Due        time.Time `json:"due,omitzero"`
	Completion time.Time `json:"completion,omitzero"`
	Start      time.Time `json:"start,omitzero"`

	// MarkedForDeletion is ZMARKEDFORDELETION == 1 (a reminder pending purge,
	// still present in the store).
	MarkedForDeletion bool `json:"marked_for_deletion,omitempty"`

	// ParentID is ZPARENTREMINDER — the Z_PK (within Store) of the parent
	// reminder when this is a subtask, else 0. Surfaced raw; subtask nesting is
	// documented-to-validate (docs/schemas/reminders.md).
	ParentID int64 `json:"parent_id,omitempty"`

	// List is the reminder's list (via ZLIST), nil when unresolved or the schema
	// lacks the list columns ("lists"/"list_link" in Capability.Missing).
	// Account is the owning account (via ZACCOUNT), nil when unresolved or absent
	// ("account" in Capability.Missing).
	List    *List    `json:"list,omitempty"`
	Account *Account `json:"account,omitempty"`

	// Recurrence is the reminder's repeat rule (a REMCDRecurrenceRule pointing
	// back at this reminder), nil when it does not recur or the schema lacks the
	// recurrence columns ("recurrence" in Capability.Missing). Surfaced RAW and
	// documented-to-validate — the frequency constants are not differentially
	// validated in this milestone (docs/schemas/reminders.md).
	Recurrence *Recurrence `json:"recurrence,omitempty"`

	// Assignee is the display name (or address) of the person a shared
	// reminder is assigned to, resolved best-effort through
	// REMCDAssignment→REMCDSharee. "" when unassigned or unresolved. Surfaced
	// RAW and documented-to-validate ("assignment" in Capability.Missing when
	// the schema lacks the columns).
	Assignee string `json:"assignee,omitempty"`
}

// List is one ZREMCDBASELIST row — a reminder list (a REMCDList, or a smart
// list / group; the table holds the whole list family).
type List struct {
	// Store is the store this list lives in; ID is ZREMCDBASELIST.Z_PK
	// (unique within Store); Identifier is ZIDENTIFIER (formatted UUID).
	Store      string `json:"store"`
	ID         int64  `json:"id"`
	Identifier string `json:"identifier,omitempty"`

	// Name is ZNAME (the list's display name).
	Name string `json:"name,omitempty"`

	// IsGroup is ZISGROUP == 1 (a group that contains other lists).
	IsGroup bool `json:"is_group,omitempty"`

	// SharingStatus is ZSHARINGSTATUS verbatim (whether/how the list is shared);
	// surfaced raw, not interpreted.
	SharingStatus int64 `json:"sharing_status,omitempty"`

	// Account is the owning account when resolvable, else nil.
	Account *Account `json:"account,omitempty"`
}

// Account is the account a reminder or list belongs to — a REMCDAccount row in
// ZREMCDOBJECT (the on-device "On My iPhone" account, or an iCloud/CalDAV
// account).
type Account struct {
	// Store is the store this account lives in; ID is its Z_PK within Store.
	Store string `json:"store"`
	ID    int64  `json:"id"`

	// Name is ZNAME (the account's display name).
	Name string `json:"name,omitempty"`
}

// Recurrence is a reminder's repeat rule (a REMCDRecurrenceRule). Its fields are
// surfaced RAW — the frequency/interval constants are documented-to-validate,
// not differentially validated in this milestone.
type Recurrence struct {
	// Frequency is ZFREQUENCY verbatim (the repeat unit — daily/weekly/monthly/
	// yearly in Apple's constant space); Interval is ZINTERVAL (every N units);
	// OccurrenceCount is ZOCCURRENCECOUNT (0 = no count limit).
	Frequency       int64 `json:"frequency"`
	Interval        int64 `json:"interval,omitempty"`
	OccurrenceCount int64 `json:"occurrence_count,omitempty"`

	// End is ZENDDATE (Cocoa seconds) — the recurrence end date, zero when the
	// rule has no end.
	End time.Time `json:"end,omitzero"`
}
