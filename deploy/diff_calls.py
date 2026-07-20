#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's calls
stream with iLEAPP, record by record. Two phases:

1. BLACK-BOX: iLEAPP's Call History TSV (produced by running ileapp.py) vs the
   parser stream. iLEAPP MERGES CallHistory.storedata with the short-lived
   CallHistoryTemp.storedata buffer, while the parser reads only the canonical
   store (a documented scope decision — docs/schemas/calls.md). So this phase
   requires every PARSER record to have a matching iLEAPP record (parser ⊆
   iLEAPP) and reports any iLEAPP-only records as informational (expected: the
   CallHistoryTemp delta), rather than hard-failing on a count difference.
   Compared fields: starting timestamp, direction, call type, phone number,
   answered, duration.

2. ORACLE-LOGIC: iLEAPP's callHistory.py query semantics (MIT, see NOTICE — the
   ZCALLTYPE 0/1/8/16 and ZORIGINATED 0/1 mappings, the ZCALLRECORD columns)
   re-run here directly against a scratch COPY of the canonical store, keyed by
   ZCALLRECORD.Z_PK. This covers every parser field — including participants
   (the Z_2REMOTEPARTICIPANTHANDLES → ZHANDLE join) and the ones phase 1 cannot
   see (unique id, read, spam, service provider, country). Calls the parser
   withholds as row errors (dangling handle references) are matched against the
   reported row errors, not treated as missing.

Usage (inside the oracle container, via `make diff-study-calls`):
    python deploy/diff_calls.py <difftmp-dir> [--db <CallHistory.storedata>]

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
from collections import Counter
from datetime import datetime, timezone

MAX_REPORT = 10
COCOA_UNIX_DELTA = 978307200

# Interpretation per iLEAPP scripts/artifacts/callHistory.py (MIT, see NOTICE).
CALL_TYPE_NAME = {0: "Third-Party App", 1: "Phone Call", 8: "FaceTime Video", 16: "FaceTime Audio"}
DIRECTION_NAME = {0: "Incoming", 1: "Outgoing"}

problems = []


def report(msg):
    problems.append(msg)


def norm(s):
    return " ".join((s or "").split())


def norm_phone(s):
    digits = re.sub(r"\D", "", s or "")
    return digits[-10:] if len(digits) > 10 else digits


def norm_dt(s):
    """Reduce any datetime rendering to 'YYYY-MM-DD HH:MM:SS' (UTC assumed)."""
    m = re.search(r"(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})", s or "")
    return "%s %s" % (m.group(1), m.group(2)) if m else ""


def cocoa_to_utc(seconds):
    if seconds is None:
        return ""
    return datetime.fromtimestamp(int(seconds) + COCOA_UNIX_DELTA, tz=timezone.utc).strftime(
        "%Y-%m-%d %H:%M:%S")


def hms_to_seconds(s):
    m = re.search(r"(\d{2}):(\d{2}):(\d{2})", s or "")
    if not m:
        return 0
    return int(m.group(1)) * 3600 + int(m.group(2)) * 60 + int(m.group(3))


