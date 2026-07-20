# messages — `sms.db`

- **Backup location:** `HomeDomain` / `Library/SMS/sms.db`. Attachments live under
  `MediaDomain` (see *Attachments* below).
- **Storage idiom:** plain app SQLite **+ blob-encoded text** (`attributedBody` is
  Apple **typedstream** — the M3 hard part).
- **Fingerprint:** `messages.1` — status **validated** (2026-07-20, operator-local
  differential vs iLEAPP; iOS 18.x baseline; schema carries RCS, satellite-send,
  key-transparency, custom-emoji-tapback columns). Every message body was cross-checked
  against the independent python-typedstream decoder, both directions, keyed by ROWID —
  zero mismatches.
- **WAL:** header `wal`; no sidecar present in the study backup (checkpointed). This
  is the highest-volume domain — the copy-to-scratch + WAL rule matters most here.

## Core tables & join topology

```
chat (ROWID)                              a conversation (1:1 or group)
  ├─◀ chat_handle_join.chat_id → handle.ROWID     participants
  └─◀ chat_message_join.chat_id
        └─ .message_id → message.ROWID
message (ROWID)                           a message
  ├─ handle_id → handle.ROWID             the other party (0 when is_from_me=1)
  └─◀ message_attachment_join.message_id
        └─ .attachment_id → attachment.ROWID
handle (ROWID)   id (phone/email/handle), service, country, uncanonicalized_id
attachment (ROWID)  guid, filename, uti, mime_type, transfer_name, total_bytes
```

All three joins are explicit FKs (`REFERENCES … ON DELETE CASCADE`). A message can
belong to multiple chats (`chat_message_join` is M:N); a chat's participants are the
union of `chat_handle_join`. `chat.style` distinguishes **1:1 vs group** — values
**{45 = direct, 43 = group}** confirmed present on the study backup (M3
re-introspection), the mapping cross-referenced from iLEAPP `sms.py`.
`message.cache_roomnames` is a denormalized group cache maintained by triggers.

## The text problem (`attributedBody` / typedstream)

`message` has both `text TEXT` and `attributedBody BLOB`. On modern iOS the `text`
column is **frequently NULL, with the content carried only in `attributedBody`** — a
well-known modern-Messages behavior — so a typedstream decoder is **mandatory**, not
optional; skipping it silently drops message bodies (exactly the wrong-but-plausible
failure the charter forbids).

- `attributedBody` is Apple **typedstream** — it opens with the magic prefix
  `04 0B "streamtyped" 81 E8 03` (streamer version 4, signature, system version 1000).
- It serializes an `NSAttributedString` (often `NSMutableAttributedString` on real
  data): the backing `NSString` — the full plain text, the first inline string in the
  graph — **plus** runs (mentions, links, formatting, message-effect ranges). Plain
  text is the M3 deliverable; run/rich extraction is deferred for v0.1.
- Parser rule: prefer `text` when it carries real content; else decode
  `attributedBody`. Confirmed against iLEAPP `sms.py` (`if not message_text and
  attributedBody: parse_typedstream(...)`).

### typedstream reference model — the decoder trap (M3, confirmed on real data)

The decoder (`internal/typedstream`) is a recursive-descent reader written from public
prose format docs only (never the GPL imessage-exporter/crabstep source). The one fact
that must be right — and is easy to get wrong — is the shared-reference model:

- There are **two independent reference tables**, each numbered from `0x92`: one for
  shared **strings** (type-encodings, class names, C-strings), one for **objects and
  classes**. A back-reference indexes the table matching its *context* (a superclass
  slot → the object table; a type-encoding → the string table). An object's number is
  assigned **before** its class is read.
- Using a single combined table decodes the short `NSAttributedString → NSObject`
  chain by luck (the wrong back-reference still lands on *a* class) but mis-resolves
  the longer `NSMutableAttributedString → NSAttributedString → NSObject` chain — a
  superclass back-reference then points at a class-name *string*. This surfaced as a
  substantial share of real message bodies failing to decode until the two-table model
  was applied; the fact was confirmed against python-typedstream's docstrings (LGPL —
  facts only).

### Decode-failure and placeholder semantics (parser contract)

- **Sole-source decode failure is surfaced, never silently empty.** If `text` is
  empty/NULL *and* the `attributedBody` blob cannot be decoded, the body is *unknown*,
  not empty: the record is still yielded with `Message.BodyUndecoded = true` (its
  metadata intact). Emitting an empty body there would be the wrong-but-plausible
  failure the charter forbids. On the supported fingerprint + a correct decoder this
  is expected to be zero across a backup (the differential asserts it).
- **U+FFFC** (the object-replacement placeholder that marks attachment positions
  inside a body) is stripped from the extracted text, so an attachment-only message
  reads as empty text rather than a stray placeholder. The differential normalizes
  U+FFFC on both sides (iLEAPP does not strip it).

## Timestamps — NANOseconds

