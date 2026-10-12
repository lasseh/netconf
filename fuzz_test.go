package netconf

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lasseh/netconf/internal/netconftest"
)

const wireHello11 = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.1</capability></capabilities><session-id>7</session-id></hello>]]>]]>`

// FuzzSessionReceive hands a session arbitrary bytes as the device's answer
// to an operation, in either framing, followed by a hang-up.  Whatever the
// bytes are, the operation returns and the session ends.
func FuzzSessionReceive(f *testing.F) {
	f.Add([]byte(framed("1", "<ok/>")), false)
	f.Add([]byte(framed("", "<rpc-error><error-severity>error</error-severity></rpc-error>")), false)
	f.Add([]byte(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime></notification>]]>]]>`+framed("1", "<ok/>")), false)
	f.Add([]byte("<!-- session end -->"), false)
	f.Add([]byte("]]>]]>]]>]]><rpc-reply"), false)
	f.Add([]byte("\n#4\n<rpc\n#6\n-reply\n##\n"), true)
	f.Add([]byte("\n#4294967295\n<rpc-reply"), true)
	f.Add([]byte("\n##\n\n##\n"), true)
	f.Fuzz(func(t *testing.T, answer []byte, chunked bool) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			if _, err := dev.Recv(); err != nil {
				return
			}
			hello := wireHello
			if chunked {
				hello = wireHello11
			}
			if err := dev.WriteRaw(hello); err != nil {
				return
			}
			if chunked {
				dev.Upgrade()
			}
			if _, err := dev.Recv(); err != nil {
				return
			}
			_ = dev.WriteRaw(string(answer))
			dev.Hangup()
		}()

		sess, err := Open(context.Background(), wire,
			WithLogger(slog.New(slog.DiscardHandler)),
			WithNotifHandler(func(context.Context, *Message) {}),
		)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		ctx := testCtx(t)
		reply, err := sess.ExecReply(ctx, "<get/>")
		if ctx.Err() != nil {
			t.Fatalf("operation did not return: %v", err)
		}
		if err == nil && len(reply.Raw) == 0 {
			t.Fatal("operation succeeded without a reply")
		}

		waitDone(t, sess)
		if err := sess.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

// FuzzHello hands Open arbitrary bytes where the hello of the device
// belongs, followed by a hang-up.  Whatever the bytes are, Open returns: a
// session that has a session-id and capabilities, or an error that says the
// hello exchange failed.
func FuzzHello(f *testing.F) {
	f.Add([]byte(wireHello))
	f.Add([]byte(wireHello11))
	f.Add([]byte(junosRestrictedHello + "]]>]]>"))
	f.Add([]byte(helloNoSessID + "]]>]]>"))
	f.Add([]byte("Last login: Sun Oct 11 09:20:11 2026\nerror: unknown command & more <"))
	f.Add([]byte(`<rpc-reply><rpc-error><error-severity>warning</error-severity></rpc-error></rpc-reply>` + wireHello))
	f.Add([]byte(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-reply><rpc-error/></rpc-reply><hello/>]]>]]>`))
	f.Fuzz(func(t *testing.T, first []byte) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			if _, err := dev.Recv(); err != nil {
				return
			}
			_ = dev.WriteRaw(string(first))
			dev.Hangup()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sess, err := Open(ctx, wire, WithLogger(slog.New(slog.DiscardHandler)))
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Open did not return for %q", first)
		}
		if err != nil {
			var helloErr *HelloError
			if !errors.Is(err, ErrHello) || !errors.As(err, &helloErr) {
				t.Fatalf("%q failed the hello exchange with %v, which does not say so", first, err)
			}
			return
		}
		if sess.SessionID() == 0 || sess.ServerCaps().Len() == 0 {
			t.Fatalf("%q made a session without a session-id or without capabilities", first)
		}
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelClose()
		_ = sess.Close(closeCtx)
	})
}

// FuzzNotification decodes arbitrary bytes as a notification, from a message
// as a handler does.  Whatever they are, that returns, and a notification
// that decodes with a content has one that decodes too: to the element its
// name says, with what Inner says is in it.
func FuzzNotification(f *testing.F) {
	f.Add([]byte(junosCommitNotification))
	f.Add([]byte(junosCommitStateNotification))
	f.Add([]byte(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0" xmlns:junos="urn:j"><eventTime>2026-10-10T11:52:01Z</eventTime><p:event xmlns:p="urn:e" junos:seconds="1" kind="commit"><p:by junos:style="brief">admin</p:by></p:event></notification>`))
	f.Add([]byte(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><first a="1"/><eventTime>2026-10-10T11:52:01Z</eventTime><second b="2">x</second></notification>`))
	f.Add([]byte(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-10T11:52:01Z</eventTime></notification>`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var notif Notification
		msg := &Message{body: raw, reader: io.NopCloser(bytes.NewReader(raw))}
		if err := msg.Decode(&notif); err != nil || notif.Content.XMLName.Local == "" {
			return
		}

		var content struct {
			XMLName xml.Name
			Inner   []byte `xml:",innerxml"`
		}
		if err := notif.Content.Decode(&content); err != nil {
			t.Fatalf("the content of %q does not decode: %v", raw, err)
		}
		if content.XMLName != notif.Content.XMLName {
			t.Fatalf("the content of %q is %v and decodes as %v", raw, notif.Content.XMLName, content.XMLName)
		}
		if !bytes.Equal(content.Inner, notif.Content.Inner) {
			t.Fatalf("the content of %q has %q in it and decodes with %q", raw, notif.Content.Inner, content.Inner)
		}
	})
}

