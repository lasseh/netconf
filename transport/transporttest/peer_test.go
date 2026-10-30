package transporttest

import (
	"io"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/transport"
)

// pipes is the device's end of a byte stream.
type pipes struct {
	io.Reader
	io.Writer
}

// A hello that was queued goes out in end-of-message framing, however soon
// the device upgrades behind it.  The client has not switched yet, and could
// not find the end of a chunked message.
func TestStreamPeerUpgradeBehindAQueuedMessage(t *testing.T) {
	deviceIn, clientOut := io.Pipe()
	clientIn, deviceOut := io.Pipe()
	t.Cleanup(func() {
		_ = deviceIn.Close()
		_ = clientIn.Close()
	})
	peer := NewStreamPeer(t, pipes{deviceIn, deviceOut}, nil)
	client := transport.NewFramer(clientIn, clientOut)

	read := func() string {
		t.Helper()
		r, err := client.MsgReader()
		be.NilErr(t, err)
		msg, err := io.ReadAll(r)
		be.NilErr(t, err)
		be.NilErr(t, r.Close())
		return string(msg)
	}

	peer.Send("<hello/>")
	upgraded := make(chan struct{})
	go func() {
		peer.Upgrade()
		close(upgraded)
	}()

	select {
	case <-upgraded:
		t.Fatal("the peer upgraded with a message on its way")
	case <-time.After(20 * time.Millisecond):
	}
	be.Equal(t, "<hello/>", read())
	<-upgraded

	client.Upgrade()
	peer.Send("<next/>")
	be.Equal(t, "<next/>", read())
}
