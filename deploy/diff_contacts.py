#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's contacts
stream with iLEAPP, record by record. Two phases:

1. BLACK-BOX: iLEAPP's Address Book TSV (produced by running ileapp.py) vs the
   parser stream. iLEAPP v2026.1.0's export drops some non-empty columns (its
   empty-column-removal count query is offset — e.g. Last Name and Company are
   removed when Suffix/Organization counts are zero), so this phase compares
   only the fields its export reliably carries: creation date, first/middle
   name, nickname, phones, emails, URLs — multi-values are CHAR(13)-joined
   "Label: value" strings.

2. ORACLE-LOGIC: iLEAPP's addressBook.py query semantics (MIT, see NOTICE —
   property constants 3/4/5/22, label join via ABMultiValueLabel rowid, entry
   fan-out, store join) re-run here directly against a scratch COPY of the
   study database, keyed by ABPerson.ROWID. This covers every parser field —
   including the ones phase 1 cannot see.

Usage (inside the oracle container, via `make diff-study`):
    python deploy/diff_contacts.py <difftmp-dir> [--db <AddressBook.sqlitedb>]

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
from datetime import datetime, timezone

MAX_REPORT = 10
COCOA_UNIX_DELTA = 978307200

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
    return datetime.fromtimestamp(seconds + COCOA_UNIX_DELTA, tz=timezone.utc).strftime(
        "%Y-%m-%d %H:%M:%S")


def split_multivalue_cell(cell):
    """iLEAPP joins multi-values with CHAR(13); entries are 'Label: value'."""
    values = []
    for part in re.split(r"[\r\n;]+", cell or ""):
        part = part.strip()
        if not part:
            continue
        if ": " in part:
            part = part.split(": ", 1)[1].strip()
        values.append(part)
    return values


