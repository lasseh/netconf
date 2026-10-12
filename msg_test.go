package netconf

import (
	"encoding/xml"
	"errors"
	"testing"

	"github.com/carlmjohnson/be"
)

var rawXMLTests = []struct {
	name        string
	element     RawXML
	xml         []byte
	noUnmarshal bool
}{
	{
		name:    "empty",
		element: RawXML(""),
		xml:     []byte("<RawXML></RawXML>"),
	},
	{
		name:    "nil",
		element: nil,
		xml:     []byte("<RawXML></RawXML>"),
		// we will never unmarshal back into a nil (see "empty" testcase)
		noUnmarshal: true,
	},
	{
		name:    "textElement",
		element: RawXML("A man a plan a canal panama"),
		xml:     []byte("<RawXML>A man a plan a canal panama</RawXML>"),
	},
	{
		name:    "xml",
		element: RawXML("<foo><bar>hamburger</bar></foo>"),
		xml:     []byte("<RawXML><foo><bar>hamburger</bar></foo></RawXML>"),
	},
}

func TestRawXMLUnmarshal(t *testing.T) {
	for _, tc := range rawXMLTests {
		if tc.noUnmarshal {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			var got RawXML
			err := xml.Unmarshal(tc.xml, &got)
			be.NilErr(t, err)
			be.Equal(t, string(tc.element), string(got))
		})
	}
}

func TestRawXMLMarshal(t *testing.T) {
	for _, tc := range rawXMLTests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := xml.Marshal(&tc.element)
			be.NilErr(t, err)
			be.Equal(t, string(tc.xml), string(got))
		})
	}
}

var helloMsgTestTable = []struct {
	name string
	raw  []byte
	msg  helloMessage
}{
	{
		name: "basic",
		raw:  []byte(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability><capability>urn:ietf:params:netconf:base:1.1</capability></capabilities></hello>`),
		msg: helloMessage{
			XMLName: xml.Name{
				Local: "hello",
				Space: "urn:ietf:params:xml:ns:netconf:base:1.0",
			},
			Capabilities: []string{
				"urn:ietf:params:netconf:base:1.0",
				"urn:ietf:params:netconf:base:1.1",
			},
		},
	},
	{
		name: "junos",
		raw: []byte(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
      <capability>urn:ietf:params:netconf:base:1.0</capability>
	  <capability>urn:ietf:params:netconf:capability:candidate:1.0</capability>
	  <capability>urn:ietf:params:netconf:capability:confirmed-commit:1.0</capability>
	  <capability>urn:ietf:params:netconf:capability:validate:1.0</capability>
	  <capability>urn:ietf:params:netconf:capability:url:1.0?scheme=http,ftp,file</capability>
	  <capability>urn:ietf:params:xml:ns:netconf:base:1.0</capability>
	  <capability>urn:ietf:params:xml:ns:netconf:capability:candidate:1.0</capability>
	  <capability>urn:ietf:params:xml:ns:netconf:capability:confirmed-commit:1.0</capability>
	  <capability>urn:ietf:params:xml:ns:netconf:capability:validate:1.0</capability>
	  <capability>urn:ietf:params:xml:ns:netconf:capability:url:1.0?scheme=http,ftp,file</capability>
	  <capability>urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring</capability>
	  <capability>http://xml.juniper.net/netconf/jdm/1.0</capability>
  </capabilities>
  <session-id>410</session-id>
</hello>`),
		msg: helloMessage{
			XMLName: xml.Name{
				Local: "hello",
				Space: "urn:ietf:params:xml:ns:netconf:base:1.0",
			},
			Capabilities: []string{
				"urn:ietf:params:netconf:base:1.0",
				"urn:ietf:params:netconf:capability:candidate:1.0",
				"urn:ietf:params:netconf:capability:confirmed-commit:1.0",
				"urn:ietf:params:netconf:capability:validate:1.0",
				"urn:ietf:params:netconf:capability:url:1.0?scheme=http,ftp,file",
				"urn:ietf:params:xml:ns:netconf:base:1.0",
				"urn:ietf:params:xml:ns:netconf:capability:candidate:1.0",
				"urn:ietf:params:xml:ns:netconf:capability:confirmed-commit:1.0",
				"urn:ietf:params:xml:ns:netconf:capability:validate:1.0",
				"urn:ietf:params:xml:ns:netconf:capability:url:1.0?scheme=http,ftp,file",
				"urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring",
				"http://xml.juniper.net/netconf/jdm/1.0",
			},
			SessionID: 410,
		},
	},
}

