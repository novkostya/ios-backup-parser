#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's calendar
stream with iLEAPP, record by record. Two phases:

1. BLACK-BOX: iLEAPP's "Calendar Events" TSV (produced by running ileapp.py's
   calendarAll.py) vs the parser stream. iLEAPP and the parser use the IDENTICAL
   events filter — CalendarItem.calendar_scale IS NOT 'gregorian' (birthday
   items are a separate iLEAPP artifact and are excluded on both sides) — so the
   record sets are the same. This phase keys events by (start time, title) and
   compares the fields iLEAPP renders: start/end time, timezone, calendar name,
   account name, location name/address, conference url and notes. Leftover
   iLEAPP-only or parser-only records are reported (no expected delta here,
   unlike calls' CallHistoryTemp).

2. ORACLE-LOGIC: iLEAPP's calendarAll.py query semantics (MIT, see NOTICE — the
   events/birthday split, the Participant.status mapping, the location /
   organizer / invitee / attachment joins) re-run here directly against a scratch
   COPY of the store, keyed by CalendarItem.ROWID. This covers EVERY parser
   field, including the ones the export omits (recurrence, alarms, status /
   availability / privacy, per-attendee role/type/is_self), and asserts the exact
   both-directions ROWID set (db event rows == yielded ids + row-errored ids: no
   invented, no silently-dropped event).

Usage (inside the oracle container, via `make diff-study-calendar`):
    python deploy/diff_calendar.py <difftmp-dir> [--db <Calendar.sqlitedb>]

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
from collections import Counter, defaultdict
from datetime import datetime, timezone

MAX_REPORT = 15
COCOA_UNIX_DELTA = 978307200
BIRTHDAY_SCALE = "gregorian"
FLOAT_TZ = "_float"

# Participant.status per iLEAPP calendarAll.py (MIT, see NOTICE).
ATTENDEE_STATUS = {0: "No response", 1: "Accepted", 2: "Declined", 3: "Maybe", 7: "No response"}

problems = []


def report(msg):
    problems.append(msg)


def norm(s):
    return " ".join((s or "").split())


def norm_dt(s):
    """Reduce any datetime rendering to 'YYYY-MM-DD HH:MM:SS' (UTC assumed)."""
    m = re.search(r"(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})", s or "")
    return "%s %s" % (m.group(1), m.group(2)) if m else ""


def cocoa_to_utc(seconds):
    """Cocoa seconds -> 'YYYY-MM-DD HH:MM:SS' UTC, or None if out of range
    (EventKit uses far-past/far-future sentinels for some floating items)."""
    if seconds is None:
        return None
    try:
        return datetime.fromtimestamp(int(seconds) + COCOA_UNIX_DELTA, tz=timezone.utc).strftime(
            "%Y-%m-%d %H:%M:%S")
    except (OSError, ValueError, OverflowError):
        return None


