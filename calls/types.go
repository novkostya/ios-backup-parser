package calls

import "time"

// Call is one ZCALLRECORD row — a single call in the history.
//
// The store is CoreData (Z-prefixed tables, Z_PK/Z_ENT indirection); Call
// flattens the one row per call plus, for multi-party calls, the participant
// handles joined through Z_2REMOTEPARTICIPANTHANDLES.
//
// Fields backed by an absent optional column stay at their zero value AND the
// domain's Capability.Missing names them — check the capability report to tell
// "empty" from "cannot know".
type Call struct {
	// ID is ZCALLRECORD.Z_PK — the CoreData primary key, the join anchor for
	// participants and a stable per-backup identifier for the call.
	ID int64 `json:"id"`

	// Time is the call time (ZCALLRECORD.ZDATE), a Cocoa-epoch SECONDS column
	// stored as REAL (fractional); zero when NULL. NOT nanoseconds — that unit
	// is the messages domain's alone (docs/schemas/README.md, the cross-domain
	// trap).
	Time time.Time `json:"time,omitzero"`

	// Duration is the call's elapsed time (ZCALLRECORD.ZDURATION, seconds);
	// zero for an unanswered or zero-length call.
	Duration time.Duration `json:"duration,omitempty"`

	// Direction is ZCALLRECORD.ZORIGINATED verbatim; see DirectionIncoming /
	// DirectionOutgoing.
	Direction int64 `json:"direction"`

	// Answered reports ZCALLRECORD.ZANSWERED == 1. A missed call is an incoming
	// call that was not answered (see Missed).
	Answered bool `json:"answered"`

	// CallType is ZCALLRECORD.ZCALLTYPE verbatim; see CallTypePhone,
	// CallTypeFaceTimeVideo, CallTypeFaceTimeAudio, CallTypeThirdParty.
	CallType int64 `json:"call_type"`

	// Address is the remote party as stored on the record
	// (ZCALLRECORD.ZADDRESS): a phone number or identifier, denormalized and
	// present for a 1:1 call. For a multi-party / group call the full set is in
	// Participants (and Address may be empty).
	Address string `json:"address,omitempty"`

	// Name is the display name the OS resolved for the call
	// (ZCALLRECORD.ZNAME); "" when none resolved or the schema lacks the column
	// ("name" in Capability.Missing).
	Name string `json:"name,omitempty"`

	// ServiceProvider is ZCALLRECORD.ZSERVICE_PROVIDER (e.g. the telephony or
	// VoIP provider bundle); ISOCountryCode is ZCALLRECORD.ZISO_COUNTRY_CODE.
	ServiceProvider string `json:"service_provider,omitempty"`
	ISOCountryCode  string `json:"iso_country_code,omitempty"`

	// UniqueID is ZCALLRECORD.ZUNIQUE_ID, the call's own stable identifier when
	// the schema carries it.
	UniqueID string `json:"unique_id,omitempty"`

	// Read reports ZCALLRECORD.ZREAD == 1 (the call has been seen in the UI);
	// false when unread or the schema lacks the column ("read" in
	// Capability.Missing).
	Read bool `json:"read,omitempty"`

	// JunkConfidence (ZCALLRECORD.ZJUNKCONFIDENCE, INTEGER) and JunkCategory
	// (ZCALLRECORD.ZJUNKIDENTIFICATIONCATEGORY, a VARCHAR category identifier)
	// are the spam/junk signals. Recent CoreData additions: both zero and
	// "spam" appears in Capability.Missing when the schema lacks them.
	JunkConfidence int64  `json:"junk_confidence,omitempty"`
	JunkCategory   string `json:"junk_category,omitempty"`

	// Participants are the resolved handles of a multi-party / FaceTime group
	// call, joined via Z_2REMOTEPARTICIPANTHANDLES → ZHANDLE. Empty for a 1:1
	// call (the counterpart is Address) and when the schema lacks the join
	// ("participants" in Capability.Missing).
	Participants []Handle `json:"participants,omitempty"`
}

// Missed reports whether this looks like a missed call: an incoming call that
// was not answered. It rests on the ZORIGINATED interpretation
// (DirectionIncoming) — cross-referenced from iLEAPP and validated
// differentially, not guessed.
func (c Call) Missed() bool {
	return c.Direction == DirectionIncoming && !c.Answered
}

// ZCALLRECORD.ZORIGINATED interpretation (call direction). Cross-referenced
// from iLEAPP's callHistory artifact (MIT, see NOTICE) and validated
// differentially (testing ladder rung 3).
const (
	DirectionIncoming = 0
	DirectionOutgoing = 1
)

// ZCALLRECORD.ZCALLTYPE interpretation (service / kind). Cross-referenced from
// iLEAPP's callHistory artifact (MIT, see NOTICE) — note the FaceTime ordering:
// 8 is video and 16 is audio, NOT the reverse — and validated differentially.
const (
	CallTypeThirdParty    = 0 // third-party VoIP app (e.g. WhatsApp)
	CallTypePhone         = 1 // cellular telephony
	CallTypeFaceTimeVideo = 8
	CallTypeFaceTimeAudio = 16
)

// Handle is one ZHANDLE row: a participant identifier in a multi-party call.
type Handle struct {
	// ID is ZHANDLE.Z_PK.
	ID int64 `json:"id"`

	// Value is the handle as stored (ZHANDLE.ZVALUE) — a phone number, email or
	// Apple ID; NormalizedValue is its normalized form (ZHANDLE.ZNORMALIZEDVALUE).
	Value           string `json:"value,omitempty"`
	NormalizedValue string `json:"normalized_value,omitempty"`

	// Type is ZHANDLE.ZTYPE verbatim (the handle's kind); exposed raw because
	// its constant space is not interpreted in this milestone.
	Type int64 `json:"type"`
}
