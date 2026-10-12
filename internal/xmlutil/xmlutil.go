// Package xmlutil holds the XML encoding helpers shared by the packages of
// this module.
package xmlutil

import "encoding/xml"

// ValidNCName reports whether s is a valid XML NCName as far as this module
// needs it: it starts with an ASCII letter or '_' and continues with ASCII
// letters, digits, '-', '_' or '.'.  encoding/xml writes element and
// attribute names verbatim, so names from callers are checked with this.
func ValidNCName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		if !letter && (i == 0 || (c < '0' || c > '9') && c != '-' && c != '.') {
			return false
		}
	}
	return true
}

// EncodePayload writes start, the payload v and the matching end element.
// A string or []byte payload is literal XML; any other value is marshaled as
// a child element the way encoding/xml would marshal it on its own.  A nil
// payload leaves the element empty.
func EncodePayload(e *xml.Encoder, start xml.StartElement, v any) error {
	switch v := v.(type) {
	case nil:
		return e.EncodeElement(struct{}{}, start)
	case string:
		return e.EncodeElement(struct {
			Inner string `xml:",innerxml"`
		}{v}, start)
	case []byte:
		return e.EncodeElement(struct {
			Inner []byte `xml:",innerxml"`
		}{v}, start)
	}

	if err := e.EncodeToken(start); err != nil {
		return err
	}
	if err := e.Encode(v); err != nil {
		return err
	}
	return e.EncodeToken(start.End())
}
