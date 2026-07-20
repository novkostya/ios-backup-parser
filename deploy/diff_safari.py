#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's safari stream
with iLEAPP, record by record. The safari domain spans TWO databases (Bookmarks.db,
History.db) and three streams (Bookmarks, ReadingList, History); each is checked in
two phases:

1. BLACK-BOX: iLEAPP's "Safari Browser - Bookmarks" and "Safari Browser - History"
   TSVs (produced by running ileapp.py's safariBookmarks.py / safariHistory.py) vs the
   parser stream.
   - Bookmarks: iLEAPP dumps `SELECT title, url, hidden FROM bookmarks` for EVERY row
     (bookmarks, folders, special folders, and reading-list items alike). The parser
     splits those rows into Bookmarks() (bookmarks.read IS NULL) and ReadingList()
     (read IS NOT NULL); their union is compared as a (title, url) multiset against
     iLEAPP's. (hidden and every other field are verified exactly in phase 2.)
   - History: iLEAPP renders one row per visit with a resolved timestamp, title, url,
     visit count and origin; compared keyed by Visit ID.

2. ORACLE-LOGIC: iLEAPP's query semantics (MIT, see NOTICE — its bookmarks column read
   and its history_visits ⟕ history_items join, origin mapping, and redirect id→url
   resolution) re-run here directly against scratch COPIES of the two stores, keyed by
   bookmarks.id and history_visits.id. This covers EVERY parser field, including the
   ones the exports omit (parent/type/special_id/order_index/num_children/deleted, the
   reading-list read flag, redirect source/destination), and asserts the exact
   both-directions id sets (db rows == yielded ids + row-errored ids: no invented, no
   silently-dropped record).

THE TWO-EPOCH TRAP is asserted here: bookmarks.last_modified is decoded from UNIX
seconds, history_visits.visit_time from COCOA seconds (docs/schemas/safari.md).

Usage (inside the oracle container, via `make diff-study-safari`):
    python deploy/diff_safari.py <difftmp-dir> [--db <Bookmarks.db>] [--history-db <History.db>]

Operator-local only: everything read or printed stays on the box (.difftmp/ is
gitignored). This file is a generic harness and carries no data. Exit 0 = all compared
fields agree; 1 = differences; 2 = setup problem.
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

MAX_REPORT = 15
COCOA_UNIX_DELTA = 978307200

# history_visits.origin per iLEAPP safariHistory.py (MIT, see NOTICE).
ORIGIN = {0: "Local Device", 1: "iCloud Synced Device"}

problems = []


def report(msg):
    problems.append(msg)


def norm(s):
    return " ".join((str(s) if s is not None else "").split())


def norm_dt(s):
    """Reduce any datetime rendering to 'YYYY-MM-DD HH:MM:SS' (UTC assumed)."""
    m = re.search(r"(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})", str(s or ""))
    return "%s %s" % (m.group(1), m.group(2)) if m else ""


def unix_to_utc(seconds):
    """UNIX seconds -> 'YYYY-MM-DD HH:MM:SS' UTC (bookmarks.last_modified)."""
    if seconds is None or seconds == "":
        return ""
    try:
        return datetime.fromtimestamp(int(float(seconds)), tz=timezone.utc).strftime("%Y-%m-%d %H:%M:%S")
    except (OSError, ValueError, OverflowError):
        return ""


def cocoa_to_utc(seconds):
    """COCOA seconds -> 'YYYY-MM-DD HH:MM:SS' UTC (history_visits.visit_time)."""
    if seconds is None or seconds == "":
        return ""
    try:
        return datetime.fromtimestamp(int(float(seconds)) + COCOA_UNIX_DELTA, tz=timezone.utc).strftime(
            "%Y-%m-%d %H:%M:%S")
    except (OSError, ValueError, OverflowError):
        return ""


