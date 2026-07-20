package calls

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint calls.1 — first observed on the iOS 18.x study backup; the full
// observed structure and its evidence live in docs/schemas/calls.md. Identity
// is the introspected structure, never a version claim; detection checks
// table/column PRESENCE and unknown extra columns never disqualify (ZCALLRECORD
// carries dozens of them — trust score, screen sharing, emergency video, …).
//
// The store is CoreData. Two CoreData facts shape this spec:
//
//   - Z_PK is a real declared column (INTEGER PRIMARY KEY), not the implicit
//     rowid, so it is required by name.
//   - The participant relationship is a CoreData many-to-many join table whose
//     name and column names embed the entities' Z_ENT ordinals
//     (Z_2REMOTEPARTICIPANTHANDLES: 2 = CallRecord, 4 = Handle). Those ordinals
//     are a per-model fact; a future model that renumbers is a DIFFERENT
//     fingerprint (calls.2), not a silent degradation — which is exactly why
//     the join is an Optional unit keyed on the exact observed names.
//
// Required is deliberately minimal: the ZCALLRECORD anchor and the columns
// without which a call record would be misleading (its time, direction,
// answered flag, kind and remote party). Everything that can degrade honestly
// is an Optional unit whose absence lands in Capability.Missing under its name.
var spec = introspect.Spec{
	Domain: "calls",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "calls.1",
			Required: introspect.Tables{
				"ZCALLRECORD": {
					"Z_PK", "ZDATE", "ZDURATION",
					"ZORIGINATED", "ZANSWERED", "ZCALLTYPE", "ZADDRESS",
				},
			},
			Optional: []introspect.Unit{
				{Name: "name", Tables: introspect.Tables{"ZCALLRECORD": {"ZNAME"}}},
				{Name: "service_provider", Tables: introspect.Tables{"ZCALLRECORD": {"ZSERVICE_PROVIDER"}}},
				{Name: "iso_country_code", Tables: introspect.Tables{"ZCALLRECORD": {"ZISO_COUNTRY_CODE"}}},
				{Name: "unique_id", Tables: introspect.Tables{"ZCALLRECORD": {"ZUNIQUE_ID"}}},
				{Name: "read", Tables: introspect.Tables{"ZCALLRECORD": {"ZREAD"}}},
				// Spam/junk signals — recent CoreData additions. Note the two
				// columns' distinct SQL types: ZJUNKCONFIDENCE is INTEGER,
				// ZJUNKIDENTIFICATIONCATEGORY is VARCHAR.
				{Name: "spam", Tables: introspect.Tables{
					"ZCALLRECORD": {"ZJUNKCONFIDENCE", "ZJUNKIDENTIFICATIONCATEGORY"},
				}},
				// Multi-party / FaceTime group participants: the CoreData M:N
				// join to ZHANDLE. Z_2REMOTEPARTICIPANTCALLS → ZCALLRECORD.Z_PK,
				// Z_4REMOTEPARTICIPANTHANDLES → ZHANDLE.Z_PK.
				{Name: "participants", Tables: introspect.Tables{
					"Z_2REMOTEPARTICIPANTHANDLES": {"Z_2REMOTEPARTICIPANTCALLS", "Z_4REMOTEPARTICIPANTHANDLES"},
					"ZHANDLE":                     {"Z_PK", "ZVALUE", "ZNORMALIZEDVALUE", "ZTYPE"},
				}},
			},
		},
	},
}
