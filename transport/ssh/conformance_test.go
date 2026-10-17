package ssh

import (
	"net"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"

	"github.com/lasseh/netconf/transport"
	"github.com/lasseh/netconf/transport/transporttest"
)

// conformancePair connects a transport, made by open from the address of the
// device, to an in-process ssh server whose end of the netconf channel is
// the peer.  open also returns what makes the peer go silent: the stall of a
// relay it has put somewhere on the way.
func conformancePair(t *testing.T, open func(t *testing.T, device string) (*Transport, func())) (transport.Transport, transporttest.Peer) {
	t.Helper()

	srv := newTestServer(t)
	channels := make(chan ssh.Channel, 1)
	release := make(chan struct{})
	srv.Serve(func(ch ssh.Channel) error {
		channels <- ch
		<-release
		return nil
	})

	t.Cleanup(func() {
		// let the server finish before the test does, it logs to t
		close(release)
		_ = srv.listener.Close()
		select {
		case <-srv.errCh: //nolint:errcheck // waits for the server to finish, its error is not under test
		case <-time.After(5 * time.Second):
		}
	})

	tr, stall := open(t, srv.Addr())
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
	case ch := <-channels:
		return tr, transporttest.NewStreamPeer(t, ch, stall)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not get the netconf channel")
		return nil, nil
	}
}

// relayed has dial connect to a relay in front of the device.
func relayed(dial func(t *testing.T, addr string) *Transport) func(*testing.T, string) (*Transport, func()) {
	return func(t *testing.T, device string) (*Transport, func()) {
		relay := transporttest.NewRelay(t, device)
		return dial(t, relay.Addr()), relay.Stall
	}
}

// Every way of making a transport has to honour the transport contract, most
// of all close-is-cancel: the session has no other way to give up on a
// device.
func TestTransportConformance(t *testing.T) {
	config := &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}

	t.Run("Dial", func(t *testing.T) {
		transporttest.Run(t, func(t *testing.T) (transport.Transport, transporttest.Peer) {
			return conformancePair(t, relayed(func(t *testing.T, addr string) *Transport {
				tr, err := Dial(t.Context(), "tcp", addr, config)
				be.NilErr(t, err)
				return tr
			}))
		})
	})

	t.Run("DialHost", func(t *testing.T) {
		transporttest.Run(t, func(t *testing.T) (transport.Transport, transporttest.Peer) {
			return conformancePair(t, relayed(func(t *testing.T, addr string) *Transport {
				host, port := hostPort(t, addr)
				tr, err := DialHost(t.Context(), host,
					WithPort(port),
					WithUser("test"),
					WithPassword(""),
					WithInsecureIgnoreHostKey(),
					WithKeepalive(time.Minute, 3),
				)
				be.NilErr(t, err)
				return tr
			}))
		})
	})

	// Through a jump host the device is one of two that can go silent, and
	// the connection to it is a channel on the jump host: closing that
	// channel is not what gets a session out.
	for _, silent := range []string{"jump host", "device behind the jump host"} {
		t.Run("DialHost through a jump host, silent "+silent, func(t *testing.T) {
			transporttest.Run(t, func(t *testing.T) (transport.Transport, transporttest.Peer) {
				return conformancePair(t, func(t *testing.T, device string) (*Transport, func()) {
					home, _ := sshHome(t)
					jump := newTestServer(t)
					serveJump(jump)

					// the relay goes in front of whichever is to go silent
					jumpAddr := jump.Addr()
					var relay *transporttest.Relay
					if silent == "jump host" {
						relay = transporttest.NewRelay(t, jumpAddr)
						jumpAddr = relay.Addr()
					} else {
						relay = transporttest.NewRelay(t, device)
						device = relay.Addr()
					}
					trust(t, home, jumpAddr, jump.hostKey)

					host, port := hostPort(t, device)
					tr, err := DialHost(t.Context(), host,
						WithPort(port),
						WithUser("test"),
						WithPassword(""),
						WithInsecureIgnoreHostKey(),
						WithProxyJump(jumpAddr),
						WithKeepalive(time.Minute, 3),
					)
					be.NilErr(t, err)
					return tr, relay.Stall
				})
			})
		})
	}

	// The caller keeps the ssh client here, so the transport has to release
	// its own IO without closing the connection.
	t.Run("NewTransport", func(t *testing.T) {
		transporttest.Run(t, func(t *testing.T) (transport.Transport, transporttest.Peer) {
			return conformancePair(t, relayed(func(t *testing.T, addr string) *Transport {
				conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
				be.NilErr(t, err)
				c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
				be.NilErr(t, err)
				client := ssh.NewClient(c, chans, reqs)
				t.Cleanup(func() { _ = client.Close() })

				tr, err := NewTransport(client)
				be.NilErr(t, err)
				return tr
			}))
		})
	})
}
