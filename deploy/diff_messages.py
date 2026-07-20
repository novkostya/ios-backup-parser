#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's messages
stream with iLEAPP, record by record. Two phases:

1. BLACK-BOX: iLEAPP's SMS export (produced by running ileapp.py) vs the parser
   stream. iLEAPP's query LEFT-JOINs message → attachment → chat, so it emits
   one ROW PER (message × attachment × chat) combination; we regroup by its
   "Message Row ID" column back to one record per message before comparing.
   iLEAPP decodes attributedBody with the independent python-typedstream library
   (`import typedstream`), so comparing its "Message" text against the parser is
   a genuine cross-check of our from-scratch Go typedstream decoder. Compared
   per message: text, timestamp, direction, service, and the set of chat ids.

2. ORACLE-LOGIC: iLEAPP's query semantics (MIT, see NOTICE — text-else-
   attributedBody, the ts>1e15 nanosecond guard, the join topology) re-run here
   directly against a scratch COPY of sms.db, keyed by message.ROWID. This is
   the BOTH-DIRECTIONS gate: the set of message ROWIDs in the database must equal
   the parser's yielded ids PLUS its row-errored ids EXACTLY — a parser that
   invents or silently drops a message fails here. Per surviving message it
   cross-checks text (via python-typedstream), timestamp (nanoseconds), service,
   direction, sender handle, chat ids and attachments (with the MediaDomain
   FileRef). Messages the parser withholds as row errors (dangling handle
   references) are matched against the reported row errors, not treated as
   missing.

Named asymmetries (Operator amendment — never accepted silently):
  * iLEAPP row expansion vs the parser's one-record-per-message — regrouped by
    Message Row ID, so counts are compared per message, not per join row.
  * U+FFFC (the object-replacement placeholder marking attachment positions) —
    the parser strips it, iLEAPP does not; text is compared with U+FFFC removed
    on BOTH sides.

Usage (inside the oracle container, via `make diff-study-messages`):
    python deploy/diff_messages.py <difftmp-dir> [--db <sms.db>]

