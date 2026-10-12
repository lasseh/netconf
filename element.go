package netconf

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ErrElementNotFound is returned by Reply.Element for a reply that does not
// contain the element asked for.
var ErrElementNotFound = errors.New("element not found in reply")

// Element is one element of a reply.  Its byte slices point into the reply
// they were found in.
type Element struct {
	// Raw is the element as it is in the reply, tags included.
	Raw []byte

	// Inner is what the element contains: everything between its tags.
	Inner []byte

	// Text is the character data directly inside the element, without the
	// text of its children and with entities resolved.
	Text string

	// Attr is the attributes of the element's start tag, as encoding/xml
	// reports them.  The Space of a name written with a prefix is the
	// namespace the prefix stands for, wherever in the reply that was
	// declared: Junos declares its junos prefix on the <rpc-reply>.  A
	// prefix that nobody declared is the Space itself.  The namespace
	// declarations of the element are among the attributes, a prefix with
	// the Space "xmlns" and the default namespace with the name "xmlns".
	Attr []xml.Attr

	// ns is what the elements around this one declare
	ns *scope
}

// Decode decodes the element into v, which must match the element itself
// the way it would for xml.Unmarshal.
//
// The namespaces declared by the elements it was found in are in force, as
// they were in the reply, so v can ask for an element or an attribute by its
// namespace although Raw no longer declares it.
func (e Element) Decode(v any) error {
	if err := decodeIn(e.ns, e.Raw, v); err != nil {
		return fmt.Errorf("failed to decode element: %w", err)
	}
	return nil
}

// scope is the namespaces declared by one element, and by the elements
// around it.  An element that declares nothing has the scope of its parent,
// so the many elements of one result share one.
type scope struct {
	// decls is the xmlns attributes of the element
	decls  []xml.Attr
	parent *scope
}

// xmlns is the name of the attribute that declares the default namespace,
// and the Space encoding/xml gives one that declares a prefix.
const xmlns = "xmlns"

// isNamespace reports whether an attribute with this name, as encoding/xml
// reports it, declares a namespace: the default one or that of a prefix.
func isNamespace(name xml.Name) bool {
	return name.Space == xmlns || name.Space == "" && name.Local == xmlns
}

// inside returns the scope inside an element with the attributes attrs that
// stands in s.
func (s *scope) inside(attrs []xml.Attr) *scope {
	n := 0
	for _, attr := range attrs {
		if isNamespace(attr.Name) {
			n++
		}
	}
	if n == 0 {
		return s
	}

	decls := make([]xml.Attr, 0, n)
	for _, attr := range attrs {
		if isNamespace(attr.Name) {
			decls = append(decls, attr)
		}
	}
	return &scope{decls: decls, parent: s}
}

// open returns the start tag of an element that declares what s has in
// force, and nothing when that is nothing.
func (s *scope) open() string {
	if s == nil {
		return ""
	}

	var (
		b strings.Builder
		// the prefixes there is a declaration for already, "" for the
		// default namespace: the nearest one counts
		seen []string
	)
	b.WriteString("<scope")
	for ; s != nil; s = s.parent {
		// encoding/xml takes the last of two declarations on one element
		for _, decl := range slices.Backward(s.decls) {
			prefix := ""
			if decl.Name.Space == xmlns {
				prefix = decl.Name.Local
			}
			if slices.Contains(seen, prefix) {
				continue
			}
			seen = append(seen, prefix)

			b.WriteString(" " + xmlns)
			if prefix != "" {
				b.WriteString(":" + prefix)
			}
			b.WriteString(`="`)
			_ = xml.EscapeText(&b, []byte(decl.Value))
			b.WriteString(`"`)
		}
	}
	b.WriteString(">")
	return b.String()
}

