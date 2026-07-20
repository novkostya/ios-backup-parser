package typedstream_test

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/novkostya/ios-backup-parser/internal/typedstream"
)

// realPrefix48 is the first 48 bytes of a real sms.db message.attributedBody,
// observed by read-only introspection of a scratch copy of the study backup
// (M3 schema re-confirmation). These are pure typedstream STRUCTURE bytes — the
// header, the root "@" type-encoding, and the start of the
// NSAttributedString -> NSObject class chain — and carry no message content.
// The encoder must reproduce them exactly, which ties the round-trip tests
// below to the REAL wire format rather than to encoder/decoder self-consistency
// alone (green fixtures do not prove correctness — the operator-local
// differential vs iLEAPP is the gate; see the charter).
const realPrefix48 = "040B73747265616D747970656481E803840140848484124E5341747472696275746564537472696E67008484084E534F"

func TestEncoderMatchesRealPrefix(t *testing.T) {
	got := strings.ToUpper(hex.EncodeToString(typedstream.EncodeAttributedString("Hi")[:48]))
	if got != realPrefix48 {
		t.Errorf("encoder prefix does not match the real backup bytes:\n got %s\nwant %s", got, realPrefix48)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	cases := map[string]string{
		"empty":    "",
		"ascii":    "Hello, world",
		"len127":   strings.Repeat("a", 127), // largest single-byte length
		"len128":   strings.Repeat("b", 128), // forces the 0x81 int16 length tag
		"len300":   strings.Repeat("c", 300), // multi-byte length, > 255
		"emoji":    "wave \U0001F44B world \U0001F30D",
		"newlines": "line1\nline2\nline3",
		"unicode":  "café — naïve — 日本語",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			blob := typedstream.EncodeAttributedString(want)
			got, err := typedstream.DecodeText(blob)
			if err != nil {
				t.Fatalf("DecodeText: %v", err)
			}
			if got != want {
				t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestDecodeBareNSStringRoot(t *testing.T) {
	// Some archives store a bare NSString (no NSAttributedString wrapper); the
	// decoder must handle that root too. Built by hand (structure only).
	got, err := typedstream.DecodeText(bareNSString("hi there"))
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got != "hi there" {
		t.Errorf("got %q, want %q", got, "hi there")
	}
}

func TestNotTypedStream(t *testing.T) {
	cases := map[string][]byte{
		"nil":       nil,
		"empty":     {},
		"short":     {0x04},
		"garbage":   []byte("this is not typedstream at all"),
		"wrong-sig": append([]byte{0x04, 0x0b}, []byte("streamtypeX-tail")...),
	}
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := typedstream.DecodeText(blob); !errors.Is(err, typedstream.ErrNotTypedStream) {
				t.Errorf("DecodeText = %v, want ErrNotTypedStream", err)
			}
		})
	}
}

func TestWrongStreamerVersion(t *testing.T) {
	blob := typedstream.EncodeAttributedString("x")
	blob[0] = 0x03 // old NeXTSTEP streamer version
	if _, err := typedstream.DecodeText(blob); !errors.Is(err, typedstream.ErrNotTypedStream) {
		t.Errorf("version 3 = %v, want ErrNotTypedStream", err)
	}
}

func TestTruncatedIsErrorNotEmpty(t *testing.T) {
	// A valid header followed by a truncated body must ERROR ("body unknown"),
	// never decode to "" — the wrong-but-plausible failure the charter forbids.
	full := typedstream.EncodeAttributedString("some body text here")
	truncated := full[:len(full)-8] // cut inside the inline string bytes
	if got, err := typedstream.DecodeText(truncated); err == nil {
		t.Fatalf("truncated blob decoded without error to %q", got)
	}
}

// bareNSString hand-builds a streamtyped archive whose root is a plain NSString
// carrying s (len(s) < 128). Structure only; no external data.
func bareNSString(s string) []byte {
	var b []byte
	b = append(b, 0x04, 0x0b)
	b = append(b, []byte("streamtyped")...)
	b = append(b, 0x81, 0xe8, 0x03) // system version 1000
	b = append(b, 0x84, 0x01, '@')  // root type-encoding "@" (shared, idx 0)
	b = append(b, 0x84)             // new object (idx 1)
	b = append(b, 0x84)             // new class (idx 2)
	b = append(b, 0x84, 0x08)       // new class-name string (idx 3), length 8
	b = append(b, []byte("NSString")...)
	b = append(b, 0x00) // class version
	b = append(b, 0x85) // nil superclass
	b = append(b, '+')  // inline string type
	b = append(b, byte(len(s)))
	b = append(b, s...)
	b = append(b, 0x86) // end of object
	return b
}
