package netconf

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

func nested(depth int) string {
	return strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth)
}

func TestShapeOf(t *testing.T) {
	for _, tc := range []struct {
		name         string
		raw          string
		depth, items int
	}{
		{name: "empty"},
		{name: "text", raw: "no markup"},
		{name: "one element", raw: "<a/>", depth: 1, items: 1},
		{name: "attributes", raw: `<a b="1" c='2'/>`, depth: 1, items: 3},
		{name: "a bracket in a value", raw: `<a b="x>y"/>`, depth: 1, items: 2},
		{name: "an equals sign in a value", raw: `<a b="x=y"/>`, depth: 1, items: 2},
		{name: "nesting", raw: "<a><b><c/></b><d/></a>", depth: 3, items: 4},
		{name: "a run of elements", raw: "<a/><b/><c/>", depth: 1, items: 3},
		{name: "a declaration", raw: `<?xml version="1.0"?><a/>`, depth: 1, items: 1},
		{name: "a comment", raw: "<a><!-- <b><c> --></a>", depth: 1, items: 1},
		{name: "cdata", raw: "<a><![CDATA[<b><c>]]></a>", depth: 1, items: 1},
		{name: "a directive", raw: "<!DOCTYPE a [<!ENTITY b 'c>'>]><a/>", depth: 1, items: 1},
		{name: "not closed", raw: "<a><b", depth: 2, items: 2},
		{name: "an open comment", raw: "<a><!-- <b/>", depth: 1, items: 1},
		{name: "an open quote", raw: `<a b="`, depth: 1, items: 2},
		{name: "nothing behind the bracket", raw: "<a><", depth: 1, items: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			depth, items := shapeOf([]byte(tc.raw))
			be.Equal(t, tc.depth, depth)
			be.Equal(t, tc.items, items)
		})
	}
}

// The walk ends what it skips where encoding/xml does.  A walk that does not is
// out of step behind these, and a message goes past it unseen.
func TestShapeOfPrefixesThatEncodingXMLReadsItsOwnWay(t *testing.T) {
	deep := nested(maxMessageDepth + 1)
	for _, prefix := range []string{
		`<!--><x y="-->`,
		`<!x <y> ' <z w=' >`,
	} {
		t.Run(prefix, func(t *testing.T) {
			be.True(t, errors.Is(checkShape([]byte(prefix+deep)), ErrMessageShape))
		})
	}
}

func TestCheckShape(t *testing.T) {
	be.NilErr(t, checkShape([]byte(nested(maxMessageDepth))))
	be.True(t, errors.Is(checkShape([]byte(nested(maxMessageDepth+1))), ErrMessageShape))
}

// A message of many small elements is refused, one with the density of a real
// reply passes at any size the limit lets through.
func TestCheckShapeDensity(t *testing.T) {
	flat := strings.Repeat("<a/>", 1<<20) // 4 MiB

	be.True(t, errors.Is(checkShape([]byte(flat)), ErrMessageShape))
	be.True(t, errors.Is(checkShape([]byte("<a"+strings.Repeat(` b=""`, 1<<20)+"/>")), ErrMessageShape))

	// under the size nothing is held to it
	be.NilErr(t, checkShape([]byte(flat[:minDenseSize-4])))

	// right at the threshold, and just past it
	entry := "<aaaaaaaaaaaaa/>" // 16 bytes
	be.Equal(t, minBytesPerItem, len(entry))
	be.NilErr(t, checkShape([]byte(strings.Repeat(entry, 1<<17))))
	be.True(t, errors.Is(checkShape([]byte(strings.Repeat("<aaaaaaaaaaaa/>", 1<<17))), ErrMessageShape))
}

// The densest real reply measured is a route table of Junos, at 24.4 bytes per
// item.  Messages of that density pass at the default limit.
func TestCheckShapeRealDensity(t *testing.T) {
	entry := "<rt-entry>abc</rt-entry>" // 24 bytes, one item
	be.Equal(t, 24, len(entry))
	raw := bytes.Repeat([]byte(entry), DefaultMaxMessageSize/len(entry))

	_, items := shapeOf(raw)
	be.Equal(t, len(raw)/len(entry), items)
	be.NilErr(t, checkShape(raw))
}

// tokenShape is shapeOf the long way, as a parser sees the message.  ended says
// that the parser read it to its end.
func tokenShape(raw []byte) (maxDepth, items int, ended bool) {
	depth := 0
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return maxDepth, items, true
		}
		if err != nil {
			return maxDepth, items, false
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			depth++
			maxDepth = max(maxDepth, depth)
			items += 1 + len(tok.Attr)
		case xml.EndElement:
			depth--
		}
	}
}

