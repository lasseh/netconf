package ssh

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// notTrustedError is what a callback of a test refuses a key with.
type notTrustedError struct{ fingerprint string }

func (e *notTrustedError) Error() string { return "not trusted: " + e.fingerprint }

// recorder is a callback that keeps what it was asked, and answers with
// err.
type recorder struct {
	mu    sync.Mutex
	hosts []string
	keys  []ssh.PublicKey
	err   error
}

func (r *recorder) check(hostname string, _ net.Addr, key ssh.PublicKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, hostname)
	r.keys = append(r.keys, key)
	return r.err
}

// A program that keeps the host keys of its devices itself, in a database
// that learns a key on first contact, checks the key with a callback of its
// own.  WithHostKeyCallback runs that callback in place of the known hosts.
func TestDialHost_HostKeyCallback(t *testing.T) {
	// nothing to find: no home with known hosts, no agent
	noHome := func(t *testing.T) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("SSH_AUTH_SOCK", "")
	}

	// the callback is what decides, and no known_hosts file is read: there is
	// none, and the dial does not fail over it
	t.Run("the callback accepts", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())
		var rec recorder

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKeyCallback(rec.check))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())

		be.Equal(t, 1, len(rec.keys))
		be.Equal(t, string(srv.hostKey.Marshal()), string(rec.keys[0].Marshal()))
		be.Equal(t, srv.Addr(), rec.hosts[0])
	})

	// its error is what the dial fails with, for the caller to find again
	t.Run("the callback refuses", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())
		rec := recorder{err: &notTrustedError{fingerprint: "SHA256:test"}}

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKeyCallback(rec.check))
		be.Nonzero(t, err)
		var refused *notTrustedError
		be.True(t, errors.As(err, &refused))
		be.Equal(t, "SHA256:test", refused.fingerprint)
		be.False(t, errors.Is(err, ErrAuth))
	})

	// a known_hosts file that says otherwise has no say
	t.Run("no known hosts are read for the device", func(t *testing.T) {
		home, _ := sshHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())
		trust(t, home, srv.Addr(), newKey(t))
		var rec recorder

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKeyCallback(rec.check))
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())
		be.Equal(t, 1, len(rec.keys))
	})

	// refused dials with opts, for a dial that has to fail before it
	// connects, and reports whether a connection was asked for.
	refused := func(opts ...DialOption) (dialed bool, err error) {
		opts = append(opts, WithUser("admin"), WithPassword("secret"),
			WithDialer(func(context.Context, string, string) (net.Conn, error) {
				dialed = true
				return nil, errors.New("nothing is dialed in this test")
			}))
		_, err = DialHost(t.Context(), "switch1.example.net", opts...)
		return dialed, err
	}

	// One check of the device, said once: a callback next to another check
	// would leave one of them without a say.
	t.Run("together with another host key check", func(t *testing.T) {
		var rec recorder
		known := filepath.Join(t.TempDir(), "known_hosts")

		for name, tc := range map[string]struct {
			opts []DialOption
			want string
		}{
			"WithHostKey after":             {[]DialOption{WithHostKeyCallback(rec.check), WithHostKey(newKey(t))}, "WithHostKey"},
			"WithHostKey before":            {[]DialOption{WithHostKey(newKey(t)), WithHostKeyCallback(rec.check)}, "WithHostKey"},
			"WithHostKey with no key":       {[]DialOption{WithHostKeyCallback(rec.check), WithHostKey()}, "WithHostKey"},
			"WithKnownHosts":                {[]DialOption{WithKnownHosts(known), WithHostKeyCallback(rec.check)}, "WithKnownHosts"},
			"WithKnownHosts with no file":   {[]DialOption{WithHostKeyCallback(rec.check), WithKnownHosts()}, "WithKnownHosts"},
			"WithInsecureIgnoreHostKey":     {[]DialOption{WithInsecureIgnoreHostKey(), WithHostKeyCallback(rec.check)}, "WithInsecureIgnoreHostKey"},
			"WithInsecureIgnoreHostKey too": {[]DialOption{WithHostKeyCallback(rec.check), WithInsecureIgnoreHostKey()}, "WithInsecureIgnoreHostKey"},
		} {
			t.Run(name, func(t *testing.T) {
				noHome(t)
				dialed, err := refused(tc.opts...)
				be.Nonzero(t, err)
				be.In(t, "WithHostKeyCallback cannot be combined with "+tc.want, err.Error())
				be.False(t, dialed)
			})
		}
		be.Equal(t, 0, len(rec.keys))
	})

	// a callback that is nil is no check, and not a dial without one
	t.Run("a nil callback", func(t *testing.T) {
		noHome(t)
		dialed, err := refused(WithHostKeyCallback(nil))
		be.Nonzero(t, err)
		be.In(t, "WithHostKeyCallback was given a nil callback", err.Error())
		be.False(t, dialed)
	})

	// The callback is for the device.  A jump host is checked against the
	// known hosts as ever, and the callback never sees its key.
	t.Run("a jump host is not the device", func(t *testing.T) {
		home, _ := sshHome(t)

		device := newTestServer(t)
		device.Serve(greet)
		host, port := hostPort(t, device.Addr())

		// the known hosts have one jump host in them, and not the device
		jumpAddr, _ := newJump(t, home)

		// a jump host nobody knows is refused over its key, though the
		// callback takes every key it is shown
		unknown := newTestServer(t)
		serveJump(unknown)
		var rec recorder
		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKeyCallback(rec.check), WithProxyJump(unknown.Addr()))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 0, len(keyErr.Want))
		be.In(t, "jump host", err.Error())
		be.Equal(t, 0, len(rec.keys))

		// the one that is known lets the dial through, and the callback is
		// asked about the device alone
		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKeyCallback(rec.check), WithProxyJump(jumpAddr))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
		be.Equal(t, 1, len(rec.keys))
		be.Equal(t, string(device.hostKey.Marshal()), string(rec.keys[0].Marshal()))
	})
}
