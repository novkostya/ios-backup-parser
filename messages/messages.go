// Package messages streams typed message records out of an iOS backup's
// Messages database (HomeDomain, Library/SMS/sms.db).
//
// The store is plain app SQLite, but the message body on modern iOS lives in a
// typedstream `attributedBody` BLOB rather than the `text` column, so messages
// decodes it via internal/typedstream. Attachments surface as structured
// backup.FileRefs into MediaDomain; chats, handles, tapbacks, edits, replies
// and app-message balloons are exposed as capability-gated fields.
//
// Open validates the schema eagerly: an unrecognized structure fails with
// backup.ErrUnsupportedSchema before any iterator exists, and absent optional
// columns degrade the Capability report instead of silently yielding empty
// fields. Iteration follows the shared error contract (see the backup package
// doc): a *backup.RowError is row-scoped and the stream continues; any other
// yielded error is stream-scoped and ends it. A message whose sole body source
// (attributedBody) cannot be decoded is NOT dropped — it is yielded with
// BodyUndecoded set, so an unknown body is never mistaken for an empty one.
package messages

import (
	"database/sql"
	"fmt"
	"io/fs"
	"iter"
	"slices"
	"strings"
	"time"

	backup "github.com/novkostya/ios-backup-parser"
	"github.com/novkostya/ios-backup-parser/internal/cocoa"
	"github.com/novkostya/ios-backup-parser/internal/introspect"
	"github.com/novkostya/ios-backup-parser/internal/sqlitedb"
	"github.com/novkostya/ios-backup-parser/internal/typedstream"
)

// Domain and RelativePath locate the Messages database inside a backup; as a
// FileRef: backup.FileRef{Domain: Domain, RelativePath: RelativePath}.
const (
	Domain       = "HomeDomain"
	RelativePath = "Library/SMS/sms.db"
)

// attachmentDomain is where attachment.filename ("~/Library/SMS/Attachments/…")
// resolves: MediaDomain, with the leading "~/" stripped.
const attachmentDomain = "MediaDomain"

// Messages is an open messages domain. It holds an open handle to the
// materialized scratch copy of the database; Close releases it.
type Messages struct {
	db          *sql.DB
	capability  backup.Capability
	unavailable map[string]bool
}

// Open materializes the sms.db database out of fsys, introspects its schema and
// — when a supported fingerprint matches — returns the open domain. An
// unrecognized structure fails with backup.ErrUnsupportedSchema (wrapped in
// *backup.UnsupportedSchemaError carrying the observed fingerprint); a backup
// without the database fails with fs.ErrNotExist.
func Open(fsys backup.FS) (*Messages, error) {
	ok, err := fsys.Exists(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("messages: backup has no %s/%s: %w", Domain, RelativePath, fs.ErrNotExist)
	}
	path, err := fsys.Materialize(Domain, RelativePath)
	if err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	result, err := introspect.Detect(db, spec)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Messages{
		db:          db,
		capability:  result.Capability,
		unavailable: result.Unavailable,
	}, nil
}

// Capability returns the capability report produced at Open.
func (m *Messages) Capability() backup.Capability {
	capability := m.capability
	capability.Missing = slices.Clone(capability.Missing)
	return capability
}

// Close closes the underlying database handle. (The scratch copy itself belongs
// to the FS that materialized it.)
func (m *Messages) Close() error {
	return m.db.Close()
}