def load_parser(path):
    capability, people, groups, row_errors = None, [], [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "person":
                people.append(obj["person"])
            elif kind == "group":
                groups.append(obj["group"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, people, groups, row_errors


# --- Phase 1: black-box TSV comparison -------------------------------------

def find_contacts_tsv(root):
    def rank(path):
        name = os.path.basename(path).lower()
        if "address" in name:
            return 0
        if "contact" in name and "interaction" not in name:
            return 1
        return 2

    candidates = [p for p in glob.glob(os.path.join(root, "**", "*.tsv"), recursive=True)
                  if rank(p) < 2]
    candidates.sort(key=rank)
    return candidates


def pick_column(header, *needles):
    for i, name in enumerate(header):
        lowered = (name or "").lower().strip("﻿ ")
        if all(n in lowered for n in needles):
            return i
    return None


def tsv_phase(difftmp, people):
    ileapp_root = os.path.join(difftmp, "ileapp")
    tsvs = find_contacts_tsv(ileapp_root)
    if not tsvs:
        report("phase1: no Address Book TSV under %s (input-type mismatch? see Makefile)" % ileapp_root)
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
        "created": pick_column(header, "creation date"),
        "first": pick_column(header, "first name"),
        "middle": pick_column(header, "middle name"),
        "nickname": pick_column(header, "nickname"),
        # "phone" alone would match "Last Name Phonetic" — be specific.
        "phones": pick_column(header, "phone", "numbers"),
        "emails": pick_column(header, "email"),
        "urls": pick_column(header, "url"),
    }

    def cell(row, key):
        i = idx.get(key)
        return row[i] if i is not None and i < len(row) else ""

    theirs = []
    for row in rows:
        theirs.append({
            "created": norm_dt(cell(row, "created")),
            "first": norm(cell(row, "first")),
            "middle": norm(cell(row, "middle")),
            "nickname": norm(cell(row, "nickname")),
            "phones": sorted(norm_phone(v) for v in split_multivalue_cell(cell(row, "phones"))),
            "emails": sorted(norm(v).lower() for v in split_multivalue_cell(cell(row, "emails"))),
            "urls": sorted(norm(v).lower() for v in split_multivalue_cell(cell(row, "urls"))),
        })
    mine = []
    for p in people:
        mine.append({
            "created": norm_dt(p.get("created", "")),
            "first": norm(p.get("first")),
            "middle": norm(p.get("middle")),
            "nickname": norm(p.get("nickname")),
            "phones": sorted(norm_phone(v["value"]) for v in p.get("phones", []) if v.get("value")),
            "emails": sorted(norm(v["value"]).lower() for v in p.get("emails", []) if v.get("value")),
            "urls": sorted(norm(v["value"]).lower() for v in p.get("urls", []) if v.get("value")),
        })

    if len(mine) != len(theirs):
        report("phase1: record count parser=%d ileapp=%d" % (len(mine), len(theirs)))

    key = lambda r: (r["created"], r["first"], r["middle"], r["phones"], r["emails"])
    for i, (a, b) in enumerate(zip(sorted(mine, key=key), sorted(theirs, key=key))):
        for field in ("created", "first", "middle", "nickname", "phones", "emails", "urls"):
            if a[field] != b[field]:
                report("phase1 record %d: %s parser=%r ileapp=%r" % (i, field, a[field], b[field]))


# --- Phase 2: oracle-logic SQL comparison ----------------------------------

# Property constants and join semantics per iLEAPP scripts/artifacts/addressBook.py
# (MIT, attributed in NOTICE).
PROP_PHONE, PROP_EMAIL, PROP_ADDRESS, PROP_URL = 3, 4, 5, 22


def sql_phase(db_path, people, groups):
    stage = tempfile.mkdtemp(prefix="diff-contacts-")
    for path in glob.glob(db_path + "*"):
        shutil.copy(path, stage)
    conn = sqlite3.connect("file:%s?mode=ro" % os.path.join(stage, os.path.basename(db_path)),
                           uri=True)
    q = conn.execute

    labels = dict(q("SELECT ROWID, value FROM ABMultiValueLabel"))
    entry_keys = dict(q("SELECT ROWID, value FROM ABMultiValueEntryKey"))
    stores = {r[0]: (r[1] or "", r[2]) for r in q(
        "SELECT ABStore.ROWID, ABStore.Name, ABAccount.AccountIdentifier"
        " FROM ABStore LEFT JOIN ABAccount ON ABAccount.ROWID = ABStore.AccountID")}
    group_names = dict(q("SELECT ROWID, Name FROM ABGroup"))
    memberships = {}
    for gid, member in q("SELECT group_id, member_id FROM ABGroupMembers"):
        memberships.setdefault(member, set()).add(group_names.get(gid) or "")

    entries = {}
    for parent, key, value in q(
            "SELECT parent_id, key, value FROM ABMultiValueEntry ORDER BY parent_id, key"):
        entries.setdefault(parent, {})[entry_keys.get(key, "?%s" % key)] = value or ""

    multivalues = {}
    for rid, uid, prop, label, value in q(
            "SELECT record_id, UID, property, label, value FROM ABMultiValue"
            " ORDER BY record_id, UID"):
        label_text = labels.get(label, "") if label is not None else ""
        multivalues.setdefault(rid, []).append((uid, prop, label_text, value))

    by_id = {p["id"]: p for p in people}
    parser_groups = {g["id"]: g for g in groups}
    person_group_names = {}
    for g in groups:
        for m in g.get("members", []):
            person_group_names.setdefault(m["member_id"], set()).add(g.get("name", ""))

    sql_people = list(q(
        "SELECT ROWID, First, Middle, Last, Prefix, Suffix, Organization, Department,"
        " JobTitle, Nickname, Note, Birthday, Kind, CreationDate, ModificationDate, StoreID"
        " FROM ABPerson ORDER BY ROWID"))
    if len(sql_people) != len(people):
        report("phase2: person count parser=%d sql=%d" % (len(people), len(sql_people)))

    scalar_fields = (
        ("first", 1), ("middle", 2), ("last", 3), ("prefix", 4), ("suffix", 5),
        ("organization", 6), ("department", 7), ("job_title", 8), ("nickname", 9),
        ("note", 10), ("birthday", 11),
    )
    for row in sql_people:
        rowid = row[0]
        p = by_id.get(rowid)
        if p is None:
            report("phase2 rowid %d: missing from parser stream" % rowid)
            continue
        for name, i in scalar_fields:
            if (p.get(name) or "") != (row[i] or ""):
                report("phase2 rowid %d: %s parser=%r sql=%r" % (rowid, name, p.get(name), row[i]))
        if (p.get("kind") or 0) != (row[12] or 0):
            report("phase2 rowid %d: kind parser=%r sql=%r" % (rowid, p.get("kind"), row[12]))
        for name, i in (("created", 13), ("modified", 14)):
            want = cocoa_to_utc(row[i])
            got = norm_dt(p.get(name, ""))
            if got != want:
                report("phase2 rowid %d: %s parser=%r sql=%r" % (rowid, name, got, want))

        want_store = stores.get(row[15]) if row[15] is not None else None
        got_store = p.get("store")
        got_pair = (got_store.get("name", ""), got_store.get("account_identifier"))\
            if got_store else None
        want_pair = (want_store[0], want_store[1]) if want_store else None
        if got_pair != want_pair:
            report("phase2 rowid %d: store parser=%r sql=%r" % (rowid, got_pair, want_pair))

        want_mv = {"phones": [], "emails": [], "urls": [], "addresses": []}
        for uid, prop, label_text, value in multivalues.get(rowid, []):
            if prop == PROP_PHONE:
                want_mv["phones"].append((label_text, value or ""))
            elif prop == PROP_EMAIL:
                want_mv["emails"].append((label_text, value or ""))
            elif prop == PROP_URL:
                want_mv["urls"].append((label_text, value or ""))
            elif prop == PROP_ADDRESS:
                want_mv["addresses"].append((label_text, entries.get(uid, {})))
        got_mv = {
            "phones": [(v.get("label", ""), v.get("value", "")) for v in p.get("phones", [])],
            "emails": [(v.get("label", ""), v.get("value", "")) for v in p.get("emails", [])],
            "urls": [(v.get("label", ""), v.get("value", "")) for v in p.get("urls", [])],
            "addresses": [(v.get("label", ""), v.get("components", {}))
                          for v in p.get("addresses", [])],
        }
        for field in ("phones", "emails", "urls", "addresses"):
            if got_mv[field] != want_mv[field]:
                report("phase2 rowid %d: %s parser=%r sql=%r"
                       % (rowid, field, got_mv[field], want_mv[field]))

        want_groups = memberships.get(rowid, set())
        got_groups = person_group_names.get(rowid, set())
        if got_groups != want_groups:
            report("phase2 rowid %d: groups parser=%r sql=%r" % (rowid, got_groups, want_groups))

    sql_group_count = len(group_names)
    if sql_group_count != len(parser_groups):
        report("phase2: group count parser=%d sql=%d" % (len(parser_groups), sql_group_count))

    print("phase2: %d records cross-checked by ROWID on names/org/dept/title/nickname/"
          "note/birthday/kind/created/modified/store/phones/emails/urls/addresses/groups"
          % len(sql_people))


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    difftmp = args[0]
    db_path = None
    if "--db" in args:
        db_path = args[args.index("--db") + 1]

    parser_path = os.path.join(difftmp, "parser.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s — run `make dump-study` first" % parser_path)
        return 2
    capability, people, groups, row_errors = load_parser(parser_path)
    print("parser: capability=%s, %d people, %d groups, %d row errors"
          % (json.dumps(capability), len(people), len(groups), len(row_errors)))
    if row_errors:
        print("NOTE: parser row errors present — inspect parser.jsonl")

    tsv_phase(difftmp, people)
    if db_path:
        sql_phase(db_path, people, groups)
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
