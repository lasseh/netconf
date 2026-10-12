package netconf

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

func TestReplyElements(t *testing.T) {
	const open = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/25.4R1/junos" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">`

	tests := []struct {
		name  string
		body  string
		find  string
		raw   []string
		inner []string
		text  []string
	}{
		{
			name: "absent",
			body: `<ok/>`,
			find: "data",
		},
		{
			name:  "empty, self closing",
			body:  `<data/>`,
			find:  "data",
			raw:   []string{`<data/>`},
			inner: []string{``},
			text:  []string{``},
		},
		{
			name:  "empty, with end tag",
			body:  `<data></data>`,
			find:  "data",
			raw:   []string{`<data></data>`},
			inner: []string{``},
			text:  []string{``},
		},
		{
			name:  "children are kept as received",
			body:  "\n<data>\n  <a  x='1'>b</a><c/>\n</data>\n",
			find:  "data",
			raw:   []string{"<data>\n  <a  x='1'>b</a><c/>\n</data>"},
			inner: []string{"\n  <a  x='1'>b</a><c/>\n"},
			text:  []string{"\n  \n"},
		},
		{
			name:  "text has entities resolved and no child text",
			body:  `<output>a &lt; b<i>not this</i> &amp; c</output>`,
			find:  "output",
			raw:   []string{`<output>a &lt; b<i>not this</i> &amp; c</output>`},
			inner: []string{`a &lt; b<i>not this</i> &amp; c`},
			text:  []string{`a < b & c`},
		},
		{
			name: "inside result wrappers, in document order",
			body: `<multi-routing-engine-results>` +
				`<multi-routing-engine-item><re-name>fpc0</re-name><output>one</output></multi-routing-engine-item>` +
				`<multi-routing-engine-item><re-name>fpc1</re-name><output>two</output></multi-routing-engine-item>` +
				`</multi-routing-engine-results>`,
			find:  "output",
			raw:   []string{`<output>one</output>`, `<output>two</output>`},
			inner: []string{`one`, `two`},
			text:  []string{`one`, `two`},
		},
		{
			name:  "an element inside a match belongs to it",
			body:  `<configuration><groups><configuration>x</configuration></groups></configuration>`,
			find:  "configuration",
			raw:   []string{`<configuration><groups><configuration>x</configuration></groups></configuration>`},
			inner: []string{`<groups><configuration>x</configuration></groups>`},
			text:  []string{``},
		},
		{
			name:  "attributes and prefixes of the element are kept",
			body:  `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1"><system/></configuration>`,
			find:  "configuration",
			raw:   []string{`<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1"><system/></configuration>`},
			inner: []string{`<system/>`},
			text:  []string{``},
		},
		{
			name:  "any namespace by default",
			body:  `<interfaces xmlns="urn:a">a</interfaces><interfaces xmlns="urn:b">b</interfaces>`,
			find:  "interfaces",
			raw:   []string{`<interfaces xmlns="urn:a">a</interfaces>`, `<interfaces xmlns="urn:b">b</interfaces>`},
			inner: []string{`a`, `b`},
			text:  []string{`a`, `b`},
		},
		{
			name:  "one namespace when given",
			body:  `<interfaces xmlns="urn:a">a</interfaces><interfaces xmlns="urn:b">b</interfaces>`,
			find:  "urn:b interfaces",
			raw:   []string{`<interfaces xmlns="urn:b">b</interfaces>`},
			inner: []string{`b`},
			text:  []string{`b`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply := &Reply{Raw: []byte(open + tc.body + `</rpc-reply>`)}

			all, err := reply.Elements(tc.find)
			be.NilErr(t, err)
			be.Equal(t, len(tc.raw), len(all))
			for i, el := range all {
				be.Equal(t, tc.raw[i], string(el.Raw))
				be.Equal(t, tc.inner[i], string(el.Inner))
				be.Equal(t, tc.text[i], el.Text)
			}

			first, err := reply.Element(tc.find)
			if len(tc.raw) == 0 {
				be.True(t, errors.Is(err, ErrElementNotFound))
				return
			}
			be.NilErr(t, err)
			be.Equal(t, tc.raw[0], string(first.Raw))
			be.Equal(t, tc.inner[0], string(first.Inner))
			be.Equal(t, tc.text[0], first.Text)
		})
	}
}

