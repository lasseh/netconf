// Package transporttest provides a conformance suite that certifies a
// transport.Transport implementation against the contract the netconf
// session layer depends on.
package transporttest

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/transport"
)

// timeout is a generous upper bound for any single blocking operation in the
// suite. It is intentionally large so slow CI machines do not flake.
const timeout = 5 * time.Second

// Peer scripts the device side of the transport under test.
type Peer interface {
	// Send queues one complete message for the client to read. Non-blocking.
	Send(msg string)
	// Recv returns the next complete message written by the client,
	// failing t after a timeout.
	Recv(t *testing.T) string
}

// upgradablePeer is the optional interface a Peer implements when it can follow
// the transport into chunked framing.
type upgradablePeer interface {
	Upgrade()
}

// silentPeer is the optional interface a Peer implements when it can go
// silent like a device that lost power: nothing the client sends is read any
// more and nothing is sent, while the connection stays up.  StreamPeer does.
type silentPeer interface {
	Stall()
}

// stuckWriteSize is more than the buffers between a transport and its device
// take, so writing it to a silent peer gets stuck part of the way.
const stuckWriteSize = 8 << 20

// Run exercises the transport contract. newPair must return a fresh connected
// Transport and its scripted Peer.
func Run(t *testing.T, newPair func(t *testing.T) (transport.Transport, Peer)) {
	t.Run("round trip", func(t *testing.T) {
		tr, peer := newPair(t)
		roundTrip(t, tr, peer, "<hello/>")
	})

	t.Run("no bleed", func(t *testing.T) {
		tr, peer := newPair(t)

		peer.Send("<one/>")
		peer.Send("<two/>")

		// The first reader must see only the first message, terminated by EOF,
		// with no bytes of the second message bleeding through.
		r1, err := tr.MsgReader()
		be.NilErr(t, err)
		be.Equal(t, "<one/>", readAll(t, r1))
		be.NilErr(t, r1.Close())

		// A fresh reader reads the second message intact.
		r2, err := tr.MsgReader()
		be.NilErr(t, err)
		be.Equal(t, "<two/>", readAll(t, r2))
		be.NilErr(t, r2.Close())
	})

	t.Run("drain on close", func(t *testing.T) {
		tr, peer := newPair(t)

		peer.Send("<first-message/>")
		peer.Send("<second-message/>")

		// Read only part of the first message, then close the reader. The
		// remainder of the first message must be drained so it does not corrupt
		// the second message.
		r1, err := tr.MsgReader()
		be.NilErr(t, err)
		buf := make([]byte, 4)
		n, err := io.ReadFull(r1, buf)
		be.NilErr(t, err)
		be.Equal(t, "<fir", string(buf[:n]))
		be.NilErr(t, r1.Close())

		r2, err := tr.MsgReader()
		be.NilErr(t, err)
		be.Equal(t, "<second-message/>", readAll(t, r2))
		be.NilErr(t, r2.Close())
	})

	t.Run("stream busy reader", func(t *testing.T) {
		tr, peer := newPair(t)

		// Two messages so that re-acquiring after close does not block on an
		// empty read queue for transports whose MsgReader blocks.
		peer.Send("<a/>")
		peer.Send("<b/>")

		r1, err := tr.MsgReader()
		be.NilErr(t, err)

		r2, err := tr.MsgReader()
		be.ErrorIs(t, transport.ErrStreamBusy, err)
		if r2 != nil {
			t.Fatal("MsgReader returned a non-nil reader alongside ErrStreamBusy")
		}

		// Releasing the first reader lets a new one be acquired.
		be.NilErr(t, r1.Close())
		r3, err := tr.MsgReader()
		be.NilErr(t, err)
		be.NilErr(t, r3.Close())
	})

	t.Run("stream busy writer", func(t *testing.T) {
		tr, peer := newPair(t)

		w1, err := tr.MsgWriter()
		be.NilErr(t, err)

		w2, err := tr.MsgWriter()
		be.ErrorIs(t, transport.ErrStreamBusy, err)
		if w2 != nil {
			t.Fatal("MsgWriter returned a non-nil writer alongside ErrStreamBusy")
		}

		// Closing a writer emits a (possibly empty) message; drain it so the
		// close does not deadlock on a synchronous transport.
		closeWriterDrained(t, w1, peer)

		w3, err := tr.MsgWriter()
		be.NilErr(t, err)
		closeWriterDrained(t, w3, peer)
	})

	t.Run("invalid io", func(t *testing.T) {
		tr, peer := newPair(t)

		// Read after the reader is closed returns ErrInvalidIO.
		peer.Send("<msg/>")
		r, err := tr.MsgReader()
		be.NilErr(t, err)
		be.Equal(t, "<msg/>", readAll(t, r))
		be.NilErr(t, r.Close())
		if _, err := r.Read(make([]byte, 1)); !errors.Is(err, transport.ErrInvalidIO) {
			t.Fatalf("Read after Close: got %v, want ErrInvalidIO", err)
		}

		// Write after the writer is closed returns ErrInvalidIO.
		w, err := tr.MsgWriter()
		be.NilErr(t, err)
		closeWriterDrained(t, w, peer)
		if _, err := w.Write([]byte("x")); !errors.Is(err, transport.ErrInvalidIO) {
			t.Fatalf("Write after Close: got %v, want ErrInvalidIO", err)
		}
	})

	t.Run("close unblocks read", func(t *testing.T) {
		tr, _ := newPair(t)

		done := make(chan struct{})
		go func() {
			defer close(done)
			// Either MsgReader itself blocks (adapters that block until data is
			// available) or the returned reader's Read blocks. Both must be
			// released by Close.
			r, err := tr.MsgReader()
			if err != nil {
				return
			}
			_, _ = r.Read(make([]byte, 1))
			_ = r.Close()
		}()

		// Give the goroutine a moment to reach its blocking point, then cancel
		// via Close from another goroutine.
		time.Sleep(50 * time.Millisecond)
		be.NilErr(t, tr.Close())

		select {
		case <-done:
		case <-time.After(timeout):
			t.Fatal("Read did not unblock within timeout after Close")
		}

		// After Close, acquiring IO must fail immediately or on first use, but
		// must never block indefinitely.
		assertNoBlockingIO(t, tr)
	})

	// The two subtests below are close-is-cancel against a device that no
	// longer takes part: Close cannot count on the peer acknowledging
	// anything, which is exactly when a session needs it to work.

	t.Run("close unblocks read from a silent peer", func(t *testing.T) {
		tr, peer := newPair(t)
		silent, ok := peer.(silentPeer)
		if !ok {
			t.Skip("peer cannot go silent")
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			r, err := tr.MsgReader()
			if err != nil {
				return
			}
			_, _ = r.Read(make([]byte, 1))
			_ = r.Close()
		}()

		time.Sleep(50 * time.Millisecond)
		silent.Stall()
		closeWithin(t, tr)

		select {
		case <-done:
		case <-time.After(timeout):
			t.Fatal("Read did not unblock within timeout after Close")
		}
		assertNoBlockingIO(t, tr)
	})

	t.Run("close unblocks write to a silent peer", func(t *testing.T) {
		tr, peer := newPair(t)
		silent, ok := peer.(silentPeer)
		if !ok {
			t.Skip("peer cannot go silent")
		}
		silent.Stall()

		done := make(chan struct{})
		go func() {
			defer close(done)
			w, err := tr.MsgWriter()
			if err != nil {
				return
			}
			_, _ = w.Write(make([]byte, stuckWriteSize))
			_ = w.Close()
		}()

		// Let the write get stuck.  Close has to return either way.
		time.Sleep(200 * time.Millisecond)
		closeWithin(t, tr)

		select {
		case <-done:
		case <-time.After(timeout):
			t.Fatal("Write did not unblock within timeout after Close")
		}
		assertNoBlockingIO(t, tr)
	})

	t.Run("upgrade", func(t *testing.T) {
		tr, peer := newPair(t)

		pUp, ok := peer.(upgradablePeer)
		if !ok {
			t.Skip("peer does not support framing upgrade")
		}

		// One round trip on the base framing.
		roundTrip(t, tr, peer, "<base-framing/>")

		// Upgrade both sides, then another round trip on the new framing.
		tr.Upgrade()
		pUp.Upgrade()
		roundTrip(t, tr, peer, "<chunked-framing/>")

		// a closed writer stays closed in the new framing as well
		w, err := tr.MsgWriter()
		be.NilErr(t, err)
		closeWriterDrained(t, w, peer)
		if _, err := w.Write([]byte("x")); !errors.Is(err, transport.ErrInvalidIO) {
			t.Fatalf("Write after Close: got %v, want ErrInvalidIO", err)
		}
	})
}