def load_parser(path):
    capability, events, calendars, row_errors = None, [], [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "event":
                events.append(obj["event"])
            elif kind == "calendar":
                calendars.append(obj["calendar"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, events, calendars, row_errors


# --- Phase 1: black-box TSV comparison -------------------------------------

def find_events_tsv(root):
    cands = [p for p in glob.glob(os.path.join(root, "**", "*.tsv"), recursive=True)
             if "calendar" in os.path.basename(p).lower()
             and "event" in os.path.basename(p).lower()]
    cands.sort()
    return cands


def pick_column(header, *needles):
    for i, name in enumerate(header):
        lowered = (name or "").lower().strip("﻿ ")
        if all(n in lowered for n in needles):
            return i
    return None


def parser_tz(ev):
    tz = ev.get("start_tz", "") or ""
    return "" if tz == FLOAT_TZ else tz


def tsv_phase(difftmp, events):
    root = os.path.join(difftmp, "ileapp-calendar")
    tsvs = find_events_tsv(root)
    if not tsvs:
        report("phase1: no Calendar Events TSV under %s (input-type mismatch? see Makefile)" % root)
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
        "start": pick_column(header, "start", "time"),
        "end": pick_column(header, "end", "time"),
        "tz": pick_column(header, "timezone"),
        "calendar": pick_column(header, "calendar", "name"),
        "account": pick_column(header, "account", "name"),
        "title": pick_column(header, "event", "title"),
        "locname": pick_column(header, "location", "name"),
        "locaddr": pick_column(header, "location", "address"),
        "conf": pick_column(header, "conference"),
        "notes": pick_column(header, "notes"),
    }

    def cell(row, key):
        i = idx.get(key)
        return row[i] if i is not None and i < len(row) else ""

    # Match on the FULL field tuple, not just start+title. Distinct events can
    # share a start time and title — a public holiday duplicated across calendars,
    # or paired train-ticket bookings differing only in seat/ticket text — so a
    # coarse key would mispair them and invent field "mismatches". Phase 2
    # (ROWID-exact) is the authoritative gate; this phase is a black-box
    # cross-check, and it must only flag a record whose every compared field has
    # no counterpart at all.
    fields = ("end", "tz", "calendar", "account", "locname", "locaddr", "conf", "notes")

    def ileapp_rec(row):
        return {
            "start": norm_dt(cell(row, "start")), "title": norm(cell(row, "title")),
            "end": norm_dt(cell(row, "end")), "tz": norm(cell(row, "tz")),
            "calendar": norm(cell(row, "calendar")), "account": norm(cell(row, "account")),
            "locname": norm(cell(row, "locname")), "locaddr": norm(cell(row, "locaddr")),
            "conf": norm(cell(row, "conf")), "notes": norm(cell(row, "notes")),
        }

    def parser_rec(ev):
        cal = ev.get("calendar") or {}
        loc = ev.get("location") or {}
        return {
            "start": norm_dt(ev.get("start_date", "")), "title": norm(ev.get("summary", "")),
            "end": norm_dt(ev.get("end_date", "")), "tz": norm(parser_tz(ev)),
            "calendar": norm(cal.get("title", "")), "account": norm((cal.get("store") or {}).get("name", "")),
            "locname": norm(loc.get("title", "")), "locaddr": norm(loc.get("address", "")),
            "conf": norm(ev.get("conference_url", "")), "notes": norm(ev.get("notes", "")),
        }

    def full_tuple(rec):
        return tuple(rec[k] for k in ("start", "title") + fields)

    theirs = Counter()
    by_subkey = defaultdict(list)
    for row in rows:
        rec = ileapp_rec(row)
        theirs[full_tuple(rec)] += 1
        by_subkey[(rec["start"], rec["title"])].append(rec)

    matched = 0
    for ev in events:
        rec = parser_rec(ev)
        ft = full_tuple(rec)
        if theirs[ft] > 0:
            theirs[ft] -= 1
            matched += 1
            continue
        # No exact counterpart — diagnose against records sharing start+title.
        cands = by_subkey.get((rec["start"], rec["title"]), [])
        if not cands:
            report("phase1: parser event id=%s (%s / %s) has no iLEAPP record with that start+title"
                   % (ev.get("id"), rec["start"], rec["title"]))
            continue
        b = min(cands, key=lambda c: sum(1 for f in fields if c[f] != rec[f]))
        for f in fields:
            if rec[f] != b[f]:
                report("phase1 event id=%s: %s parser=%r ileapp=%r" % (ev.get("id"), f, rec[f], b[f]))

    leftover = sum(v for v in theirs.values() if v > 0)
    if leftover:
        report("phase1: %d iLEAPP-only event record(s) with no exact parser counterpart" % leftover)
    print("phase1: %d/%d parser events matched an iLEAPP record (full-field)" % (matched, len(events)))


# --- Phase 2: oracle-logic SQL comparison ----------------------------------

def sql_phase(db_path, events, calendars, row_errors):
    stage = tempfile.mkdtemp(prefix="diff-calendar-")
    for path in glob.glob(db_path + "*"):
        shutil.copy(path, stage)
    conn = sqlite3.connect(os.path.join(stage, os.path.basename(db_path)))
    q = conn.execute

    stores = {r[0]: {"id": r[0], "name": r[1] or "", "type": r[2] or 0}
              for r in q("SELECT ROWID, name, type FROM Store")}
    cals = {}
    for r in q("SELECT ROWID, store_id, title, color, type, sharing_status FROM Calendar"):
        cals[r[0]] = {"id": r[0], "store_id": r[1], "title": r[2] or "", "color": r[3] or "",
                      "type": r[4] or "", "sharing_status": r[5] or 0}
    locs = {r[0]: {"title": r[1] or "", "address": r[2] or "", "lat": r[3], "long": r[4]}
            for r in q("SELECT ROWID, title, address, latitude, longitude FROM Location")}
    idents = {r[0]: (r[1] or "") for r in q("SELECT ROWID, display_name FROM Identity")}

    parts = {}
    attendees_by_owner = {}
    for r in q("SELECT ROWID, entity_type, owner_id, email, phone_number, status, role, type, is_self, identity_id "
               "FROM Participant ORDER BY ROWID"):
        a = {"name": idents.get(r[9], "") if r[9] is not None else "",
             "email": r[3] or "", "phone_number": r[4] or "",
             "status": r[5] or 0, "role": r[6] or 0, "type": r[7] or 0, "is_self": bool(r[8])}
        parts[r[0]] = a
        if r[1] == 7 and r[2] is not None:
            attendees_by_owner.setdefault(r[2], []).append(a)

    recur_by_owner = {}
    for r in q("SELECT owner_id, frequency, interval, count, end_date, specifier FROM Recurrence ORDER BY owner_id, ROWID"):
        recur_by_owner.setdefault(r[0], []).append(
            {"frequency": r[1] or 0, "interval": r[2] or 0, "count": r[3] or 0,
             "end_date": r[4], "specifier": r[5] or ""})

    alarm_by_owner = {}
    for r in q("SELECT calendaritem_owner_id, trigger_date, trigger_interval, type, proximity FROM Alarm "
               "ORDER BY calendaritem_owner_id, ROWID"):
        alarm_by_owner.setdefault(r[0], []).append(
            {"trigger_date": r[1], "trigger_interval": r[2] or 0, "type": r[3] or 0, "proximity": r[4] or 0})

    attach_by_owner = {}
    for r in q("SELECT a.owner_id, f.filename, f.file_size, f.UUID, f.url, f.local_path "
               "FROM Attachment a LEFT JOIN AttachmentFile f ON f.ROWID = a.file_id ORDER BY a.owner_id, a.ROWID"):
        attach_by_owner.setdefault(r[0], []).append(
            {"filename": r[1] or "", "file_size": r[2] or 0, "uuid": r[3] or "",
             "url": r[4] or "", "local_path": r[5] or ""})

    sql_events = list(q(
        "SELECT ROWID, summary, description, start_date, end_date, start_tz, end_tz, all_day, url, "
        "conference_url_detected, conference_url, status, availability, privacy_level, creation_date, "
        "last_modified, calendar_id, location_id, organizer_id, entity_type, calendar_scale "
        "FROM CalendarItem WHERE calendar_scale IS NOT '%s' ORDER BY ROWID" % BIRTHDAY_SCALE))

    # Both-directions ROWID set check.
    sql_ids = {row[0] for row in sql_events}
    parser_ids = {ev["id"] for ev in events}
    errored = set()
    for e in row_errors:
        m = re.search(r"rowid (\d+)", e)
        if m:
            errored.add(int(m.group(1)))
    missing = sql_ids - parser_ids - errored
    invented = parser_ids - sql_ids
    if missing:
        report("phase2: %d event rowids in the db were neither yielded nor row-errored: %s"
               % (len(missing), sorted(missing)[:10]))
    if invented:
        report("phase2: %d yielded event ids are not in the db: %s" % (len(invented), sorted(invented)[:10]))

    by_id = {ev["id"]: ev for ev in events}

    def opt_dt(seconds):
        if seconds is None or seconds == 0:
            return ""
        return cocoa_to_utc(seconds)

    checked = 0
    time_skipped = 0
    for row in sql_events:
        pk = row[0]
        ev = by_id.get(pk)
        if ev is None:
            continue  # already reported as missing / errored
        checked += 1

        def cmp(field, mine, theirs):
            if mine != theirs:
                report("phase2 ROWID %d: %s parser=%r sql=%r" % (pk, field, mine, theirs))

        cmp("summary", ev.get("summary", ""), row[1] or "")
        cmp("notes", ev.get("notes", ""), row[2] or "")

        # Times: tolerate out-of-range sentinels (both sides then uncomparable).
        for field, mine_raw, sec in (("start_date", ev.get("start_date", ""), row[3]),
                                     ("end_date", ev.get("end_date", ""), row[4])):
            s = cocoa_to_utc(sec)
            if s is None:
                time_skipped += 1
                continue
            cmp(field, norm_dt(mine_raw), s)

        cmp("start_tz", ev.get("start_tz", ""), row[5] or "")
        cmp("end_tz", ev.get("end_tz", ""), row[6] or "")
        cmp("all_day", bool(ev.get("all_day")), bool(row[7]))
        cmp("url", ev.get("url", ""), row[8] or "")
        conf = (row[9] if row[9] else row[10]) or ""
        cmp("conference_url", ev.get("conference_url", ""), conf)
        cmp("status", ev.get("status", 0), row[11] or 0)
        cmp("availability", ev.get("availability", 0), row[12] or 0)
        cmp("privacy_level", ev.get("privacy_level", 0), row[13] or 0)
        cmp("created", norm_dt(ev.get("created", "")), norm_dt(opt_dt(row[14]) or ""))
        cmp("last_modified", norm_dt(ev.get("last_modified", "")), norm_dt(opt_dt(row[15]) or ""))
        cmp("entity_type", ev.get("entity_type", 0), row[19] or 0)
        cmp("calendar_scale", ev.get("calendar_scale", ""), row[20] or "")

        # Calendar / account (soft-nil).
        cal = cals.get(row[16])
        pcal = ev.get("calendar")
        if cal is None:
            if pcal is not None:
                report("phase2 ROWID %d: calendar parser=%r sql=None" % (pk, pcal))
        elif pcal is None:
            report("phase2 ROWID %d: calendar parser=None sql=%r" % (pk, cal["id"]))
        else:
            cmp("calendar.title", pcal.get("title", ""), cal["title"])
            cmp("calendar.color", pcal.get("color", ""), cal["color"])
            cmp("calendar.sharing_status", pcal.get("sharing_status", 0), cal["sharing_status"])
            store = stores.get(cal["store_id"])
            pstore = pcal.get("store")
            if store and pstore:
                cmp("calendar.store.name", pstore.get("name", ""), store["name"])
                cmp("calendar.store.type", pstore.get("type", 0), store["type"])
            elif bool(store) != bool(pstore):
                report("phase2 ROWID %d: calendar.store parser=%r sql=%r" % (pk, pstore, store))

        # Location (soft-nil).
        loc = locs.get(row[17])
        ploc = ev.get("location")
        if loc is None:
            if ploc is not None:
                report("phase2 ROWID %d: location parser=%r sql=None" % (pk, ploc))
        elif ploc is None:
            report("phase2 ROWID %d: location parser=None sql=%r" % (pk, loc["title"]))
        else:
            cmp("location.title", ploc.get("title", ""), loc["title"])
            cmp("location.address", ploc.get("address", ""), loc["address"])
            cmp("location.latitude", ploc.get("latitude", 0) or 0, loc["lat"] or 0)
            cmp("location.longitude", ploc.get("longitude", 0) or 0, loc["long"] or 0)

        # Organizer (soft-nil).
        org = parts.get(row[18]) if row[18] else None
        porg = ev.get("organizer")
        if org is None:
            if porg is not None:
                report("phase2 ROWID %d: organizer parser=%r sql=None" % (pk, porg))
        elif porg is None:
            report("phase2 ROWID %d: organizer parser=None sql=%r" % (pk, org["email"]))
        else:
            cmp("organizer.name", porg.get("name", ""), org["name"])
            cmp("organizer.email", porg.get("email", ""), org["email"])

        # Attendees.
        want_att = attendees_by_owner.get(pk, [])
        got_att = ev.get("attendees", []) or []
        if len(got_att) != len(want_att):
            report("phase2 ROWID %d: attendee count parser=%d sql=%d" % (pk, len(got_att), len(want_att)))
        else:
            for g, w in zip(got_att, want_att):
                gt = (g.get("name", ""), g.get("email", ""), g.get("status", 0), g.get("role", 0),
                      g.get("type", 0), bool(g.get("is_self")))
                wt = (w["name"], w["email"], w["status"], w["role"], w["type"], w["is_self"])
                if gt != wt:
                    report("phase2 ROWID %d: attendee parser=%r sql=%r" % (pk, gt, wt))

        # Recurrences.
        want_rec = recur_by_owner.get(pk, [])
        got_rec = ev.get("recurrences", []) or []
        if len(got_rec) != len(want_rec):
            report("phase2 ROWID %d: recurrence count parser=%d sql=%d" % (pk, len(got_rec), len(want_rec)))
        else:
            for g, w in zip(got_rec, want_rec):
                if (g.get("frequency", 0), g.get("interval", 0), g.get("count", 0), g.get("specifier", "")) != \
                        (w["frequency"], w["interval"], w["count"], w["specifier"]):
                    report("phase2 ROWID %d: recurrence parser=%r sql=%r" % (pk, g, w))
                gend = norm_dt(g.get("end_date", ""))
                wend = norm_dt(opt_dt(w["end_date"]) or "")
                if gend != wend:
                    report("phase2 ROWID %d: recurrence end_date parser=%r sql=%r" % (pk, gend, wend))

        # Alarms.
        want_al = alarm_by_owner.get(pk, [])
        got_al = ev.get("alarms", []) or []
        if len(got_al) != len(want_al):
            report("phase2 ROWID %d: alarm count parser=%d sql=%d" % (pk, len(got_al), len(want_al)))
        else:
            for g, w in zip(got_al, want_al):
                if (g.get("trigger_interval", 0), g.get("type", 0), g.get("proximity", 0)) != \
                        (w["trigger_interval"], w["type"], w["proximity"]):
                    report("phase2 ROWID %d: alarm parser=%r sql=%r" % (pk, g, w))
                gtd = norm_dt(g.get("trigger_date", ""))
                wtd = norm_dt(opt_dt(w["trigger_date"]) or "")
                if gtd != wtd:
                    report("phase2 ROWID %d: alarm trigger_date parser=%r sql=%r" % (pk, gtd, wtd))

        # Attachments.
        want_at = attach_by_owner.get(pk, [])
        got_at = ev.get("attachments", []) or []
        if len(got_at) != len(want_at):
            report("phase2 ROWID %d: attachment count parser=%d sql=%d" % (pk, len(got_at), len(want_at)))
        else:
            for g, w in zip(got_at, want_at):
                gt = (g.get("filename", ""), g.get("file_size", 0), g.get("uuid", ""),
                      g.get("url", ""), g.get("local_path", ""))
                wt = (w["filename"], w["file_size"], w["uuid"], w["url"], w["local_path"])
                if gt != wt:
                    report("phase2 ROWID %d: attachment parser=%r sql=%r" % (pk, gt, wt))

    # Calendars() stream vs the Calendar table.
    if calendars:
        want_cal_ids = sorted(cals.keys())
        got_cal_ids = sorted(c["id"] for c in calendars)
        if got_cal_ids != want_cal_ids:
            report("phase2: Calendars() ids parser=%r sql=%r" % (got_cal_ids[:10], want_cal_ids[:10]))

    print("phase2: %d events cross-checked by ROWID on time/tz/all_day/url/conference/status/"
          "availability/privacy/calendar/location/organizer/attendees/recurrence/alarms/attachments"
          % checked)
    if time_skipped:
        print("phase2: %d timestamp comparison(s) skipped (out-of-range EventKit sentinel dates)" % time_skipped)


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    difftmp = args[0]
    db_path = None
    if "--db" in args:
        db_path = args[args.index("--db") + 1]

    parser_path = os.path.join(difftmp, "parser-calendar.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s — run `make dump-study-calendar` first" % parser_path)
        return 2
    capability, events, calendars, row_errors = load_parser(parser_path)
    print("parser: capability=%s, %d events, %d calendars, %d row errors"
          % (json.dumps(capability), len(events), len(calendars), len(row_errors)))

    tsv_phase(difftmp, events)
    if db_path:
        sql_phase(db_path, events, calendars, row_errors)
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
