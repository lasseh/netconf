package netconf

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"iter"
	"unicode"
	"unicode/utf8"
)

// SyntaxError is what reading a message fails with when the message is not
// XML: a reply given to Reply.Element, Reply.Elements or Reply.Decode, an
// element given to Element.Decode, a notification.  Find it with errors.As.
// The error of the parser, an *xml.SyntaxError with a line number, is
// underneath.
//
// The usual cause is a character XML does not allow, such as a control
// character in text the device passes on from somewhere else.
// WithReplyRepair is for those.
type SyntaxError struct {
	// Offset is where in what was being read the trouble is, in bytes from
	// its start: in Reply.Raw for a reply, in Element.Raw for an element.
	// It is the first thing there that XML does not allow in a document, a
	// character or a reference to one, when the parser had read that far,
	// and where the parser stopped otherwise.
	Offset int64

	// Around is the bytes at Offset, with up to 32 on either side.  Error
	// does not print them: the text of an error goes where it is logged,
	// and next to a control character in a configuration there may be a
	// password hash.  They are here for who wants to see them.  What the
	// parser names in its own error, a character or a name, is in the text.
	Around []byte

	// Err is the error of the parser.
	Err error
}

// aroundSyntaxError is how much of a message a SyntaxError shows on either
// side of its offset.
const aroundSyntaxError = 32

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%v at byte %d", e.Err, e.Offset)
}

func (e *SyntaxError) Unwrap() error { return e.Err }

// located returns err with the place in doc it is about, when it is a
// syntax error of the parser.  stopped is how far into doc the parser had
// read.
func located(doc []byte, stopped int64, err error) error {
	var (
		xmlErr  *xml.SyntaxError
		already *SyntaxError
	)
	if !errors.As(err, &xmlErr) || errors.As(err, &already) {
		return err
	}

	offset := int(min(max(stopped, 0), int64(len(doc))))
	// The parser looks at the characters of a text when it has read all of
	// it, so it has stopped behind the one it refuses.
	for at := range invalidChars(doc[:offset]) {
		offset = at
		break
	}

	from, to := max(offset-aroundSyntaxError, 0), min(offset+aroundSyntaxError, len(doc))
	return &SyntaxError{Offset: int64(offset), Around: bytes.Clone(doc[from:to]), Err: err}
}

// How the markup begins and ends in which a parser reads no references.
var (
	cdataBegin       = []byte("<![CDATA[")
	cdataEnd         = []byte("]]>")
	commentBegin     = []byte("<!--")
	commentEnd       = []byte("-->")
	instructionBegin = []byte("<?")
	instructionEnd   = []byte("?>")
)

// isXMLChar reports whether XML allows the character r in a document.
func isXMLChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= unicode.MaxRune
}

// invalidChars returns the places in doc where there is something XML does
// not allow in a document and a parser refuses, each with its length in
// bytes: a character that is no character to XML, a byte that is not UTF-8,
// and a reference to a character that is none.
//
// A parser reads comments and processing instructions without looking at
// their characters, so nothing in them counts, and it reads no references in
// a CDATA section.
func invalidChars(doc []byte) iter.Seq2[int, int] {
	return func(yield func(at, size int) bool) {
		// up to here "&" begins no reference: the inside of a CDATA section
		literal := 0
		for i := 0; i < len(doc); {
			size := 0
			switch c := doc[i]; {
			case c == '<' && i >= literal:
				n := unparsed(doc[i:])
				if n > 1 && !bytes.HasPrefix(doc[i:], cdataBegin) {
					i += n
					continue
				}
				literal = i + n
			case c == '&' && i >= literal:
				size = invalidRef(doc[i:])
			case c < utf8.RuneSelf:
				if !isXMLChar(rune(c)) {
					size = 1
				}
			default:
				r, n := utf8.DecodeRune(doc[i:])
				if r != utf8.RuneError && isXMLChar(r) || r == utf8.RuneError && n > 1 {
					i += n
					continue
				}
				size = n
			}

			if size == 0 {
				i++
				continue
			}
			if !yield(i, size) {
				return
			}
			i += size
		}
	}
}

