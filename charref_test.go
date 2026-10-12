package netconf

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

func TestJoinByteRefs(t *testing.T) {
	tests := []struct {
		name, doc, want string
	}{
		// as captured from Junos 26.2, for text loaded as UTF-8
		{"two bytes", `<building>x&#195;&#165;x</building>`, `<building>xåx</building>`},
		{"three bytes", `x&#226;&#130;&#172;x`, `x€x`},
		{"four bytes", `x&#240;&#159;&#152;&#128;x`, `x😀x`},
		{"a no-break space", `x&#194;&#160;x`, "x\u00a0x"},
		{"characters in a row", `x&#230;&#151;&#165;&#230;&#156;&#172;x`, `x日本x`},
		{"a commit comment", `<log>Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund</log>`, `<log>Tromsø – Ålesund</log>`},
		{"an error", `<error-message>Invalid numeric value: 'sjuende &#226;&#128;&#147; 7'</error-message>`, `<error-message>Invalid numeric value: 'sjuende – 7'</error-message>`},
		{"at the start and the end", `&#195;&#184;x&#195;&#184;`, `øxø`},
		{"in an attribute", `<a b="&#195;&#165;" c='&#195;&#184;'/>`, `<a b="å" c='ø'/>`},

		// as captured for text that was loaded as the reference "&#229;":
		// one byte that is a character by itself, as in Latin-1
		{"one byte", `<building>x&#229;x</building>`, `<building>x&#229;x</building>`},
		{"one byte, then two", `x&#229;&#195;&#165;x`, `x&#229;åx`},
		{"two bytes, then one", `x&#195;&#165;&#229;x`, `xå&#229;x`},
		{"one byte at the end", `x&#229;`, `x&#229;`},

		// bytes that are no character in UTF-8 stay what the parser makes of
		// them
		{"a first byte without the second", `x&#195;x&#165;`, `x&#195;x&#165;`},
		{"two bytes of three", `&#226;&#130;x`, `&#226;&#130;x`},
		{"two bytes of three at the end", `&#226;&#130;`, `&#226;&#130;`},
		{"a second byte by itself", `&#165;&#165;`, `&#165;&#165;`},
		{"longer than it has to be", `&#192;&#175;`, `&#192;&#175;`},
		{"half a surrogate pair", `&#237;&#160;&#128;`, `&#237;&#160;&#128;`},
		{"beyond the last character", `&#244;&#144;&#128;&#128;`, `&#244;&#144;&#128;&#128;`},
		{"a first byte, then a character", `&#195;&#195;&#165;`, `&#195;å`},

		// XML has no way to write these two, so they stay three characters
		{"not a character", `&#239;&#191;&#190;&#239;&#191;&#191;`, `&#239;&#191;&#190;&#239;&#191;&#191;`},
		{"the replacement character", `&#239;&#191;&#189;`, "\ufffd"},

		// only what Junos writes for a byte is one
		{"a reference below 128", `&#65;&#10;&#127;`, `&#65;&#10;&#127;`},
		{"a reference above 255", `&#256;&#165;&#8211;`, `&#256;&#165;&#8211;`},
		{"four digits", `&#0195;&#0165;`, `&#0195;&#0165;`},
		{"hexadecimal", `&#xC3;&#xA5;`, `&#xC3;&#xA5;`},
		{"no semicolon", `&#195&#165;`, `&#195&#165;`},
		{"something in between", `&#195;&amp;&#165; &#195; &#165;`, `&#195;&amp;&#165; &#195; &#165;`},
		{"other references", `a &lt; b &amp;&amp; c &#195;&#165;`, `a &lt; b &amp;&amp; c å`},
		{"cut short", `&#19`, `&#19`},
		{"an ampersand at the end", `x&`, `x&`},

		// where a parser does not read references, they are text
		{"a CDATA section", `<a><![CDATA[&#195;&#165;]]>&#195;&#165;</a>`, `<a><![CDATA[&#195;&#165;]]>å</a>`},
		{"a comment", `<!-- &#195;&#165; --><a>&#195;&#165;</a>`, `<!-- &#195;&#165; --><a>å</a>`},
		{"a processing instruction", `<?x &#195;&#165;?><a>&#195;&#165;</a>`, `<?x &#195;&#165;?><a>å</a>`},
		{"a comment without an end", `<a>&#195;&#165;</a><!-- &#195;&#165;`, `<a>å</a><!-- &#195;&#165;`},
		{"a CDATA section without an end", `<![CDATA[&#195;&#165;]] >&#195;&#165;`, `<![CDATA[&#195;&#165;]] >&#195;&#165;`},
		{"a tag", `<a>&#195;&#165;<b/><!x>&#195;&#165;</a>`, `<a>å<b/><!x>å</a>`},

		{"nothing to join", `<a b="1">x &amp; y</a>`, `<a b="1">x &amp; y</a>`},
		{"nothing at all", ``, ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := joinByteRefs([]byte(tc.doc))
			be.Equal(t, tc.want, string(got))

			// there is nothing left to join in the result
			be.Equal(t, tc.want, string(joinByteRefs(bytes.Clone(got))))
		})
	}

	// The reply is not copied: a configuration can be hundreds of megabytes.
	t.Run("in place", func(t *testing.T) {
		for _, doc := range []string{`<a>no reference</a>`, `<a>x&#195;&#165;x</a>`, `<a>&#229;</a>`} {
			in := []byte(doc)
			out := joinByteRefs(in)
			be.Equal(t, &in[0], &out[0])
		}
	})
}

// What an XML parser reads from a reply is the text that was loaded.  The
// reply is the one Junos 26.2 gave to a <get-configuration> for a statement
// loaded as "xåx" in UTF-8; the references of the other values are those it
// sent in the diff after each of them was loaded.
func TestJoinByteRefs_Parsed(t *testing.T) {
	const reply = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="5" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1791538182" junos:changed-localtime="2026-10-09 09:29:42 UTC">
    <groups>
        <name>netconf-lib-probe</name>
        <system>
            <location>
                <building>x%sx</building>
            </location>
        </system>
    </groups>
</configuration>
</rpc-reply>
`
	for want, sent := range map[string]string{
		"å":               "&#195;&#165;",
		"æ":               "&#195;&#166;",
		"Tromsø":          "Troms&#195;&#184;",
		"ñ":               "&#195;&#177;",
		"€":               "&#226;&#130;&#172;",
		"😀":               "&#240;&#159;&#152;&#128;",
		"\u00a0":          "&#194;&#160;",
		"日本":              "&#230;&#151;&#165;&#230;&#156;&#172;",
		"Blåbær og rømme": "Bl&#195;&#165;b&#195;&#166;r og r&#195;&#184;mme",
	} {
		var got struct {
			Building string `xml:"configuration>groups>system>location>building"`
		}
		doc := joinByteRefs([]byte(strings.Replace(reply, "%s", sent, 1)))
		be.NilErr(t, xml.Unmarshal(doc, &got))
		be.Equal(t, "x"+want+"x", got.Building)
	}
}
