package netconf

import (
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

// buffered returns raw as a message that was read whole.
func buffered(raw string) *Message {
	return &Message{body: []byte(raw), reader: io.NopCloser(strings.NewReader(raw))}
}

// allocated returns what f allocates, in bytes.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A device decides how many errors a reply holds.  One with more than a program
// can use has failed all the same.
func TestParseReplyKeepsAFixedNumberOfErrors(t *testing.T) {
	raw := `<rpc-reply xmlns="` + NetconfNamespace + `">` + strings.Repeat(`<rpc-error/>`, 100_000) + `</rpc-reply>`

	reply, err := parseReply([]byte(raw))
	be.Nonzero(t, reply)
	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
	be.Equal(t, maxRPCErrors, len(rpcErrs))
}

func TestParseReplyKeepsAFixedNumberOfWarnings(t *testing.T) {
	warning := `<rpc-error><error-severity>warning</error-severity></rpc-error>`
	raw := `<rpc-reply xmlns="` + NetconfNamespace + `">` + strings.Repeat(warning, 5000) + `</rpc-reply>`

	reply, err := parseReply([]byte(raw))
	be.NilErr(t, err)
	be.Equal(t, maxRPCErrors, len(reply.Warnings))
}

// Warnings are no reason to stop: an error can still follow them.
func TestParseReplyFindsTheErrorBehindManyWarnings(t *testing.T) {
	warning := `<rpc-error><error-severity>warning</error-severity></rpc-error>`
	raw := `<rpc-reply xmlns="` + NetconfNamespace + `">` + strings.Repeat(warning, 5000) +
		`<rpc-error><error-severity>error</error-severity></rpc-error></rpc-reply>`

	_, err := parseReply([]byte(raw))
	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
	be.Equal(t, 1, len(rpcErrs))
}

func TestGreetingKeepsAFixedNumberOfErrors(t *testing.T) {
	warnings := strings.Repeat(`<rpc-error><error-severity>warning</error-severity></rpc-error>`, 700)
	reply := `<rpc-reply xmlns="` + NetconfNamespace + `">` + warnings + `</rpc-reply>`

	g, err := readGreeting([]byte(reply + reply + reply))
	be.NilErr(t, err)
	be.Equal(t, maxRPCErrors, len(g.errors))
}

// A reply is read to its end for what it holds, whether or not the name is found
// early in it.
func TestHasElement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		found bool
		fails bool
	}{
		{name: "there", raw: `<rpc-reply><ok/></rpc-reply>`, found: true},
		{name: "nested", raw: `<rpc-reply><results><ok/></results></rpc-reply>`, found: true},
		{name: "not there", raw: `<rpc-reply><no/></rpc-reply>`},
		{name: "not XML behind it", raw: `<rpc-reply><ok/></rpc-rep`, fails: true},
		{name: "not XML in front of it", raw: `<rpc-reply><<ok/></rpc-reply>`, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := (&Reply{Raw: []byte(tc.raw)}).HasElement("ok")
			be.Equal(t, tc.fails, err != nil)
			be.Equal(t, tc.found, found)
		})
	}
}

func TestHasElementInANamespace(t *testing.T) {
	r := &Reply{Raw: []byte(`<rpc-reply xmlns:x="urn:x"><x:ok/></rpc-reply>`)}

	found, err := r.HasElement("urn:x ok")
	be.NilErr(t, err)
	be.True(t, found)

	found, err = r.HasElement("urn:y ok")
	be.NilErr(t, err)
	be.False(t, found)
}

// Looking for one name in a reply of many elements keeps none of them.
func TestHasElementKeepsNothing(t *testing.T) {
	raw := []byte(`<rpc-reply>` + strings.Repeat(`<ok/>`, 200_000) + `</rpc-reply>`)
	r := &Reply{Raw: raw}

	elements := allocated(func() {
		found, err := r.Elements("ok")
		be.NilErr(t, err)
		be.Equal(t, 200_000, len(found))
	})
	has := allocated(func() {
		found, err := r.HasElement("ok")
		be.NilErr(t, err)
		be.True(t, found)
	})
	t.Logf("Elements allocates %d bytes, HasElement %d", elements, has)
	be.True(t, has < elements/2)
}

// A notification keeps one element, whatever it holds.
func TestNotificationOfManyElements(t *testing.T) {
	const count = 200_000
	raw := `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">` +
		`<eventTime>2026-10-31T12:00:00Z</eventTime>` +
		strings.Repeat(`<a/>`, count) + `<last x="y">text</last></notification>`

	var n Notification
	cost := allocated(func() {
		be.NilErr(t, buffered(raw).Decode(&n))
	})

	be.Equal(t, "last", n.Content.XMLName.Local)
	be.Equal(t, "text", string(n.Content.Inner))
	be.Equal(t, 1, len(n.Content.Attr))
	t.Logf("%d bytes of notification allocate %d", len(raw), cost)

	// 185 times with an element kept for each one, most of it garbage in both
	// cases.  This catches the elements coming back.
	be.True(t, cost < 150*uint64(len(raw)))
}

func TestNotificationThatHoldsNothingElse(t *testing.T) {
	raw := `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">` +
		`<eventTime>2026-10-31T12:00:00Z</eventTime></notification>`

	var n Notification
	be.NilErr(t, buffered(raw).Decode(&n))

	be.Equal(t, "", n.Content.XMLName.Local)
	be.Equal(t, 2026, n.EventTime.Year())
}
