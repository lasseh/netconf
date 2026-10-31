package netconf

import (
	"bytes"
	"errors"
)

// maxMessageDepth is how deep a buffered message may nest: what encoding/xml
// refuses to unmarshal into a field, and which no parser here needs more of.
const maxMessageDepth = 10000

// ErrMessageShape ends a session whose peer sent a message that no parser should
// walk, see WithMaxMessageSize.
var ErrMessageShape = errors.New("message exceeds the maximum message shape")

// checkShape refuses a message before a parser sees it.  The limit on a message
// counts its bytes, and a parser spends per element and per level of nesting.
func checkShape(raw []byte) error {
	if depth, _ := shapeOf(raw); depth > maxMessageDepth {
		return ErrMessageShape
	}
	return nil
}

// shapeOf returns how deep raw nests and how many elements and attributes it
// holds.  It allocates nothing, so a start tag with millions of attributes costs
// it nothing.  What is not XML is for the parser behind it to report.
//
// It has to end what it skips where encoding/xml does: a walk that gets out of
// step with the parser counts what the parser does not see, and misses what it
// does.
func shapeOf(raw []byte) (maxDepth, items int) {
	depth := 0
	for i := 0; i < len(raw); i++ {
		n := bytes.IndexByte(raw[i:], '<')
		if n < 0 {
			return maxDepth, items
		}
		i += n + 1
		if i >= len(raw) {
			return maxDepth, items
		}

		rest := raw[i:]
		var end, skip int
		switch {
		case rest[0] == '/':
			depth--
			end = bytes.IndexByte(rest, '>')
		case rest[0] == '?':
			end, skip = bytes.Index(rest, []byte("?>")), 1
		case bytes.HasPrefix(rest, []byte("!--")):
			// from behind the opening: <!--> opens a comment and ends none
			end, skip = bytes.Index(rest[3:], []byte("-->")), 5
		case bytes.HasPrefix(rest, []byte("![CDATA[")):
			end, skip = bytes.Index(rest, []byte("]]>")), 2
		case rest[0] == '!':
			end = directiveEnd(rest)
		default:
			items++
			depth++
			maxDepth = max(maxDepth, depth)
			for ; i < len(raw) && raw[i] != '>'; i++ {
				switch raw[i] {
				case '"', '\'':
					m := bytes.IndexByte(raw[i+1:], raw[i])
					if m < 0 {
						return maxDepth, items
					}
					i += m + 1
				case '=':
					items++
				}
			}
			if i < len(raw) && raw[i-1] == '/' {
				depth--
			}
			continue
		}
		if end < 0 {
			return maxDepth, items
		}
		i += end + skip
	}
	return maxDepth, items
}

// directiveEnd returns where the directive that rest begins with ends, -1 for
// nowhere, by the rules of encoding/xml: the byte behind <! is taken as it is, a
// quote hides what is in it, brackets nest, and a comment is skipped.
func directiveEnd(rest []byte) int {
	var quote byte
	depth := 0
	for i := 2; i < len(rest); i++ {
		switch b := rest[i]; {
		case quote != 0:
			if b == quote {
				quote = 0
			}
		case b == '"' || b == '\'':
			quote = b
		case b == '>':
			if depth == 0 {
				return i
			}
			depth--
		case bytes.HasPrefix(rest[i:], []byte("<!--")):
			n := bytes.Index(rest[i+4:], []byte("-->"))
			if n < 0 {
				return -1
			}
			i += n + 6
		case b == '<':
			depth++
		}
	}
	return -1
}