// The error for a missing element names it, so "reply has no <data>" does
// not need to be assembled by every caller.
func TestReplyElementNotFound(t *testing.T) {
	reply := &Reply{Raw: []byte(`<rpc-reply><ok/></rpc-reply>`)}

	_, err := reply.Element("urn:x data")
	be.True(t, errors.Is(err, ErrElementNotFound))
	be.In(t, "<data>", err.Error())
}

func TestReplyElementsMalformed(t *testing.T) {
	for _, raw := range []string{
		`<rpc-reply><data><a></data></rpc-reply>`,
		`<rpc-reply><data><a>`,
		`<rpc-reply><data>`,
	} {
		reply := &Reply{Raw: []byte(raw)}

		_, err := reply.Elements("data")
		be.Nonzero(t, err)
		_, err = reply.Element("data")
		be.Nonzero(t, err)
		be.False(t, errors.Is(err, ErrElementNotFound))
	}
}

func TestElementDecode(t *testing.T) {
	// Captured from Junos 25.4 (EX4100): the same RPC answers inside a
	// routing engine wrapper on one platform and without it on another.
	const wrapped = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos" message-id="12" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<multi-routing-engine-results>

<multi-routing-engine-item>

<re-name>fpc0</re-name>

<system-uptime-information xmlns="http://xml.juniper.net/junos/25.4R1-S1.7/junos">
<current-time>
<date-time junos:seconds="1791408835">2026-10-07 23:33:55 CEST</date-time>
</current-time>
</system-uptime-information>
</multi-routing-engine-item>

</multi-routing-engine-results>
</rpc-reply>`
	const bare = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos" message-id="12" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<system-uptime-information xmlns="http://xml.juniper.net/junos/25.4R1-S1.7/junos">
<current-time>
<date-time junos:seconds="1791408835">2026-10-07 23:33:55 CEST</date-time>
</current-time>
</system-uptime-information>
</rpc-reply>`

	for _, raw := range []string{wrapped, bare} {
		reply := &Reply{Raw: []byte(raw)}

		el, err := reply.Element("system-uptime-information")
		be.NilErr(t, err)

		var got struct {
			Current struct {
				Time struct {
					Seconds int64  `xml:"seconds,attr"`
					Text    string `xml:",chardata"`
				} `xml:"date-time"`
			} `xml:"current-time"`
		}
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, int64(1791408835), got.Current.Time.Seconds)
		be.Equal(t, "2026-10-07 23:33:55 CEST", got.Current.Time.Text)
	}

	// decoding into something the element is not fails
	reply := &Reply{Raw: []byte(bare)}
	el, err := reply.Element("system-uptime-information")
	be.NilErr(t, err)
	var wrong struct {
		XMLName xml.Name `xml:"something-else"`
	}
	be.Nonzero(t, el.Decode(&wrong))
}

// Captured from Junos 26.2R1.7 (vJunos-router) in a lab: the committed
// configuration under "system services netconf".  Junos says on the
// <configuration> element when it was committed and by whom, in attributes
// whose prefix the <rpc-reply> declares.  The user is not the one of the
// device.
const junosConfigurationReply = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="1" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:commit-seconds="1791461035" junos:commit-localtime="2026-10-08 12:03:55 UTC" junos:commit-user="operator">
    <system>
        <services>
            <netconf>
                <ssh>
                </ssh>
            </netconf>
        </services>
    </system>
</configuration>
</rpc-reply>`

// The same device on <get-system-uptime-information>, shortened: a result
// element with a namespace of its own, and times with the seconds in an
// attribute.
const junosUptimeReply = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="2" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<system-uptime-information xmlns="http://xml.juniper.net/junos/26.2R1.7/junos">
<current-time>
<date-time junos:seconds="1791700479">2026-10-11 06:34:39 UTC</date-time>
</current-time>
<system-booted-time>
<date-time junos:seconds="1791459185">2026-10-08 11:33:05 UTC</date-time>
<time-length junos:seconds="241294">2d 19:01</time-length>
</system-booted-time>
</system-uptime-information>
</rpc-reply>`

const (
	junos262Namespace = "http://xml.juniper.net/junos/26.2R1.7/junos"
	xnmNamespace      = "http://xml.juniper.net/xnm/1.1/xnm"
)

