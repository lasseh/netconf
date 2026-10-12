package netconf

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

// A reply with a character XML does not allow.  It is written by hand:
// Junos 26.2 sends a reference to another character in place of a control
// character, in a configuration and in the output of a command, so no such
// reply came from the lab.  The reports are about text a device passes on,
// the description an LLDP neighbour sends.
const (
	lldpDescription = "Vendor OS 1.2\x01build 7"
	lldpReply       = `<lldp-neighbors-information><lldp-neighbor-information>` +
		`<lldp-remote-system-name>core1</lldp-remote-system-name>` +
		`<lldp-remote-system-description>` + lldpDescription + `</lldp-remote-system-description>` +
		`</lldp-neighbor-information></lldp-neighbors-information>`
)

type lldpNeighbors struct {
	Description string `xml:"lldp-neighbors-information>lldp-neighbor-information>lldp-remote-system-description"`
}

// A message that is not XML fails with where it is not, and what is there.
func TestSyntaxError(t *testing.T) {
	raw := netconftest.Reply("7", lldpReply)
	reply := &Reply{Raw: []byte(raw)}
	at := int64(strings.IndexByte(raw, 0x01))

	located := func(t *testing.T, err error) *SyntaxError {
		t.Helper()
		var syntaxErr *SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("no SyntaxError in: %v", err)
		}
		// the error of the parser is still there
		var xmlErr *xml.SyntaxError
		be.True(t, errors.As(err, &xmlErr))
		return syntaxErr
	}

	t.Run("looking for an element", func(t *testing.T) {
		_, err := reply.Element("lldp-remote-system-description")
		syntaxErr := located(t, err)
		be.Equal(t, at, syntaxErr.Offset)
		be.In(t, "Vendor OS 1.2\x01build 7", string(syntaxErr.Around))
		be.True(t, len(syntaxErr.Around) < 100)

		// on one line, with the offset.  The bytes are in Around for who
		// asks for them and not in the text, which goes where it is logged.
		be.In(t, "illegal character code U+0001", err.Error())
		be.In(t, "at byte "+strconv.FormatInt(at, 10), err.Error())
		be.False(t, strings.Contains(err.Error(), "Vendor OS"))
		be.False(t, strings.ContainsAny(err.Error(), "\x01\n"))

		_, err = reply.Elements("lldp-remote-system-name")
		be.Equal(t, at, located(t, err).Offset)
	})

	t.Run("decoding the reply", func(t *testing.T) {
		var v lldpNeighbors
		be.Equal(t, at, located(t, reply.Decode(&v)).Offset)
	})

	// the offset is into what was being read: the element, not the reply
	t.Run("decoding an element", func(t *testing.T) {
		el := Element{Raw: []byte("<b>ab\x01cd</b>"), ns: (*scope)(nil).inside([]xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: "urn:x"}})}
		var v struct {
			Text string `xml:",chardata"`
		}
		be.Equal(t, int64(5), located(t, el.Decode(&v)).Offset)
		be.Equal(t, int64(5), located(t, Element{Raw: el.Raw}.Decode(&v)).Offset)
	})

	t.Run("a reference to such a character", func(t *testing.T) {
		raw := netconftest.Reply("7", `<output>ab&#27;cd &#x1b;</output>`)
		_, err := (&Reply{Raw: []byte(raw)}).Element("output")
		syntaxErr := located(t, err)
		be.Equal(t, int64(strings.Index(raw, "&#27;")), syntaxErr.Offset)
		be.In(t, "ab&#27;cd", string(syntaxErr.Around))
	})

	t.Run("bytes that are not UTF-8", func(t *testing.T) {
		raw := netconftest.Reply("7", "<output>ab\xffcd</output>")
		_, err := (&Reply{Raw: []byte(raw)}).Element("output")
		be.Equal(t, int64(strings.IndexByte(raw, 0xff)), located(t, err).Offset)
	})

	// anything else that is wrong is where the parser stopped
	t.Run("an element that does not end", func(t *testing.T) {
		raw := `<rpc-reply><data><a></data></rpc-reply>`
		_, err := (&Reply{Raw: []byte(raw)}).Element("data")
		syntaxErr := located(t, err)
		be.Equal(t, int64(strings.Index(raw, "</data>")+len("</data>")), syntaxErr.Offset)
		be.In(t, "<a></data>", string(syntaxErr.Around))
	})

	t.Run("at the ends of a message", func(t *testing.T) {
		_, err := (&Reply{Raw: []byte("\x01<rpc-reply/>")}).Element("data")
		be.Equal(t, int64(0), located(t, err).Offset)

		long := "<rpc-reply><data>" + strings.Repeat("x", 500) + "\x01"
		_, err = (&Reply{Raw: []byte(long)}).Element("data")
		syntaxErr := located(t, err)
		be.Equal(t, int64(len(long)-1), syntaxErr.Offset)
		be.True(t, strings.HasSuffix(string(syntaxErr.Around), "xxx\x01"))
	})

	// what is missing is not a syntax error
	t.Run("not for a reply that is XML", func(t *testing.T) {
		_, err := (&Reply{Raw: []byte(`<rpc-reply><ok/></rpc-reply>`)}).Element("data")
		var syntaxErr *SyntaxError
		be.False(t, errors.As(err, &syntaxErr))
	})
}

