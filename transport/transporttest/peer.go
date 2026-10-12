package transporttest

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lasseh/netconf/transport"
)

// StreamPeer is a Peer for transports that carry RFC6242 framing on a byte
// stream.  It speaks the same framing on the device's end of that stream.
type StreamPeer struct {
	fr    *transport.Framer
	stall func()

	// outbox holds the messages still to be sent, wake signals a change to
	// it or to done
	mu     sync.Mutex
	wake   *sync.Cond
	outbox []string
	done   bool
}

// NewStreamPeer returns a Peer on rw, the device's end of the byte stream
// under the transport being tested.
//
// stall is what makes the device go silent, see Stall.  It may be nil for a
// stream that hands over bytes only when they are read, such as io.Pipe: not
// reading is silence there.  A stream with buffers in between, such as a TCP
// connection, needs a Relay.
func NewStreamPeer(t *testing.T, rw io.ReadWriter, stall func()) *StreamPeer {
	t.Helper()

	p := &StreamPeer{fr: transport.NewFramer(rw, rw), stall: stall}
	p.wake = sync.NewCond(&p.mu)

	go p.sendLoop()
	t.Cleanup(func() {
		p.mu.Lock()
		p.done = true
		p.mu.Unlock()
		p.wake.Broadcast()
	})
	return p
}

// sendLoop writes the queued messages one at a time, so Send never waits for
// the client to read.
func (p *StreamPeer) sendLoop() {
	for {
		p.mu.Lock()
		for len(p.outbox) == 0 && !p.done {
			p.wake.Wait()
		}
		if p.done {
			p.mu.Unlock()
			return
		}
		msg := p.outbox[0]
		p.outbox = p.outbox[1:]
		p.mu.Unlock()

		w, err := p.fr.MsgWriter()
		if err != nil {
			return
		}
		_, _ = io.WriteString(w, msg)
		if err := w.Close(); err != nil {
			return
		}
	}
}

// Send queues one complete message for the client.
func (p *StreamPeer) Send(msg string) {
	p.mu.Lock()
	p.outbox = append(p.outbox, msg)
	p.mu.Unlock()
	p.wake.Broadcast()
}

// Recv returns the next complete message written by the client.
func (p *StreamPeer) Recv(t *testing.T) string {
	t.Helper()

	type result struct {
		msg string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := p.fr.MsgReader()
		if err != nil {
			ch <- result{err: err}
			return
		}
		b, err := io.ReadAll(r)
		if cerr := r.Close(); err == nil {
			err = cerr
		}
		ch <- result{msg: string(b), err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("peer Recv: %v", res.err)
		}
		return res.msg
	case <-time.After(timeout):
		t.Fatal("peer Recv timed out")
		return ""
	}
}

// Upgrade follows the client into chunked framing.
func (p *StreamPeer) Upgrade() { p.fr.Upgrade() }

// Stall makes the device go silent, like one that lost power: from here on
// it reads nothing the client sends and sends nothing itself, without
// closing the stream.
func (p *StreamPeer) Stall() {
	if p.stall != nil {
		p.stall()
	}
}

// Relay forwards one TCP connection to a target address until it is told to
// stall.  Put between a transport and the server it dials, it turns that
// server into a device that can go silent at the network level: the
// connection stays open while nothing is read from it and nothing arrives
// on it, which is what a transport sees when a device loses power.
type Relay struct {
	ln      net.Listener
	stalled atomic.Bool
}

// NewRelay starts a relay to target.  It is torn down when the test ends.
func NewRelay(t *testing.T, target string) *Relay {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	r := &Relay{ln: ln}

	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})

	go func() {
		client, err := ln.Accept()
		if err != nil {
			return
		}
		// Little room for unread bytes, so a stalled relay pushes back on
		// the sender soon.
		if tc, ok := client.(*net.TCPConn); ok {
			_ = tc.SetReadBuffer(8 << 10)
		}
		server, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", target)
		if err != nil {
			_ = client.Close()
			return
		}

		mu.Lock()
		conns = append(conns, client, server)
		mu.Unlock()

		go r.pump(server, client)
		go r.pump(client, server)
	}()
	return r
}

// pump copies src to dst until the relay stalls.  It leaves both
// connections open: silence, not a hang-up.
func (r *Relay) pump(dst, src net.Conn) {
	buf := make([]byte, 16<<10)
	for {
		n, err := src.Read(buf)
		if err != nil || r.stalled.Load() {
			return
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

// Addr is the address to dial instead of the target's.
func (r *Relay) Addr() string { return r.ln.Addr().String() }

// Stall stops the relay from moving bytes in either direction.
func (r *Relay) Stall() { r.stalled.Store(true) }
