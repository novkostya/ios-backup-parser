package typedstream

// Synthetic typedstream encoder — support for testing ladder rungs 1–2 only.
//
// EncodeAttributedString produces a streamtyped NSAttributedString blob whose
// byte layout mirrors the structure observed on a real sms.db (header, root
// "@", the NSAttributedString → NSObject class chain, a backing NSString, the
// shared reference table numbered from 0x92). The messages fixture embeds these
// blobs so a build→encode→parse→compare round-trip exercises the decoder, and a
// golden test asserts the emitted prefix equals the exact bytes confirmed from
// the study backup — tying the round-trip to the REAL format, not merely to
// encoder/decoder self-consistency. (Green fixtures still do not prove
// correctness; the operator-local differential vs iLEAPP is the gate that moves
// messages.1 from fixture-only to validated — see the charter.)
//
// It intentionally emits a minimal attributed string: the backing string plus
// the two object end markers, with no explicit attribute runs (runs are
// deferred for v0.1 — docs/schemas/messages.md). The decoder ignores everything
// after the backing string, so this is sufficient and honest for text fixtures.

const tagEnd = 0x86 // end of an object's contents

// EncodeAttributedString returns a streamtyped NSAttributedString archive whose
// plain text is s (UTF-8). s may be empty, long (exercising multi-byte length
// prefixes) or contain non-ASCII/emoji.
func EncodeAttributedString(s string) []byte {
	var b []byte
	// Header: version 4, signature "streamtyped", system version 1000.
	b = append(b, 0x04, 0x0b)
	b = append(b, []byte("streamtyped")...)
	b = appendInt(b, 1000)

	// Root type-encoding "@" as a new shared string (reference index 0).
	b = append(b, tagNew)
	b = appendInt(b, 1)
	b = append(b, '@')

	// The reference tables are separate: strings (type-encodings, class names)
	// and objects (objects + classes), each numbered from 0x92. Object-table
	// numbering here: NSAttributedString object=0, its class=1, NSObject class=2,
	// backing NSString object=3, NSString class=4.

	// NSAttributedString object (object #0): new object, then its class chain.
	b = append(b, tagNew)
	b = appendClass(b, "NSAttributedString") // class object #1
	b = appendSuperclassNSObject(b)          // NSObject class object #2, nil super

	// Backing string member: type "@" reused by back-reference to string #0.
	b = append(b, refBase+0)

	// Backing NSString object (object #3): new object, class NSString whose
	// superclass is the already-seen NSObject (object-table back-reference #2).
	b = append(b, tagNew)
	b = append(b, tagNew)                 // NSString class object #4
	b = appendSharedString(b, "NSString") // name (into the string table)
	b = appendInt(b, 0)                   // class version
	b = append(b, refBase+2)              // superclass NSObject (object #2)
	// NSString contents: a "+"-inline UTF-8 string.
	b = append(b, '+')
	b = appendInt(b, int64(len(s)))
	b = append(b, s...)

	// End markers (ignored by DecodeText; present so the blob is well-formed).
	b = append(b, tagEnd, tagEnd)
	return b
}

// appendClass writes a new class definition (new-class, new-name string,
// version 0) and leaves the superclass to the caller.
func appendClass(b []byte, name string) []byte {
	b = append(b, tagNew) // new class
	b = appendSharedString(b, name)
	b = appendInt(b, 0) // class version
	return b
}

// appendSuperclassNSObject writes the NSObject superclass (new class + name,
// version 0) terminated by a nil superclass.
func appendSuperclassNSObject(b []byte) []byte {
	b = appendClass(b, "NSObject")
	b = append(b, tagNil) // NSObject has no superclass
	return b
}

// appendSharedString writes a new shared C-string (0x84 + length + bytes).
func appendSharedString(b []byte, s string) []byte {
	b = append(b, tagNew)
	b = appendInt(b, int64(len(s)))
	return append(b, s...)
}

// appendInt encodes a non-negative typedstream integer: a single byte for
// 0..127, else the 0x81 (int16) or 0x82 (int32) little-endian tagged form.
func appendInt(b []byte, v int64) []byte {
	switch {
	case v >= 0 && v <= 0x7f:
		return append(b, byte(v))
	case v >= -0x8000 && v <= 0x7fff:
		return append(b, tagInt16, byte(v), byte(v>>8))
	default:
		return append(b, tagInt32, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
}
