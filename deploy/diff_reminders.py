#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's reminders
stream against an independent oracle, reminder by reminder, across every store.

Like notes, iLEAPP's own reminders export is NOT usable as a black-box oracle
here. iLEAPP reminders.py queries `SELECT ... FROM ZREMCDOBJECT WHERE ZTITLE1
<> ''` and guards on does_column_exist_in_db(ZREMCDOBJECT, ZLASTMODIFIEDDATE);
on the iOS 17/18 schema reminders live in ZREMCDREMINDER (title ZTITLE, not
ZREMCDOBJECT.ZTITLE1) and ZREMCDOBJECT has no ZLASTMODIFIEDDATE, so the guard is
false and iLEAPP returns ZERO reminders (the same notes-class staleness). So the
oracle is split, and still independent + MIT-derived:

 - EPOCH + GLOB (from iLEAPP reminders.py, MIT, attributed in NOTICE): the store
   glob Container_v1/Stores/*.sqlite* and the Cocoa conversion
   DATETIME(col + 978307200, 'UNIXEPOCH') are correct and reused here.
 - ORACLE-LOGIC + SET: this harness re-derives the field semantics against the
   correct tables and runs its OWN SQL against a scratch COPY of every store,
   keyed by (store, ZREMCDREMINDER.Z_PK), with the exact both-directions set
   check (db reminder rows == yielded ids + row-errored ids: no invented, no
   silently-dropped reminder). Entity Z_ENT ordinals are resolved per store from
   Z_PRIMARYKEY (the stores do not agree on them), matching the parser.

The parser only ever opens scratch copies (BackupFS.Materialize); this harness
copies each store to a scratch dir too. Operator-local only: everything read or
printed stays on the box (.difftmp/ is gitignored). This file is a generic
harness and carries no data. Exit 0 = all compared fields agree; 1 = differences;
2 = setup problem.

Usage (inside the oracle container, via `make diff-study-reminders`):
    python deploy/diff_reminders.py <difftmp-dir> --stores <Container_v1/Stores dir>
"""

import binascii
import glob
import json
import os
import re
import shutil
import sqlite3
import sys
import tempfile
from datetime import datetime, timezone

MAX_REPORT = 20
COCOA_UNIX_DELTA = 978307200

problems = []


def report(msg):
    problems.append(msg)


def norm(s):
    return " ".join((s or "").split())


def norm_dt(s):
    m = re.search(r"(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})", s or "")
    return "%s %s" % (m.group(1), m.group(2)) if m else ""


def cocoa_to_utc(seconds):
    if seconds is None:
        return ""
    try:
        return datetime.fromtimestamp(float(seconds) + COCOA_UNIX_DELTA, timezone.utc).strftime("%Y-%m-%d %H:%M:%S")
    except (ValueError, OSError, OverflowError):
        return ""


def fmt_uuid(blob):
    """Match the Go formatUUID: 16 bytes -> canonical UUID; else hex; else ''."""
    if blob is None:
        return ""
    b = bytes(blob)
    if len(b) == 0:
        return ""
    hx = lambda s: binascii.hexlify(s).decode()
    if len(b) != 16:
        return hx(b)
    return "%s-%s-%s-%s-%s" % (hx(b[0:4]), hx(b[4:6]), hx(b[6:8]), hx(b[8:10]), hx(b[10:16]))


def sharee_name(first, last, address):
    name = ((first or "").strip() + " " + (last or "").strip()).strip()
    return name if name else (address or "")


# --- parser stream -------------------------------------------------------------

def load_parser(path):
    capability, reminders, lists, row_errors = None, [], [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "reminder":
                reminders.append(obj["reminder"])
            elif kind == "list":
                lists.append(obj["list"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, reminders, lists, row_errors


def entity_map(cur):
    m = {}
    for ent, name in cur.execute("SELECT Z_ENT, Z_NAME FROM Z_PRIMARYKEY"):
        m[name] = ent
    return m


def check_field(key, field, got, want):
    if norm(str(got)) != norm(str(want)):
        report("%s: %s = %r, oracle %r" % (key, field, got, want))


def build_oracle_store(store_name, db_path):
    """Return (reminders, lists) dicts keyed by (store_name, Z_PK) for one store."""
    stage = tempfile.mkdtemp(prefix="diff-reminders-")
    shutil.copy(db_path, os.path.join(stage, os.path.basename(db_path)))
    conn = sqlite3.connect(os.path.join(stage, os.path.basename(db_path)))
    cur = conn.cursor()
    ent = entity_map(cur)
    if "REMCDReminder" not in ent:
        conn.close()
        shutil.rmtree(stage, ignore_errors=True)
        report("%s: entity map lacks REMCDReminder" % store_name)
        return {}, {}

    accounts = {}
    if "REMCDAccount" in ent:
        for pk, name in cur.execute("SELECT Z_PK, ZNAME FROM ZREMCDOBJECT WHERE Z_ENT = ?", (ent["REMCDAccount"],)):
            accounts[pk] = name or ""

    lists = {}
    for pk, ident, name, is_group, sharing, acct in cur.execute(
            "SELECT Z_PK, ZIDENTIFIER, ZNAME, ZISGROUP, ZSHARINGSTATUS, ZACCOUNT FROM ZREMCDBASELIST"):
        lists[(store_name, pk)] = {
            "name": name or "", "is_group": bool(is_group), "sharing_status": sharing or 0,
            "identifier": fmt_uuid(ident), "account": accounts.get(acct, "") if acct is not None else "",
        }

    recurrences = {}
    if "REMCDRecurrenceRule" in ent:
        for rem_id, freq, interval in cur.execute(
                "SELECT ZREMINDER4, ZFREQUENCY, ZINTERVAL FROM ZREMCDOBJECT WHERE Z_ENT = ?",
                (ent["REMCDRecurrenceRule"],)):
            if rem_id is not None:
                recurrences[rem_id] = {"frequency": freq or 0, "interval": interval or 0}

    sharees = {}
    if "REMCDSharee" in ent:
        for pk, first, last, addr in cur.execute(
                "SELECT Z_PK, ZFIRSTNAME, ZLASTNAME, ZADDRESS1 FROM ZREMCDOBJECT WHERE Z_ENT = ?",
                (ent["REMCDSharee"],)):
            sharees[pk] = sharee_name(first, last, addr)
    assignees = {}
    if "REMCDAssignment" in ent:
        for rem_id, assignee in cur.execute(
                "SELECT ZREMINDER1, ZASSIGNEE FROM ZREMCDOBJECT WHERE Z_ENT = ?", (ent["REMCDAssignment"],)):
            if rem_id is not None and sharees.get(assignee):
                assignees[rem_id] = sharees[assignee]

    reminders = {}
    for row in cur.execute(
            "SELECT Z_PK, ZIDENTIFIER, ZTITLE, ZNOTES, ZCOMPLETED, ZCOMPLETIONDATE, ZFLAGGED, "
            "ZPRIORITY, ZALLDAY, ZCREATIONDATE, ZLASTMODIFIEDDATE, ZDUEDATE, ZMARKEDFORDELETION, "
            "ZPARENTREMINDER, ZLIST, ZACCOUNT FROM ZREMCDREMINDER WHERE Z_ENT = ? ORDER BY Z_PK",
            (ent["REMCDReminder"],)):
        (pk, ident, title, notes, completed, completion, flagged, priority, all_day,
         created, modified, due, marked, parent, list_id, acct) = row
        reminders[(store_name, pk)] = {
            "identifier": fmt_uuid(ident), "title": title or "", "notes": notes or "",
            "completed": bool(completed), "completion": cocoa_to_utc(completion),
            "flagged": bool(flagged), "priority": priority or 0, "all_day": bool(all_day),
            "created": cocoa_to_utc(created), "modified": cocoa_to_utc(modified), "due": cocoa_to_utc(due),
            "marked_for_deletion": bool(marked), "parent_id": parent or 0,
            "list": lists.get((store_name, list_id), {}).get("name", "") if list_id is not None else "",
            "account": accounts.get(acct, "") if acct is not None else "",
            "recurrence": recurrences.get(pk), "assignee": assignees.get(pk, ""),
        }
    conn.close()
    shutil.rmtree(stage, ignore_errors=True)
    return reminders, lists


def main():
    args = sys.argv[1:]
    if not args:
        print("usage: diff_reminders.py <difftmp-dir> --stores <Container_v1/Stores dir>", file=sys.stderr)
        return 2
    difftmp = args[0]
    stores_dir = args[args.index("--stores") + 1] if "--stores" in args else None
    parser_path = os.path.join(difftmp, "parser-reminders.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s (run dump-study-reminders first)" % parser_path, file=sys.stderr)
        return 2
    if not stores_dir or not os.path.isdir(stores_dir):
        print("missing --stores <Container_v1/Stores dir>", file=sys.stderr)
        return 2

    capability, p_reminders, p_lists, row_errors = load_parser(parser_path)
    print("parser: %d reminders, %d lists, %d row-errors; capability=%s"
          % (len(p_reminders), len(p_lists), len(row_errors), capability))

    # Oracle: enumerate the same Data-*.sqlite stores the parser reads.
    store_paths = sorted(p for p in glob.glob(os.path.join(stores_dir, "Data-*.sqlite")))
    print("oracle stores: %s" % [os.path.basename(p) for p in store_paths])
    o_reminders, o_lists = {}, {}
    for path in store_paths:
        rems, lists = build_oracle_store(os.path.basename(path), path)
        o_reminders.update(rems)
        o_lists.update(lists)

    p_by_key = {(r["store"], r["id"]): r for r in p_reminders}

    date_fields = ["created", "modified", "due", "completion"]
    bool_fields = ["completed", "flagged", "all_day", "marked_for_deletion"]
    str_fields = ["identifier", "title", "notes"]
    int_fields = ["priority", "parent_id"]  # omitempty in the JSON — absent means 0
    checked = 0
    for key, o in o_reminders.items():
        p = p_by_key.get(key)
        if p is None:
            report("reminder %s: in DB but not yielded by the parser" % (key,))
            continue
        checked += 1
        label = "%s/%s" % key
        for f in date_fields:
            check_field(label, f, norm_dt(p.get(f, "")), o[f])
        for f in bool_fields:
            check_field(label, f, bool(p.get(f)), o[f])
        for f in str_fields:
            check_field(label, f, p.get(f, ""), o[f])
        for f in int_fields:
            check_field(label, f, p.get(f, 0), o[f])
        # References.
        check_field(label, "list", (p.get("list") or {}).get("name", ""), o["list"])
        check_field(label, "account", (p.get("account") or {}).get("name", ""), o["account"])
        check_field(label, "assignee", p.get("assignee", ""), o["assignee"])
        # Recurrence (raw, documented-to-validate) — presence + freq/interval.
        po, oo = p.get("recurrence"), o["recurrence"]
        if bool(po) != bool(oo):
            report("%s: recurrence presence mismatch parser=%s oracle=%s" % (label, bool(po), bool(oo)))
        elif po and oo:
            check_field(label, "rec.frequency", po.get("frequency", 0), oo["frequency"])
            check_field(label, "rec.interval", po.get("interval", 0), oo["interval"])

    # Lists cross-check.
    l_by_key = {(l["store"], l["id"]): l for l in p_lists}
    lists_checked = 0
    for key, o in o_lists.items():
        p = l_by_key.get(key)
        if p is None:
            report("list %s: in DB but not yielded" % (key,))
            continue
        lists_checked += 1
        label = "list %s/%s" % key
        check_field(label, "name", p.get("name", ""), o["name"])
        check_field(label, "is_group", bool(p.get("is_group")), o["is_group"])
        check_field(label, "identifier", p.get("identifier", ""), o["identifier"])
        check_field(label, "account", (p.get("account") or {}).get("name", ""), o["account"])

    # Both-directions set check (reminders). Row errors carry no store, so accept a
    # row-errored id in ANY store as accounted-for.
    parser_keys = set(p_by_key)
    oracle_keys = set(o_reminders)
    err_ids = set(int(m.group(1)) for e in row_errors for m in [re.search(r"rowid (\d+)", e)] if m)
    invented = {k for k in parser_keys - oracle_keys}
    dropped = {k for k in oracle_keys - parser_keys if k[1] not in err_ids}
    if invented:
        report("parser invented reminders not in any store: %s" % sorted(invented)[:MAX_REPORT])
    if dropped:
        report("store reminders neither yielded nor row-errored: %s" % sorted(dropped)[:MAX_REPORT])
    if set(l_by_key) - set(o_lists):
        report("parser invented lists: %s" % sorted(set(l_by_key) - set(o_lists))[:MAX_REPORT])
    if set(o_lists) - set(l_by_key):
        report("store lists not yielded: %s" % sorted(set(o_lists) - set(l_by_key))[:MAX_REPORT])

    print("checked: %d reminders (all fields), %d lists; set: %d parser / %d oracle reminders, %d row-errors"
          % (checked, lists_checked, len(parser_keys), len(oracle_keys), len(err_ids)))

    if problems:
        print("\nDIFFERENCES (%d):" % len(problems))
        for p in problems[:MAX_REPORT]:
            print("  -", p)
        if len(problems) > MAX_REPORT:
            print("  ... and %d more" % (len(problems) - MAX_REPORT))
        return 1
    print("\nALL CHECKS PASSED — reminders.1 differential clean.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