// FuzzParseReply checks that a reply comes back whole, alone or with the
// errors it carries, and that warnings and errors are told apart.
func FuzzParseReply(f *testing.F) {
	f.Add([]byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1"><ok/></rpc-reply>`))
	f.Add([]byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><commit-results><rpc-error><error-severity>warning</error-severity><error-message>w</error-message></rpc-error></commit-results><ok/></rpc-reply>`))
	f.Add([]byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error><error-type>protocol</error-type><error-info><bad-element>x</bad-element></error-info></rpc-error><rpc-error><error-severity>warning</error-severity></rpc-error></rpc-reply>`))
	f.Add([]byte(`<rpc-reply><data>rpc-error</data></rpc-reply>`))
	f.Add([]byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error>`))
	f.Add([]byte(evolvedARPError))
	f.Add([]byte(evolved23Error))
	f.Add([]byte(junosErrorWithoutMessage))
	f.Add([]byte(evolvedSyslogSeverities))
	f.Fuzz(func(t *testing.T, raw []byte) {
		reply, err := parseReply(raw)

		var rpcErrs RPCErrors
		switch {
		case err == nil:
			if !bytes.Equal(reply.Raw, raw) {
				t.Fatalf("Raw = %q, want the reply as received", reply.Raw)
			}
			for i := range reply.Warnings {
				if reply.Warnings[i].Severity != SevWarning {
					t.Fatalf("%q severity reported as a warning", reply.Warnings[i].Severity)
				}
			}
		case errors.As(err, &rpcErrs):
			if reply == nil || !bytes.Equal(reply.Raw, raw) {
				t.Fatal("the errors came without the reply they are from")
			}
			if len(rpcErrs) == 0 {
				t.Fatal("failed without an error")
			}
			for i := range rpcErrs {
				if rpcErrs[i].Severity == SevWarning {
					t.Fatal("a warning failed the operation")
				}
			}
		default:
			// what cannot be gone through comes back as received
			if reply == nil || !bytes.Equal(reply.Raw, raw) {
				t.Fatalf("no reply as received together with %v", err)
			}
		}
	})
}

// FuzzReplyElements checks that what is found in a reply is part of that
// reply: every match is a piece of the input, in document order, starting at
// a tag and holding its own content.  A match decodes to the element it was
// in the reply: with the name it was looked for under, namespace included,
// and the content it has.
func FuzzReplyElements(f *testing.F) {
	f.Add([]byte(`<rpc-reply><output>a</output><output>b</output></rpc-reply>`), "output")
	f.Add([]byte(junosConfigurationReply), "configuration")
	f.Add([]byte(junosUptimeReply), "http://xml.juniper.net/junos/26.2R1.7/junos date-time")
	f.Add([]byte(`<rpc-reply xmlns="urn:a" xmlns:p="urn:p&amp;&#10;&quot;"><x xmlns="" xmlns:p="urn:q" xmlns:p="urn:r"><p:data p:y="1"/></x><p:data/></rpc-reply>`), "data")
	f.Add([]byte(`<rpc-reply><interface-information><xnm:error xmlns:xmlns="urn:x" xmlns:="y"><message>m</message></xnm:error></interface-information></rpc-reply>`), "xnm error")
	f.Add([]byte(`<rpc-reply><configuration-text>x <output>y</output></configuration-text></rpc-reply>`), "output")
	f.Add([]byte(`<rpc-reply><configuration junos:changed="1"><system/></configuration></rpc-reply>`), "configuration")
	f.Add([]byte(`<rpc-reply><a xmlns="urn:x"><data/></a><data>1</data></rpc-reply>`), "urn:x data")
	f.Add([]byte(`<rpc-reply><output>a</output><output>`), "output")
	f.Fuzz(func(t *testing.T, raw []byte, name string) {
		reply := &Reply{Raw: raw}

		found, err := reply.Elements(name)
		first, firstErr := reply.Element(name)
		if err != nil {
			// where a reply is not XML is a place in the reply
			var syntaxErr *SyntaxError
			if errors.As(err, &syntaxErr) {
				if syntaxErr.Offset < 0 || syntaxErr.Offset > int64(len(raw)) || !bytes.Contains(raw, syntaxErr.Around) {
					t.Fatalf("%q is said to go wrong at byte %d, around %q", raw, syntaxErr.Offset, syntaxErr.Around)
				}
			}
			// A reply that does not parse to its end may still have a
			// first match in front of the damage.
			return
		}

		if len(found) == 0 {
			if !errors.Is(firstErr, ErrElementNotFound) {
				t.Fatalf("nothing found, but Element said %v", firstErr)
			}
			return
		}
		if firstErr != nil || !bytes.Equal(first.Raw, found[0].Raw) {
			t.Fatalf("Element = %q, %v; first of Elements is %q", first.Raw, firstErr, found[0].Raw)
		}

		rest := raw
		for _, el := range found {
			if len(el.Raw) == 0 || el.Raw[0] != '<' {
				t.Fatalf("match %q does not start at a tag", el.Raw)
			}
			if !bytes.Contains(el.Raw, el.Inner) {
				t.Fatalf("content %q is not inside its element %q", el.Inner, el.Raw)
			}
			// matches come in document order and do not overlap
			i := bytes.Index(rest, el.Raw)
			if i < 0 {
				t.Fatalf("match %q is not part of the reply %q", el.Raw, raw)
			}
			rest = rest[i+len(el.Raw):]

			var decoded struct {
				XMLName xml.Name
				Inner   []byte `xml:",innerxml"`
			}
			if err := el.Decode(&decoded); err != nil {
				t.Fatalf("match %q of %q does not decode: %v", el.Raw, raw, err)
			}
			space, local := splitName(name)
			if decoded.XMLName.Local != local || (space != "" && decoded.XMLName.Space != space) {
				t.Fatalf("match %q of %q, looked for as %q, decodes as %q in %q", el.Raw, raw, name, decoded.XMLName.Local, decoded.XMLName.Space)
			}
			if !bytes.Equal(decoded.Inner, el.Inner) {
				t.Fatalf("match %q decodes with the content %q, not %q", el.Raw, decoded.Inner, el.Inner)
			}
		}
	})
}

// byteRefPattern is what joinByteRefs looks for, said another way.
var byteRefPattern = regexp.MustCompile(`&#(12[89]|1[3-9]\d|2[0-4]\d|25[0-5]);`)

// FuzzJoinByteRefs checks what putting the text of a Junos device back
// together must not do: touch a message without a byte written as a
// reference, leave work for a second pass, or turn a message that an XML
// parser reads into one it does not read, or reads other markup from.
func FuzzJoinByteRefs(f *testing.F) {
	for _, seed := range []string{
		`<building>x&#195;&#165;x</building>`,
		`<log>Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund</log>`,
		`x&#240;&#159;&#152;&#128;x&#229;&#195;&#165;`,
		`<a b="&#195;&#165;"><![CDATA[&#195;&#165;]]><!-- &#195;&#165; --><?x &#195;&#165;?></a>`,
		`&#239;&#191;&#190;&#237;&#160;&#128;&#192;&#175;&#226;&#130;`,
		`<!DOCTYPE x "&#195;&#165;"><a>&#195;&amp;&#165;&#0195;&#xC3;&#195</a>`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		got := joinByteRefs(bytes.Clone(doc))

		if !byteRefPattern.Match(doc) && !bytes.Equal(got, doc) {
			t.Fatalf("%q has nothing to join and became %q", doc, got)
		}
		if again := joinByteRefs(bytes.Clone(got)); !bytes.Equal(again, got) {
			t.Fatalf("%q became %q, and then %q", doc, got, again)
		}

		want, err := markup(doc)
		if err != nil {
			return
		}
		have, err := markup(got)
		if err != nil {
			t.Fatalf("%q became %q, which is not XML: %v", doc, got, err)
		}
		if !slices.Equal(want, have) {
			t.Fatalf("%q became %q, with other markup", doc, got)
		}
	})
}

// FuzzStripInvalidChars checks the repair for a message with something in it
// that XML does not allow.  It leaves nothing for a second pass, does not
// touch a message that has nothing of the kind, and only takes away.  A
// parser does not fail over a character in what is left, and a message that
// a parser could read before has the elements and attributes it had.
func FuzzStripInvalidChars(f *testing.F) {
	for _, seed := range []string{
		netconftest.Reply("1", lldpReply),
		"<a b=\"x\x01y&#1;z\">a&#0;b&#x1b;c&#xFFFE;d&#xD800;e&#x110000;f&#99999999999999999999;g</a>",
		"<a>a\xffb\xc3c\xe2\x82d\xed\xa0\x80e\uFFFEf\uFFFFg\uFFFDh</a>",
		"<!-- &#1; \x01 --><a><![CDATA[&#1; \x01]]><?pi &#1; \x01?></a>",
		"<!DOCTYPE x \"&#1;\"><a xmlns:p=\"u&#xD800;\"><p:b/>&#;&#x;&#1 &#x1g; &amp;#1; &#1</a>",
		"<a><![CDATA[&#1;\x01",
		// what is left of a reference with something in it is a reference
		"&#0\xea;",
		// and of a reference in a reference, many deep
		"<a>&&&&&&&&&&&&#1;#1;#1;#1;#1;#1;#1;#1;#1;#1;#1;#1;</a>",
		// what is left can be markup that was none, and in it more of it
		"<a><\x01!-- <? --> <\x01!-- <? --> <\x01!-- <? --> \x01 ?> ?> ?></a>",
		"<a><!\x01[CDATA[&#1;\x02]\x01]>&#1;</a>",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		got := StripInvalidChars(bytes.Clone(doc))

		if again := StripInvalidChars(bytes.Clone(got)); !bytes.Equal(again, got) {
			t.Fatalf("%q became %q, and then %q", doc, got, again)
		}

		// what is left is what was there, in order
		rest := doc
		for _, b := range got {
			i := bytes.IndexByte(rest, b)
			if i < 0 {
				t.Fatalf("%q became %q, which has something new in it", doc, got)
			}
			rest = rest[i+1:]
		}
		clean := utf8.Valid(doc) && !bytes.Contains(doc, []byte("&#"))
		for _, r := range string(doc) {
			clean = clean && isXMLChar(r)
		}
		if clean && !bytes.Equal(got, doc) {
			t.Fatalf("%q has nothing to take out and became %q", doc, got)
		}

		// A directive is markup the repair knows nothing of: it and a
		// parser may differ on where one ends.  No message of the protocol
		// has one.
		if hasDirective(doc) || hasDirective(got) {
			return
		}

		have, haveErr := markup(got)
		var xmlErr *xml.SyntaxError
		if errors.As(haveErr, &xmlErr) && (strings.HasPrefix(xmlErr.Msg, "illegal character code") || xmlErr.Msg == "invalid UTF-8") {
			t.Fatalf("%q became %q, which a parser still refuses: %v", doc, got, haveErr)
		}

		want, err := markup(doc)
		if err != nil {
			return
		}
		if haveErr != nil {
			t.Fatalf("%q became %q, which is not XML: %v", doc, got, haveErr)
		}
		if !slices.Equal(names(want), names(have)) {
			t.Fatalf("%q became %q, with other elements or attributes", doc, got)
		}
	})
}