// A reply that the session looks into for errors, and cannot read, comes
// back with the error: what the device sent is still there to look at.
func TestExecReplyReturnsUnreadableReply(t *testing.T) {
	// the description has the text "rpc-error" in it, so the reply is gone
	// through for errors
	const body = `<output>rpc-error counters` + "\x01" + `</output>`
	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, body) })

	reply, err := sess.ExecReply(testCtx(t), "<get/>")
	var syntaxErr *SyntaxError
	be.True(t, errors.As(err, &syntaxErr))
	be.Nonzero(t, reply)
	be.Equal(t, netconftest.Reply("1", body), string(reply.Raw))
	be.Equal(t, int64(strings.IndexByte(string(reply.Raw), 0x01)), syntaxErr.Offset)

	// and the session goes on
	be.Nonzero(t, sess.Exec(testCtx(t), "<get/>", &struct{}{}))
	be.NilErr(t, sess.Err())
}

// With WithReplyRepair a function of the caller's sees each buffered
// message before the session reads it.  StripInvalidChars is one that takes
// out what XML does not allow.
func TestWithReplyRepair(t *testing.T) {
	respond := func(msgID, _ string) string { return framed(msgID, lldpReply) }

	t.Run("without it no reply is changed", func(t *testing.T) {
		sess := wireSession(t, respond)

		reply, err := sess.ExecReply(testCtx(t), "<get-lldp-neighbors-information/>")
		be.NilErr(t, err)
		be.Equal(t, netconftest.Reply("1", lldpReply), string(reply.Raw))

		var v lldpNeighbors
		var syntaxErr *SyntaxError
		be.True(t, errors.As(sess.Exec(testCtx(t), "<get-lldp-neighbors-information/>", &v), &syntaxErr))
	})

	t.Run("a reply decodes", func(t *testing.T) {
		sess := wireSession(t, respond, WithReplyRepair(StripInvalidChars))

		var v lldpNeighbors
		be.NilErr(t, sess.Exec(testCtx(t), "<get-lldp-neighbors-information/>", &v))
		be.Equal(t, "Vendor OS 1.2build 7", v.Description)

		reply, err := sess.ExecReply(testCtx(t), "<get-lldp-neighbors-information/>")
		be.NilErr(t, err)
		be.False(t, strings.Contains(string(reply.Raw), "\x01"))
	})

	t.Run("a notification decodes", func(t *testing.T) {
		const notification = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime><event><text>ab` + "\x1b" + `cd</text></event></notification>]]>]]>`

		got := make(chan string, 1)
		sess := wireSession(t, func(msgID, _ string) string { return notification + framed(msgID, "<ok/>") },
			WithReplyRepair(StripInvalidChars),
			WithNotifHandler(func(_ context.Context, msg *Message) {
				var notif Notification
				if err := msg.Decode(&notif); err != nil {
					t.Errorf("decode: %v", err)
				}
				var event struct {
					Text string `xml:"text"`
				}
				if err := notif.Content.Decode(&event); err != nil {
					t.Errorf("decode content: %v", err)
				}
				got <- event.Text
			}))
		be.NilErr(t, sess.Exec(testCtx(t), "<create-subscription/>", nil))

		select {
		case text := <-got:
			be.Equal(t, "abcd", text)
		case <-testCtx(t).Done():
			t.Fatal("no notification")
		}
	})

	// The function gets the message as the device sent it.  The text of a
	// Junos device is put back together after it: here the control
	// character sits between the two bytes of an "ø".
	t.Run("before the text of Junos is put back together", func(t *testing.T) {
		var saw string
		sess := wireSessionAfter(t, junosHello,
			func(msgID, _ string) string { return framed(msgID, "<output>Troms&#195;\x01&#184;</output>") },
			WithReplyRepair(func(msg []byte) []byte {
				saw = string(msg)
				return StripInvalidChars(msg)
			}))

		reply, err := sess.ExecReply(testCtx(t), "<get/>")
		be.NilErr(t, err)
		be.In(t, "Troms&#195;\x01&#184;", saw)
		be.In(t, "<output>Tromsø</output>", string(reply.Raw))
	})

	t.Run("a streamed reply is the caller's", func(t *testing.T) {
		sess := wireSession(t, respond, WithReplyRepair(StripInvalidChars))

		msg, err := sess.Stream(testCtx(t), "<get-lldp-neighbors-information/>")
		be.NilErr(t, err)
		var v lldpNeighbors
		be.Nonzero(t, msg.Decode(&v))
	})
}

func TestStripInvalidChars(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"nothing to take out", "<a b=\"c\">tab\there\r\nÅ 日本 😀 &amp; &#229; &#xE5; &#9; &#x20;</a>", ""},
		{"control characters", "<a>a\x00b\x01c\x08d\x0be\x0cf\x0eg\x1fh</a>", "<a>abcdefgh</a>"},
		{"what is no character to XML", "<a>a￾b￿c�d</a>", "<a>abc�d</a>"},
		{"bytes that are not UTF-8", "<a>a\xffb\xc3c\xe2\x82d\xed\xa0\x80e</a>", "<a>abcde</a>"},
		{"in an attribute", "<a b=\"x\x01y&#1;z\"/>", "<a b=\"xyz\"/>"},
		{"references", "<a>a&#0;b&#1;c&#x1b;d&#x1B;e&#27;f&#xFFFE;g&#65535;h&#xD800;i&#x110000;j&#99999999999999999999;k</a>", "<a>abcdefghijk</a>"},
		{"references XML allows", "<a>&#9;&#10;&#13;&#32;&#xD7FF;&#xE000;&#xFFFD;&#x10000;&#x10FFFF;</a>", ""},
		{"what is no reference", "<a>&#;&#x;&#1 &#x1g; &amp;#1; &#1</a>", ""},
		{
			"no reference where a parser reads none",
			"<!-- &#1; \x01 --><a><![CDATA[&#1; \x01]]><?pi &#1; \x01?>\x01</a>",
			"<!-- &#1; \x01 --><a><![CDATA[&#1; ]]><?pi &#1; \x01?></a>",
		},
		{"markup that does not end", "<a><![CDATA[&#1;\x01", "<a><![CDATA[&#1;"},
		{"a comment that does not end", "<a>\x01<!-- \x01", "<a><!-- \x01"},
		{"references in references", "<a>a&&&#1;#1;#1;b&#&#1;1;c&#\x01&#1;2;d</a>", "<a>abcd</a>"},
		{"a reference through markup is none", "<a>&#1<b/>;&#1<!-- x -->;</a>", ""},
		{
			"markup that what goes leaves behind",
			"<a><\x01!-- \x02 &#1; --><&#1;?pi \x02?><!\x01[CDATA[&#1;\x02]\x01]>&#1;</a>",
			"<a><!-- \x02 &#1; --><?pi \x02?><![CDATA[&#1;]]></a>",
		},
		{
			"markup in markup that what goes leaves behind",
			"<a>" + strings.Repeat("<\x01!-- <? --> ", 12) + "\x01" + strings.Repeat(" ?>", 12) + "</a>",
			"<a>" + strings.Repeat("<!-- <? --> ", 12) + strings.Repeat(" ?>", 12) + "</a>",
		},
		{"the end of a CDATA section that what goes leaves behind", "<a><![CDATA[x]\x01]>&#1;\x01</a>", "<a><![CDATA[x]]></a>"},
		{"at the ends", "\x01<a/>\x01", "<a/>"},
		{"nothing at all", "", ""},
		{"nothing left", "\x01\x02", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" && tc.name != "nothing left" {
				want = tc.in
			}
			in := []byte(tc.in)
			got := StripInvalidChars(in)
			be.Equal(t, want, string(got))
			// and there is nothing more to take out of that
			be.Equal(t, want, string(StripInvalidChars(bytes.Clone(got))))

			// a message with nothing in it to take out is not copied
			if want == tc.in && len(in) > 0 {
				be.True(t, &got[0] == &in[0])
			}
		})
	}
}

// A message made to keep the repair busy: a reference inside a reference
// inside a reference, a quarter of a million deep.  One pass takes them all.
func TestStripInvalidCharsNested(t *testing.T) {
	const depth = 1 << 18
	msg := []byte("<a>" + strings.Repeat("&", depth) + strings.Repeat("#1;", depth) + "</a>")

	done := make(chan []byte, 1)
	go func() { done <- StripInvalidChars(msg) }()
	select {
	case got := <-done:
		be.Equal(t, "<a></a>", string(got))
	case <-time.After(10 * time.Second):
		t.Fatal("the repair is still at it")
	}
}
