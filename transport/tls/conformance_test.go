package tls

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/transport"
	"github.com/lasseh/netconf/transport/transporttest"
)

func TestTransportConformance(t *testing.T) {
	transporttest.Run(t, func(t *testing.T) (transport.Transport, transporttest.Peer) {
		srv := newTestServer(t)
		conns := make(chan net.Conn, 1)
		release := make(chan struct{})
		srv.Serve(func(c net.Conn) error {
			conns <- c
			<-release
			return nil
		})
		// the relay lets the peer go silent
		relay := transporttest.NewRelay(t, srv.Addr())

		t.Cleanup(func() {
			// let the server finish before the test does, it logs to t
			close(release)
			_ = srv.listener.Close()
			select {
			case <-srv.errCh:
			case <-time.After(5 * time.Second):
			}
		})

		tr, err := Dial(t.Context(), "tcp", relay.Addr(), &tls.Config{InsecureSkipVerify: true})
		be.NilErr(t, err)
		t.Cleanup(func() {
			// a Close that hangs has failed its subtest by now, and must not
			// hang the cleanup too
			closed := make(chan struct{})
			go func() {
				defer close(closed)
				_ = tr.Close()
			}()
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
			}
		})

		select {
		case c := <-conns:
			return tr, transporttest.NewStreamPeer(t, c, relay.Stall)
		case <-time.After(5 * time.Second):
			t.Fatal("server did not get the connection")
			return nil, nil
		}
	})
}
