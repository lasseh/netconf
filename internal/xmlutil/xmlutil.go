// Package xmlutil holds the XML encoding helpers shared by the packages of
// this module.
package xmlutil

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"reflect"
)

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

// IsEmpty reports whether payload holds no configuration: a nil pointer, a string
// or []byte with nothing but white space in it, or one element with nothing in
// it.  A value of any other type is not looked into.
func IsEmpty(payload any) bool {
	var raw []byte
	switch v := payload.(type) {
	case nil:
		return true
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		value := reflect.ValueOf(payload)
		return value.Kind() == reflect.Pointer && value.IsNil()
	}

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return true
	}

	// what is not XML, such as text or set commands, has something in it
	seen := false
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return seen
		}
		if err != nil {
			return false
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if seen {
				return false
			}
			seen = true
		case xml.CharData:
			if len(bytes.TrimSpace(tok)) > 0 {
				return false
			}
		}
	}
}
