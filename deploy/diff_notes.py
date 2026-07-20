#!/usr/bin/env python3
"""Differential harness (testing ladder rung 3): compare ibp-dump's notes stream
against independent oracles, note by note.

Unlike the other domains, iLEAPP's own notes export is NOT usable here: its
notes.py hard-codes the note->account INNER JOIN on ZACCOUNT4, but on the iOS
17/18 schema a note's account is ZACCOUNT7, so iLEAPP returns ZERO notes (its own
sample_data records "iOS 18.x | 0 rows"). So the oracle is split, and still
independent and MIT:

1. BODY DECODER (the from-scratch Go reader's real test): iLEAPP notes.py's OWN
   body decoder — get_uncompressed_data() + process_note_body_blob(), a
   fixed-offset byte-walk of the gunzipped protobuf — ported here verbatim (MIT,
   see NOTICE; iLEAPP credits mac_apt's Notes plugin) and run against a scratch
   COPY of the store. Its output must equal the parser's Body for EVERY note — a
   different algorithm (byte-walk vs recursive-descent) validating the same bytes,
   exactly as python-typedstream validates the messages decoder.

2. SNIPPET: every decoded body is cross-checked against Apple's own stored
   ZSNIPPET preview — an oracle-independent confirmation the text is real.

3. METADATA + SET: iLEAPP's query column choices (ZTITLE1, ZSNIPPET,
   ZCREATIONDATE3, ZMODIFICATIONDATE1, ZFOLDER->ZTITLE2, ZACCOUNT7->ZNAME,
   ZISPASSWORDPROTECTED, ZISPINNED, ZMARKEDFORDELETION) re-run against the scratch
   copy keyed by ICNote Z_PK, with the exact both-directions set check (db ICNote
   rows == yielded ids + row-errored ids: no invented, no silently-dropped note).

4. MEDIA: every media FileRef the parser emits is checked to os.path.exists under
   the study tree — proving the Accounts/<acct>/Media/<id>/<gen>/<file> path is
   real, not fabricated.

Usage (inside the oracle container, via `make diff-study-notes`):
    python deploy/diff_notes.py <difftmp-dir> --db <NoteStore.sqlite> --study <root>

Operator-local only: everything read or printed stays on the box (.difftmp/ is
gitignored). This file is a generic harness and carries no data. Exit 0 = all
compared fields agree; 1 = differences; 2 = setup problem.
"""

import binascii
import json
import os
import re
import shutil
import sqlite3
import sys
import tempfile
import zlib
from datetime import datetime, timezone

MAX_REPORT = 20
COCOA_UNIX_DELTA = 978307200

problems = []


def report(msg):
    problems.append(msg)


def norm(s):
    return " ".join((s or "").split())


def norm_body(s):
    """Whitespace-collapsed, U+FFFC (embedded-object placeholder) stripped — for
    the tolerant snippet comparison only. The exact body check keeps raw text."""
    return " ".join((s or "").replace("￼", " ").split())


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


# --- iLEAPP notes.py body decoder, ported verbatim (MIT, see NOTICE) -----------
# This is an INDEPENDENT implementation of the note-body decode (a fixed-offset
# byte-walk), deliberately different from the Go package's recursive protobuf
# reader, so agreement is real cross-validation and not a shared bug.

def get_uncompressed_data(compressed):
    if compressed is None:
        return None
    try:
        return zlib.decompress(compressed, 15 + 32)
    except zlib.error:
        return None


def read_length_field(blob):
    length = 0
    skip = 0
    try:
        data_length = int(blob[0])
        length = data_length & 0x7F
        while data_length > 0x7F:
            skip += 1
            data_length = int(blob[skip])
            length = ((data_length & 0x7F) << (skip * 7)) + length
    except (IndexError, ValueError):
        return 0, 1
    skip += 1
    return length, skip


def process_note_body_blob(blob):
    if blob is None:
        return ""
    try:
        pos = 0
        if blob[0:3] != b"\x08\x00\x12":
            return "\x00HEADER"
        pos += 3
        _, skip = read_length_field(blob[pos:])
        pos += skip
        if blob[pos:pos + 3] != b"\x08\x00\x10":
            return "\x00HEADER2"
        pos += 3
        _, skip = read_length_field(blob[pos:])
        pos += skip
        if blob[pos] != 0x1A:
            return "\x00TEXTHDR"
        pos += 1
        _, skip = read_length_field(blob[pos:])
        pos += skip
        if blob[pos] != 0x12:
            return "\x00TEXTTAG"
        pos += 1
        length, skip = read_length_field(blob[pos:])
        pos += skip
        return blob[pos:pos + length].decode("utf-8", "backslashreplace")
    except (IndexError, ValueError):
        return "\x00EXC"


# --- parser stream -------------------------------------------------------------

