// Package typedstream decodes Apple's legacy NeXTSTEP "typedstream" archive
// format — the output of NSArchiver with the "streamtyped" signature — far
// enough to extract the plain text of an NSAttributedString serialized into a
// Messages sms.db message.attributedBody BLOB. On modern iOS the message body
// frequently lives only in that blob, with the message.text column NULL, so
// decoding it is mandatory, not optional (see docs/schemas/messages.md).
//
// # Format and license posture
//
// This decoder is implemented from PUBLIC prose format documentation only — the
// imessage-exporter author's reverse-engineering write-up, the LGPL
// python-typedstream library's docstrings (consulted for format facts; function
// bodies unread; no code ported), archived NeXT/GNUstep NSArchiver notes, and
// the file(1) magic database. Per the charter's license hygiene the GPL
// imessage-exporter / crabstep SOURCE is never read; those tools are black-box
// differential oracles only (testing ladder rung 3). See NOTICE.
//
// # The wire format (only what text extraction needs)
//
// A stream opens with a header: a streamer-version byte (4), a length-prefixed
// signature ("streamtyped", little-endian), then a system-version integer
// (1000). After that come typed values: an Objective-C @encode type string
// followed by the value's bytes. Integers use a tag scheme — a plain signed
// byte is the literal, 0x81 introduces an int16 and 0x82 an int32 (little
// endian). 0x84 ("new") introduces a fresh literal definition, 0x85 is nil, any
// other byte is a back-reference.
//
// Crucially there are TWO independent reference tables, each numbered from 0x92:
// one for shared STRINGS (type-encodings, class names, C-strings) and one for
// OBJECTS AND CLASSES. A back-reference indexes the table matching its context —
// a class slot resolves against the object table, a type-encoding against the
// string table. (Getting this wrong — using a single combined table — shifts
// every object/class back-reference and mis-resolves superclasses; confirmed
// against the study backup.) An object's number is assigned before its class is
// read; a class is a name (shared string) + version + superclass (nil-term).
//
// An NSAttributedString archives as its backing NSString — the full plain text,
// and the first inline string in the graph — followed by run/attribute
// information. This decoder performs a real structured descent to that backing
// string and returns it. It deliberately stops there: attribute runs
// (mentions/links/formatting) are deferred for v0.1 (documented in
// docs/schemas/messages.md), so the run bytes are neither required nor parsed —
// which keeps text extraction robust against the format's least-documented
// corner.
package typedstream

import (
	"errors"
	"fmt"
)

// ErrNotTypedStream reports that the blob is not a streamtyped archive: the
// header signature or streamer version did not match. Callers distinguish this
// (a wrong-format or truncated blob) from a well-formed archive that carried no
// usable string.
var ErrNotTypedStream = errors.New("not a typedstream archive")

// Wire tags.
const (
	tagInt16 = 0x81 // next 2 bytes: int16 little-endian
	tagInt32 = 0x82 // next 4 bytes: int32 little-endian
	tagNew   = 0x84 // a new class/object/shared-string definition follows
	tagNil   = 0x85 // nil (also terminates a superclass chain)
	refBase  = 0x92 // the first reference number (byte 0x92 → table index 0)
	// refOffset maps a signed reference number back to a 0-based table index:
	// byte 0x92 read as a signed char is -110, so index = number + 110.
	refOffset = 0x100 - refBase
)

// DecodeText parses a message.attributedBody typedstream blob and returns the
// plain text of its NSAttributedString (or of a bare NSString). A
// header/signature mismatch yields ErrNotTypedStream; a well-formed archive
// from which no backing string can be read yields a different error. Callers
// MUST treat any non-nil error as "body unknown", never as an empty message
// (see the messages package and docs/schemas/messages.md) — decoding an empty
// body to "" silently would be exactly the wrong-but-plausible failure the
// charter forbids.
func DecodeText(data []byte) (string, error) {
	d := &decoder{b: data}
	if err := d.header(); err != nil {
		return "", err
	}
	// Top level: a typed value. NSArchiver's encodeRootObject writes the root
	// type-encoding ("@") then the object.
	_, val, err := d.typedValue()
	if err != nil {
		return "", fmt.Errorf("typedstream: %w", err)
	}
	obj, ok := val.(*object)
	if !ok || obj == nil {
		return "", fmt.Errorf("typedstream: root is not an object")
	}
	if !obj.hasString {
		return "", fmt.Errorf("typedstream: root object %q carried no string", obj.className)
	}
	return obj.text, nil
}

// decoder walks a typedstream blob. The two reference tables are independent, as
// the format assigns them: strings holds shared C-strings (type-encodings, class
// names); objects holds classes (*class) and objects (*object). Index i in each
// corresponds to that table's own reference number refBase+i.
type decoder struct {
	b       []byte
	pos     int
	strings []string
	objects []any
}

