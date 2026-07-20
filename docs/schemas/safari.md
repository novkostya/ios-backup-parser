# safari — `Bookmarks.db` (+ `History.db`)

- **Backup location:** `HomeDomain` / `Library/Safari/` — this domain spans **two**
  plain-SQLite stores:
  - `Bookmarks.db` — **bookmarks + reading list** (the reading list lives inside it
    as a bookmark subtree; there is no separate reading-list file).
  - `History.db` — **browsing history** (a distinct database; treated as an optional
    stream — see *Two databases* below).
  - Siblings `BrowserState.db` (open-tab session state) and `SafariTabs.db` (tab
    metadata / iCloud tabs) are **not** parsed by this domain.
- **Storage idiom:** plain app SQLite (no CoreData) for both stores.
- **Fingerprint:** `safari.1` — status **validated** (M7 differential vs iLEAPP;
  iOS 18.x baseline).
- **WAL:** both stores were observed checkpointed (no `-wal`/`-shm` sidecar present).
  A read-only open needs `immutable=1` (or a writable scratch copy — what
  `Materialize` provides). Sidecars, when present, are materialized alongside.

## Two databases — one domain, one capability report

`safari.Open` opens `Bookmarks.db` (the primary store) and, when present, also opens
`History.db`. History is an **optional unit** (`history` in `Capability.Missing`): a
backup without `History.db`, or with a `History.db` whose schema does not match,
degrades `History()` to `backup.ErrUnavailable` rather than failing `Open` — the
bookmarks/reading-list streams stand alone. `Bookmarks.db`'s structure alone
determines the `safari.1` fingerprint. History has moved and been
protection-class-gated across iOS versions (the charter's Safari caveat), so its
presence is **verified per backup**, never assumed.

## `Bookmarks.db` — core tables

| Table | Role |
|---|---|
| `bookmarks` | **the record table** — every bookmark, folder, special folder, and reading-list item is one row; the tree is self-referential via `parent` |
| `folder_ancestors` | folder transitive-closure (`folder_id` → `ancestor_id`) — folders only; leaf bookmarks are not listed here |
| `bookmark_title_words` | per-word title search index (derived; not a source of truth) |
| `database_properties` / `sync_properties` / `generations` | sync/versioning bookkeeping |

### The `bookmarks` tree

Relations are by id convention (a self-FK `parent` → `bookmarks.id`); there are no
CoreData `Z_PK` indirections.

```
bookmarks.id = 0  "Root"            (special_id 0, parent NULL, type 1)
  ├─ special_id 1  "BookmarksBar"   (the Favorites bar)     ─┐
  ├─ special_id 2  "BookmarksMenu"                           │ built-in folders,
  ├─ special_id 3  "com.apple.ReadingList"                   │ parent = Root
  │     └─ reading-list items (leaves; see discriminator)   ─┘
  └─ user folders / bookmarks … (nested via parent)
```

- **`type`** (the folder/leaf discriminator): **`0` = leaf** (a bookmark or a
  reading-list item — a URL entry), **`1` = folder**. No other value observed.
- **`special_id`** (Apple built-in folder identity): `0` ordinary; `1` BookmarksBar;
  `2` BookmarksMenu; `3` the `com.apple.ReadingList` root. The titles above are
  Apple's structural identifiers, not user content.
- **`parent`** points at the containing folder's `id` (Root is `0`). Observed
  referentially intact (no dangling parents); a dangling `parent` resolves **soft-nil**
  (no folder), never withholding the row.
- Sibling order is **`order_index`** (NOT NULL); `num_children` is the folder's child
  count; `hidden` / `deletable` / `editable` are flags. `deleted` is a soft-delete
  tombstone flag (all rows observed `deleted = 0`); it is surfaced, not filtered
  (matching iLEAPP, which reads every `bookmarks` row).

### Reading list — the discriminator (inside `Bookmarks.db`)

Reading-list items are ordinary **leaf** rows (`type = 0`) that hang directly off the
`special_id = 3` `com.apple.ReadingList` folder. The clean per-row marker is the
**`read`** column:

- **`read IS NOT NULL`** ⇔ the row is a reading-list item (`0` = unread, `1` = read).
- **`read IS NULL`** ⇔ an ordinary bookmark or a folder.

On the study backup the two definitions coincide exactly (every `read`-non-NULL row
is a direct child of the reading-list folder, and vice versa). The parser partitions
on `read IS NOT NULL` (a single column, no tree walk) and documents the folder-
membership equivalence. When the `read` column is absent (`reading_list` in
`Capability.Missing`), `ReadingList()` degrades to `backup.ErrUnavailable` and
`Bookmarks()` cannot split the two — it then emits every non-history row.

**Reading-list metadata is a binary plist.** Each reading-list row carries an
`extra_attributes` BLOB — a binary property list with a `com.apple.ReadingList`
dict holding `DateAdded`, `PreviewText`, `DateLastViewed`, `DateLastFetched`,
`ReadingListIconURL` (keys observed; a subset per item). Decoding it needs a
binary-plist reader; **deferred** in v0.1 (a forward note, like calendar's
`ExceptionDate` and messages' rich runs). The column-level `last_modified`
(see below) tracks the reading-list item's add/refresh time and is surfaced as the
reading-list timestamp; on the observed data it equals the plist `DateAdded` for a
freshly-added item (the epoch cross-check that pinned the unit — see below).

## `History.db` — core tables

| Table | Role |
|---|---|
| `history_items` | one row per **distinct URL**: `url` (UNIQUE, NOT NULL), `visit_count`, `domain_expansion`, `visit_count_score`, … |
| `history_visits` | one row per **visit**: `visit_time`, `title`, `history_item` → item, `redirect_source` / `redirect_destination` (self-FKs), `origin`, `load_successful`, `synthesized` |
| `history_tombstones` | deletion records (`url`, `start_time`/`end_time`) — not surfaced |
| `history_tags` / `history_items_to_tags` / `history_events` / `metadata` | tagging + bookkeeping — not surfaced |

### History join topology (matches iLEAPP `safariHistory.py`)

```
history_visits (one per visit)  ── the record
  ├─ history_item          ─▶ history_items.id   (LEFT JOIN → url, visit_count)
  ├─ redirect_source       ─▶ history_visits.id  (the visit this one was redirected FROM)
  └─ redirect_destination  ─▶ history_visits.id  (the visit this one redirected TO)
```

- One record **per visit** (`history_visits`), the item's `url` and `visit_count`
  joined in (LEFT JOIN — observed referentially intact, no dangling `history_item`).
- `redirect_source` / `redirect_destination` are **visit ids** (self-references),
  surfaced raw; iLEAPP resolves them to URLs via an id→url map (the differential
  harness does the same to compare).
- **`origin`**: `0` = Local Device, `1` = iCloud-Synced Device (cross-referenced from
  iLEAPP `safariHistory.py`; only `0` observed on this backup — surfaced raw, the
  interpretation documented not asserted).

## Timestamps — the cross-**store** trap (two epochs in one domain)

Getting an epoch wrong yields wrong-but-plausible dates (off by 31 years). Safari's
two stores do **not** agree on their epoch — this is the single most dangerous footgun
in this domain, and the reason the schema spike checked both magnitudes and
cross-checked against the reading-list plist `DateAdded`:

| Store | Column | Epoch | **Unit** | SQL type | Converter |
|---|---|---|---|---|---|
| `Bookmarks.db` | `bookmarks.last_modified` | **1970-01-01 (Unix)** | seconds | REAL | `time.Unix` (no Cocoa delta) |
| `History.db` | `history_visits.visit_time` | 2001-01-01 (Cocoa) | seconds | REAL | `cocoa.FromSecondsFloat` |

- **`bookmarks.last_modified` is Unix seconds, not Cocoa.** Verified two ways: the
  raw magnitude lands in 2012–2021 as Unix (2043–2052 as Cocoa — an impossible future
  for a modified date), and for reading-list rows it equals the `com.apple.ReadingList`
  plist `DateAdded` (a real `CFDate`) exactly when read as Unix. Treating it as Cocoa
  is the wrong-but-plausible bug this domain must not ship.
- **`history_visits.visit_time` is Cocoa 2001 seconds.** Magnitude lands in 2026 as
  Cocoa (1995 as Unix — before Safari existed); iLEAPP confirms with
  `datetime(visit_time + 978307200, 'unixepoch')`.

## Icons / attachments — no `FileRef`

`bookmarks.icon` is an inline favicon BLOB and `fetched_icon` a flag; favicons also
live in a separate Safari favicon store (`Favicons/`, iLEAPP `safariFavicons.py`) —
out of scope. No file referenced by a bookmark, reading-list item, or visit lives in
the backup as a separate domain file, so the domain emits **no** `backup.FileRef`
(the never-fabricate rule). Inline icon BLOBs are not surfaced in v0.1.

## Capability mapping (validated against reality)

The `safari.1` fingerprint requires only the `bookmarks` anchor (`id`, `parent`,
`type`, `title`, `url`); everything else is an Optional unit whose absence lands its
name in `Capability.Missing`:

| Unit (`Missing[]` name) | Source | Record field |
|---|---|---|
| `special` | `bookmarks.special_id` | `Bookmark.SpecialID` (BookmarksBar/Menu/ReadingList/root identity) |
| `order` | `bookmarks.order_index` | `Bookmark.OrderIndex` |
| `hidden` | `bookmarks.hidden` | `Bookmark.Hidden` |
| `num_children` | `bookmarks.num_children` | `Bookmark.NumChildren` |
| `modified` | `bookmarks.last_modified` (Unix) | `Bookmark.LastModified` / `ReadingListItem.LastModified` |
| `uuid` | `bookmarks.external_uuid` | `Bookmark.UUID` |
| `deleted` | `bookmarks.deleted` | `Bookmark.Deleted` |
| `reading_list` | `bookmarks.read` | the `ReadingList()` stream + `ReadingListItem.Read` |
| `history` | `History.db` → `history_visits` + `history_items` | the `History()` stream |

On the observed study backup every unit was present (empty `Capability.Missing`),
zero row errors, and the differential (phase 1: iLEAPP's Safari **Bookmarks** and
**History** exports; phase 2: iLEAPP's query semantics re-run against scratch copies,
keyed by `bookmarks.id` and `history_visits.id` with a both-directions set check)
agreed on every surfaced field — moving `safari.1` from `observed` to **validated**.

The only phase-1 divergence was a **±1-second rendering artifact** on the visit
timestamp: iLEAPP renders `visit_time` via SQLite `datetime(…,'unixepoch')`, which
**rounds** the fractional second, while the parser keeps the precise sub-second value
and truncates on display; the two disagree by a second only for a visit whose
fractional part is ≥ 0.5. The parser holds the exact value, so `diff_safari.py` phase 1
tolerates ±1s (the same Julian-day rounding tolerance the calls domain applies) and
phase 2 — truncating identically on both sides — is exact. The parser needed no
correctness change.
