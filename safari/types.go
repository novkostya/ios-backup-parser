package safari

import "time"

// Bookmark is one row of the Bookmarks.db `bookmarks` table — a bookmark, a folder,
// or one of Safari's built-in special folders. The whole bookmark tree is these
// rows: the hierarchy is self-referential through Parent, and folders vs leaves are
// told apart by Type. Reading-list items are a distinct kind and are streamed by
// ReadingList (not Bookmarks) — see the package doc.
//
// The store is plain app SQLite (Safari's own schema). Fields backed by an absent
// optional column stay at their zero value AND the domain's Capability.Missing names
// them: check the capability report to tell "empty" from "cannot know".
type Bookmark struct {
	// ID is bookmarks.id — the tree node identifier (Root is 0) and the anchor a
	// child's Parent points at.
	ID int64 `json:"id"`

	// Parent is bookmarks.parent, the containing folder's ID. It is 0 both for a
	// direct child of Root and for Root itself (whose parent is NULL); use IsRoot to
	// tell them apart. A parent that does not resolve leaves the reference dangling —
	// the row is still yielded (soft-nil), never withheld.
	Parent int64 `json:"parent"`

	// Type is bookmarks.type verbatim: 0 = leaf (a bookmark), 1 = folder. Use
	// IsFolder rather than comparing the raw code.
	Type int64 `json:"type"`

	// SpecialID is bookmarks.special_id — Safari's built-in folder identity: 0
	// ordinary, 1 BookmarksBar (the Favorites bar), 2 BookmarksMenu, 3 the
	// com.apple.ReadingList root. Zero and "special" in Capability.Missing when the
	// column is absent. See the Special* constants.
	SpecialID int64 `json:"special_id,omitempty"`

	// Title is bookmarks.title; URL is bookmarks.url (empty for a folder). URL uses
	// NOCASE collation in the schema and is surfaced verbatim.
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`

	// OrderIndex is bookmarks.order_index — the sibling sort order within a folder
	// ("order" in Capability.Missing when absent). NumChildren is bookmarks.num_children
	// for a folder ("num_children" in Capability.Missing when absent).
	OrderIndex  int64 `json:"order_index,omitempty"`
	NumChildren int64 `json:"num_children,omitempty"`

	// Hidden reports bookmarks.hidden != 0 ("hidden" in Capability.Missing when
	// absent); Deleted reports bookmarks.deleted != 0, a soft-delete tombstone flag
	// surfaced (not filtered), so a consumer can drop tombstoned rows itself
	// ("deleted" in Capability.Missing when absent).
	Hidden  bool `json:"hidden,omitempty"`
	Deleted bool `json:"deleted,omitempty"`

	// UUID is bookmarks.external_uuid, a stable cross-device identifier ("uuid" in
	// Capability.Missing when absent).
	UUID string `json:"uuid,omitempty"`

	// LastModified is bookmarks.last_modified — a UNIX-epoch SECONDS timestamp
	// (REAL). NOT Cocoa: unlike History's visit times (Cocoa seconds) and every other
	// domain's timestamps (Cocoa), Safari's Bookmarks.db stores Unix seconds — see
	// docs/schemas/safari.md, the two-epoch trap. Zero when NULL or the schema lacks
	// the column ("modified" in Capability.Missing).
	LastModified time.Time `json:"last_modified,omitzero"`
}

// IsFolder reports whether the row is a folder (Type == 1) rather than a leaf
// bookmark (Type == 0).
func (b Bookmark) IsFolder() bool { return b.Type == bookmarkTypeFolder }

// IsRoot reports whether the row is the tree Root (ID 0) — the implicit parent of the
// built-in special folders, not a user-visible bookmark.
func (b Bookmark) IsRoot() bool { return b.ID == rootBookmarkID }

// bookmarks.type values.
const (
	bookmarkTypeLeaf   = 0
	bookmarkTypeFolder = 1
	rootBookmarkID     = 0
)

// bookmarks.special_id values — Safari's built-in special folders (0 = an ordinary
// user bookmark or folder). SpecialReadingList (3) is the com.apple.ReadingList root
// whose leaf children are reading-list items.
const (
	SpecialNone          = 0
	SpecialBookmarksBar  = 1
	SpecialBookmarksMenu = 2
	SpecialReadingList   = 3
)

// ReadingListItem is one Safari Reading List entry — a leaf bookmarks row hanging off
// the com.apple.ReadingList folder (SpecialReadingList). It is discriminated from an
// ordinary bookmark by a non-NULL bookmarks.read column (0 = unread, 1 = read), which
// on the observed schema coincides exactly with membership under that folder.
//
// Reading-list-specific metadata (the saved-preview text and the DateAdded /
// DateLastViewed timestamps) lives in an extra_attributes BINARY PLIST BLOB and is
// deferred in v0.1 (decoding it needs a binary-plist reader); the column-level
// LastModified below tracks the item's add/refresh time.
type ReadingListItem struct {
	// ID is bookmarks.id; Parent is the com.apple.ReadingList folder's ID.
	ID     int64 `json:"id"`
	Parent int64 `json:"parent"`

	// Title is bookmarks.title; URL is bookmarks.url (the saved page).
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`

	// Read reports bookmarks.read != 0 (whether the item has been read).
	Read bool `json:"read"`

	// LastModified is bookmarks.last_modified — a UNIX-epoch SECONDS timestamp (see
	// Bookmark.LastModified and the two-epoch trap). For a freshly-saved item it
	// equals the reading-list plist DateAdded. Zero when NULL or "modified" is in
	// Capability.Missing.
	LastModified time.Time `json:"last_modified,omitzero"`
}