// roundTrip writes msg from the client (draining it on the peer) and then has
// the peer echo it back for the client to read, asserting both directions
// deliver msg intact and that the reader latches EOF.
func roundTrip(t *testing.T, tr transport.Transport, peer Peer, msg string) {
	t.Helper()

	// client -> peer
	w, err := tr.MsgWriter()
	be.NilErr(t, err)
	writeString(t, w, msg)
	be.Equal(t, msg, closeWriterRecv(t, w, peer))

	// peer -> client
	peer.Send(msg)
	r, err := tr.MsgReader()
	be.NilErr(t, err)
	be.Equal(t, msg, readAll(t, r))

	// EOF latch: further reads keep returning io.EOF.
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("reader did not latch EOF: got %v, want io.EOF", err)
	}
	be.NilErr(t, r.Close())
}

// closeWriterRecv closes w concurrently with a peer receive so a synchronous
// transport (whose flush blocks until the peer reads) does not deadlock. It
// returns the message the peer received.
func closeWriterRecv(t *testing.T, w io.WriteCloser, peer Peer) string {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- w.Close() }()
	got := peer.Recv(t)
	select {
	case err := <-done:
		be.NilErr(t, err)
	case <-time.After(timeout):
		t.Fatal("writer Close did not return within timeout")
	}
	return got
}

