package netconftest

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/transport"
	"github.com/lasseh/netconf/transport/transporttest"
)

// scriptedPeer adapts *Transport to the transporttest.Peer contract: the client
// writes are recorded in Outputs() and unsolicited messages are queued with
// Push.
type scriptedPeer struct {
	tr   *Transport
	seen int // number of Outputs already consumed by Recv
}

func (p *scriptedPeer) Send(msg string) {
	p.tr.Push(msg)
}

func (p *scriptedPeer) Recv(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		outputs := p.tr.Outputs()
		if len(outputs) > p.seen {
			msg := outputs[p.seen]
			p.seen++
			return msg
		}
		if time.Now().After(deadline) {
			t.Fatal("peer Recv timed out waiting for client output")
			return ""
		}
		time.Sleep(time.Millisecond)
	}
}

// Upgrade is a no-op: Transport.Upgrade switches the device along with the
// client.
func (p *scriptedPeer) Upgrade() {}

func (p *scriptedPeer) Stall() { p.tr.dev.Stall() }

func TestTransportConformance(t *testing.T) {
	transporttest.Run(t, func(t *testing.T) (transport.Transport, transporttest.Peer) {
		// nil handler so client writes produce no automatic responses; the suite
		// drives both directions explicitly.
		tr := NewTransport(t, nil)
		return tr, &scriptedPeer{tr: tr}
	})
}

func TestTransport_WriteAndRead(t *testing.T) {
	tr := NewTransport(t, func(req string) []string {
		return []string{"reply to: " + req}
	})

	w, err := tr.MsgWriter()
	be.NilErr(t, err)
	_, err = io.WriteString(w, "hello")
	be.NilErr(t, err)
	be.NilErr(t, w.Close())

	r, err := tr.MsgReader()
	be.NilErr(t, err)
	data, err := io.ReadAll(r)
	be.NilErr(t, err)
	be.Equal(t, "reply to: hello", string(data))
	be.NilErr(t, r.Close())

	be.Equal(t, 1, len(tr.Outputs()))
	be.Equal(t, "hello", tr.Outputs()[0])
	be.False(t, tr.Closed())
	be.NilErr(t, tr.Close())
	be.True(t, tr.Closed())
}

// The device follows the client into chunked framing between two messages.
func TestTransport_Upgrade(t *testing.T) {
	tr := NewTransport(t, func(req string) []string {
		return []string{"reply to: " + req}
	})

	exchange := func(msg string) string {
		w, err := tr.MsgWriter()
		be.NilErr(t, err)
		_, err = io.WriteString(w, msg)
		be.NilErr(t, err)
		be.NilErr(t, w.Close())

		r, err := tr.MsgReader()
		be.NilErr(t, err)
		data, err := io.ReadAll(r)
		be.NilErr(t, err)
		be.NilErr(t, r.Close())
		return string(data)
	}

	be.Equal(t, "reply to: one", exchange("one"))
	be.False(t, tr.Upgraded())
	tr.Upgrade()
	be.True(t, tr.Upgraded())
	be.Equal(t, "reply to: two", exchange("two"))
}

// A handler that is still at it when the test ends is waited for: it does
// not go on next to the tests that follow.
func TestTransport_WaitsForHandler(t *testing.T) {
	var finished atomic.Bool

	t.Run("a test with a slow device", func(t *testing.T) {
		started := make(chan struct{})
		tr := NewTransport(t, func(string) []string {
			close(started)
			time.Sleep(50 * time.Millisecond)
			finished.Store(true)
			return nil
		})

		w, err := tr.MsgWriter()
		be.NilErr(t, err)
		_, err = io.WriteString(w, "request")
		be.NilErr(t, err)
		be.NilErr(t, w.Close())
		<-started
	})

	be.True(t, finished.Load())
}

// A device made with Hello says hello by itself and leaves the rest to its
// handler.
func TestHello(t *testing.T) {
	var asked []string
	handler := Hello("<hello>device</hello>", func(req string) []string {
		asked = append(asked, req)
		return []string{"reply to: " + req}
	})

	// whatever prefix the client writes its hello with
	for _, hello := range []string{
		`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"/>`,
		`<?xml version="1.0"?><!-- hi --><nc:hello xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0"/>`,
	} {
		be.Equal(t, 1, len(handler(hello)))
		be.Equal(t, "<hello>device</hello>", handler(hello)[0])
	}
	be.Zero(t, len(asked))

	// an operation that only mentions one is no hello
	const rpc = `<rpc message-id="1"><command>show &lt;hello</command></rpc>`
	be.Equal(t, "reply to: "+rpc, handler(rpc)[0])
	be.Equal(t, 1, len(asked))

	be.Zero(t, len(Hello("<hello/>", nil)(rpc)))
}