def load_parser(path):
    capability, notes, folders, row_errors = None, [], [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            obj = json.loads(line)
            kind = obj.get("type")
            if kind == "capability":
                capability = obj.get("capability")
            elif kind == "note":
                notes.append(obj["note"])
            elif kind == "folder":
                folders.append(obj["folder"])
            elif kind == "row_error":
                row_errors.append(obj.get("error", ""))
    return capability, notes, folders, row_errors


def entity_map(cur):
    m = {}
    for ent, name in cur.execute("SELECT Z_ENT, Z_NAME FROM Z_PRIMARYKEY"):
        m[name] = ent
    return m


def check_field(note_id, field, got, want):
    if norm(str(got)) != norm(str(want)):
        report("note %s: %s = %r, oracle %r" % (note_id, field, got, want))


def main():
    args = sys.argv[1:]
    if not args:
        print("usage: diff_notes.py <difftmp-dir> --db <NoteStore.sqlite> [--study <root>]", file=sys.stderr)
        return 2
    difftmp = args[0]
    db_path = args[args.index("--db") + 1] if "--db" in args else None
    study = args[args.index("--study") + 1] if "--study" in args else None
    parser_path = os.path.join(difftmp, "parser-notes.jsonl")
    if not os.path.exists(parser_path):
        print("missing %s (run dump-study-notes first)" % parser_path, file=sys.stderr)
        return 2
    if not db_path or not os.path.exists(db_path):
        print("missing --db NoteStore.sqlite", file=sys.stderr)
        return 2

    capability, notes, folders, row_errors = load_parser(parser_path)
    print("parser: %d notes, %d folders, %d row-errors; capability=%s"
          % (len(notes), len(folders), len(row_errors), capability))

    # Open a scratch COPY (never the study original) — matches the parser's
    # Materialize semantics and the never-mutate-input rule.
    stage = tempfile.mkdtemp(prefix="diff-notes-")
    shutil.copy(db_path, stage)
    conn = sqlite3.connect(os.path.join(stage, os.path.basename(db_path)))
    cur = conn.cursor()
    ent = entity_map(cur)
    for name in ("ICNote", "ICFolder", "ICAccount", "ICAttachment", "ICMedia"):
        if name not in ent:
            print("entity map lacks %s" % name, file=sys.stderr)
            return 2

    by_id = {n["id"]: n for n in notes}

    # Oracle rows (iLEAPP's column choices), keyed by ICNote Z_PK.
    q = """
        SELECT n.Z_PK, n.ZTITLE1, n.ZSNIPPET, n.ZCREATIONDATE3, n.ZMODIFICATIONDATE1,
               n.ZISPASSWORDPROTECTED, n.ZISPINNED, n.ZMARKEDFORDELETION,
               f.ZTITLE2, acc.ZNAME, acc.ZIDENTIFIER,
               (SELECT d.ZDATA FROM ZICNOTEDATA d WHERE d.ZNOTE = n.Z_PK)
        FROM ZICCLOUDSYNCINGOBJECT n
        LEFT JOIN ZICCLOUDSYNCINGOBJECT f ON f.Z_PK = n.ZFOLDER AND f.Z_ENT = ?
        LEFT JOIN ZICCLOUDSYNCINGOBJECT acc ON acc.Z_PK = n.ZACCOUNT7 AND acc.Z_ENT = ?
        WHERE n.Z_ENT = ?
        ORDER BY n.Z_PK"""
    db_ids = set()
    body_checked = snippet_checked = attach_checked = media_ok = 0
    # Materialize the outer result set: the per-note attachment query below reuses
    # a cursor, and a live outer iteration would be invalidated by it.
    note_rows = cur.execute(q, (ent["ICFolder"], ent["ICAccount"], ent["ICNote"])).fetchall()
    for row in note_rows:
        (pk, title, snippet, created, modified, locked, pinned, marked,
         folder_title, acct_name, acct_ident, zdata) = row
        db_ids.add(pk)
        p = by_id.get(pk)
        if p is None:
            report("note %s: in DB (ICNote) but not yielded by the parser" % pk)
            continue

        check_field(pk, "title", p.get("title", ""), title or "")
        check_field(pk, "snippet", p.get("snippet", ""), snippet or "")
        check_field(pk, "created", norm_dt(p.get("created", "")), cocoa_to_utc(created))
        check_field(pk, "modified", norm_dt(p.get("modified", "")), cocoa_to_utc(modified))
        check_field(pk, "locked", bool(p.get("locked")), bool(locked))
        check_field(pk, "pinned", bool(p.get("pinned")), bool(pinned))
        check_field(pk, "marked_for_deletion", bool(p.get("marked_for_deletion")), bool(marked))
        check_field(pk, "folder", (p.get("folder") or {}).get("title", ""), folder_title or "")
        check_field(pk, "account", (p.get("account") or {}).get("name", ""), acct_name or "")

        # Body: decoder cross-check (skip locked — its ZDATA is ciphertext).
        p_body = p.get("body", "")
        p_undec = bool(p.get("body_undecoded"))
        if locked:
            if p_body or not p.get("locked"):
                report("note %s: locked note body should be empty, got %r" % (pk, p_body))
        elif zdata is None:
            if p_body or p_undec:
                report("note %s: NULL body should decode to empty, got (%r, undec=%s)" % (pk, p_body, p_undec))
        else:
            oracle = process_note_body_blob(get_uncompressed_data(bytes(zdata)))
            if oracle.startswith("\x00"):
                # iLEAPP's decoder itself failed to parse — expect BodyUndecoded.
                if not p_undec:
                    report("note %s: oracle decode failed (%s) but parser did not flag undecoded" % (pk, oracle[1:]))
            else:
                body_checked += 1
                if p_undec:
                    report("note %s: parser flagged undecoded but oracle decoded fine" % pk)
                elif p_body != oracle:
                    report("note %s: body mismatch\n  parser: %r\n  oracle: %r" % (pk, p_body[:120], oracle[:120]))
                # Snippet cross-check (tolerant: prefix, placeholders stripped).
                ns = norm_body(snippet)
                if ns:
                    snippet_checked += 1
                    nb = norm_body(p_body)
                    if not (nb.startswith(ns) or ns in nb):
                        report("note %s: snippet %r not found in body %r" % (pk, ns[:60], nb[:120]))

        # Attachments vs the store, incl. media file existence.
        aq = """SELECT a.Z_PK, a.ZTYPEUTI, m.ZIDENTIFIER, m.ZGENERATION1, m.ZFILENAME
                FROM ZICCLOUDSYNCINGOBJECT a
                LEFT JOIN ZICCLOUDSYNCINGOBJECT m ON m.ZATTACHMENT1 = a.Z_PK AND m.Z_ENT = ?
                WHERE a.Z_ENT = ? AND a.ZNOTE = ? ORDER BY a.Z_PK"""
        db_att = list(cur.execute(aq, (ent["ICMedia"], ent["ICAttachment"], pk)))
        p_att = p.get("attachments") or []
        if len(db_att) != len(p_att):
            report("note %s: %d attachments, oracle %d" % (pk, len(p_att), len(db_att)))
        else:
            for pa, (aid, uti, mid, gen, fname) in zip(p_att, db_att):
                attach_checked += 1
                check_field("%s/att%s" % (pk, aid), "type_uti", pa.get("type_uti", ""), uti or "")
                check_field("%s/att%s" % (pk, aid), "filename", pa.get("filename", ""), fname or "")
                ref = pa.get("file")
                if mid and gen and fname:
                    want_rel = "Accounts/%s/Media/%s/%s/%s" % (acct_ident, mid, gen, fname)
                    if not ref or ref.get("relative_path") != want_rel:
                        report("note %s att %s: FileRef %r, want %r" % (pk, aid, ref, want_rel))
                    elif study:
                        # study tree is <root>/<Domain>/<relativePath>.
                        media_path = os.path.join(study, ref["domain"], ref["relative_path"])
                        if os.path.exists(media_path):
                            media_ok += 1
                        else:
                            report("note %s att %s: media file missing on disk: %s" % (pk, aid, media_path))
                elif ref is not None:
                    report("note %s att %s: non-media attachment has a FileRef %r" % (pk, aid, ref))

    # Both-directions set check.
    parser_ids = set(by_id)
    err_ids = set(int(m.group(1)) for e in row_errors for m in [re.search(r"rowid (\d+)", e)] if m)
    invented = parser_ids - db_ids
    dropped = db_ids - parser_ids - err_ids
    if invented:
        report("parser invented note ids not in the DB: %s" % sorted(invented)[:MAX_REPORT])
    if dropped:
        report("DB ICNote rows neither yielded nor row-errored: %s" % sorted(dropped)[:MAX_REPORT])

    conn.close()
    shutil.rmtree(stage, ignore_errors=True)

    print("checked: %d bodies (decoder cross-check), %d snippet cross-checks, %d attachments, %d media files on disk"
          % (body_checked, snippet_checked, attach_checked, media_ok))
    print("set check: %d parser notes, %d DB ICNote rows, %d row-errors"
          % (len(parser_ids), len(db_ids), len(err_ids)))

    if problems:
        print("\nDIFFERENCES (%d):" % len(problems))
        for p in problems[:MAX_REPORT]:
            print("  -", p)
        if len(problems) > MAX_REPORT:
            print("  ... and %d more" % (len(problems) - MAX_REPORT))
        return 1
    print("\nALL CHECKS PASSED — notes.1 differential clean.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