// decodeIn decodes the element raw into v with the namespaces of ns in
// force.
func decodeIn(ns *scope, raw []byte, v any) error {
	var r io.Reader = bytes.NewReader(raw)

	// the element inside one that declares what was declared around it
	open := ns.open()
	if open != "" {
		r = io.MultiReader(strings.NewReader(open), r)
	}
	d := xml.NewDecoder(r)
	if open != "" {
		if _, err := d.Token(); err != nil {
			return err
		}
	}
	if err := d.Decode(v); err != nil {
		return located(raw, d.InputOffset()-int64(len(open)), err)
	}
	return nil
}

// Element returns the first element called name in the reply, see Elements
// for how elements are found.  A reply without one fails with
// ErrElementNotFound: an operation that was answered with something else
// must not read as an empty result.
func (r *Reply) Element(name string) (Element, error) {
	found, err := findElements(r.Raw, name, true)
	if err != nil {
		return Element{}, err
	}
	if len(found) == 0 {
		_, local := splitName(name)
		return Element{}, fmt.Errorf("%w: <%s>", ErrElementNotFound, local)
	}
	return found[0], nil
}

// Elements returns the elements called name in the reply, in document
// order.
//
// Elements are found at any depth, because devices wrap results: Junos puts
// the output of each routing engine in a <multi-routing-engine-item>, for
// instance.  An element inside a match is part of that match and not
// returned separately.
//
// name is the element's local name and matches it in any namespace, which is
// what replies with a namespace per software release need.  To match one
// namespace only write it in front, separated by a space, as in an
// encoding/xml struct tag: "urn:example:interfaces interfaces".
func (r *Reply) Elements(name string) ([]Element, error) {
	return findElements(r.Raw, name, false)
}

func splitName(name string) (space, local string) {
	if i := strings.LastIndexByte(name, ' '); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// findElements returns the elements called name in raw, or only the first
// one.  raw does not have to be one document: a run of elements will do.
func findElements(raw []byte, name string, first bool) ([]Element, error) {
	space, local := splitName(name)

	var (
		found []Element
		// ns is what is declared where the decoder is, and around what is
		// declared around each of the elements it is inside of
		ns     *scope
		around []*scope
	)
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		begin := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return found, nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse reply: %w", located(raw, d.InputOffset(), err))
		}

		var start xml.StartElement
		switch tok := tok.(type) {
		case xml.StartElement:
			start = tok
		case xml.EndElement:
			if len(around) > 0 {
				ns, around = around[len(around)-1], around[:len(around)-1]
			}
			continue
		default:
			continue
		}
		if start.Name.Local != local || (space != "" && start.Name.Space != space) {
			around = append(around, ns)
			ns = ns.inside(start.Attr)
			continue
		}

		el, err := readElement(raw, d, begin)
		if err != nil {
			return nil, fmt.Errorf("failed to parse <%s>: %w", local, located(raw, d.InputOffset(), err))
		}
		el.Attr, el.ns = start.Attr, ns
		found = append(found, el)
		if first {
			return found, nil
		}
	}
}

// readElement reads the rest of the element whose start tag d has just read
// from raw and which began at offset begin.
func readElement(raw []byte, d *xml.Decoder, begin int64) (Element, error) {
	var (
		text       strings.Builder
		innerBegin = d.InputOffset()
		depth      = 1
	)
	for {
		innerEnd := d.InputOffset()
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return Element{}, err
		}

		switch tok := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.CharData:
			if depth == 1 {
				text.Write(tok)
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				return Element{
					Raw:   raw[begin:d.InputOffset()],
					Inner: raw[innerBegin:innerEnd],
					Text:  text.String(),
				}, nil
			}
		}
	}
}

// child is one of the elements of a run of elements.
type child struct {
	// name is the name of the element as it is written, without its prefix
	name string
	raw  []byte
}

// elements returns the elements that raw, a run of elements, is made of.
func elements(raw []byte) ([]child, error) {
	var found []child
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		begin := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return found, nil
		}
		if err != nil {
			return nil, err
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		el, err := readElement(raw, d, begin)
		if err != nil {
			return nil, err
		}
		found = append(found, child{name: start.Name.Local, raw: el.Raw})
	}
}