def load_parser(path):
    capability, calls, row_errors = None, [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "call":
                calls.append(obj["call"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, calls, row_errors


# --- Phase 1: black-box TSV comparison -------------------------------------

def find_calls_tsv(root):
    candidates = [p for p in glob.glob(os.path.join(root, "**", "*.tsv"), recursive=True)
                  if "call" in os.path.basename(p).lower()
                  and "group" not in os.path.basename(p).lower()
                  and "transaction" not in os.path.basename(p).lower()]
    candidates.sort()
    return candidates


def pick_column(header, *needles):
    for i, name in enumerate(header):
        lowered = (name or "").lower().strip("﻿ ")
        if all(n in lowered for n in needles):
            return i
    return None


def call_type_name(v):
    try:
        return CALL_TYPE_NAME.get(int(v), str(int(v)))
    except (TypeError, ValueError):
        return str(v)


def tsv_phase(difftmp, calls):
    ileapp_root = os.path.join(difftmp, "ileapp-calls")
    tsvs = find_calls_tsv(ileapp_root)
    if not tsvs:
        report("phase1: no Call History TSV under %s (input-type mismatch? see Makefile)" % ileapp_root)
        return
    tsv = tsvs[0]
    with open(tsv, encoding="utf-8", errors="replace", newline="") as f:
        rows = list(csv.reader(f, delimiter="\t"))
    if not rows:
        report("phase1: empty TSV %s" % tsv)
        return
    header, rows = rows[0], rows[1:]
    print("phase1: %s — %d records" % (os.path.basename(tsv), len(rows)))
    idx = {
        "start": pick_column(header, "starting", "timestamp"),
        "direction": pick_column(header, "direction"),
        "type": pick_column(header, "call", "type"),
        "number": pick_column(header, "phone", "number"),
        "answered": pick_column(header, "answered"),
        "duration": pick_column(header, "duration"),
    }

    def cell(row, key):
        i = idx.get(key)
        return row[i] if i is not None and i < len(row) else ""

    def key(rec):
        return (rec["start"], rec["number"], rec["direction"])

    theirs = Counter()
    theirs_detail = {}
    for row in rows:
        rec = {
            "start": norm_dt(cell(row, "start")),
            "direction": norm(cell(row, "direction")),
            "type": norm(cell(row, "type")),
            "number": norm_phone(cell(row, "number")),
            "answered": norm(cell(row, "answered")).lower(),
            "duration": hms_to_seconds(cell(row, "duration")),
        }
        theirs[key(rec)] += 1
        theirs_detail[key(rec)] = rec

    matched = 0
    for c in calls:
        rec = {
            "start": norm_dt(c.get("time", "")),
            "direction": DIRECTION_NAME.get(c.get("direction"), str(c.get("direction"))),
            "type": call_type_name(c.get("call_type")),
            "number": norm_phone(c.get("address", "")),
            "answered": "yes" if c.get("answered") else "no",
            # iLEAPP renders duration via strftime('%H:%M:%S', …), which
            # TRUNCATES to whole seconds; floor here to compare like-for-like
            # (the parser keeps the exact fractional duration — phase 2 checks
            # that against the raw ZDURATION float).
            "duration": int((c.get("duration") or 0) / 1e9),
        }
        k = key(rec)
        if theirs[k] <= 0:
            report("phase1: parser call id=%s (%s %s) has no matching iLEAPP record"
                   % (c.get("id"), rec["start"], rec["number"]))
            continue
        theirs[k] -= 1
        matched += 1
        b = theirs_detail[k]
        for field in ("type", "answered"):
            if rec[field] != b[field]:
                report("phase1 call id=%s: %s parser=%r ileapp=%r"
                       % (c.get("id"), field, rec[field], b[field]))
        # iLEAPP's HH:MM:SS duration comes from strftime('%H:%M:%S', …), which is
        # second-granular AND subject to Julian-day float rounding (±1s); phase 2
        # checks duration exactly against the raw ZDURATION. Tolerate ±1s here so
        # that rendering artifact is not mistaken for a parser defect.
        if abs(rec["duration"] - b["duration"]) > 1:
            report("phase1 call id=%s: duration parser=%r ileapp=%r"
                   % (c.get("id"), rec["duration"], b["duration"]))

    leftover = sum(v for v in theirs.values() if v > 0)
    print("phase1: %d/%d parser calls matched an iLEAPP record; %d iLEAPP-only "
          "record(s) (expected: CallHistoryTemp, out of parser scope)"
          % (matched, len(calls), leftover))


# --- Phase 2: oracle-logic SQL comparison ----------------------------------

def sql_phase(db_path, calls, row_errors):
    stage = tempfile.mkdtemp(prefix="diff-calls-")
    for path in glob.glob(db_path + "*"):
        shutil.copy(path, stage)
    conn = sqlite3.connect(os.path.join(stage, os.path.basename(db_path)))
    q = conn.execute

    handles = {r[0]: {"id": r[0], "value": r[1] or "", "normalized_value": r[2] or "", "type": r[3] or 0}
               for r in q("SELECT Z_PK, ZVALUE, ZNORMALIZEDVALUE, ZTYPE FROM ZHANDLE")}
    participants = {}
    for call_pk, handle_pk in q(
            "SELECT Z_2REMOTEPARTICIPANTCALLS, Z_4REMOTEPARTICIPANTHANDLES"
            " FROM Z_2REMOTEPARTICIPANTHANDLES ORDER BY Z_2REMOTEPARTICIPANTCALLS, Z_4REMOTEPARTICIPANTHANDLES"):
        participants.setdefault(call_pk, []).append(handle_pk)

    by_id = {c["id"]: c for c in calls}
    errored = set()
    for e in row_errors:
        m = re.search(r"rowid (\d+)", e)
        if m:
            errored.add(int(m.group(1)))

    sql_calls = list(q(
        "SELECT Z_PK, ZDATE, ZDURATION, ZORIGINATED, ZANSWERED, ZCALLTYPE, ZADDRESS,"
        " ZNAME, ZSERVICE_PROVIDER, ZISO_COUNTRY_CODE, ZUNIQUE_ID, ZREAD,"
        " ZJUNKCONFIDENCE, ZJUNKIDENTIFICATIONCATEGORY"
        " FROM ZCALLRECORD ORDER BY Z_PK"))

    checked = 0
    for row in sql_calls:
        pk = row[0]
        want_handles = participants.get(pk, [])
        # A call whose participant list references a missing ZHANDLE is one the
        # parser withholds (row-scoped); that must show up as a reported error.
        if any(h not in handles for h in want_handles):
            if pk not in errored:
                report("phase2 Z_PK %d: dangling handle not reported as a row error" % pk)
            continue
        c = by_id.get(pk)
        if c is None:
            if pk not in errored:
                report("phase2 Z_PK %d: missing from parser stream" % pk)
            continue
        checked += 1

        if norm_dt(c.get("time", "")) != cocoa_to_utc(row[1]):
            report("phase2 Z_PK %d: time parser=%r sql=%r" % (pk, norm_dt(c.get("time", "")), cocoa_to_utc(row[1])))
        if round((c.get("duration") or 0) / 1e9, 3) != round(row[2] or 0.0, 3):
            report("phase2 Z_PK %d: duration parser=%r sql=%r" % (pk, (c.get("duration") or 0) / 1e9, row[2]))
        for name, i, default in (("direction", 3, 0), ("call_type", 5, 0)):
            if (c.get(name) or 0) != (row[i] or default):
                report("phase2 Z_PK %d: %s parser=%r sql=%r" % (pk, name, c.get(name), row[i]))
        if bool(c.get("answered")) != bool(row[4]):
            report("phase2 Z_PK %d: answered parser=%r sql=%r" % (pk, c.get("answered"), row[4]))
        if bool(c.get("read")) != bool(row[11]):
            report("phase2 Z_PK %d: read parser=%r sql=%r" % (pk, c.get("read"), row[11]))
        for name, i in (("address", 6), ("name", 7), ("service_provider", 8),
                        ("iso_country_code", 9), ("unique_id", 10), ("junk_category", 13)):
            if (c.get(name) or "") != (row[i] or ""):
                report("phase2 Z_PK %d: %s parser=%r sql=%r" % (pk, name, c.get(name), row[i]))
        if (c.get("junk_confidence") or 0) != (row[12] or 0):
            report("phase2 Z_PK %d: junk_confidence parser=%r sql=%r" % (pk, c.get("junk_confidence"), row[12]))

        got_parts = [(p.get("id"), p.get("value", ""), p.get("normalized_value", ""), p.get("type", 0))
                     for p in c.get("participants", [])]
        want_parts = [(handles[h]["id"], handles[h]["value"], handles[h]["normalized_value"], handles[h]["type"])
                      for h in want_handles]
        if got_parts != want_parts:
            report("phase2 Z_PK %d: participants parser=%r sql=%r" % (pk, got_parts, want_parts))

    print("phase2: %d records cross-checked by Z_PK on time/duration/direction/answered/"
          "call_type/address/name/provider/country/unique_id/read/spam/participants" % checked)


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    difftmp = args[0]
    db_path = None
    if "--db" in args:
        db_path = args[args.index("--db") + 1]

    parser_path = os.path.join(difftmp, "parser-calls.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s — run `make dump-study-calls` first" % parser_path)
        return 2
    capability, calls, row_errors = load_parser(parser_path)
    print("parser: capability=%s, %d calls, %d row errors"
          % (json.dumps(capability), len(calls), len(row_errors)))

    tsv_phase(difftmp, calls)
    if db_path:
        sql_phase(db_path, calls, row_errors)
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
