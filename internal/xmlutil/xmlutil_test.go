package xmlutil

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

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