// invalidRef returns the length of the character reference that doc begins
// with when it names a character XML does not allow, and 0 for anything
// else.
func invalidRef(doc []byte) int {
	if len(doc) < len("&#0;") || doc[1] != '#' {
		return 0
	}
	i, base := 2, rune(10)
	if doc[2] == 'x' {
		i, base = 3, 16
	}

	var (
		r      rune
		digits int
	)
	for ; i < len(doc); i, digits = i+1, digits+1 {
		digit := rune(-1)
		switch c := doc[i]; {
		case c >= '0' && c <= '9':
			digit = rune(c - '0')
		case base == 16 && c >= 'a' && c <= 'f':
			digit = rune(c-'a') + 10
		case base == 16 && c >= 'A' && c <= 'F':
			digit = rune(c-'A') + 10
		}
		if digit < 0 {
			break
		}
		// a number that has left the characters behind stays there
		if r <= unicode.MaxRune {
			r = r*base + digit
		}
	}
	if digits == 0 || i == len(doc) || doc[i] != ';' || isXMLChar(r) {
		return 0
	}
	return i + 1
}

// StripInvalidChars takes out of a message what XML does not allow in a
// document, so that a message a device spoilt with it can be read.  It is
// made for WithReplyRepair.  What goes:
//
//   - the control characters below U+0020 other than tab, line feed and
//     carriage return, and U+FFFE and U+FFFF,
//   - bytes that are not UTF-8,
//   - references to a character XML does not allow, such as &#27; and
//     &#x1B;, where a parser reads references: not in a CDATA section.
//
// Comments and processing instructions are left as they are.  The parser of
// this package does not look at their characters, and taking something out
// of one can end it early.
//
// Nothing is put in the place of what goes.  msg is changed in place, and
// what is returned is the part of it that is left; a message with nothing
// to take out is returned as it is.  The time it takes grows with the size
// of the message and not with what is in it.
//
// What goes can leave something behind that was not there: "&#2" and ";"
// around a control character are a reference afterwards, "<" and "!--" a
// comment.  So the message is read the way a parser will read what is left
// of it, and one pass leaves nothing more to take out:
//
//   - a reference is looked for in what is left, at every ";", from the "&"
//     in front of it,
//   - a comment, a processing instruction and a CDATA section begin where
//     what is left says so, and a CDATA section ends there too.
func StripInvalidChars(msg []byte) []byte {
	// most messages have nothing to take out, and are not written over
	clean := true
	for range invalidChars(msg) {
		clean = false
		break
	}
	if clean {
		return msg
	}

	var (
		// out is what is left, written over the beginning of msg
		out = msg[:0]
		// amps is where in out a "&" stands that may still begin a
		// reference
		amps []int
		// markup is where in out the last "<" stands, which may yet begin
		// a comment, a processing instruction or a CDATA section, and -1
		// without one
		markup = -1
		// cdata is where in out the inside of a CDATA section begins that
		// has not ended, and -1 outside of one.  "&" and "<" are text in
		// there.
		cdata = -1
	)
	for i := 0; i < len(msg); {
		c, size := msg[i], 1
		if c < utf8.RuneSelf {
			if !isXMLChar(rune(c)) {
				i++
				continue
			}
		} else {
			r, n := utf8.DecodeRune(msg[i:])
			if r == utf8.RuneError && n == 1 || r != utf8.RuneError && !isXMLChar(r) {
				i += n
				continue
			}
			size = n
		}
		out = append(out, msg[i:i+size]...)
		i += size

		if cdata >= 0 {
			if c == '>' && bytes.HasSuffix(out[cdata:], cdataEnd) {
				cdata = -1
			}
			continue
		}

		switch c {
		case '<':
			// no reference goes on through markup
			amps = amps[:0]
			markup = len(out) - 1
		case '&':
			amps = append(amps, len(out)-1)
		case ';':
			if len(amps) == 0 {
				break
			}
			from := amps[len(amps)-1]
			amps = amps[:len(amps)-1]
			if invalidRef(out[from:]) == len(out)-from {
				out = out[:from]
			}
		}

		if markup < 0 {
			continue
		}
		// what the last "<" begins, now that this byte stands behind it
		var end []byte
		switch begun := out[markup:]; len(begun) {
		case len(instructionBegin):
			if bytes.Equal(begun, instructionBegin) {
				end = instructionEnd
			}
		case len(commentBegin):
			if bytes.Equal(begun, commentBegin) {
				end = commentEnd
			}
		case len(cdataBegin):
			if bytes.Equal(begun, cdataBegin) {
				markup, cdata = -1, len(out)
			}
		}
		if end == nil {
			continue
		}
		// A parser does not look at the characters of a comment or of a
		// processing instruction, and so they stay: to the end of it, or
		// of the message.
		n := len(msg) - i
		if at := bytes.Index(msg[i:], end); at >= 0 {
			n = at + len(end)
		}
		out = append(out, msg[i:i+n]...)
		i += n
		markup = -1
	}
	return out
}
