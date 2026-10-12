package netconf

import (
	"bytes"
	"unicode/utf8"
)

// junosCapability is what a Junos device advertises, the Capability of the
// junos package.
const junosCapability = "http://xml.juniper.net/netconf/junos/1.0"

// joinByteRefs puts the text of a message from a Junos device back together.
//
// Junos keeps text as bytes and writes every byte above 127 as a character
// reference of its own: "å", in UTF-8 the bytes C3 A5, arrives as
// "&#195;&#165;", which an XML parser reads as the two characters "Ã¥".
// References in a row whose bytes are one character in UTF-8 are replaced by
// that character.  Every other reference stays as it is: text that Junos
// holds in Latin-1 arrives as "&#229;", which is "å" to a parser already.
//
// Two characters in a row from the upper half of Latin-1 that happen to be
// UTF-8 as bytes, such as "Ã¥", cannot be told from a character in UTF-8
// and are taken for one.
//
// doc is changed in place, as a message may be large, and the returned
// slice is the part of it that is left.
func joinByteRefs(doc []byte) []byte {
	// most messages have no numeric reference in them
	if !bytes.Contains(doc, []byte("&#")) {
		return doc
	}

	// What is joined gets shorter, so the result is written over doc, behind
	// the place that is being read.
	out := doc[:0]
	for i := 0; i < len(doc); {
		next := bytes.IndexAny(doc[i:], "&<")
		if next < 0 {
			next = len(doc) - i
		}
		out = append(out, doc[i:i+next]...)
		i += next
		if i == len(doc) {
			break
		}

		// what is here goes out as it is, unless it is a character in bytes
		keep := 1
		if doc[i] == '<' {
			keep = unparsed(doc[i:])
		} else if char, size := byteRefChar(doc[i:]); size > 0 {
			out = append(out, char[:size]...)
			i += size * len("&#195;")
			continue
		}
		out = append(out, doc[i:i+keep]...)
		i += keep
	}
	return out
}

// byteRefChar reads the character whose bytes in UTF-8 the references at the
// start of doc name, and returns those bytes.  The size is 0 when there is
// no such character there.
func byteRefChar(doc []byte) (char [utf8.UTFMax]byte, size int) {
	n := 0
	for ; n < len(char); n++ {
		b, ok := byteRef(doc[n*len("&#195;"):])
		if !ok {
			break
		}
		char[n] = b
	}

	r, size := utf8.DecodeRune(char[:n])
	// One byte is no character above 127, and neither is what does not
	// decode.  U+FFFE and U+FFFF are no characters to XML: a parser would
	// refuse the message for them.
	if size < 2 || r == 0xFFFE || r == 0xFFFF {
		return char, 0
	}
	return char, size
}

// byteRef returns the byte that the reference at the start of doc names, if
// it is one the way Junos writes a byte above 127: "&#", three digits and
// ";", for 128 to 255.
func byteRef(doc []byte) (b byte, ok bool) {
	if len(doc) < len("&#195;") || doc[0] != '&' || doc[1] != '#' || doc[5] != ';' {
		return 0, false
	}

	v := 0
	for _, digit := range doc[2:5] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		v = v*10 + int(digit-'0')
	}
	if v < 128 || v > 255 {
		return 0, false
	}
	return byte(v), true
}

// The markup in which "&" does not begin a reference.
var unparsedMarkup = [...]struct{ begin, end []byte }{
	{commentBegin, commentEnd},
	{cdataBegin, cdataEnd},
	{instructionBegin, instructionEnd},
}

// unparsed returns the length of what begins doc, which begins with "<",
// when an XML parser reads no references in it: a comment, a CDATA section
// or a processing instruction.  One that does not end takes the rest of doc.
// For any other markup the length is that of the "<" alone.
func unparsed(doc []byte) int {
	if len(doc) < 2 || doc[1] != '!' && doc[1] != '?' {
		return 1
	}
	for _, markup := range unparsedMarkup {
		if !bytes.HasPrefix(doc, markup.begin) {
			continue
		}
		end := bytes.Index(doc[len(markup.begin):], markup.end)
		if end < 0 {
			return len(doc)
		}
		return len(markup.begin) + end + len(markup.end)
	}
	return 1
}