func TestUnmarshalHelloMsg(t *testing.T) {
	for _, tc := range helloMsgTestTable {
		t.Run(tc.name, func(t *testing.T) {
			var got helloMessage
			err := xml.Unmarshal(tc.raw, &got)
			be.NilErr(t, err)
			be.Equal(t, tc.msg.SessionID, got.SessionID)
			be.Equal(t, len(tc.msg.Capabilities), len(got.Capabilities))
		})
	}
}
func TestMarshalHelloMsg(t *testing.T) {
	for _, tc := range helloMsgTestTable {
		t.Run(tc.name, func(t *testing.T) {
			out, err := xml.Marshal(tc.msg)
			t.Logf("out: %s", out)
			be.NilErr(t, err)
		})
	}
}

func TestMarshalRPCMsg(t *testing.T) {
	tt := []struct {
		name      string
		operation any
		err       bool
		want      []byte
	}{
		{
			name:      "string",
			operation: "<foo><bar/></foo>",
			want:      []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1"><foo><bar/></foo></rpc>`),
		},
		{
			name:      "byteslice",
			operation: []byte("<baz><qux/></baz>"),
			want:      []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1"><baz><qux/></baz></rpc>`),
		},
		{
			name: "namedStruct",
			operation: struct {
				XMLName xml.Name `xml:"http://xml.juniper.net/junos/22.4R0/junos command"`
				Command string   `xml:",innerxml"`
			}{
				Command: "show bgp neighbors",
			},
			want: []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1"><command xmlns="http://xml.juniper.net/junos/22.4R0/junos">show bgp neighbors</command></rpc>`),
		},
		/*
			{
				name: "unnamedStruct",
				operation: struct {
					Command string `xml:"command"`
				}{
					Command: "show version",
				},
			},*/
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			out, err := xml.Marshal(&RPC{
				MessageID: "1",
				Operation: tc.operation,
			})
			t.Logf("out: %s", out)

			if tc.err {
				be.Nonzero(t, err)
				return
			}

			be.NilErr(t, err)
			be.Equal(t, string(tc.want), string(out))
		})
	}
}

var replyJunosGetConfigError = []byte(`
<rpc-reply xmlns:junos="http://xml.juniper.net/junos/20.3R0/junos" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1">
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>syntax error, expecting &lt;candidate/&gt; or &lt;running/&gt;</error-message>
<error-info>
<bad-element>non-exist</bad-element>
</error-info>
</rpc-error>
</rpc-reply>
`)

// A reply decoded into a struct that embeds RPCReply gets the envelope.  The
// errors of the reply are what parseReply returns, with all there is to them.
func TestUnmarshalRPCReply(t *testing.T) {
	var got RPCReply
	be.NilErr(t, xml.Unmarshal(replyJunosGetConfigError, &got))
	be.Equal(t, "1", got.MessageID)
	be.Equal(t, xml.Name{Space: NetconfNamespace, Local: "rpc-reply"}, got.XMLName)

	_, err := parseReply(replyJunosGetConfigError)
	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, ErrTypeProtocol, rpcErr.Type)
	be.Equal(t, ErrOperationFailed, rpcErr.Tag)
	be.Equal(t, SevError, rpcErr.Severity)
	be.Equal(t, "syntax error, expecting <candidate/> or <running/>", rpcErr.Message)

	bad, ok := rpcErr.Detail("bad-element")
	be.True(t, ok)
	be.Equal(t, "non-exist", bad)
}