// hasDirective reports whether doc has what may be a directive in it: a "<!"
// that begins neither a comment nor a CDATA section.
func hasDirective(doc []byte) bool {
	for {
		i := bytes.Index(doc, []byte("<!"))
		if i < 0 {
			return false
		}
		doc = doc[i:]
		if !bytes.HasPrefix(doc, []byte("<!--")) && !bytes.HasPrefix(doc, cdataBegin) {
			return true
		}
		doc = doc[2:]
	}
}

// names returns the elements and attributes among the tokens that markup
// lists, without their namespaces: the value of a declaration is text, which
// a repair may change.
func names(tokens []string) []string {
	var out []string
	for _, tok := range tokens {
		if tok[0] == '<' || tok[0] == '/' || tok[0] == '@' {
			out = append(out, tok[:1]+tok[strings.LastIndexByte(tok, ' ')+1:])
		}
	}
	return out
}

// markup lists the tokens of doc that are not text, with the names of the
// elements and attributes.
func markup(doc []byte) ([]string, error) {
	var tokens []string
	d := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return tokens, nil
		}
		if err != nil {
			return nil, err
		}

		switch tok := tok.(type) {
		case xml.StartElement:
			tokens = append(tokens, "<"+tok.Name.Space+" "+tok.Name.Local)
			for _, attr := range tok.Attr {
				tokens = append(tokens, "@"+attr.Name.Space+" "+attr.Name.Local)
			}
		case xml.EndElement:
			tokens = append(tokens, "/"+tok.Name.Space+" "+tok.Name.Local)
		case xml.Comment:
			tokens = append(tokens, "!"+string(tok))
		case xml.ProcInst:
			tokens = append(tokens, "?"+tok.Target+" "+string(tok.Inst))
		case xml.Directive:
			tokens = append(tokens, "directive")
		}
	}
}