// closeWriterDrained closes w and discards whatever message the peer receives.
func closeWriterDrained(t *testing.T, w io.WriteCloser, peer Peer) {
	t.Helper()
	_ = closeWriterRecv(t, w, peer)
}

// writeString writes s to w in a goroutine-free path; small writes are buffered
// by framers and do not block until the writer is closed and flushed.
func writeString(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readAll reads r to EOF with a timeout guard and returns the bytes as a string.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := io.ReadAll(r)
		ch <- res{b, err}
	}()
	select {
	case out := <-ch:
		be.NilErr(t, out.err)
		return string(out.b)
	case <-time.After(timeout):
		t.Fatal("read did not complete within timeout")
		return ""
	}
}

// closeWithin closes tr and fails the test when Close itself does not
// return: a Close that waits for the peer cannot cancel anything.
func closeWithin(t *testing.T, tr transport.Transport) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tr.Close()
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("Close did not return within timeout")
	}
}

// assertNoBlockingIO verifies that acquiring and exercising a reader/writer
// after Close returns within the timeout (it may error, it must not hang).
func assertNoBlockingIO(t *testing.T, tr transport.Transport) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if r, err := tr.MsgReader(); err == nil {
			_, _ = r.Read(make([]byte, 1))
			_ = r.Close()
		}
		if w, err := tr.MsgWriter(); err == nil {
			_, _ = w.Write([]byte("x"))
			_ = w.Close()
		}
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("MsgReader/MsgWriter blocked after Close")
	}
}
