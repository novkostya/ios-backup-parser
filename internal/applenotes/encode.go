package applenotes

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
)

// EncodeBody builds a note-body blob (gzip of a Note Store proto) carrying text
// as Note.note_text, structured exactly like a real body: a document(2) holding a
// note(3), with the same leading scalar version/flag fields the real format
// emits (root f1=0; document f1=0, f2=0). It is the inverse of DecodeBody and
// exists so synthetic fixtures embed real-shaped gzip+protobuf bodies (testing
// ladder rung 1) rather than hand-waved bytes — the same role internal/typedstream's
// encoder plays for the messages fixture. Test-support code kept in the package so
// fixtures (which are _test files elsewhere) can reach it.
func EncodeBody(text string) []byte {
	// Note { note_text = text }  (plus nothing else — runs are deferred)
	note := lenFieldBytes(fieldNoteText, []byte(text))

	// Document { version(1)=0; flag(2)=0; note(3)=Note }
	var doc bytes.Buffer
	doc.Write(varintFieldBytes(1, 0))
	doc.Write(varintFieldBytes(2, 0))
	doc.Write(lenFieldBytes(fieldNote, note))

	// NoteStoreProto { version(1)=0; document(2)=Document }
	var root bytes.Buffer
	root.Write(varintFieldBytes(1, 0))
	root.Write(lenFieldBytes(fieldDocument, doc.Bytes()))

	return gzipBytes(root.Bytes())
}

// EncodeRawGzip gzips arbitrary protobuf bytes (for negative tests that need a
// gzip blob whose inner structure is deliberately not a note body).
func EncodeRawGzip(protobuf []byte) []byte { return gzipBytes(protobuf) }

func lenFieldBytes(field int, payload []byte) []byte {
	var b bytes.Buffer
	b.Write(key(field, wireLen))
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(payload)))
	b.Write(tmp[:n])
	b.Write(payload)
	return b.Bytes()
}

func varintFieldBytes(field int, v uint64) []byte {
	var b bytes.Buffer
	b.Write(key(field, wireVarint))
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	b.Write(tmp[:n])
	return b.Bytes()
}

func key(field, wire int) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(field)<<3|uint64(wire))
	return tmp[:n]
}

func gzipBytes(raw []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return buf.Bytes()
}
