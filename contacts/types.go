package contacts

import "time"

// Person is one ABPerson row with its multi-valued properties resolved.
//
// Rows are streamed as stored: linked/unified contacts (the same human known
// to several accounts) arrive as separate Person records, exactly as the
// database keeps them — merging is a consumer policy, not a parser guess.
//
// Fields backed by an absent optional column stay at their zero value AND the
// domain's Capability.Missing names them — check the capability report to
// distinguish "empty" from "cannot know".
type Person struct {
	// ID is ABPerson.ROWID — the join anchor for groups and (in later
	// milestones) message handles.
	ID int64 `json:"id"`

	First  string `json:"first,omitempty"`
	Middle string `json:"middle,omitempty"`
	Last   string `json:"last,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`

	Nickname     string `json:"nickname,omitempty"`
	Organization string `json:"organization,omitempty"`
	Department   string `json:"department,omitempty"`
	JobTitle     string `json:"job_title,omitempty"`
	Note         string `json:"note,omitempty"`

	// Kind is ABPerson.Kind verbatim; see KindPerson / KindOrganization.
	Kind int64 `json:"kind"`

	// Birthday is ABPerson.Birthday verbatim — a free TEXT column with mixed
	// representations across accounts, not a parsed date.
	Birthday string `json:"birthday,omitempty"`

	// Created / Modified are Cocoa-epoch SECONDS columns; zero when NULL.
	Created  time.Time `json:"created,omitzero"`
	Modified time.Time `json:"modified,omitzero"`

	// Store describes the source store/account this row belongs to; nil when
	// the schema lacks the account unit or the row's store is unresolvable.
	Store *Store `json:"store,omitempty"`

	Phones    []Value           `json:"phones,omitempty"`
	Emails    []Value           `json:"emails,omitempty"`
	URLs      []Value           `json:"urls,omitempty"`
	Addresses []StructuredValue `json:"addresses,omitempty"`
}

// Classic ABPerson.Kind values (interpretation; validated differentially).
const (
	KindPerson       = 0
	KindOrganization = 1
)

// Value is one scalar multi-value: a phone number, email address or URL.
type Value struct {
	// Label is the raw label string, e.g. "_$!<Home>!$_" or a user-defined
	// label; empty for unlabeled values. See CanonicalLabel.
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
}

// StructuredValue is one composite multi-value — a postal address — as a
// label plus its components keyed by the raw ABMultiValueEntryKey strings
// (street, city, state, ZIP, country, …).
type StructuredValue struct {
	Label      string            `json:"label,omitempty"`
	Components map[string]string `json:"components"`
}

// Store is the source store/account of a Person, joined from ABStore and
// ABAccount via ABPerson.StoreID.
type Store struct {
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`
	Type int64  `json:"type"`
	// AccountID is ABStore.AccountID (-1 when the store has no account);
	// AccountIdentifier is the joined ABAccount.AccountIdentifier, empty when
	// there is none.
	AccountID         int64  `json:"account_id"`
	AccountIdentifier string `json:"account_identifier,omitempty"`
}

// Group is one ABGroup row with its raw membership.
type Group struct {
	ID      int64         `json:"id"`
	Name    string        `json:"name,omitempty"`
	StoreID int64         `json:"store_id"`
	Members []GroupMember `json:"members,omitempty"`
}

// GroupMember is one ABGroupMembers row, verbatim: member_type's constant
// space is undocumented, so it is exposed raw rather than interpreted.
type GroupMember struct {
	Type     int64 `json:"type"`
	MemberID int64 `json:"member_id"`
}
