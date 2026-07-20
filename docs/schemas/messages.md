# messages — `sms.db`

- **Backup location:** `HomeDomain` / `Library/SMS/sms.db`. Attachments live under
  `MediaDomain` (see *Attachments* below).
- **Storage idiom:** plain app SQLite **+ blob-encoded text** (`attributedBody` is
  Apple **typedstream** — the M3 hard part).
- **Fingerprint:** `messages.1` — status **observed** (iOS 18.x baseline; schema
  carries RCS, satellite-send, key-transparency, custom-emoji-tapback columns).
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
union of `chat_handle_join`. `chat.style` distinguishes **1:1 vs group**
(conventionally 45 = direct, 43 = group — codes to validate).
`message.cache_roomnames` is a denormalized group cache maintained by triggers.

## The text problem (`attributedBody` / typedstream)

`message` has both `text TEXT` and `attributedBody BLOB`. On modern iOS the `text`
column is **frequently NULL, with the content carried only in `attributedBody`** — a
well-known modern-Messages behavior — so a typedstream decoder is **mandatory**, not
optional; skipping it silently drops message bodies (exactly the wrong-but-plausible
failure the charter forbids).

- `attributedBody` is Apple **typedstream** — it opens with the magic prefix
  `04 0B "streamtyped" …`.
- It serializes an `NSAttributedString`: the plain string **plus** runs (mentions,
  links, formatting, message-effect ranges). Plain text is the minimum; richer
  extraction is capability-gated.
- Parser rule: prefer `text` when non-empty; else decode `attributedBody`. Confirmed
  against iLEAPP `sms.py` (`if not message_text and attributedBody: parse_typedstream(...)`).
- **M3 plan:** implement typedstream from public format docs; differentially validate
  against imessage-exporter as a **black box** (GPL — never read its source).

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
  custom-emoji/sticker variants and the matching 3000-range removals —
  **interpretation, to validate** (other codes exist and need mapping). `0` = ordinary
  message.
- **Edit / unsend:** `date_edited` (edit time), `date_retracted` (unsend time), and
  `message_summary_info` (BLOB) carrying edit history/parts. Capability-gated extras,
  **awaiting differential validation** — validate each against a backup that exercises
  an edit and an unsend.
- **Replies/threads:** `thread_originator_guid` / `thread_originator_part`,
  `reply_to_guid`.
- **Item kind:** `item_type` = 0 for ordinary messages; non-zero for system /
  group-event rows (participant add/remove, name change, …) — mapping to validate.
  `group_action_type`, `group_title` accompany group events.
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
| rich formatting / mentions | `attributedBody` runs | beyond plain text → capability-gated |

**`Missing[]` candidates:** satellite/off-grid (`is_pending_satellite_send`,
`sent_or_received_off_grid`), key-transparency (`is_kt_verified`), scheduled-send, and
custom-emoji-tapback columns are iOS-18-era — absent on older fingerprints.