| Column | Epoch | **Unit** | Type |
|---|---|---|---|
| `message.date` | Cocoa 2001 | **nanoseconds** | INTEGER |
| `message.date_read` / `date_delivered` / `date_played` | Cocoa 2001 | **nanoseconds** | INTEGER |
| `message.date_edited` / `date_retracted` | Cocoa 2001 | **nanoseconds** | INTEGER |
| `chat_message_join.message_date` | Cocoa 2001 | **nanoseconds** | INTEGER (mirror of `message.date`) |

**Unique among the five domains: messages is in nanoseconds.** To Unix seconds:
`date/1e9 + 978307200`. iLEAPP guards this exactly (`if ts > 1e15: ts/=1e9`). A shared
Cocoa-date helper must be told the unit per column.

## Reactions, edits, threads, kinds

- **Tapbacks:** `associated_message_type` (with `associated_message_guid`,
  `associated_message_range_*`, `associated_message_emoji`). Codes cluster in a **2000
  range** (add) and **3000 range** (remove); conventionally 2000 love / 2001 like /
  2002 dislike / 2003 laugh / 2004 emphasize / 2005 question, with newer
  custom-emoji/sticker variants (2006/2007) and the matching 3000-range removals. The
  M3 re-introspection confirmed the ranges present ({2000–2007} add, {3000–3006}
  remove, plus `0` = ordinary and a few legacy low codes). The parser surfaces
  `AssociatedType` **raw** with range helpers (`IsTapback`/`TapbackRemoved`) — the
  per-code label is interpretation cross-referenced from iLEAPP `sms.py`, not asserted.
- **Edit / unsend:** `date_edited` (edit time), `date_retracted` (unsend time), and
  `message_summary_info` (BLOB) carrying edit history/parts. Capability-gated extras,
  **awaiting differential validation** — validate each against a backup that exercises
  an edit and an unsend.
- **Replies/threads:** `thread_originator_guid` / `thread_originator_part`,
  `reply_to_guid`.
- **Item kind:** `item_type` = 0 for ordinary messages; non-zero for system /
  group-event rows (participant add/remove, name change, …). Values {0–5} confirmed
  present (M3 re-introspection); surfaced raw (`ItemType`), the non-zero mapping is
  interpretation, not asserted. `group_action_type` (values {0,1}), `group_title`
  accompany group events.
- **Rich/app messages:** `balloon_bundle_id`, `payload_data`, `expressive_send_style_id`.
- **Direction / service:** `is_from_me` (0 received / 1 sent); `service`
  (`iMessage` / `SMS`; `RCS` supported in schema).

## Attachments

`attachment.filename` is a tilde-prefixed path, `~/Library/SMS/Attachments/…`. In
backup terms this resolves to **`MediaDomain`**, relativePath `Library/SMS/Attachments/…`
(strip the leading `~/`). Surface as a structured
`FileRef{Domain:"MediaDomain", RelativePath:"Library/SMS/Attachments/…"}`, never a
bare path. Other columns: `uti`, `mime_type`, `transfer_name`, `total_bytes`,
`is_sticker`. **Caveat:** a row can have `filename = NULL` (not-downloaded / purged /
iCloud) — the reference is absent, not the parser's fault; report it, don't fabricate
a path.

## Capability mapping

| Record field (intended) | Source | Notes |
|---|---|---|
| body text | `message.text` **else** `attributedBody` (typedstream) | decoder **required** |
| timestamp(s) | `message.date` / `date_read` / `date_delivered` | Cocoa **nanoseconds** |
| direction | `message.is_from_me` | 0 recv / 1 sent |
| sender/recipient | `message.handle_id` → `handle`; group via `chat_handle_join` | |
| conversation | `chat` via `chat_message_join` | 1:1 vs group by `style` |
| service | `message.service` | iMessage/SMS/RCS |
| attachments | `message_attachment_join` → `attachment` | `FileRef` into `MediaDomain` |
| tapbacks | `associated_message_type` + `associated_message_guid` | capability-gated |
| edits | `date_edited` + `message_summary_info` | capability-gated |
| reply/thread | `thread_originator_guid` | capability-gated |
| rich formatting / mentions | `attributedBody` runs | **deferred for v0.1** — plain text only |

**Capability shape (M3 ruling).** `Capability` stays the ruled four fields
(`{Domain, Supported, Schema, Missing}`); the scheduled "present-but-partial" field is
**not** added. Plain text from typedstream is *complete*, so rich runs (mentions,
formatting) are deferred and documented, not modeled as a capability. Per-record
app-message signals (`BalloonBundleID`, `HasPayload`) and the raw `AssociatedType` /
`ItemType` are `Message` fields, not domain capabilities.

**`Missing[]` units (schema-absence axis only).** Each optional unit whose backing
columns/tables are absent lands its name in `Missing`: `attributed_text`, `service`,
`delivery`, `tapbacks`, `tapback_emoji`, `edits`, `threads`, `app_messages`,
`group_events`, `handles`, `chats`, `attachments`. On the study backup all are present
(empty `Missing`). The iOS-18-era columns the schema also carries but the parser does
**not** surface (satellite/off-grid `is_pending_satellite_send` /
`sent_or_received_off_grid`, key-transparency `is_kt_verified`, scheduled-send) are
deliberately **not** modeled as units — a `Missing[]` name must map to a record field
the parser provides.
