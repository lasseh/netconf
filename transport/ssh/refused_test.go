package ssh

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// writeKnownHosts writes a known hosts file that knows addr by key.
func writeKnownHosts(t *testing.T, path, addr string, key ssh.PublicKey) {
	t.Helper()
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
	be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
}

// accepted is a listener that hands on the connections it accepts.
type accepted struct {
	net.Listener
	conns chan net.Conn
}

func (l *accepted) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.conns <- conn
	}
	return conn, err
}

// passwordServer returns a server that lets in admin with the password
// secret, and its host and port.
func passwordServer(t *testing.T) (srv *testServer, host string, port int) {
	t.Helper()
	srv = newTestServer(t)
	srv.config.NoClientAuth = false
	srv.config.PasswordCallback = func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if conn.User() != "admin" || string(password) != "secret" {
			return nil, fmt.Errorf("bad credentials")
		}
		return nil, nil
	}
	srv.Serve(greet)
	host, port = hostPort(t, srv.Addr())
	return srv, host, port
}

// A login that the device refuses is an error a program can ask for.  It
// says who was refused and what was offered, which comes from what the ssh
// client asked this package for, and has the error of the ssh client
// underneath.
func TestDialHost_LoginRefused(t *testing.T) {
	t.Run("a wrong password", func(t *testing.T) {
		_, host, port := passwordServer(t)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("wrong"), WithInsecureIgnoreHostKey())
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, `user "admin"`, err.Error())
		be.In(t, "offered the password", err.Error())
		be.In(t, "ssh: unable to authenticate", err.Error())
		be.False(t, errors.Is(err, ErrSubsystemRefused))
	})

	t.Run("a user the device does not know", func(t *testing.T) {
		_, host, port := passwordServer(t)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("nobody"), WithPassword("secret"), WithInsecureIgnoreHostKey())
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, `user "nobody"`, err.Error())
	})

	t.Run("a key the device does not know", func(t *testing.T) {
		srv := newTestServer(t)
		_, known := writeKey(t, "")
		requireKey(srv, "admin", known)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		other, _ := writeKey(t, "")
		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPrivateKeyFile(other, ""), WithInsecureIgnoreHostKey())
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, "offered 1 key", err.Error())
	})

	// WithClientConfig runs last and can replace what the facts come from.
	// The error is the same one, with less to say.
	t.Run("with the login of the caller", func(t *testing.T) {
		_, host, port := passwordServer(t)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("unused"), WithInsecureIgnoreHostKey(),
			WithClientConfig(func(c *ssh.ClientConfig) {
				c.Auth = []ssh.AuthMethod{ssh.Password("wrong")}
				c.HostKeyCallback = func(string, net.Addr, ssh.PublicKey) error { return nil }
			}))
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, `user "admin"`, err.Error())
	})

	t.Run("with a ready-made configuration", func(t *testing.T) {
		srv, _, _ := passwordServer(t)

		_, err := Dial(t.Context(), "tcp", srv.Addr(), &ssh.ClientConfig{
			User:            "admin",
			Auth:            []ssh.AuthMethod{ssh.Password("wrong")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // a test server
		})
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, `user "admin"`, err.Error())
	})

	t.Run("at a jump host", func(t *testing.T) {
		home, _ := sshHome(t)
		t.Setenv("HOME", home)
		t.Setenv("SSH_AUTH_SOCK", "")

		jump := newTestServer(t)
		recordAuth(jump, nil) // lets nobody in
		serveJump(jump)
		trust(t, home, jump.Addr(), jump.hostKey)

		_, err := DialHost(t.Context(), "192.0.2.1", WithUser("admin"), WithPassword("secret"), WithProxyJump(jump.Addr()))
		be.True(t, errors.Is(err, ErrAuth))
		host, _ := hostPort(t, jump.Addr())
		be.In(t, "jump host "+host+": ssh: login failed: ", err.Error())
		be.In(t, " at "+jump.Addr(), err.Error())
		be.In(t, "offered 1 key", err.Error())
	})
}

// What is not a refused login is not called one: these failures have errors
// of their own, those of the ssh client.
func TestDialHost_NotALoginRefused(t *testing.T) {
	t.Run("a host key that is not the known one", func(t *testing.T) {
		srv, host, port := passwordServer(t)
		_, other := writeKey(t, "")
		known := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHosts(t, known, srv.Addr(), other)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("secret"), WithKnownHosts(known))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 1, len(keyErr.Want))
		be.False(t, errors.Is(err, ErrAuth))
	})

	t.Run("a host nobody knows", func(t *testing.T) {
		srv, host, port := passwordServer(t)
		known := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHosts(t, known, "192.0.2.9:22", srv.hostKey)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("secret"), WithKnownHosts(known))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 0, len(keyErr.Want))
		be.False(t, errors.Is(err, ErrAuth))
	})

	t.Run("no algorithm in common", func(t *testing.T) {
		_, host, port := passwordServer(t)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("secret"), WithInsecureIgnoreHostKey(),
			WithClientConfig(func(c *ssh.ClientConfig) { c.HostKeyAlgorithms = []string{ssh.KeyAlgoRSA} }))
		var algErr *ssh.AlgorithmNegotiationError
		be.True(t, errors.As(err, &algErr))
		be.False(t, errors.Is(err, ErrAuth))
	})

	// a device that hangs up in the middle of the login has refused nothing
	t.Run("a connection that ends during the login", func(t *testing.T) {
		srv := newTestServer(t)
		conns := make(chan net.Conn, 1)
		srv.listener = &accepted{Listener: srv.listener, conns: conns}
		srv.config.NoClientAuth = false
		srv.config.PasswordCallback = func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			_ = (<-conns).Close()
			return nil, errors.New("gone")
		}
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("secret"), WithInsecureIgnoreHostKey())
		be.Nonzero(t, err)
		be.False(t, errors.Is(err, ErrAuth))
	})
}

// A device that lets the user in and refuses the netconf subsystem has no
// NETCONF on that port, or none at all.
func TestDial_SubsystemRefused(t *testing.T) {
	srv := newTestServer(t)
	srv.RejectSubsystem = true
	srv.Serve(func(ch ssh.Channel) error {
		_, err := io.ReadAll(ch)
		return err
	})
	host, port := hostPort(t, srv.Addr())

	_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword("secret"), WithInsecureIgnoreHostKey())
	be.True(t, errors.Is(err, ErrSubsystemRefused))
	be.False(t, errors.Is(err, ErrAuth))
	be.In(t, "NETCONF", err.Error())
	be.NilErr(t, srv.Wait(t))
}
