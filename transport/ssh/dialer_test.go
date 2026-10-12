package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// watched is a connection that says when it was closed.
type watched struct {
	net.Conn
	closed atomic.Bool
}

func (c *watched) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// calledHome returns a device that has opened a connection to a listener of
// the test, and that connection: an SSH server that dials in, the way a
// Junos device with outbound-ssh does.  setup, if any, sees the server
// before it starts.
func calledHome(t *testing.T, handler func(ssh.Channel) error, setup func(*testServer)) (srv *testServer, conn *watched) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	srv = newTestServer(t)
	_ = srv.listener.Close()
	if setup != nil {
		setup(srv)
	}
	srv.CallHome(ln.Addr().String(), handler)

	accepted, err := ln.Accept()
	be.NilErr(t, err)
	return srv, &watched{Conn: accepted}
}

// A connection the caller has already: one a device opened towards the
// program.  DialHost runs on it what it runs on a connection of its own.
func TestDialHost_Dialer(t *testing.T) {
	// the device is known by a name, which is not where the connection is
	// from or to
	const device = "device-7.example.net"
	knownAs := func(t *testing.T, key ssh.PublicKey) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHosts(t, path, net.JoinHostPort(device, "830"), key)
		return path
	}
	hand := func(conn net.Conn, asked *string) DialOption {
		return WithDialer(func(_ context.Context, network, addr string) (net.Conn, error) {
			if asked != nil {
				*asked = network + " " + addr
			}
			return conn, nil
		})
	}

	t.Run("the host key is checked under the name given", func(t *testing.T) {
		srv, conn := calledHome(t, greet, func(srv *testServer) {
			srv.config.NoClientAuth = false
			srv.config.PasswordCallback = func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if meta.User() != "admin" || string(password) != "secret" {
					return nil, errors.New("bad credentials")
				}
				return nil, nil
			}
		})

		var asked string
		tr, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword("secret"),
			WithKnownHosts(knownAs(t, srv.hostKey)), hand(conn, &asked))
		be.NilErr(t, err)
		be.Equal(t, "tcp "+device+":830", asked)
		readGreeting(t, tr)

		// the transport owns the connection
		be.False(t, conn.closed.Load())
		be.NilErr(t, tr.Close())
		be.True(t, conn.closed.Load())
		be.NilErr(t, srv.Wait(t))
	})

	t.Run("a host key that is not the one of that name", func(t *testing.T) {
		_, conn := calledHome(t, greet, nil)

		_, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""),
			WithKnownHosts(knownAs(t, newKey(t))), hand(conn, nil))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 1, len(keyErr.Want))
		be.True(t, conn.closed.Load())
	})

	t.Run("a name nobody knows", func(t *testing.T) {
		srv, conn := calledHome(t, greet, nil)
		known := filepath.Join(t.TempDir(), "known_hosts")
		// known by the address it called from, which proves nothing
		writeKnownHosts(t, known, conn.RemoteAddr().String(), srv.hostKey)

		_, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""), WithKnownHosts(known), hand(conn, nil))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 0, len(keyErr.Want))
	})

	t.Run("the keepalive runs on it", func(t *testing.T) {
		_, conn := calledHome(t, func(ch ssh.Channel) error {
			_, err := io.Copy(io.Discard, ch)
			return err
		}, func(srv *testServer) { srv.IgnoreRequests = true })

		tr, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithKeepalive(20*time.Millisecond, 2), hand(conn, nil))
		be.NilErr(t, err)

		done := make(chan error, 1)
		go func() {
			r, err := tr.MsgReader()
			if err != nil {
				done <- err
				return
			}
			_, err = io.ReadAll(r)
			done <- err
		}()
		be.True(t, errors.Is(recvWithin(t, done, "the keepalive to give up"), ErrKeepalive))
		be.True(t, conn.closed.Load())
	})

	t.Run("a connection of any kind", func(t *testing.T) {
		// one whose addresses are no host and port: a unix socket
		dir, err := os.MkdirTemp("", "nc")
		be.NilErr(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", filepath.Join(dir, "s"))
		be.NilErr(t, err)
		t.Cleanup(func() { _ = ln.Close() })

		srv := newTestServer(t)
		_ = srv.listener.Close()
		go func() {
			defer close(srv.errCh)
			server, err := ln.Accept()
			if err != nil {
				srv.errCh <- err
				return
			}
			srv.serve(server, greet)
		}()
		client, err := (&net.Dialer{}).DialContext(t.Context(), "unix", ln.Addr().String())
		be.NilErr(t, err)

		tr, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""),
			WithKnownHosts(knownAs(t, srv.hostKey)), hand(client, nil))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})

	t.Run("a function that fails", func(t *testing.T) {
		busy := errors.New("no device has called yet")
		_, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithDialer(func(context.Context, string, string) (net.Conn, error) { return nil, busy }))
		be.True(t, errors.Is(err, busy))
		be.Equal(t, "connecting to "+device+":830: no device has called yet", err.Error())
	})

	// A function that hands over nothing and says nothing went wrong, as a
	// receive from a closed channel of accepted connections does.
	t.Run("a function that returns neither", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		_, err := DialHost(ctx, device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithDialer(func(context.Context, string, string) (net.Conn, error) { return nil, nil }))
		be.Nonzero(t, err)
		be.Equal(t, "connecting to "+device+":830: the function given to WithDialer returned no connection", err.Error())

		// and nothing of the dial is left that the end of its context
		// would run into
		cancel()
		time.Sleep(20 * time.Millisecond)
	})

	t.Run("a function that returns a connection and an error", func(t *testing.T) {
		ours, theirs := net.Pipe()
		t.Cleanup(func() { _ = theirs.Close() })
		conn := &watched{Conn: ours}

		busy := errors.New("the wrong device called")
		_, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithDialer(func(context.Context, string, string) (net.Conn, error) { return conn, busy }))
		be.True(t, errors.Is(err, busy))
		be.True(t, conn.closed.Load())
	})

	// net.DialTCP and its kind return a nil pointer with their error, and
	// in a net.Conn that is no nil: nothing may be called on it.
	t.Run("a function that returns a nil pointer and an error", func(t *testing.T) {
		busy := errors.New("connection refused")
		_, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithDialer(func(context.Context, string, string) (net.Conn, error) {
				var none *net.TCPConn
				return none, busy
			}))
		be.True(t, errors.Is(err, busy))
		be.Equal(t, "connecting to "+device+":830: connection refused", err.Error())
	})

	t.Run("a function that returns a nil pointer and no error", func(t *testing.T) {
		_, err := DialHost(t.Context(), device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithDialer(func(context.Context, string, string) (net.Conn, error) {
				var none *net.TCPConn
				return none, nil
			}))
		be.Nonzero(t, err)
		be.Equal(t, "connecting to "+device+":830: the function given to WithDialer returned no connection", err.Error())
	})

	t.Run("a function that waits for its context", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := DialHost(ctx, device, WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
			WithDialer(func(ctx context.Context, _, _ string) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}))
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		be.In(t, "connecting to "+device+":830", err.Error())
	})

	// With jump hosts the first connection is the one to the first of
	// them, and that is the one the function makes.
	t.Run("the first connection is to the jump host", func(t *testing.T) {
		home, _ := sshHome(t)
		jumpAddr, _ := newJump(t, home)

		dev := newTestServer(t)
		dev.Serve(greet)
		_, port := hostPort(t, dev.Addr())

		var asked []string
		tr, err := DialHost(t.Context(), "127.0.0.1", WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithProxyJump(jumpAddr),
			WithDialer(func(ctx context.Context, network, addr string) (net.Conn, error) {
				asked = append(asked, addr)
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
		be.AllEqual(t, []string{jumpAddr}, asked)
	})
}
