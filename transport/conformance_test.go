package transport_test

import (
	"io"
	"testing"

	"github.com/lasseh/netconf/transport"
	"github.com/lasseh/netconf/transport/transporttest"
)

// framerTransport is the client side: a Framer wired to one direction of each
// pipe pair. Close closes the client's pipe ends, which is the cancellation
// mechanism the contract requires (close-is-cancel).
type framerTransport struct {
	*transport.Framer
	wr *io.PipeWriter // client -> peer
	rd *io.PipeReader // peer -> client
}

func (t *framerTransport) Close() error {
	// Closing the read end unblocks any in-flight client Read; closing the write
	// end signals EOF to the peer.
	_ = t.rd.Close()
	_ = t.wr.Close()
	return nil
}

func newFramerPair(t *testing.T) (transport.Transport, transporttest.Peer) {
	// c2p: client writes, peer reads. p2c: peer writes, client reads.
	c2pR, c2pW := io.Pipe()
	p2cR, p2cW := io.Pipe()

	client := &framerTransport{
		Framer: transport.NewFramer(p2cR, c2pW),
		wr:     c2pW,
		rd:     p2cR,
	}
	// A pipe hands over bytes only when they are read, so a peer that does
	// not read is a silent one and needs no help to stall.
	peer := transporttest.NewStreamPeer(t, struct {
		io.Reader
		io.Writer
	}{c2pR, p2cW}, nil)

	t.Cleanup(func() {
		// Close the client ends first so any peer write blocked on the pipe
		// unblocks.
		_ = client.Close()
		_ = c2pR.Close()
		_ = p2cW.Close()
	})

	return client, peer
}

func TestFramerConformance(t *testing.T) {
	transporttest.Run(t, newFramerPair)
}
