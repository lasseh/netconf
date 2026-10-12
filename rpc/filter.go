package rpc

import (
	"encoding/xml"
	"fmt"
	"maps"
	"slices"

	"github.com/lasseh/netconf/internal/xmlutil"
)

// Filter limits what a retrieval operation returns.  Create one with
// SubtreeFilter or XPathFilter.
type Filter interface {
	xml.Marshaler
	filter()
}

type subtreeFilter struct {
	f any
}

func (f subtreeFilter) filter() {}

func (f subtreeFilter) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "type"}, Value: "subtree"})

	return xmlutil.EncodePayload(e, start, f.f)
}

// SubtreeFilter creates a filter matching the provided XML structure: a
// string or []byte holding literal XML, or a value that marshals to the
// filter's top-level element.
func SubtreeFilter(filter any) Filter {
	return subtreeFilter{f: filter}
}

type xpathFilter struct {
	Select     string
	Namespaces map[string]string
}

func (f xpathFilter) filter() {}

func (f xpathFilter) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	start.Attr = append(start.Attr,
		xml.Attr{Name: xml.Name{Local: "type"}, Value: "xpath"},
		xml.Attr{Name: xml.Name{Local: "select"}, Value: f.Select},
	)

	for _, prefix := range slices.Sorted(maps.Keys(f.Namespaces)) {
		if !xmlutil.ValidNCName(prefix) {
			return fmt.Errorf("xpath filter: invalid namespace prefix: %q", prefix)
		}
		uri := f.Namespaces[prefix]
		attrName := "xmlns:" + prefix
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: attrName}, Value: uri})
	}

	return e.EncodeElement(struct{}{}, start)
}

// XPathFilter creates a filter using XPath 1.0 expression.
// namespaces map prefixes used in the path to their URIs.
func XPathFilter(path string, namespaces map[string]string) Filter {
	return xpathFilter{Select: path, Namespaces: namespaces}
}