Operator-local only: everything read or printed stays on the box (.difftmp/ is
gitignored). This file is a generic harness and carries no data. Exit 0 = all
compared fields agree; 1 = differences; 2 = setup problem.
"""

import csv
import glob
import json
import os
import re
import shutil
import sqlite3
import sys
import tempfile
from collections import defaultdict
from datetime import datetime, timezone

MAX_REPORT = 15
COCOA_UNIX_DELTA = 978307200
OBJ_REPLACEMENT = "￼"

problems = []


def report(msg):
    problems.append(msg)


def clean_text(s):
    """Normalize a message body for comparison: drop U+FFFC placeholders (the
    parser strips them, iLEAPP does not) and coerce None to ''."""
    return (s or "").replace(OBJ_REPLACEMENT, "")


def norm_dt(s):
    m = re.search(r"(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})", s or "")
    return "%s %s" % (m.group(1), m.group(2)) if m else ""


def cocoa_ns_to_utc(ns):
    if not ns:
        return ""
    if ns > 1_000_000_000_000_000:  # nanoseconds (iLEAPP's own guard)
        ns = ns / 1_000_000_000
    return datetime.fromtimestamp(int(ns) + COCOA_UNIX_DELTA, tz=timezone.utc).strftime("%Y-%m-%d %H:%M:%S")


def load_parser(path):
    capability, messages, chats, row_errors = None, [], [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "message":
                messages.append(obj["message"])
            elif kind == "chat":
                chats.append(obj["chat"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, messages, chats, row_errors


# --- Phase 1: black-box SMS export comparison ------------------------------

def find_sms_tsv(root):
    # iLEAPP emits several SMS-related TSVs (the main "SMS.tsv" plus auxiliaries
    # like "SMS - Missing ROWIDs.tsv"/deleted-message exports with different
    # columns). Keep only the message exports and prefer the main "SMS.tsv".
    candidates = [p for p in glob.glob(os.path.join(root, "**", "*.tsv"), recursive=True)
                  if ("sms" in os.path.basename(p).lower() or "message" in os.path.basename(p).lower())
                  and "missing" not in os.path.basename(p).lower()
                  and "deleted" not in os.path.basename(p).lower()]
    candidates.sort(key=lambda p: (os.path.basename(p).lower() != "sms.tsv", os.path.basename(p).lower()))
    return candidates


def pick_column(header, *needles):
    for i, name in enumerate(header):
        lowered = (name or "").lower().strip("﻿ ")
        if all(n in lowered for n in needles):
            return i
    return None


def tsv_phase(difftmp, messages):
    root = os.path.join(difftmp, "ileapp-messages")
    tsvs = find_sms_tsv(root)
    if not tsvs:
        report("phase1: no SMS TSV under %s (input-type mismatch? see Makefile)" % root)
        return
    tsv = tsvs[0]
    with open(tsv, encoding="utf-8", errors="replace", newline="") as f:
        rows = list(csv.reader(f, delimiter="\t"))
    if not rows:
        report("phase1: empty TSV %s" % tsv)
        return
    header, rows = rows[0], rows[1:]
    idx = {
        "rowid": pick_column(header, "message", "row", "id"),
        "time": pick_column(header, "message", "timestamp"),
        "text": None,
        "direction": pick_column(header, "direction"),
        "service": pick_column(header, "service"),
        "chatid": None,
    }
    # Resolve the ambiguous columns by EXACT header name: "Message" is the body
    # (not "Message Row ID"), and "Chat ID" is the numeric chat rowid (NOT the
    # "Chat Contact ID" phone/identifier column that also contains "chat"+"id").
    for i, name in enumerate(header):
        low = (name or "").lower().strip("﻿ ")
        if low == "message":
            idx["text"] = i
        elif low == "chat id":
            idx["chatid"] = i
    if idx["rowid"] is None or idx["text"] is None:
        report("phase1: TSV missing Message Row ID or Message column (headers: %s)" % header)
        return

    def cell(row, key):
        i = idx.get(key)
        return row[i] if i is not None and i < len(row) else ""

    # Regroup iLEAPP's join-expanded rows back to one record per message.
    by_rowid = {}
    chatids = defaultdict(set)
    for row in rows:
        rid = cell(row, "rowid").strip()
        if not rid:
            continue
        by_rowid[rid] = {
            "text": clean_text(cell(row, "text")),
            "time": norm_dt(cell(row, "time")),
            "direction": cell(row, "direction").strip().lower(),
            "service": cell(row, "service").strip(),
        }
        cid = cell(row, "chatid").strip()
        if cid:
            chatids[rid].add(cid)
    print("phase1: %s — %d message rows regrouped to %d messages"
          % (os.path.basename(tsv), len(rows), len(by_rowid)))

    matched = 0
    for m in messages:
        rid = str(m.get("id"))
        b = by_rowid.get(rid)
        if b is None:
            report("phase1: parser message id=%s not present in iLEAPP export" % rid)
            continue
        matched += 1
        if clean_text(m.get("text", "")) != b["text"]:
            report("phase1 id=%s: text parser=%r ileapp=%r"
                   % (rid, clean_text(m.get("text", "")), b["text"]))
        direction = "outgoing" if m.get("is_from_me") else "incoming"
        if b["direction"] and direction != b["direction"]:
            report("phase1 id=%s: direction parser=%r ileapp=%r" % (rid, direction, b["direction"]))
        if b["service"] and (m.get("service") or "") != b["service"]:
            report("phase1 id=%s: service parser=%r ileapp=%r" % (rid, m.get("service"), b["service"]))
        if b["time"] and norm_dt(m.get("time", "")) != b["time"]:
            report("phase1 id=%s: time parser=%r ileapp=%r" % (rid, norm_dt(m.get("time", "")), b["time"]))
        if chatids.get(rid):
            got = set(str(c) for c in (m.get("chat_ids") or []))
            if got != chatids[rid]:
                report("phase1 id=%s: chat ids parser=%s ileapp=%s" % (rid, sorted(got), sorted(chatids[rid])))
    print("phase1: %d/%d parser messages matched an iLEAPP record" % (matched, len(messages)))


# --- Phase 2: oracle-logic SQL comparison ----------------------------------

def sql_phase(db_path, messages, chats, row_errors):
    try:
        import typedstream  # python-typedstream, bundled with iLEAPP (independent decoder)
    except ImportError:
        typedstream = None
        report("phase2: python-typedstream unavailable — text cross-check via SQL skipped")

    def decode(buf):
        if typedstream is None or not buf:
            return None
        try:
            root = typedstream.unarchive_from_data(buf)
        except (ValueError, TypeError, OSError):
            return None
        if hasattr(root, "contents") and root.contents:
            first = root.contents[0]
            if hasattr(first, "value") and hasattr(first.value, "value"):
                return first.value.value
        return None

    stage = tempfile.mkdtemp(prefix="diff-messages-")
    for path in glob.glob(db_path + "*"):
        shutil.copy(path, stage)
    conn = sqlite3.connect(os.path.join(stage, os.path.basename(db_path)))
    q = conn.execute

    handles = {r[0]: {"id": r[1] or "", "service": r[2] or "", "country": r[3] or ""}
               for r in q("SELECT ROWID, id, service, country FROM handle")}
    chat_ids = defaultdict(list)
    for mid, cid in q("SELECT message_id, chat_id FROM chat_message_join ORDER BY message_id, chat_id"):
        chat_ids[mid].append(cid)
    atts = defaultdict(list)
    for mid, aid, fn, uti, mime, tn, tb, sticker in q(
            "SELECT j.message_id, a.ROWID, a.filename, a.uti, a.mime_type, a.transfer_name,"
            " a.total_bytes, a.is_sticker FROM message_attachment_join j"
            " JOIN attachment a ON a.ROWID = j.attachment_id ORDER BY j.message_id, a.ROWID"):
        atts[mid].append({"id": aid, "filename": fn, "uti": uti or "", "mime": mime or "",
                          "transfer_name": tn or "", "total_bytes": tb or 0, "sticker": bool(sticker)})

    by_id = {m["id"]: m for m in messages}
    errored = set()
    for e in row_errors:
        m = re.search(r"rowid (\d+)", e)
        if m:
            errored.add(int(m.group(1)))

    db_rowids = [r[0] for r in q("SELECT ROWID FROM message ORDER BY ROWID")]

    # BOTH-DIRECTIONS exact set check: every db message is either yielded or a
    # reported row error; the parser invents nothing and drops nothing silently.
    for rid in db_rowids:
        if rid not in by_id and rid not in errored:
            report("phase2 rowid %d: present in db, missing from parser stream and row errors" % rid)
    for rid in by_id:
        if rid not in set(db_rowids):
            report("phase2 rowid %d: parser yielded a message not in the database" % rid)

    checked = 0
    for row in q(
            "SELECT ROWID, date, text, attributedBody, service, is_from_me, handle_id,"
            " associated_message_type, associated_message_guid, cache_has_attachments"
            " FROM message ORDER BY ROWID"):
        rid = row[0]
        m = by_id.get(rid)
        if m is None:
            continue  # withheld (checked above)
        checked += 1

        text = row[2]
        if not text:
            text = decode(row[3])
        if clean_text(m.get("text", "")) != clean_text(text):
            report("phase2 rowid %d: text parser=%r sql=%r" % (rid, clean_text(m.get("text", "")), clean_text(text)))
        if norm_dt(m.get("time", "")) != cocoa_ns_to_utc(row[1]):
            report("phase2 rowid %d: time parser=%r sql=%r" % (rid, norm_dt(m.get("time", "")), cocoa_ns_to_utc(row[1])))
        if (m.get("service") or "") != (row[4] or ""):
            report("phase2 rowid %d: service parser=%r sql=%r" % (rid, m.get("service"), row[4]))
        if bool(m.get("is_from_me")) != bool(row[5]):
            report("phase2 rowid %d: is_from_me parser=%r sql=%r" % (rid, m.get("is_from_me"), row[5]))
        if (m.get("associated_type") or 0) != (row[7] or 0):
            report("phase2 rowid %d: associated_type parser=%r sql=%r" % (rid, m.get("associated_type"), row[7]))

        # Sender handle.
        hid = row[6] or 0
        got_handle = m.get("handle")
        if hid and hid in handles:
            want = handles[hid]
            if not got_handle or got_handle.get("identifier") != want["id"] \
                    or got_handle.get("service", "") != want["service"]:
                report("phase2 rowid %d: handle parser=%r sql=%r" % (rid, got_handle, want))
        elif got_handle:
            report("phase2 rowid %d: parser has handle %r but db handle_id=%s" % (rid, got_handle, hid))

        # Chat ids.
        got_chats = sorted(m.get("chat_ids") or [])
        if got_chats != sorted(chat_ids.get(rid, [])):
            report("phase2 rowid %d: chat ids parser=%s sql=%s" % (rid, got_chats, sorted(chat_ids.get(rid, []))))

        # Attachments (order by attachment ROWID on both sides).
        want_atts = atts.get(rid, [])
        got_atts = m.get("attachments") or []
        if len(got_atts) != len(want_atts):
            report("phase2 rowid %d: attachment count parser=%d sql=%d" % (rid, len(got_atts), len(want_atts)))
        else:
            for g, w in zip(got_atts, want_atts):
                if g.get("id") != w["id"]:
                    report("phase2 rowid %d: attachment id parser=%s sql=%s" % (rid, g.get("id"), w["id"]))
                want_ref = None
                if w["filename"]:
                    want_ref = "MediaDomain/" + w["filename"].replace("~/", "", 1)
                got_ref = None
                if g.get("file"):
                    got_ref = g["file"]["domain"] + "/" + g["file"]["relative_path"]
                if got_ref != want_ref:
                    report("phase2 rowid %d: attachment file parser=%r sql=%r" % (rid, got_ref, want_ref))

    print("phase2: %d messages cross-checked by ROWID on text/time/service/direction/"
          "associated/handle/chats/attachments (both-directions set check on %d db rows)"
          % (checked, len(db_rowids)))

    # Chats (participants) — spot-check the Chats() stream against chat_handle_join.
    chat_handles = defaultdict(list)
    for cid, hid in q("SELECT chat_id, handle_id FROM chat_handle_join ORDER BY chat_id, handle_id"):
        chat_handles[cid].append(hid)
    for c in chats:
        want = sorted(chat_handles.get(c["id"], []))
        got = sorted(h["id"] for h in (c.get("participants") or []))
        if got != want:
            report("phase2 chat %s: participants parser=%s sql=%s" % (c["id"], got, want))
    print("phase2: %d chats cross-checked on participants" % len(chats))


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    difftmp = args[0]
    db_path = None
    if "--db" in args:
        db_path = args[args.index("--db") + 1]

    parser_path = os.path.join(difftmp, "parser-messages.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s — run `make dump-study-messages` first" % parser_path)
        return 2
    capability, messages, chats, row_errors = load_parser(parser_path)
    print("parser: capability=%s, %d messages, %d chats, %d row errors"
          % (json.dumps(capability), len(messages), len(chats), len(row_errors)))

    tsv_phase(difftmp, messages)
    if db_path:
        sql_phase(db_path, messages, chats, row_errors)
    else:
        print("phase2 skipped (no --db)")

    if problems:
        print("DIFFERENTIAL: %d problem(s)" % len(problems))
        for p in problems[:MAX_REPORT]:
            print("  -", p)
        if len(problems) > MAX_REPORT:
            print("  ... and %d more" % (len(problems) - MAX_REPORT))
        return 1
    print("DIFFERENTIAL: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
