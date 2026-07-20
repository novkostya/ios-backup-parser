// Package applenotes decodes the body of an Apple Notes note — the
// gzip-compressed protobuf stored in NoteStore.sqlite's ZICNOTEDATA.ZDATA — far
// enough to extract the note's plain text. On modern iOS a note has NO text in
// any column; the text lives only inside this blob, so decoding it is mandatory,
// not optional (see docs/schemas/notes.md).
//
// # Format and license posture
//
// This decoder is implemented from PUBLIC format documentation only — the
// published "Apple Notes" / "Note Store proto" reverse-engineering write-ups
// (CCL Solutions / Ciofeca Forensics), the protobuf wire-format specification
// (Google's public encoding docs), and the field numbers as used by iLEAPP's
// notes.py (MIT — reading/porting permitted with attribution; it in turn credits
// mac_apt's Notes plugin, also MIT). The field path was independently confirmed
// against instrumented dumps of the study backup. No GPL source is read; per the
// charter's license hygiene, GPL tools are black-box differential oracles only.
// See NOTICE.
//
// # The blob (only what text extraction needs)
//
// ZDATA is gzip (magic 1F 8B) wrapping a serialized "Note Store proto". The note
// text sits at a fixed field path in that message tree:
//
//	NoteStoreProto { Document document = 2; }
//	Document       { Note note = 3; }        // (plus version/flag scalar fields)
//	Note           { string note_text = 2; } // (plus attribute_run, etc.)
//
// so the plain text is document(2) → note(3) → note_text(2). This decoder walks
// exactly that path with a minimal protobuf reader (varint + length-delimited,
// skipping every other field and wire type) and returns the note_text bytes as
// UTF-8. It deliberately stops there: attribute runs (formatting, embedded-object
// placeholders, tables, hashtags) are deferred for v0.1 — documented in
// docs/schemas/notes.md — so the run bytes are neither required nor parsed, which
// keeps text extraction robust against the format's least-documented corner.
//
// Object-replacement characters (U+FFFC) that mark embedded-attachment positions
// are left in the returned text verbatim: they are meaningful positional markers,
// the note's own attachments are surfaced separately (see the notes package), and
// keeping them matches the independent oracle used by the differential.
package applenotes

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// ErrNotNoteBody reports that a blob is not a decodable note body: it is not
// gzip, or the gunzipped bytes are not a Note Store proto whose document/note
// path can be walked. Callers MUST treat any non-nil error as "body unknown",
// never as an empty note (see the notes package and docs/schemas/notes.md) —
// decoding an unknown body to "" silently would be exactly the wrong-but-plausible
// failure the charter forbids.
var ErrNotNoteBody = errors.New("not an apple-notes body blob")

// maxDecompressed caps gunzip output to guard against a decompression bomb in a
// malformed blob. Real note bodies are far below this; a note that legitimately
// exceeds it would fail closed (reported as undecoded), never silently truncated.
const maxDecompressed = 64 << 20 // 64 MiB

// Field numbers on the note-text path (see the package doc). They are format
// facts, not a version claim.
const (
	fieldDocument = 2 // NoteStoreProto.document
	fieldNote     = 3 // Document.note
	fieldNoteText = 2 // Note.note_text
)

// Protobuf wire types (the wire-format spec). Only 0/1/2/5 occur; groups
// (3/4) are deprecated and absent from this format.
const (
	wireVarint = 0
	wireI64    = 1
	wireLen    = 2
	wireI32    = 5
)

// DecodeBody gunzips a ZICNOTEDATA.ZDATA blob and returns the note's plain text
// (Note.note_text). A well-formed body whose note_text field is simply absent
// (e.g. an attachment-only note) decodes to "" with a nil error; only a blob that
// is not gzip, or whose document→note structure cannot be walked, is an error
// (wrapping ErrNotNoteBody). It must not be called on a locked note's ZDATA
// (that is AES-GCM ciphertext, not gzip) — the notes package gates on
// ZISPASSWORDPROTECTED first.
func DecodeBody(zdata []byte) (string, error) {
	raw, err := gunzip(zdata)
	if err != nil {
		return "", err
	}
	// document(2) → note(3): both required; their absence means the blob is not
	// the note structure we understand.
	doc, ok := lenField(raw, fieldDocument)
	if !ok {
		return "", fmt.Errorf("%w: document field absent", ErrNotNoteBody)
	}
	note, ok := lenField(doc, fieldNote)
	if !ok {
		return "", fmt.Errorf("%w: note field absent", ErrNotNoteBody)
	}
	// note_text(2): a present-but-empty note legitimately lacks it → "".
	text, ok := lenField(note, fieldNoteText)
	if !ok {
		return "", nil
	}
	return string(text), nil
}

// gunzip decompresses a gzip blob, bounded by maxDecompressed.
func gunzip(zdata []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(zdata))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip: %v", ErrNotNoteBody, err)
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(io.LimitReader(zr, maxDecompressed+1))
	if err != nil {
		return nil, fmt.Errorf("%w: gunzip: %v", ErrNotNoteBody, err)
	}
	if len(raw) > maxDecompressed {
		return nil, fmt.Errorf("%w: decompressed body exceeds %d bytes", ErrNotNoteBody, maxDecompressed)
	}
	return raw, nil
}

// lenField returns the bytes of the FIRST length-delimited (wire type 2) field
// numbered want in a protobuf message, skipping every other field. ok is false
// when the field is absent or the message is truncated/malformed on the way to
// it. It reads only what the note-text path needs; it does not validate the whole
// message.
func lenField(buf []byte, want int) (value []byte, ok bool) {
	pos := 0
	for pos < len(buf) {
		key, n := uvarint(buf[pos:])
		if n <= 0 {
			return nil, false
		}
		pos += n
		field := int(key >> 3)
		wire := int(key & 7)
		switch wire {
		case wireVarint:
			_, n := uvarint(buf[pos:])
			if n <= 0 {
				return nil, false
			}
			pos += n
		case wireI64:
			if pos+8 > len(buf) {
				return nil, false
			}
			pos += 8
		case wireI32:
			if pos+4 > len(buf) {
				return nil, false
			}
			pos += 4
		case wireLen:
			ln, n := uvarint(buf[pos:])
			if n <= 0 {
				return nil, false
			}
			pos += n
			if ln > uint64(len(buf)-pos) {
				return nil, false
			}
			end := pos + int(ln)
			if field == want {
				return buf[pos:end], true
			}
			pos = end
		default:
			return nil, false // unsupported wire type (group) — stop
		}
	}
	return nil, false
}

// uvarint reads a base-128 varint (protobuf wire spec). It returns the value and
// the number of bytes consumed, or n<=0 on truncation / overlong encoding —
// mirroring encoding/binary.Uvarint's contract but without its panic-free-length
// ambiguity for our bounds checks.
func uvarint(b []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, c := range b {
		if i >= 10 { // a 64-bit varint is at most 10 bytes
			return 0, -1
		}
		if c < 0x80 {
			if i == 9 && c > 1 {
				return 0, -1 // overflow
			}
			return x | uint64(c)<<s, i + 1
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0 // truncated
}