// The walk has to stay in step with encoding/xml, and a later release of Go can
// change how that reads what the walk skips.  It may count more, on what the parser
// stops at, and never less.
func FuzzShapeOf(f *testing.F) {
	for _, seed := range []string{
		`<a/>`,
		`<a b="1" c='2'><d/>text</a>`,
		`<?xml version="1.0"?><a/>`,
		`<a><!-- <b> --></a>`,
		`<a><![CDATA[<b>]]></a>`,
		`<!DOCTYPE a [<!ENTITY b 'c>'>]><a/>`,
		`<!--><x y="--><a/>`,
		`<!x <y> ' <z w=' ><a/>`,
		`<a b="x>y" c='=' />`,
		`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/></rpc-reply>`,
		`<a:b xmlns:a="urn:a" a:c="d"/>`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		depth, items := shapeOf(raw)
		wantDepth, wantItems, ended := tokenShape(raw)
		if depth < wantDepth || items < wantItems {
			t.Fatalf("the walk counts less than the parser: depth %d of %d, items %d of %d in %q",
				depth, wantDepth, items, wantItems, raw)
		}
		if ended && (depth != wantDepth || items != wantItems) {
			t.Fatalf("the walk disagrees on a document the parser read: depth %d and %d, items %d and %d in %q",
				depth, wantDepth, items, wantItems, raw)
		}
	})
}

// deepDevice is a device that answers the first request with a reply nested
// too deep.
func deepDevice(t *testing.T, dev *netconftest.Device, body string) {
	t.Helper()
	_, _ = dev.Recv()
	_ = dev.WriteRaw(wireHello)
	req, _ := dev.Recv()
	_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), body))
}

func TestMessageNestedTooDeep(t *testing.T) {
	deep := nested(maxMessageDepth + 1)

	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{name: "plain"},
		{name: "behind a comment that is no comment", prefix: `<!--><x y="-->`},
		{name: "behind a directive", prefix: `<!x <y> ' <z w=' >`},
	} {
		t.Run("a reply, "+tc.name, func(t *testing.T) {
			wire, dev := netconftest.NewWire(t)
			go deepDevice(t, dev, tc.prefix+deep)

			sess, err := Open(context.Background(), wire, WithLogger(nil))
			be.NilErr(t, err)

			_, err = sess.ExecReply(testCtx(t), NewRPC("<get/>"))
			be.True(t, errors.Is(err, ErrMessageShape))
			be.True(t, errors.Is(sess.Err(), ErrMessageShape))
			waitDone(t, sess)
		})
	}

	t.Run("a flat reply of 1<<21 elements", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go deepDevice(t, dev, strings.Repeat("<a/>", 1<<21))

		sess, err := Open(context.Background(), wire, WithLogger(nil))
		be.NilErr(t, err)

		_, err = sess.ExecReply(testCtx(t), NewRPC("<get/>"))
		be.True(t, errors.Is(err, ErrMessageShape))
		waitDone(t, sess)
	})

	t.Run("one start tag full of attributes", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go deepDevice(t, dev, "<a"+strings.Repeat(` b=""`, 1<<21)+"/>")

		sess, err := Open(context.Background(), wire, WithLogger(nil))
		be.NilErr(t, err)

		_, err = sess.ExecReply(testCtx(t), NewRPC("<get/>"))
		be.True(t, errors.Is(err, ErrMessageShape))
		waitDone(t, sess)
	})

	t.Run("a reply one level less goes through", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go deepDevice(t, dev, nested(maxMessageDepth-10))

		sess, err := Open(context.Background(), wire, WithLogger(nil))
		be.NilErr(t, err)

		reply, err := sess.ExecReply(testCtx(t), NewRPC("<get/>"))
		be.NilErr(t, err)
		be.Nonzero(t, len(reply.Raw))
	})

	t.Run("a hello", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw(deep + "]]>]]>")
		}()

		_, err := Open(context.Background(), wire, WithLogger(nil))
		be.True(t, errors.Is(err, ErrMessageShape))
	})

	t.Run("a hello behind a comment that is no comment", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw(`<!--><x y="-->` + deep + "]]>]]>")
		}()

		_, err := Open(context.Background(), wire, WithLogger(nil))
		be.True(t, errors.Is(err, ErrMessageShape))
	})
}
