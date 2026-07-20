package applenotes

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeRoundTrip(t *testing.T) {
	cases := map[string]string{
		"ascii":     "Shopping list: milk, eggs, bread",
		"empty":     "",
		"emoji":     "meeting notes 📝 with Renée — café ☕",
		"multiline": "line one\nline two\n\tindented",
		"long":      strings.Repeat("x", 5000), // forces multi-byte length varints
		"ffcc":      "before ￼ after",          // embedded-object placeholder kept verbatim
		"non-latin": "Банк-получатель: перевод",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeBody(EncodeBody(want))
			if err != nil {
				t.Fatalf("DecodeBody: %v", err)
			}
			if got != want {
				t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestGoldenHeaderBytes asserts the encoder reproduces the exact leading bytes an
// Apple Notes body carries on the real study backup (confirmed by instrumented
// dumps): the gunzipped stream opens with the document path 08 00 12 (root f1
// varint 0, then f2/Document length-delimited) and the document opens with
// 08 00 10 (f1 varint 0, f2 varint 0) before the note. Pinning these makes the
// synthetic fixtures real-shaped, and guards the field numbers against drift.
func TestGoldenHeaderBytes(t *testing.T) {
	raw := mustGunzip(t, EncodeBody("hi"))
	if !bytes.HasPrefix(raw, []byte{0x08, 0x00, 0x12}) {
		t.Fatalf("body header = % x, want prefix 08 00 12", raw[:min(6, len(raw))])
	}
	doc, ok := lenField(raw, fieldDocument)
	if !ok {
		t.Fatal("document field not found")
	}
	if !bytes.HasPrefix(doc, []byte{0x08, 0x00, 0x10}) {
		t.Fatalf("document header = % x, want prefix 08 00 10", doc[:min(6, len(doc))])
	}
}

// TestAttachmentOnlyNote: a well-formed body whose Note carries no note_text
// (only an attribute run, field 3) decodes to "" with NO error — a decoded empty
// note, never an undecoded one.
func TestAttachmentOnlyNote(t *testing.T) {
	// Note { field3(len) = "run" }  — no field 2.
	note := lenFieldBytes(3, []byte("run"))
	var doc bytes.Buffer
	doc.Write(varintFieldBytes(1, 0))
	doc.Write(lenFieldBytes(fieldNote, note))
	var root bytes.Buffer
	root.Write(lenFieldBytes(fieldDocument, doc.Bytes()))

	got, err := DecodeBody(EncodeRawGzip(root.Bytes()))
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestDecodeErrors(t *testing.T) {
	t.Run("not gzip", func(t *testing.T) {
		if _, err := DecodeBody([]byte("plain, not gzip")); !errors.Is(err, ErrNotNoteBody) {
			t.Fatalf("err = %v, want ErrNotNoteBody", err)
		}
	})
	t.Run("nil", func(t *testing.T) {
		if _, err := DecodeBody(nil); !errors.Is(err, ErrNotNoteBody) {
			t.Fatalf("err = %v, want ErrNotNoteBody", err)
		}
	})
	t.Run("gzip but no document", func(t *testing.T) {
		// A valid gzip of a protobuf that has only a scalar field 1 — document(2)
		// is absent, so the structure cannot be walked.
		body := EncodeRawGzip(varintFieldBytes(1, 7))
		if _, err := DecodeBody(body); !errors.Is(err, ErrNotNoteBody) {
			t.Fatalf("err = %v, want ErrNotNoteBody", err)
		}
	})
	t.Run("document without note", func(t *testing.T) {
		var root bytes.Buffer
		root.Write(lenFieldBytes(fieldDocument, varintFieldBytes(1, 0))) // document has no note(3)
		if _, err := DecodeBody(EncodeRawGzip(root.Bytes())); !errors.Is(err, ErrNotNoteBody) {
			t.Fatalf("err = %v, want ErrNotNoteBody", err)
		}
	})
	t.Run("truncated gzip", func(t *testing.T) {
		full := EncodeBody("hello world")
		if _, err := DecodeBody(full[:len(full)-3]); err == nil {
			t.Fatal("want error on truncated gzip, got nil")
		}
	})
}

// TestUvarint exercises the varint reader's boundary and error paths directly.
func TestUvarint(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1 << 20, 1<<64 - 1} {
		var tmp [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(tmp[:], v)
		got, m := uvarint(tmp[:n])
		if got != v || m != n {
			t.Fatalf("uvarint(%d): got (%d,%d) want (%d,%d)", v, got, m, v, n)
		}
	}
	if _, n := uvarint([]byte{0x80}); n != 0 { // truncated
		t.Fatalf("truncated varint: n=%d want 0", n)
	}
	if _, n := uvarint(nil); n != 0 {
		t.Fatalf("empty varint: n=%d want 0", n)
	}
}

func mustGunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