// attr returns the value of the attribute called name, and whether the
// element has it.
func attr(el Element, name xml.Name) (string, bool) {
	for _, a := range el.Attr {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// The attributes of an element are there to read, with the namespace their
// prefix stands for, wherever that was declared.
func TestElementAttr(t *testing.T) {
	reply := &Reply{Raw: []byte(junosConfigurationReply)}

	config, err := reply.Element("configuration")
	be.NilErr(t, err)
	seconds, ok := attr(config, xml.Name{Space: junos262Namespace, Local: "commit-seconds"})
	be.True(t, ok)
	be.Equal(t, "1791461035", seconds)
	at, _ := attr(config, xml.Name{Space: junos262Namespace, Local: "commit-localtime"})
	be.Equal(t, "2026-10-08 12:03:55 UTC", at)
	user, _ := attr(config, xml.Name{Space: junos262Namespace, Local: "commit-user"})
	be.Equal(t, "operator", user)

	// what the element declares itself is among them, as encoding/xml has it
	own, _ := attr(config, xml.Name{Local: "xmlns"})
	be.Equal(t, xnmNamespace, own)
	be.Equal(t, 4, len(config.Attr))

	// the envelope is an element like any other
	envelope, err := reply.Element("rpc-reply")
	be.NilErr(t, err)
	id, _ := attr(envelope, xml.Name{Local: "message-id"})
	be.Equal(t, "1", id)
	prefix, _ := attr(envelope, xml.Name{Space: "xmlns", Local: "junos"})
	be.Equal(t, junos262Namespace, prefix)

	none, err := reply.Element("netconf")
	be.NilErr(t, err)
	be.Equal(t, 0, len(none.Attr))

	// every match has its own
	times, err := (&Reply{Raw: []byte(junosUptimeReply)}).Elements("date-time")
	be.NilErr(t, err)
	be.Equal(t, 2, len(times))
	first, _ := attr(times[0], xml.Name{Space: junos262Namespace, Local: "seconds"})
	second, _ := attr(times[1], xml.Name{Space: junos262Namespace, Local: "seconds"})
	be.Equal(t, "1791700479", first)
	be.Equal(t, "1791459185", second)
}

// An element that was cut out of a reply no longer declares what the
// elements around it declared.  Decode has those declarations in force all
// the same, so a struct can ask for a namespace.
func TestElementDecodeNamespaces(t *testing.T) {
	uptime := &Reply{Raw: []byte(junosUptimeReply)}

	t.Run("a prefix declared by an ancestor", func(t *testing.T) {
		el, err := uptime.Element("date-time")
		be.NilErr(t, err)

		var got struct {
			Seconds int64  `xml:"http://xml.juniper.net/junos/26.2R1.7/junos seconds,attr"`
			Text    string `xml:",chardata"`
		}
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, int64(1791700479), got.Seconds)
		be.Equal(t, "2026-10-11 06:34:39 UTC", got.Text)
	})

	t.Run("the default namespace of an ancestor", func(t *testing.T) {
		el, err := uptime.Element("system-booted-time")
		be.NilErr(t, err)

		var got struct {
			XMLName xml.Name `xml:"http://xml.juniper.net/junos/26.2R1.7/junos system-booted-time"`
			Length  string   `xml:"http://xml.juniper.net/junos/26.2R1.7/junos time-length"`
		}
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, "2d 19:01", got.Length)

		var other struct {
			XMLName xml.Name `xml:"urn:example:other system-booted-time"`
		}
		be.Nonzero(t, el.Decode(&other))
	})

	t.Run("the default namespace of the envelope", func(t *testing.T) {
		reply := &Reply{Raw: []byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><data><x>1</x></data></rpc-reply>`)}
		el, err := reply.Element("data")
		be.NilErr(t, err)

		var got struct {
			XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 data"`
			X       int      `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 x"`
		}
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, 1, got.X)
	})

	t.Run("the nearest declaration counts", func(t *testing.T) {
		reply := &Reply{Raw: []byte(`<rpc-reply xmlns="urn:outer" xmlns:p="urn:outer-p">` +
			`<a xmlns="urn:middle" xmlns:p="urn:middle-p" xmlns:q="urn:q&amp;&quot;"><b><c p:x="1" q:y="2">3</c></b></a>` +
			`<c p:x="4">5</c></rpc-reply>`)}
		found, err := reply.Elements("c")
		be.NilErr(t, err)
		be.Equal(t, 2, len(found))

		var inner struct {
			XMLName xml.Name `xml:"urn:middle c"`
			X       int      `xml:"urn:middle-p x,attr"`
			Y       int      `xml:"urn:q&\" y,attr"`
		}
		be.NilErr(t, found[0].Decode(&inner))
		be.Equal(t, 1, inner.X)
		be.Equal(t, 2, inner.Y)

		var outer struct {
			XMLName xml.Name `xml:"urn:outer c"`
			X       int      `xml:"urn:outer-p x,attr"`
		}
		be.NilErr(t, found[1].Decode(&outer))
		be.Equal(t, 4, outer.X)
		be.Nonzero(t, found[1].Decode(&inner))
	})

	// what the element declares itself has the last word
	t.Run("the element's own declaration", func(t *testing.T) {
		el, err := (&Reply{Raw: []byte(junosConfigurationReply)}).Element("configuration")
		be.NilErr(t, err)

		var got struct {
			XMLName xml.Name `xml:"http://xml.juniper.net/xnm/1.1/xnm configuration"`
			Seconds int64    `xml:"http://xml.juniper.net/junos/26.2R1.7/junos commit-seconds,attr"`
		}
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, int64(1791461035), got.Seconds)
	})

	// Junos OS Evolved 23.2 writes the prefix of its error element without
	// declaring it.  The search must not fail over that, and the prefix
	// stays what it was written as.
	t.Run("a prefix nobody declared", func(t *testing.T) {
		reply := &Reply{Raw: []byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` +
			`<interface-information junos:style="terse"><xnm:error><message>device et-0/0/9 not found</message></xnm:error></interface-information></rpc-reply>`)}

		info, err := reply.Element("interface-information")
		be.NilErr(t, err)
		style, ok := attr(info, xml.Name{Space: "junos", Local: "style"})
		be.True(t, ok)
		be.Equal(t, "terse", style)

		el, err := reply.Element("xnm error")
		be.NilErr(t, err)
		var got struct {
			XMLName xml.Name
			Message string `xml:"message"`
		}
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, xml.Name{Space: "xnm", Local: "error"}, got.XMLName)
		be.Equal(t, "device et-0/0/9 not found", got.Message)
	})

	// an element made by hand, or one nothing was declared around
	t.Run("nothing declared", func(t *testing.T) {
		var got struct {
			X int `xml:"x"`
		}
		be.NilErr(t, Element{Raw: []byte(`<data><x>1</x></data>`)}.Decode(&got))
		be.Equal(t, 1, got.X)

		el, err := (&Reply{Raw: []byte(`<rpc-reply><data><x>2</x></data></rpc-reply>`)}).Element("data")
		be.NilErr(t, err)
		be.NilErr(t, el.Decode(&got))
		be.Equal(t, 2, got.X)
	})
}

// A reply can have thousands of matches, an interface each.  What is
// declared around them is kept once and not once for each: a match costs the
// same whether or not anything is declared.
func TestElementsShareNamespaces(t *testing.T) {
	interfaces := func(open string, n int) *Reply {
		return &Reply{Raw: []byte(open + `<interface-information>` +
			strings.Repeat(`<physical-interface><name>ge-0/0/0</name></physical-interface>`, n) +
			`</interface-information></rpc-reply>`)}
	}
	const (
		declared   = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">`
		undeclared = `<rpc-reply>`
	)

	// what one more match allocates
	perMatch := func(open string) float64 {
		allocs := func(n int) float64 {
			reply := interfaces(open, n)
			return testing.AllocsPerRun(10, func() {
				if _, err := reply.Elements("physical-interface"); err != nil {
					t.Fatal(err)
				}
			})
		}
		return (allocs(1100) - allocs(100)) / 1000
	}
	with, without := perMatch(declared), perMatch(undeclared)
	if with > without+0.01 {
		t.Errorf("a match costs %.2f allocations with namespaces declared around it and %.2f without", with, without)
	}

	found, err := interfaces(declared, 3).Elements("physical-interface")
	be.NilErr(t, err)
	be.Equal(t, 3, len(found))
	be.True(t, found[0].ns != nil)
	be.True(t, found[0].ns == found[1].ns && found[1].ns == found[2].ns)
}
