package netconf

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

// declaring returns a reply whose root declares count prefixes, with count
// elements in it.
func declaring(count int) []byte {
	var b strings.Builder
	b.WriteString(`<rpc-reply xmlns="` + NetconfNamespace + `"`)
	for i := range count {
		fmt.Fprintf(&b, ` xmlns:p%d="urn:%d"`, i, i)
	}
	b.WriteString(`>`)
	for range count {
		b.WriteString(`<e><f>x</f></e>`)
	}
	b.WriteString(`</rpc-reply>`)
	return []byte(b.String())
}

// What decoding the elements of a reply costs follows the elements, and not
// declarations times elements.  A device needs no more than a small reply to get
// the second: 3000 of each took 2.3 s.
func TestDecodeCostDoesNotGrowWithDeclarations(t *testing.T) {
	r := &Reply{Raw: declaring(3000)}
	found, err := r.Elements("e")
	be.NilErr(t, err)
	be.Equal(t, 3000, len(found))

	started := time.Now()
	for _, el := range found {
		var v struct {
			F string `xml:"f"`
		}
		be.NilErr(t, el.Decode(&v))
		be.Equal(t, "x", v.F)
	}
	took := time.Since(started)
	t.Logf("3000 elements under 3000 declarations took %s", took)
	be.True(t, took < time.Second)
}

// An element still decodes with what it uses declared, from wherever it was
// declared.
func TestDecodeKeepsTheDeclarationsAnElementUses(t *testing.T) {
	r := &Reply{Raw: []byte(`<rpc-reply xmlns="` + NetconfNamespace + `" xmlns:a="urn:a" xmlns:b="urn:b">` +
		`<x xmlns:a="urn:near"><y xmlns="urn:default"><a:z b:w="v">t</a:z></y></x></rpc-reply>`)}

	el, err := r.Element("y")
	be.NilErr(t, err)

	var v struct {
		XMLName xml.Name `xml:"urn:default y"`
		Z       struct {
			Text string `xml:",chardata"`
			W    string `xml:"urn:b w,attr"`
		} `xml:"urn:near z"`
	}
	be.NilErr(t, el.Decode(&v))
	be.Equal(t, "t", v.Z.Text)
	be.Equal(t, "v", v.Z.W)
}

// The last of two declarations on one element counts, as in encoding/xml.
func TestDecodeTakesTheLastOfTwoDeclarations(t *testing.T) {
	r := &Reply{Raw: []byte(`<rpc-reply xmlns:a="urn:one" xmlns:a="urn:two"><x><a:y>t</a:y></x></rpc-reply>`)}

	el, err := r.Element("x")
	be.NilErr(t, err)

	var v struct {
		Y string `xml:"urn:two y"`
	}
	be.NilErr(t, el.Decode(&v))
	be.Equal(t, "t", v.Y)
}

func nestedDeclarations(depth int) []byte {
	var b strings.Builder
	for i := range depth {
		fmt.Fprintf(&b, `<n xmlns:p%d="urn:%d">`, i, i)
	}
	b.WriteString(`<e/>`)
	b.WriteString(strings.Repeat(`</n>`, depth))
	return []byte(b.String())
}

// The lookup walks what is around an element, so there is a limit to that.
func TestNestedDeclarationsHaveALimit(t *testing.T) {
	found, err := (&Reply{Raw: nestedDeclarations(maxScopes)}).Elements("e")
	be.NilErr(t, err)
	be.Equal(t, 1, len(found))

	_, err = (&Reply{Raw: nestedDeclarations(maxScopes + 1)}).Elements("e")
	be.True(t, errors.Is(err, errTooManyScopes))
}

// Nesting that declares nothing costs nothing here.
func TestNestingWithoutDeclarationsHasNoLimit(t *testing.T) {
	raw := strings.Repeat(`<n>`, 2*maxScopes) + `<e/>` + strings.Repeat(`</n>`, 2*maxScopes)
	found, err := (&Reply{Raw: []byte(raw)}).Elements("e")
	be.NilErr(t, err)
	be.Equal(t, 1, len(found))
}