// Messages streams every `message` row in (date, ROWID) order. See the package
// doc for the row-scoped vs stream-scoped error contract.
func (m *Messages) Messages() iter.Seq2[Message, error] {
	return func(yield func(Message, error) bool) {
		var handles map[int64]Handle
		if !m.unavailable["handles"] {
			var err error
			if handles, err = m.loadHandles(); err != nil {
				yield(Message{}, fmt.Errorf("messages: %w", err))
				return
			}
		}
		// ONE PASS OVER chat_message_join INSTEAD OF ONE QUERY PER MESSAGE — quince#1512
		// measured the per-message form at 73.5% of this scan. Loaded here rather than in
		// enrich so the cost is paid once for the iterator, exactly as handles are.
		var chatIDs map[int64][]int64
		if !m.unavailable["chats"] {
			var err error
			if chatIDs, err = m.loadChatIDs(); err != nil {
				yield(Message{}, fmt.Errorf("messages: %w", err))
				return
			}
		}

		row := &messageRow{}
		sel, dest := m.messageColumns(row, "")

		rows, err := m.db.Query("SELECT " + strings.Join(sel, ", ") + " FROM message ORDER BY date, ROWID")
		if err != nil {
			yield(Message{}, fmt.Errorf("messages: query messages: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = messageRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Message{}, &backup.RowError{
					Domain: "messages", Table: "message", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			msg := row.message()
			if err, rowScoped := m.enrich(&msg, row, handles, chatIDs); err != nil {
				if !rowScoped {
					yield(Message{}, fmt.Errorf("messages: %w", err))
					return
				}
				if !yield(Message{}, &backup.RowError{
					Domain: "messages", Table: "message", RowID: msg.ID, Err: err,
				}) {
					return
				}
				continue
			}
			if !yield(msg, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Message{}, fmt.Errorf("messages: read messages: %w", err))
		}
	}
}

// ChatCursor is a position in one conversation's newest-first order: the message strictly
// before it is the next one returned. The zero value starts at the newest.
//
// (At, ID) RATHER THAN ID ALONE, because ROWID order is insertion order and messages arrive out
// of date order — a synced conversation interleaves. The pair is what makes the ordering total,
// so a page boundary cannot repeat or drop a message that shares a timestamp.
//
// `At` IS A time.Time, NOT THE STORED INTEGER, and that is deliberate. The column holds COCOA
// nanoseconds (epoch 2001); a caller holding `Message.Time` and reaching for `UnixNano()` is off
// by 31 years, the comparison then matches nothing, and the page walk silently never advances
// rather than failing. Taking a `time.Time` and converting inside is what makes that mistake
// unavailable — it was made here first, and the test that caught it is
// TestChatMessagesPagingMatchesOneShot.
type ChatCursor struct {
	At time.Time
	ID int64 // message ROWID
}

// ChatMessages streams ONE conversation, newest first, from `before` and no further than
// `limit` messages.
//
// WHY THIS EXISTS: reading a thread does not need the whole database. `Messages()` scans every
// row so a consumer can build an index; this answers the question a reader actually asks, and
// on a real 254,949-message backup a 50-row page costs about a millisecond against Apple's own
// covering index — `chat_message_join(message_date, message_id, chat_id)`, which SQLite uses as
// a covering search for `chat_id = ?` (quince#1531).
//
// IT ORDERS AND CURSORS ON `message.date`, NOT ON THE JOIN'S DENORMALIZED `message_date`, AND
// THAT COSTS SOMETHING ON PURPOSE. Apple ships a covering index —
// `chat_message_join(message_date, message_id, chat_id)` — which SQLite uses for `chat_id = ?`
// and which makes the paged query effectively free. Ordering on `message.date` instead needs a
// sort. Measured on a real 254,949-message backup, newest 50 of a 98,598-message conversation:
//
//	ORDER BY j.message_date (indexed)   ~0 ms
//	ORDER BY m.date         (a sort)   120 ms
//
// **The 120 ms is paid deliberately, because the fast column can be WRONG.** The two agree on
// real data — measured, 0 mismatches and 0 NULLs across 236,372 join rows — but they are two
// copies of one fact, and this package's own fixture carries join rows whose `message_date` is
// 0 while the message's date is real. A reader ordering on the stale copy returns a
// conversation in an order that is not its own, and a cursor built on it never advances: the
// page walk silently repeats its first page forever. That is worse than 120 ms, and it is not
// hypothetical — it is what the first version of this did, and
// TestChatMessagesPagingMatchesOneShot is what caught it.
//
// 120 ms is a page load replacing an ~11 s scan. If it ever matters, the fix is an index on the
// session's own copy, not trusting the denormalized column.
// NO PREFETCH HERE, DELIBERATELY. `loadChatIDs` exists for the full scan and would preload
// 236,372 rows to answer 50 — the per-message lookup is the right shape at page scale and costs
// ~50 indexed queries. The lever is a property of scanning everything.
func (m *Messages) ChatMessages(chatID int64, before ChatCursor, limit int) iter.Seq2[Message, error] {
	return func(yield func(Message, error) bool) {
		if m.unavailable["chats"] {
			yield(Message{}, fmt.Errorf("messages: chats: %w", backup.ErrUnavailable))
			return
		}
		var handles map[int64]Handle
		if !m.unavailable["handles"] {
			var err error
			if handles, err = m.loadHandles(); err != nil {
				yield(Message{}, fmt.Errorf("messages: %w", err))
				return
			}
		}

		row := &messageRow{}
		sel, dest := m.messageColumns(row, "m")

		q := "SELECT " + strings.Join(sel, ", ") +
			" FROM chat_message_join j JOIN message m ON m.ROWID = j.message_id" +
			" WHERE j.chat_id = ?"
		args := []any{chatID}
		if !before.At.IsZero() || before.ID != 0 {
			// STRICTLY BEFORE, on the pair. `<` on the tuple, spelled out because SQLite's
			// row-value form is not universally available in the versions this targets.
			q += " AND (m.date < ? OR (m.date = ? AND m.ROWID < ?))"
			at := cocoa.ToNanoseconds(before.At)
			args = append(args, at, at, before.ID)
		}
		q += " ORDER BY m.date DESC, m.ROWID DESC"
		if limit > 0 {
			q += " LIMIT ?"
			args = append(args, limit)
		}

		rows, err := m.db.Query(q, args...)
		if err != nil {
			yield(Message{}, fmt.Errorf("messages: query chat messages: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			*row = messageRow{}
			if err := rows.Scan(dest...); err != nil {
				if !yield(Message{}, &backup.RowError{
					Domain: "messages", Table: "message", RowID: row.id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			msg := row.message()
			// nil chatIDs: enrich falls back to the per-message query, which is what page
			// scale wants.
			if err, rowScoped := m.enrich(&msg, row, handles, nil); err != nil {
				if !rowScoped {
					yield(Message{}, fmt.Errorf("messages: %w", err))
					return
				}
				if !yield(Message{}, &backup.RowError{
					Domain: "messages", Table: "message", RowID: msg.ID, Err: err,
				}) {
					return
				}
				continue
			}
			if !yield(msg, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Message{}, fmt.Errorf("messages: read chat messages: %w", err))
		}
	}
}

// messageColumns builds the SELECT list and scan targets for a message row.
//
// SHARED BY BOTH READ PATHS — the full scan and the per-chat page — so a column that becomes
// available or gated cannot appear on one and not the other. Two hand-kept lists is the drift
// this exists to prevent.
//
// The caller supplies the row so the scan targets point into storage it owns and can reset
// between rows.
func (m *Messages) messageColumns(row *messageRow, qualify string) (sel []string, dest []any) {
	q := func(c string) string {
		if qualify == "" {
			return c
		}
		return qualify + "." + c
	}
	sel = []string{q("ROWID"), q("guid"), q("text"), q("date"), q("is_from_me"), q("handle_id")}
	dest = []any{&row.id, &row.guid, &row.text, &row.date, &row.isFromMe, &row.handleID}
	col := func(unit, expr string, target any) {
		if !m.unavailable[unit] {
			sel = append(sel, expr)
			dest = append(dest, target)
		}
	}
	col("attributed_text", q("attributedBody"), &row.attributedBody)
	col("service", q("service"), &row.service)
	col("delivery", q("date_read"), &row.dateRead)
	col("delivery", q("date_delivered"), &row.dateDelivered)
	col("tapbacks", q("associated_message_type"), &row.associatedType)
	col("tapbacks", q("associated_message_guid"), &row.associatedGUID)
	col("tapback_emoji", q("associated_message_emoji"), &row.associatedEmoji)
	col("edits", q("date_edited"), &row.dateEdited)
	col("edits", q("date_retracted"), &row.dateRetracted)
	col("threads", q("thread_originator_guid"), &row.threadGUID)
	col("threads", q("reply_to_guid"), &row.replyToGUID)
	col("app_messages", q("balloon_bundle_id"), &row.balloonBundleID)
	// payload_data is a BLOB; select only its presence, never its bytes.
	col("app_messages", "("+q("payload_data")+" IS NOT NULL)", &row.hasPayload)
	col("group_events", q("item_type"), &row.itemType)
	col("group_events", q("group_title"), &row.groupTitle)
	col("group_events", q("group_action_type"), &row.groupActionType)
	col("attachments", q("cache_has_attachments"), &row.cacheHasAttachments)
	return sel, dest
}

// enrich resolves the per-message sender, chat memberships and attachments. The
// bool result classifies a non-nil error: true = row-scoped (this message
// only), false = stream-scoped.
func (m *Messages) enrich(msg *Message, row *messageRow, handles map[int64]Handle, chatIDs map[int64][]int64) (error, bool) {
	// Sender handle: a non-zero handle_id that resolves to no handle row is a
	// dangling reference — withhold the message (row-scoped) rather than emit it
	// with a silently-missing sender.
	if handles != nil && row.handleID.Valid && row.handleID.Int64 != 0 {
		h, ok := handles[row.handleID.Int64]
		if !ok {
			return fmt.Errorf("message %d: dangling handle reference %d", msg.ID, row.handleID.Int64), true
		}
		msg.Handle = &h
	}
	if !m.unavailable["chats"] {
		// TWO SHAPES, ONE RULE: prefetched map for the full scan, per-message query for a page.
		//
		// `chatIDs == nil` MEANS "ask per message", not "no memberships" — the scan always
		// passes a non-nil map (possibly empty) when chats are available, and ChatMessages
		// always passes nil. Preloading 236,372 rows to answer a 50-row page would be the
		// wrong trade in the other direction (quince#1531).
		if chatIDs != nil {
			// THE SLICE IS COPIED rather than aliased: the map outlives this call and is
			// shared by every message in the scan, so handing out the backing array would let
			// a consumer's append write into the next message's memberships.
			if ids := chatIDs[msg.ID]; len(ids) > 0 {
				msg.ChatIDs = append(msg.ChatIDs, ids...)
			}
		} else if err, rowScoped := m.fillChatIDs(msg); err != nil {
			return err, rowScoped
		}
	}
	if !m.unavailable["attachments"] && row.cacheHasAttachments.Int64 != 0 {
		if err, rowScoped := m.fillAttachments(msg); err != nil {
			return err, rowScoped
		}
	}
	return nil, false
}

// fillAttachments resolves a message's attachments through
// message_attachment_join. A join row pointing to a missing attachment is a
// dangling reference (row-scoped) — the whole message is withheld rather than
// its attachment set silently truncated.
func (m *Messages) fillAttachments(msg *Message) (error, bool) {
	rows, err := m.db.Query(
		`SELECT j.attachment_id, a.ROWID, a.guid, a.filename, a.uti, a.mime_type,
			a.transfer_name, a.total_bytes, a.is_sticker
		FROM message_attachment_join j
		LEFT JOIN attachment a ON a.ROWID = j.attachment_id
		WHERE j.message_id = ? ORDER BY j.attachment_id`, msg.ID)
	if err != nil {
		return fmt.Errorf("query attachments: %w", err), false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var joinID, id, totalBytes sql.NullInt64
		var guid, filename, uti, mimeType, transferName sql.NullString
		var isSticker sql.NullInt64
		if err := rows.Scan(&joinID, &id, &guid, &filename, &uti, &mimeType,
			&transferName, &totalBytes, &isSticker); err != nil {
			return fmt.Errorf("attachment: %w", err), true
		}
		if !id.Valid {
			return fmt.Errorf("attachment: dangling reference %d", joinID.Int64), true
		}
		msg.Attachments = append(msg.Attachments, Attachment{
			ID:           id.Int64,
			GUID:         guid.String,
			File:         attachmentFileRef(filename),
			UTI:          uti.String,
			MIMEType:     mimeType.String,
			TransferName: transferName.String,
			TotalBytes:   totalBytes.Int64,
			IsSticker:    isSticker.Valid && isSticker.Int64 != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read attachments: %w", err), false
	}
	return nil, false
}

// Chats streams every `chat` row with its resolved participants. When the schema
// lacks the chat tables ("chats" in Capability.Missing) the iterator yields
// backup.ErrUnavailable instead of a misleading empty stream.
func (m *Messages) Chats() iter.Seq2[Chat, error] {
	return func(yield func(Chat, error) bool) {
		if m.unavailable["chats"] {
			yield(Chat{}, fmt.Errorf("messages: chats: %w", backup.ErrUnavailable))
			return
		}
		var handles map[int64]Handle
		if !m.unavailable["handles"] {
			var err error
			if handles, err = m.loadHandles(); err != nil {
				yield(Chat{}, fmt.Errorf("messages: %w", err))
				return
			}
		}
		rows, err := m.db.Query(
			`SELECT ROWID, guid, style, chat_identifier, service_name, display_name, room_name, group_id
			FROM chat ORDER BY ROWID`)
		if err != nil {
			yield(Chat{}, fmt.Errorf("messages: query chats: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id, style sql.NullInt64
			var guid, identifier, serviceName, displayName, roomName, groupID sql.NullString
			if err := rows.Scan(&id, &guid, &style, &identifier, &serviceName,
				&displayName, &roomName, &groupID); err != nil {
				if !yield(Chat{}, &backup.RowError{
					Domain: "messages", Table: "chat", RowID: id.Int64, Err: err,
				}) {
					return
				}
				continue
			}
			chat := Chat{
				ID:          id.Int64,
				GUID:        guid.String,
				Identifier:  identifier.String,
				DisplayName: displayName.String,
				ServiceName: serviceName.String,
				Style:       style.Int64,
				RoomName:    roomName.String,
				GroupID:     groupID.String,
			}
			if handles != nil {
				if err, rowScoped := m.fillParticipants(&chat, handles); err != nil {
					if !rowScoped {
						yield(Chat{}, fmt.Errorf("messages: %w", err))
						return
					}
					if !yield(Chat{}, &backup.RowError{
						Domain: "messages", Table: "chat", RowID: chat.ID, Err: err,
					}) {
						return
					}
					continue
				}
			}
			if !yield(chat, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Chat{}, fmt.Errorf("messages: read chats: %w", err))
		}
	}
}

// fillParticipants resolves a chat's handles through chat_handle_join. A
// dangling handle reference is row-scoped (the chat is withheld).
func (m *Messages) fillParticipants(chat *Chat, handles map[int64]Handle) (error, bool) {
	rows, err := m.db.Query(
		"SELECT handle_id FROM chat_handle_join WHERE chat_id = ? ORDER BY handle_id", chat.ID)
	if err != nil {
		return fmt.Errorf("query participants: %w", err), false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var handleID sql.NullInt64
		if err := rows.Scan(&handleID); err != nil {
			return fmt.Errorf("participant: %w", err), true
		}
		h, ok := handles[handleID.Int64]
		if !ok {
			return fmt.Errorf("participant: dangling handle reference %d", handleID.Int64), true
		}
		chat.Participants = append(chat.Participants, h)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read participants: %w", err), false
	}
	return nil, false
}

// fillChatIDs loads ONE message's chat memberships (chat_message_join is many-to-many).
//
// THE PAGE PATH'S SHAPE, and it is not dead code the prefetch replaced: ChatMessages serves ~50
// rows and calls this ~50 times, which is cheaper than preloading the whole join table. The full
// scan uses loadChatIDs instead, because there the per-message form was 73.5% of the work
// (quince#1512).
func (m *Messages) fillChatIDs(msg *Message) (error, bool) {
	rows, err := m.db.Query(
		"SELECT chat_id FROM chat_message_join WHERE message_id = ? ORDER BY chat_id", msg.ID)
	if err != nil {
		return fmt.Errorf("query chat ids: %w", err), false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var chatID sql.NullInt64
		if err := rows.Scan(&chatID); err != nil {
			return fmt.Errorf("chat id: %w", err), true
		}
		if chatID.Valid {
			msg.ChatIDs = append(msg.ChatIDs, chatID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read chat ids: %w", err), false
	}
	return nil, false
}

// loadChatIDs preloads every message's chat memberships in ONE pass.
//
// WHY THIS EXISTS: it was 73.5% of the scan. `fillChatIDs` ran one indexed query per message —
// 254,949 of them on a real backup, 24.3 µs each, 6.192 s of an 8.422 s scan (quince#1512,
// measured by instrumenting this function). Replacing that with a single pass over the join
// table is the same lever `loadHandles` already applies to `handle`, one table over.
//
// IT IS NOT BOUNDED THE WAY `loadHandles` IS, AND THE COMMENT ABOVE IT SHOULD NOT BE COPIED.
// `handle` holds one row per distinct correspondent — bounded by who you talk to. This is
// bounded by how MUCH you talk: one row per (message, chat), 236,372 on the same backup, so the
// map is O(messages) rather than O(correspondents). At that size it is a few tens of MB against
// a decrypted manifest already in scratch that is an order larger, which is why it is worth it —
// but the reason is a size comparison, not a boundedness claim.
//
// ONLY THE FULL SCAN SHOULD CALL THIS. A future per-chat cursored accessor serving one 50-row
// page must NOT preload 236,372 rows to answer it; at page scale the per-message query this
// replaces costs ~50 lookups and is the right shape. The lever is a property of scanning
// everything, not of reading chat memberships.
//
// ITS ERROR IS DELIBERATELY NOT A *backup.RowError, AND WRAPPING IT AS ONE WOULD BE A SILENT
// REGRESSION. Consumers classify: a RowError withholds one record and the stream continues, and
// quince counts those and reports "N message(s) could not be read". **A prefetch failure is not
// attributable to one message** — every message would lose its memberships while a reassuring
// count said one, so group chats would collapse to nothing and threads would lose their
// membership rows with nothing saying why. A wrong-but-plausible result is worse than a loud
// stop, which is why this fails the whole scan instead.
//
// The better answer is probably a third one — skip the bad join row, count it, report it, which
// is the pattern quince's own consumer implements — and it needs a warning channel this package
// does not have. That is an API question, not a line here.
func (m *Messages) loadChatIDs() (map[int64][]int64, error) {
	rows, err := m.db.Query(
		"SELECT message_id, chat_id FROM chat_message_join ORDER BY message_id, chat_id")
	if err != nil {
		return nil, fmt.Errorf("load chat ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]int64{}
	for rows.Next() {
		var messageID, chatID sql.NullInt64
		if err := rows.Scan(&messageID, &chatID); err != nil {
			return nil, fmt.Errorf("load chat ids: %w", err)
		}
		// A NULL on either side is a join row that points nowhere; the per-message query this
		// replaces skipped a NULL chat_id the same way, so the emitted set is unchanged.
		if messageID.Valid && chatID.Valid {
			out[messageID.Int64] = append(out[messageID.Int64], chatID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load chat ids: %w", err)
	}
	return out, nil
}

// loadHandles preloads the `handle` reference table (bounded: one row per
// distinct correspondent). Nothing here outlives the iterator.
func (m *Messages) loadHandles() (map[int64]Handle, error) {
	rows, err := m.db.Query("SELECT ROWID, id, service, country FROM handle")
	if err != nil {
		return nil, fmt.Errorf("load handles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]Handle{}
	for rows.Next() {
		var id sql.NullInt64
		var identifier, service, country sql.NullString
		if err := rows.Scan(&id, &identifier, &service, &country); err != nil {
			return nil, fmt.Errorf("load handles: %w", err)
		}
		out[id.Int64] = Handle{
			ID:         id.Int64,
			Identifier: identifier.String,
			Service:    service.String,
			Country:    country.String,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load handles: %w", err)
	}
	return out, nil
}

// messageRow holds one scanned `message` row; only the columns selected for this
// database's capability are filled.
type messageRow struct {
	id       sql.NullInt64
	guid     sql.NullString
	text     sql.NullString
	date     sql.NullInt64 // Cocoa NANOseconds
	isFromMe sql.NullInt64
	handleID sql.NullInt64

	attributedBody      []byte
	service             sql.NullString
	dateRead            sql.NullInt64
	dateDelivered       sql.NullInt64
	associatedType      sql.NullInt64
	associatedGUID      sql.NullString
	associatedEmoji     sql.NullString
	dateEdited          sql.NullInt64
	dateRetracted       sql.NullInt64
	threadGUID          sql.NullString
	replyToGUID         sql.NullString
	balloonBundleID     sql.NullString
	hasPayload          sql.NullInt64
	itemType            sql.NullInt64
	groupTitle          sql.NullString
	groupActionType     sql.NullInt64
	cacheHasAttachments sql.NullInt64
}

func (r *messageRow) message() Message {
	m := Message{
		ID:                   r.id.Int64,
		GUID:                 r.guid.String,
		IsFromMe:             r.isFromMe.Valid && r.isFromMe.Int64 != 0,
		Service:              r.service.String,
		AssociatedType:       r.associatedType.Int64,
		AssociatedGUID:       r.associatedGUID.String,
		AssociatedEmoji:      r.associatedEmoji.String,
		ThreadOriginatorGUID: r.threadGUID.String,
		ReplyToGUID:          r.replyToGUID.String,
		BalloonBundleID:      r.balloonBundleID.String,
		HasPayload:           r.hasPayload.Valid && r.hasPayload.Int64 != 0,
		ItemType:             r.itemType.Int64,
		GroupTitle:           r.groupTitle.String,
		GroupActionType:      r.groupActionType.Int64,
	}
	m.Text, m.BodyUndecoded = bodyText(r.text, r.attributedBody)
	if r.date.Valid {
		m.Time = cocoa.FromNanoseconds(r.date.Int64)
	}
	if r.dateRead.Valid && r.dateRead.Int64 != 0 {
		m.DateRead = cocoa.FromNanoseconds(r.dateRead.Int64)
	}
	if r.dateDelivered.Valid && r.dateDelivered.Int64 != 0 {
		m.DateDelivered = cocoa.FromNanoseconds(r.dateDelivered.Int64)
	}
	if r.dateEdited.Valid && r.dateEdited.Int64 != 0 {
		m.DateEdited = cocoa.FromNanoseconds(r.dateEdited.Int64)
	}
	if r.dateRetracted.Valid && r.dateRetracted.Int64 != 0 {
		m.DateRetracted = cocoa.FromNanoseconds(r.dateRetracted.Int64)
	}
	return m
}

// bodyText applies the modern-iOS body rule: prefer message.text when it
// carries real content, otherwise decode the typedstream attributedBody. The
// bool result is BodyUndecoded — true only when text was the sole source and
// the blob failed to decode (body unknown, never silently ""). U+FFFC (the
// object-replacement placeholder that marks attachment positions) is stripped so
// an attachment-only body reads as empty rather than as a stray placeholder.
func bodyText(text sql.NullString, attributedBody []byte) (string, bool) {
	if text.Valid {
		if s := cleanBody(text.String); s != "" {
			return s, false
		}
	}
	if len(attributedBody) > 0 {
		decoded, err := typedstream.DecodeText(attributedBody)
		if err != nil {
			return "", true // sole-source decode failure → body unknown
		}
		return cleanBody(decoded), false
	}
	return "", false // genuinely empty
}

// cleanBody removes U+FFFC object-replacement placeholders (attachment
// positions) from a decoded body.
func cleanBody(s string) string {
	return strings.ReplaceAll(s, "￼", "")
}

// attachmentFileRef turns attachment.filename ("~/Library/SMS/Attachments/…")
// into a structured MediaDomain reference, or nil when the filename is absent
// (not downloaded / purged / iCloud-only) — never a fabricated path.
func attachmentFileRef(filename sql.NullString) *backup.FileRef {
	if !filename.Valid || filename.String == "" {
		return nil
	}
	rel := strings.TrimPrefix(filename.String, "~/")
	return &backup.FileRef{Domain: attachmentDomain, RelativePath: rel}
}
