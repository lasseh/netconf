// Package netconftest provides scripted NETCONF devices for tests.
package netconftest

import (
	"encoding/xml"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lasseh/netconf/transport"
)

// RequestHandler is called for each message written by the client.
// It receives the request and returns zero or more responses.
// Returning nil or empty slice means no response.
type RequestHandler func(req string) []string

// Transport is a Wire whose device answers whole messages through a
// RequestHandler, for tests that script a conversation rather than bytes.
// Use Push to queue unsolicited messages like notifications.
type Transport struct {
	*Wire
	t       testing.TB
	dev     *Device
	handler RequestHandler

	mu       sync.Mutex
	wake     *sync.Cond // signals outbox/closed changes
	outputs  []string
	outbox   []string
	closed   bool
	upgraded bool
	// chunks says that the hello of the client offered chunked framing
	chunks bool
}

// chunked is the capability two peers must both have to chunk their
// messages.
const chunked = "urn:ietf:params:netconf:base:1.1"

// isHello reports whether msg is a hello, whatever prefix it is written
// with.
func isHello(msg string) bool {
	decoder := xml.NewDecoder(strings.NewReader(msg))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Local == "hello"
		}
	}
}

// Hello returns a handler for a device that answers the hello of the client
// with hello, and every message after it with what handler returns.  A nil
// handler answers nothing else.
func Hello(hello string, handler RequestHandler) RequestHandler {
	return func(req string) []string {
		switch {
		case isHello(req):
			return []string{hello}
		case handler == nil:
			return nil
		}
		return handler(req)
	}
}

// NewTransport returns a transport to a device that answers each client
// message with what handler returns.  A nil handler never answers.
func NewTransport(t testing.TB, handler RequestHandler) *Transport {
	t.Helper()

	// The device is part of the test: a handler that is still at it would go
	// on next to the tests that follow, and may report to a test that is
	// over.  So the test waits for the device, last of all: after the
	// transport is closed and the wire is gone, which is what stops it.
	var device sync.WaitGroup
	t.Cleanup(func() {
		stopped := make(chan struct{})
		go func() {
			device.Wait()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(stopTimeout):
			t.Errorf("netconftest: the device is still running %s after the test: is a handler stuck?", stopTimeout)
		}
	})

	wire, dev := NewWire(t)
	tr := &Transport{Wire: wire, t: t, dev: dev, handler: handler}
	tr.wake = sync.NewCond(&tr.mu)

	device.Go(tr.receive)
	device.Go(tr.deliver)
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// stopTimeout is how long the device gets to stop when the test is over.
const stopTimeout = 5 * time.Second

// receive records what the client sends and queues the handler's answers.
func (t *Transport) receive() {
	for {
		msg, err := t.dev.Recv()
		if err != nil {
			return
		}

		t.mu.Lock()
		t.outputs = append(t.outputs, msg)
		if isHello(msg) {
			t.chunks = strings.Contains(msg, chunked)
		}
		t.mu.Unlock()

		if t.handler != nil {
			t.Push(t.handler(msg)...)
		}
	}
}

// deliver sends queued messages to the client, one at a time.
func (t *Transport) deliver() {
	for {
		t.mu.Lock()
		for len(t.outbox) == 0 && !t.closed {
			t.wake.Wait()
		}
		if t.closed {
			t.mu.Unlock()
			return
		}
		msg := t.outbox[0]
		t.outbox = t.outbox[1:]
		// A hello that settles on chunked framing is the last message of the
		// device in the old one.  The device switches with it and does not
		// wait for the client to do so: what is queued behind the hello would
		// go out in whichever framing the client had got to by then.
		last := isHello(msg) && strings.Contains(msg, chunked) && t.chunks
		t.mu.Unlock()

		if err := t.dev.send(msg, last); err != nil {
			// The framing refusing a scripted message is a mistake in the
			// test, and would otherwise show as a client waiting for ever.
			if errors.Is(err, transport.ErrEndOfMsgInPayload) {
				t.t.Errorf("netconftest: scripted message cannot be framed: %v", err)
			}
			return
		}
	}
}

// Push queues messages for the client, independently of any request.
func (t *Transport) Push(msgs ...string) {
	t.mu.Lock()
	t.outbox = append(t.outbox, msgs...)
	t.mu.Unlock()
	t.wake.Broadcast()
}

// Upgrade switches both the client and the device to chunked framing.  A
// device that has said hello to a client that chunks has switched already.
func (t *Transport) Upgrade() {
	t.mu.Lock()
	t.upgraded = true
	t.mu.Unlock()

	t.Wire.Upgrade()
	t.dev.Upgrade()
}

// Upgraded reports whether Upgrade was called.
func (t *Transport) Upgraded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upgraded
}

// Close closes the client side of the transport.
func (t *Transport) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.wake.Broadcast()

	return t.Wire.Close()
}

// Outputs returns the messages the client sent to the server.
func (t *Transport) Outputs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.outputs)
}

// Closed returns whether the transport has been closed.
func (t *Transport) Closed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// ExtractMessageID extracts the message-id attribute from an XML message.
func ExtractMessageID(msg string) string {
	decoder := xml.NewDecoder(strings.NewReader(msg))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return ""
		}
		if start, ok := tok.(xml.StartElement); ok {
			for _, attr := range start.Attr {
				if attr.Name.Local == "message-id" {
					return attr.Value
				}
			}
			return ""
		}
	}
}

// Reply creates an rpc-reply with the given message-id and body.
func Reply(msgID, body string) string {
	return `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="` + msgID + `">` + body + `</rpc-reply>`
}

// OKReply creates an rpc-reply with <ok/> for the given message-id.
func OKReply(msgID string) string {
	return Reply(msgID, "<ok/>")
}
