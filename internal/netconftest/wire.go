package netconftest

import (
	"io"
	"sync/atomic"
	"testing"

	"github.com/lasseh/netconf/transport"
)

// Wire is a transport backed by the real RFC6242 framer over in-memory
// pipes.  Its device side is scripted through Device, down to the byte, so
// tests can replay exactly what a device put on the wire, including bytes
// outside any frame.
type Wire struct {
	*transport.Framer
	rd *io.PipeReader // device -> client
	wr *io.PipeWriter // client -> device
}

// Close closes the client's ends of the pipes, unblocking any in-flight IO.
func (w *Wire) Close() error {
	_ = w.rd.Close()
	_ = w.wr.Close()
	return nil
}

// Device is the scripted device side of a Wire.
type Device struct {
	fr *transport.Framer
	in *gate          // client -> device
	wr *io.PipeWriter // device -> client
}

// Recv returns the next message sent by the client.
func (d *Device) Recv() (string, error) {
	// The framing of a message is fixed when its reader is created, so wait
	// for the message to start: the client may switch framing in between.
	if err := d.in.wait(); err != nil {
		return "", err
	}

	r, err := d.fr.MsgReader()
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(r)
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	return string(b), err
}

// Send writes msg to the client as one message in the current framing.  It
// blocks until the client has consumed it or closed the transport.
func (d *Device) Send(msg string) error {
	return d.send(msg, false)
}

// send is Send, with which the device can make msg its last message in the
// current framing: it switches to chunked framing before any of msg is on
// its way, so before the client can have answered it.
func (d *Device) send(msg string, last bool) error {
	w, err := d.fr.MsgWriter()
	if err != nil {
		return err
	}
	if last {
		d.fr.Upgrade()
	}
	if _, err := io.WriteString(w, msg); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// WriteRaw writes s to the client verbatim; the caller supplies the framing.
// It blocks until the client has consumed the bytes or closed the transport.
func (d *Device) WriteRaw(s string) error {
	_, err := io.WriteString(d.wr, s)
	return err
}

// Upgrade switches the device to chunked framing.
func (d *Device) Upgrade() { d.fr.Upgrade() }

// Hangup closes the device's sending side so the client sees EOF.
func (d *Device) Hangup() { _ = d.wr.Close() }

// Stall makes the device go silent, like one that lost power: it stops
// reading what the client sends, so the client's writes get stuck, and the
// connection stays up.
func (d *Device) Stall() { d.in.stalled.Store(true) }

// gate lets a reader's owner wait for data without consuming it, and stops
// reading altogether once stalled.
type gate struct {
	r      io.Reader
	peeked []byte

	stalled atomic.Bool
	// closed is closed when the wire is torn down
	closed <-chan struct{}
}

// wait sees what the client has written and the device has not read.  It
// does not see what the framer of the device has read ahead, and need not:
// the client is a Framer, which ends a write where a message ends, so the
// device never has the beginning of the next message with the end of one.
func (g *gate) wait() error {
	if len(g.peeked) > 0 {
		return nil
	}
	var b [1]byte
	n, err := g.read(b[:])
	g.peeked = b[:n]
	if n > 0 {
		return nil
	}
	return err
}

func (g *gate) Read(p []byte) (int, error) {
	if len(g.peeked) > 0 {
		n := copy(p, g.peeked)
		g.peeked = g.peeked[n:]
		return n, nil
	}
	return g.read(p)
}

// read reads from the client unless the device is stalled, before or by the
// time data arrives.  A stalled device leaves the client's write unfinished.
func (g *gate) read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if g.stalled.Load() {
		<-g.closed
		return 0, io.ErrClosedPipe
	}
	return n, err
}

// NewWire returns a connected Wire and its Device.  Both are torn down when
// the test ends.
func NewWire(t testing.TB) (*Wire, *Device) {
	t.Helper()

	c2dR, c2dW := io.Pipe()
	d2cR, d2cW := io.Pipe()

	closed := make(chan struct{})
	in := &gate{r: c2dR, closed: closed}
	wire := &Wire{Framer: transport.NewFramer(d2cR, c2dW), rd: d2cR, wr: c2dW}
	dev := &Device{fr: transport.NewFramer(in, d2cW), in: in, wr: d2cW}

	t.Cleanup(func() {
		_ = wire.Close()
		_ = c2dR.Close()
		_ = d2cW.Close()
		close(closed)
	})
	return wire, dev
}