// object is a decoded Objective-C object. Only the fields text extraction needs
// are populated.
type object struct {
	className string
	hasString bool
	text      string
}

// class is a decoded class definition; only the most-derived name matters for
// dispatch.
type class struct {
	name string
}

func (d *decoder) header() error {
	// version(1) + sig-length(1) + "streamtyped" + system-version int.
	if len(d.b) < 2 {
		return ErrNotTypedStream
	}
	sigLen := int(d.b[1])
	if 2+sigLen > len(d.b) || string(d.b[2:2+sigLen]) != "streamtyped" {
		return ErrNotTypedStream
	}
	if d.b[0] != 4 { // streamer version 4 = modern macOS/iOS; 3 = old NeXTSTEP
		return fmt.Errorf("%w: unsupported streamer version %d", ErrNotTypedStream, d.b[0])
	}
	d.pos = 2 + sigLen
	if _, err := d.readInt(); err != nil { // system version (1000)
		return fmt.Errorf("typedstream: header: %w", err)
	}
	return nil
}

func (d *decoder) readByte() (byte, error) {
	if d.pos >= len(d.b) {
		return 0, fmt.Errorf("unexpected end of stream at %d", d.pos)
	}
	c := d.b[d.pos]
	d.pos++
	return c, nil
}

func (d *decoder) peekByte() (byte, error) {
	if d.pos >= len(d.b) {
		return 0, fmt.Errorf("unexpected end of stream at %d", d.pos)
	}
	return d.b[d.pos], nil
}

// readInt reads a variable-length signed integer. Length/count/version fields
// are small non-negatives, so the reserved-range bytes never collide with them
// here; 0x81/0x82 introduce a 2/4-byte little-endian value.
func (d *decoder) readInt() (int64, error) {
	b, err := d.readByte()
	if err != nil {
		return 0, err
	}
	switch b {
	case tagInt16:
		if d.pos+2 > len(d.b) {
			return 0, fmt.Errorf("truncated int16 at %d", d.pos)
		}
		v := int16(uint16(d.b[d.pos]) | uint16(d.b[d.pos+1])<<8)
		d.pos += 2
		return int64(v), nil
	case tagInt32:
		if d.pos+4 > len(d.b) {
			return 0, fmt.Errorf("truncated int32 at %d", d.pos)
		}
		v := int32(uint32(d.b[d.pos]) | uint32(d.b[d.pos+1])<<8 |
			uint32(d.b[d.pos+2])<<16 | uint32(d.b[d.pos+3])<<24)
		d.pos += 4
		return int64(v), nil
	default:
		return int64(int8(b)), nil
	}
}

// refIndex turns a back-reference head byte b (already consumed) into a 0-based
// table index. Single-byte numbers 0x92..0xFF map to 0..109; the rare >110-entry
// case uses the multi-byte integer form. The caller chooses which table.
func (d *decoder) refIndex(b byte) (int, error) {
	num := int64(int8(b))
	if b == tagInt16 || b == tagInt32 {
		d.pos-- // step back onto the tag so readInt consumes the whole integer
		var err error
		if num, err = d.readInt(); err != nil {
			return 0, err
		}
	}
	return int(num) + refOffset, nil
}

// typedValue reads a type-encoding followed by its value.
func (d *decoder) typedValue() (string, any, error) {
	tenc, err := d.typeEncoding()
	if err != nil {
		return "", nil, err
	}
	val, err := d.value(tenc)
	return tenc, val, err
}

// typeEncoding reads an @encode type string. The encodings text extraction
// meets are a single inline character (e.g. "+" for an NSString's bytes) or a
// shared string reused by reference (e.g. "@", defined once and back-referenced).
func (d *decoder) typeEncoding() (string, error) {
	b, err := d.peekByte()
	if err != nil {
		return "", err
	}
	switch {
	case b == tagNew || b == tagNil || b >= refBase:
		return d.sharedString()
	default:
		// Exactly one inline type character (multi-char struct encodings use the
		// shared mechanism and are out of scope). Reading a single byte avoids
		// mistaking a following printable length byte for part of the encoding.
		d.pos++
		return string(b), nil
	}
}

// value reads a value of the given type-encoding. Only the encodings that occur
// on the path to the backing string are handled.
func (d *decoder) value(tenc string) (any, error) {
	switch tenc {
	case "@":
		return d.objectValue()
	case "+":
		return d.inlineString()
	case "*":
		return d.sharedString()
	default:
		return nil, fmt.Errorf("unsupported type encoding %q at %d", tenc, d.pos)
	}
}