def dt_within(a, b, tol=1):
    """True if two 'YYYY-MM-DD HH:MM:SS' strings are within tol seconds, or either is
    empty. iLEAPP renders visit_time via SQLite datetime(...,'unixepoch'), which ROUNDS
    the fractional second to the nearest whole second, while the parser (and phase 2)
    TRUNCATE it — so a visit whose fractional part is >= 0.5 legitimately renders one
    second apart. The parser keeps the precise sub-second value; phase 2 (truncation on
    both sides, keyed by id) is the exact gate. Same Julian-day rounding tolerance the
    calls domain applies (see deploy/diff_calls.py)."""
    if not a or not b:
        return True
    try:
        da = datetime.strptime(a, "%Y-%m-%d %H:%M:%S")
        db = datetime.strptime(b, "%Y-%m-%d %H:%M:%S")
    except ValueError:
        return a == b
    return abs((da - db).total_seconds()) <= tol


def load_parser(path):
    capability = None
    bookmarks, reading_list, visits, row_errors = [], [], [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "bookmark":
                bookmarks.append(obj["bookmark"])
            elif kind == "reading_list":
                reading_list.append(obj["reading_list"])
            elif kind == "visit":
                visits.append(obj["visit"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, bookmarks, reading_list, visits, row_errors


def errored_ids(row_errors, table):
    """The rowids row-errored for a given table (parsed from 'safari: <table> rowid N: ...')."""
    ids = set()
    for e in row_errors:
        m = re.search(r"\b%s rowid (\d+)" % re.escape(table), e)
        if m:
            ids.add(int(m.group(1)))
    return ids


def find_tsv(root, *needles):
    cands = [p for p in glob.glob(os.path.join(root, "**", "*.tsv"), recursive=True)
             if all(n in os.path.basename(p).lower() for n in needles)]
    cands.sort()
    return cands


def pick_column(header, *needles):
    for i, name in enumerate(header):
        lowered = (name or "").lower().strip("﻿ ")
        if all(n in lowered for n in needles):
            return i
    return None


def read_tsv(path):
    with open(path, encoding="utf-8", errors="replace", newline="") as f:
        rows = list(csv.reader(f, delimiter="\t"))
    return (rows[0], rows[1:]) if rows else ([], [])


# --- Phase 1: black-box TSV comparison -------------------------------------

def tsv_bookmarks_phase(difftmp, bookmarks, reading_list):
    root = os.path.join(difftmp, "ileapp-safari")
    tsvs = find_tsv(root, "bookmark")
    if not tsvs:
        report("phase1(bookmarks): no Bookmarks TSV under %s (input-type mismatch? see Makefile)" % root)
        return
    header, rows = read_tsv(tsvs[0])
    print("phase1(bookmarks): %s — %d records" % (os.path.basename(tsvs[0]), len(rows)))
    ti = pick_column(header, "title")
    ui = pick_column(header, "url")

    def cell(row, i):
        return row[i] if i is not None and i < len(row) else ""

    theirs = Counter((norm(cell(r, ti)), norm(cell(r, ui))) for r in rows)
    mine = Counter()
    for b in bookmarks:
        mine[(norm(b.get("title", "")), norm(b.get("url", "")))] += 1
    for it in reading_list:
        mine[(norm(it.get("title", "")), norm(it.get("url", "")))] += 1

    matched = sum((theirs & mine).values())
    their_only = sum((theirs - mine).values())
    my_only = sum((mine - theirs).values())
    if their_only:
        report("phase1(bookmarks): %d iLEAPP-only (title,url) with no parser counterpart" % their_only)
    if my_only:
        report("phase1(bookmarks): %d parser-only (title,url) with no iLEAPP counterpart" % my_only)
    print("phase1(bookmarks): %d/%d (title,url) matched iLEAPP" % (matched, sum(mine.values())))


def tsv_history_phase(difftmp, visits):
    root = os.path.join(difftmp, "ileapp-safari")
    tsvs = find_tsv(root, "history")
    if not tsvs:
        report("phase1(history): no History TSV under %s (input-type mismatch? see Makefile)" % root)
        return
    header, rows = read_tsv(tsvs[0])
    print("phase1(history): %s — %d records" % (os.path.basename(tsvs[0]), len(rows)))
    idx = {
        "time": pick_column(header, "visit", "timestamp"),
        "title": pick_column(header, "title"),
        "url": pick_column(header, "url"),
        "count": pick_column(header, "visit", "count"),
        "id": pick_column(header, "visit", "id"),
        "origin": pick_column(header, "origin"),
    }

    def cell(row, key):
        i = idx.get(key)
        return row[i] if i is not None and i < len(row) else ""

    by_id = {}
    for r in rows:
        try:
            by_id[int(cell(r, "id"))] = r
        except (ValueError, TypeError):
            pass

    matched = 0
    for v in visits:
        r = by_id.pop(v.get("id"), None)
        if r is None:
            report("phase1(history): parser visit id=%s has no iLEAPP row" % v.get("id"))
            continue
        matched += 1
        # Timestamps: ±1s tolerance for SQLite's round-vs-truncate (see dt_within).
        mt, tt = norm_dt(v.get("time", "")), norm_dt(cell(r, "time"))
        if not dt_within(mt, tt):
            report("phase1(history) id=%s: time parser=%r ileapp=%r" % (v.get("id"), mt, tt))
        for field, mine, theirs in (
            ("title", norm(v.get("title", "")), norm(cell(r, "title"))),
            ("url", norm(v.get("url", "")), norm(cell(r, "url"))),
            ("visit_count", norm(v.get("visit_count", 0)), norm(cell(r, "count"))),
            ("origin", norm(ORIGIN.get(v.get("origin", 0), "")), norm(cell(r, "origin"))),
        ):
            if mine != theirs:
                report("phase1(history) id=%s: %s parser=%r ileapp=%r" % (v.get("id"), field, mine, theirs))
    if by_id:
        report("phase1(history): %d iLEAPP-only visit id(s) with no parser record" % len(by_id))
    print("phase1(history): %d/%d visits matched iLEAPP by Visit ID" % (matched, len(visits)))


# --- Phase 2: oracle-logic SQL comparison ----------------------------------

def scratch_conn(db_path, prefix):
    stage = tempfile.mkdtemp(prefix=prefix)
    for path in glob.glob(db_path + "*"):
        shutil.copy(path, stage)
    return sqlite3.connect(os.path.join(stage, os.path.basename(db_path)))


def sql_bookmarks_phase(db_path, bookmarks, reading_list, row_errors):
    conn = scratch_conn(db_path, "diff-safari-bm-")
    rows = list(conn.execute(
        "SELECT id, parent, type, title, url, special_id, order_index, hidden, "
        "num_children, last_modified, external_uuid, deleted, read FROM bookmarks ORDER BY id"))

    sql_ids = {r[0] for r in rows}
    parser_ids = {b["id"] for b in bookmarks} | {it["id"] for it in reading_list}
    errored = errored_ids(row_errors, "bookmarks")
    missing = sql_ids - parser_ids - errored
    invented = parser_ids - sql_ids
    if missing:
        report("phase2(bookmarks): %d ids neither yielded nor row-errored: %s"
               % (len(missing), sorted(missing)[:10]))
    if invented:
        report("phase2(bookmarks): %d yielded ids not in the db: %s" % (len(invented), sorted(invented)[:10]))

    by_bm = {b["id"]: b for b in bookmarks}
    by_rl = {it["id"]: it for it in reading_list}
    checked = 0
    for r in rows:
        (bid, parent, typ, title, url, special_id, order_index, hidden,
         num_children, last_modified, uuid, deleted, read) = r
        is_rl = read is not None
        if is_rl:
            it = by_rl.get(bid)
            if it is None:
                if bid not in errored:
                    report("phase2(bookmarks): reading-list id=%s not in ReadingList()" % bid)
                continue
            if bid in by_bm:
                report("phase2(bookmarks): reading-list id=%s ALSO in Bookmarks()" % bid)
            checked += 1

            def cmp_rl(field, mine, theirs):
                if mine != theirs:
                    report("phase2(reading_list) id=%s: %s parser=%r sql=%r" % (bid, field, mine, theirs))

            cmp_rl("parent", it.get("parent", 0), parent or 0)
            cmp_rl("title", it.get("title", ""), title or "")
            cmp_rl("url", it.get("url", ""), url or "")
            cmp_rl("read", bool(it.get("read")), bool(read))
            cmp_rl("last_modified", norm_dt(it.get("last_modified", "")), norm_dt(unix_to_utc(last_modified)))
        else:
            b = by_bm.get(bid)
            if b is None:
                if bid not in errored:
                    report("phase2(bookmarks): id=%s not in Bookmarks()" % bid)
                continue
            if bid in by_rl:
                report("phase2(bookmarks): id=%s ALSO in ReadingList()" % bid)
            checked += 1

            def cmp_b(field, mine, theirs):
                if mine != theirs:
                    report("phase2(bookmarks) id=%s: %s parser=%r sql=%r" % (bid, field, mine, theirs))

            cmp_b("parent", b.get("parent", 0), parent or 0)
            cmp_b("type", b.get("type", 0), typ or 0)
            cmp_b("title", b.get("title", ""), title or "")
            cmp_b("url", b.get("url", ""), url or "")
            cmp_b("special_id", b.get("special_id", 0), special_id or 0)
            cmp_b("order_index", b.get("order_index", 0), order_index or 0)
            cmp_b("num_children", b.get("num_children", 0), num_children or 0)
            cmp_b("hidden", bool(b.get("hidden")), bool(hidden))
            cmp_b("deleted", bool(b.get("deleted")), bool(deleted))
            cmp_b("uuid", b.get("uuid", ""), uuid or "")
            cmp_b("last_modified", norm_dt(b.get("last_modified", "")), norm_dt(unix_to_utc(last_modified)))
    print("phase2(bookmarks): %d rows cross-checked by id (Unix last_modified, read split)" % checked)


def sql_history_phase(db_path, visits, row_errors):
    conn = scratch_conn(db_path, "diff-safari-hist-")
    rows = list(conn.execute(
        "SELECT v.id, v.visit_time, v.title, i.url, i.visit_count, "
        "v.redirect_source, v.redirect_destination, v.origin "
        "FROM history_visits v LEFT JOIN history_items i ON i.id = v.history_item ORDER BY v.id"))

    sql_ids = {r[0] for r in rows}
    parser_ids = {v["id"] for v in visits}
    errored = errored_ids(row_errors, "history_visits")
    missing = sql_ids - parser_ids - errored
    invented = parser_ids - sql_ids
    if missing:
        report("phase2(history): %d ids neither yielded nor row-errored: %s"
               % (len(missing), sorted(missing)[:10]))
    if invented:
        report("phase2(history): %d yielded ids not in the db: %s" % (len(invented), sorted(invented)[:10]))

    by_id = {v["id"]: v for v in visits}
    checked = 0
    for r in rows:
        vid, visit_time, title, url, visit_count, rsrc, rdst, origin = r
        v = by_id.get(vid)
        if v is None:
            continue
        checked += 1

        def cmp_v(field, mine, theirs):
            if mine != theirs:
                report("phase2(history) id=%s: %s parser=%r sql=%r" % (vid, field, mine, theirs))

        cmp_v("time", norm_dt(v.get("time", "")), norm_dt(cocoa_to_utc(visit_time)))
        cmp_v("title", v.get("title", ""), title or "")
        cmp_v("url", v.get("url", ""), url or "")
        cmp_v("visit_count", v.get("visit_count", 0), visit_count or 0)
        cmp_v("redirect_source", v.get("redirect_source", 0), rsrc or 0)
        cmp_v("redirect_destination", v.get("redirect_destination", 0), rdst or 0)
        cmp_v("origin", v.get("origin", 0), origin or 0)
    print("phase2(history): %d visits cross-checked by id (Cocoa visit_time, redirects, origin)" % checked)


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    difftmp = args[0]
    db_path = args[args.index("--db") + 1] if "--db" in args else None
    history_db = args[args.index("--history-db") + 1] if "--history-db" in args else None

    parser_path = os.path.join(difftmp, "parser-safari.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s — run `make dump-study-safari` first" % parser_path)
        return 2
    capability, bookmarks, reading_list, visits, row_errors = load_parser(parser_path)
    print("parser: capability=%s, %d bookmarks, %d reading-list, %d visits, %d row errors"
          % (json.dumps(capability), len(bookmarks), len(reading_list), len(visits), len(row_errors)))

    tsv_bookmarks_phase(difftmp, bookmarks, reading_list)
    tsv_history_phase(difftmp, visits)
    if db_path:
        sql_bookmarks_phase(db_path, bookmarks, reading_list, row_errors)
    else:
        print("phase2(bookmarks) skipped (no --db)")
    if history_db:
        sql_history_phase(history_db, visits, row_errors)
    else:
        print("phase2(history) skipped (no --history-db)")

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
