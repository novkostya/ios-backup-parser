package safari

import "github.com/novkostya/ios-backup-parser/internal/introspect"

// Fingerprint safari.1 — first observed on the iOS 18.x study backup; the full
// observed structure and its evidence live in docs/schemas/safari.md. Identity is the
// introspected structure of Bookmarks.db, never a version claim; detection checks
// table/column PRESENCE and unknown extra columns never disqualify (the bookmarks
// table carries ~40 of them — sync/icon/archive bookkeeping).
//
// The store is plain app SQLite (Safari's own schema), so relations are by id
// convention (a self-FK bookmarks.parent → bookmarks.id) rather than CoreData Z_PK
// indirection. Required is deliberately minimal: the bookmarks anchor and the columns
// without which a bookmark row would be meaningless — its identity, its place in the
// tree, whether it is a folder, and its title/url. Everything that can degrade
// honestly is an Optional unit whose absence lands its name in Capability.Missing,
// including the "reading_list" discriminator (bookmarks.read) that separates
// reading-list items from ordinary bookmarks.
var bookmarksSpec = introspect.Spec{
	Domain: "safari",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "safari.1",
			Required: introspect.Tables{
				"bookmarks": {"id", "parent", "type", "title", "url"},
			},
			Optional: []introspect.Unit{
				{Name: "special", Tables: introspect.Tables{"bookmarks": {"special_id"}}},
				{Name: "order", Tables: introspect.Tables{"bookmarks": {"order_index"}}},
				{Name: "hidden", Tables: introspect.Tables{"bookmarks": {"hidden"}}},
				{Name: "num_children", Tables: introspect.Tables{"bookmarks": {"num_children"}}},
				{Name: "modified", Tables: introspect.Tables{"bookmarks": {"last_modified"}}},
				{Name: "uuid", Tables: introspect.Tables{"bookmarks": {"external_uuid"}}},
				{Name: "deleted", Tables: introspect.Tables{"bookmarks": {"deleted"}}},
				// The reading-list read/unread discriminator: a non-NULL read column
				// marks a reading-list item. Absent → ReadingList() is unavailable and
				// Bookmarks() cannot exclude reading-list items.
				{Name: "reading_list", Tables: introspect.Tables{"bookmarks": {"read"}}},
			},
			// "history" is the cross-file unit for the separate History.db; it is not a
			// column of Bookmarks.db, so it is added to Capability.Missing at Open when
			// History.db is absent or unrecognized (see Reader.openHistory), not here.
		},
	},
}

// historySpec introspects the SEPARATE History.db. It is not a safari fingerprint of
// its own: a backup without History.db, or with a History.db whose structure does not
// match, simply degrades the History() stream to backup.ErrUnavailable ("history" in
// Capability.Missing) — it never fails safari.Open. Required lists exactly the columns
// History() reads, so a differently-shaped History.db degrades cleanly rather than
// mis-reading.
var historySpec = introspect.Spec{
	Domain: "safari.history",
	Fingerprints: []introspect.Fingerprint{
		{
			Label: "safari.history.1",
			Required: introspect.Tables{
				"history_visits": {
					"id", "history_item", "visit_time", "title",
					"redirect_source", "redirect_destination", "origin",
				},
				"history_items": {"id", "url", "visit_count"},
			},
		},
	},
}