// objectValue reads a "@"-typed value: nil, a back-reference (into the object
// table), or a new object.
func (d *decoder) objectValue() (any, error) {
	b, err := d.peekByte()
	if err != nil {
		return nil, err
	}
	switch b {
	case tagNil:
		d.pos++
		return (*object)(nil), nil
	case tagNew:
		return d.newObject()
	default:
		d.pos++
		idx, err := d.refIndex(b)
		if err != nil {
			return nil, err
		}
		if idx < 0 || idx >= len(d.objects) {
			return nil, fmt.Errorf("object reference out of range (%d of %d) at %d", idx, len(d.objects), d.pos)
		}
		return d.objects[idx], nil
	}
}

// newObject reads a fresh object (0x84 next): assign its object-table number
// BEFORE reading its class, read the class, then the class-specific contents. It
// does NOT require the object's 0x86 end marker — text extraction stops at the
// backing string, so trailing run bytes are left unread by design.
func (d *decoder) newObject() (*object, error) {
	if _, err := d.readByte(); err != nil { // consume tagNew
		return nil, err
	}
	obj := &object{}
	d.objects = append(d.objects, obj) // number assigned before the class is read
	cl, err := d.class()
	if err != nil {
		return nil, err
	}
	obj.className = cl.name
	if err := d.objectContents(obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// objectContents decodes the parts of an object needed for text.
func (d *decoder) objectContents(obj *object) error {
	switch obj.className {
	case "NSString", "NSMutableString":
		_, v, err := d.typedValue() // one inline ("+") string
		if err != nil {
			return err
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: expected inline string, got %T", obj.className, v)
		}
		obj.text, obj.hasString = s, true
		return nil
	case "NSAttributedString", "NSMutableAttributedString":
		_, v, err := d.typedValue() // the backing NSString (type "@")
		if err != nil {
			return err
		}
		str, ok := v.(*object)
		if !ok || str == nil || !str.hasString {
			return fmt.Errorf("NSAttributedString: backing string missing")
		}
		obj.text, obj.hasString = str.text, true
		return nil
	default:
		return fmt.Errorf("unsupported root class %q", obj.className)
	}
}

// inlineString reads a "+"-encoded unshared byte string: a length then that
// many UTF-8 bytes. A zero length is legal and yields "".
func (d *decoder) inlineString() (string, error) {
	n, err := d.readInt()
	if err != nil {
		return "", err
	}
	if n < 0 || d.pos+int(n) > len(d.b) {
		return "", fmt.Errorf("bad inline-string length %d at %d", n, d.pos)
	}
	s := string(d.b[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s, nil
}

// sharedString reads a shared C-string: a "new" definition (0x84 + length +
// bytes) appended to the STRING table, or a back-reference into that table.
// Shared strings carry class names and reused type-encodings.
func (d *decoder) sharedString() (string, error) {
	b, err := d.readByte()
	if err != nil {
		return "", err
	}
	switch b {
	case tagNil:
		return "", nil
	case tagNew:
		n, err := d.readInt()
		if err != nil {
			return "", err
		}
		if n < 0 || d.pos+int(n) > len(d.b) {
			return "", fmt.Errorf("bad shared-string length %d at %d", n, d.pos)
		}
		s := string(d.b[d.pos : d.pos+int(n)])
		d.pos += int(n)
		d.strings = append(d.strings, s)
		return s, nil
	default:
		idx, err := d.refIndex(b)
		if err != nil {
			return "", err
		}
		if idx < 0 || idx >= len(d.strings) {
			return "", fmt.Errorf("string reference out of range (%d of %d) at %d", idx, len(d.strings), d.pos)
		}
		return d.strings[idx], nil
	}
}

// class reads a class definition or a reference to one (into the OBJECT table):
// name (shared string), version, then the superclass chain (nil-terminated).
// Only the most-derived name is retained.
func (d *decoder) class() (*class, error) {
	b, err := d.peekByte()
	if err != nil {
		return nil, err
	}
	switch b {
	case tagNil:
		d.pos++
		return &class{}, nil
	case tagNew:
		d.pos++
		slot := len(d.objects)
		d.objects = append(d.objects, nil) // reserve the class's object-table slot
		name, err := d.sharedString()      // the name lands in the STRING table
		if err != nil {
			return nil, err
		}
		c := &class{name: name}
		d.objects[slot] = c
		if _, err := d.readInt(); err != nil { // class version
			return nil, err
		}
		if _, err := d.class(); err != nil { // superclass chain
			return nil, err
		}
		return c, nil
	default:
		d.pos++
		idx, err := d.refIndex(b)
		if err != nil {
			return nil, err
		}
		if idx < 0 || idx >= len(d.objects) {
			return nil, fmt.Errorf("class reference out of range (%d of %d) at %d", idx, len(d.objects), d.pos)
		}
		c, ok := d.objects[idx].(*class)
		if !ok {
			return nil, fmt.Errorf("reference %d is not a class at %d", idx, d.pos)
		}
		return c, nil
	}
}
