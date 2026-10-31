package xmlutil

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

func TestIsEmpty(t *testing.T) {
	var noBytes []byte
	var noStruct *struct{}

	for _, tc := range []struct {
		name    string
		payload any
		want    bool
	}{
		{"nil", nil, true},
		{"nil pointer", noStruct, true},
		{"nil bytes", noBytes, true},
		{"no string", "", true},
		{"white space", " \n\t", true},
		{"white space in bytes", []byte(" \n"), true},
		{"an empty element", "<configuration/>", true},
		{"an empty element, written out", "<configuration></configuration>", true},
		{"an empty element with white space", " <configuration>\n</configuration>\n", true},
		{"an empty element in a namespace", `<configuration xmlns="urn:x"/>`, true},
		{"an element with something in it", "<configuration><system/></configuration>", false},
		{"an element with text in it", "<a>text</a>", false},
		{"two elements", "<a/><b/>", false},
		{"text format", "system { host-name router1; }", false},
		{"set commands", "set system host-name router1", false},
		{"a value of another type", struct{}{}, false},
		{"a pointer to something", &struct{}{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be.Equal(t, tc.want, IsEmpty(tc.payload))
		})
	}
}

func TestValidNCName(t *testing.T) {
	for name, want := range map[string]bool{
		"running":   true,
		"_x":        true,
		"a-b.c_d9":  true,
		"":          false,
		"9lives":    false,
		"-a":        false,
		"a b":       false,
		`a="1"><b`:  false,
		"xmlns:foo": false,
		"é":         false,
	} {
		be.Equal(t, want, ValidNCName(name))
	}
}

func TestEncodePayload(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
	}

	for name, tc := range map[string]struct {
		payload any
		want    string
	}{
		"nil":    {nil, `<wrap></wrap>`},
		"string": {`<a/>`, `<wrap><a/></wrap>`},
		"bytes":  {[]byte(`<b/>`), `<wrap><b/></wrap>`},
		"struct": {item{Name: "x"}, `<wrap><item><name>x</name></item></wrap>`},
	} {
		t.Run(name, func(t *testing.T) {
			var sb strings.Builder
			e := xml.NewEncoder(&sb)
			be.NilErr(t, EncodePayload(e, xml.StartElement{Name: xml.Name{Local: "wrap"}}, tc.payload))
			be.NilErr(t, e.Flush())
			be.Equal(t, tc.want, sb.String())
		})
	}
}