// Visit is one row of History.db's history_visits table — a single visit to a page —
// with the page URL and aggregate visit count joined in from history_items.
//
// History is a SEPARATE database (History.db) opened alongside Bookmarks.db; when it
// is absent or its schema is unrecognized, History() yields backup.ErrUnavailable and
// "history" appears in Capability.Missing.
type Visit struct {
	// ID is history_visits.id — the per-visit identifier and the target of another
	// visit's RedirectSource / RedirectDestination.
	ID int64 `json:"id"`

	// Time is history_visits.visit_time — a COCOA 2001-epoch SECONDS timestamp
	// (REAL), converted with the Cocoa epoch. NOTE this differs from Bookmarks.db's
	// Unix-epoch last_modified (docs/schemas/safari.md, the two-epoch trap). Zero when
	// NULL.
	Time time.Time `json:"time,omitzero"`

	// Title is history_visits.title (the page title at visit time; may be empty).
	Title string `json:"title,omitempty"`

	// URL is history_items.url and VisitCount is history_items.visit_count — the
	// distinct page and its aggregate visit count, joined via
	// history_visits.history_item (LEFT JOIN; empty/zero if the item does not resolve).
	URL        string `json:"url,omitempty"`
	VisitCount int64  `json:"visit_count,omitempty"`

	// RedirectSource / RedirectDestination are history_visits.id values (self-
	// references): the visit this one was redirected FROM / TO. 0 when there is no
	// redirect. Surfaced as raw visit ids; iLEAPP resolves them to URLs (the
	// differential harness does the same to compare).
	RedirectSource      int64 `json:"redirect_source,omitempty"`
	RedirectDestination int64 `json:"redirect_destination,omitempty"`

	// Origin is history_visits.origin verbatim: 0 = Local Device, 1 = iCloud-Synced
	// Device (cross-referenced from iLEAPP safariHistory.py, MIT — see NOTICE).
	// Surfaced raw; see the Origin* constants.
	Origin int64 `json:"origin,omitempty"`
}

// history_visits.origin interpretation (cross-referenced from iLEAPP safariHistory.py,
// MIT — see NOTICE).
const (
	OriginLocalDevice        = 0
	OriginICloudSyncedDevice = 1
)
