package ssh

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// newKey returns a new ed25519 public key.
func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	key, err := ssh.NewPublicKey(pub)
	be.NilErr(t, err)
	return key
}

// A program without a known_hosts file, in a container for one, knows the
// key of each device from its inventory.  WithHostKey holds the device to
// that key, and reads no file for it.
func TestDialHost_HostKey(t *testing.T) {
	// nothing to find: no home with known hosts, no agent
	noHome := func(t *testing.T) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("SSH_AUTH_SOCK", "")
	}

	t.Run("the key of the device", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""), WithHostKey(srv.hostKey))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})

	t.Run("one of several keys given", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKey(newKey(t), srv.hostKey), WithHostKey(newKey(t)))
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())
	})

	// The key types given are the ones asked for, so a device that has no
	// key of such a type does not get as far as showing one.
	t.Run("a key of a type the device has none of", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t) // its key is an Ed25519 one
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		be.NilErr(t, err)
		pinned, err := ssh.NewPublicKey(&priv.PublicKey)
		be.NilErr(t, err)

		_, err = DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""), WithHostKey(pinned))
		var algErr *ssh.AlgorithmNegotiationError
		be.True(t, errors.As(err, &algErr))
	})

	t.Run("another key", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())
		pinned := newKey(t)

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""), WithHostKey(pinned))
		be.Nonzero(t, err)
		be.In(t, "key mismatch", err.Error())

		// the error of a host key that is not the known one, with the key
		// that was asked for
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 1, len(keyErr.Want))
		be.Equal(t, string(pinned.Marshal()), string(keyErr.Want[0].Key.Marshal()))
		be.False(t, errors.Is(err, ErrAuth))
	})

	// A device has a key of every type it knows, and offers the first that
	// the client takes.  The client asks for the types of the keys it was
	// given, so the device shows the one that can match.
	t.Run("a device with two keys, one of them given", func(t *testing.T) {
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		be.NilErr(t, err)
		rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
		be.NilErr(t, err)

		for name, given := range map[string]func(srv *testServer) ssh.PublicKey{
			"the rsa key":     func(*testServer) ssh.PublicKey { return rsaSigner.PublicKey() },
			"the ed25519 key": func(srv *testServer) ssh.PublicKey { return srv.hostKey },
		} {
			t.Run(name, func(t *testing.T) {
				noHome(t)
				srv := newTestServer(t)
				srv.config.AddHostKey(rsaSigner)
				srv.Serve(greet)
				host, port := hostPort(t, srv.Addr())

				var presented string
				tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
					WithHostKey(given(srv)),
					WithClientConfig(func(c *ssh.ClientConfig) {
						check := c.HostKeyCallback
						c.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
							presented = key.Type()
							return check(hostname, remote, key)
						}
					}))
				be.NilErr(t, err)
				be.Equal(t, given(srv).Type(), presented)
				be.NilErr(t, tr.Close())
			})
		}
	})

	// a known_hosts file that says otherwise has no say
	t.Run("no known hosts are read for the device", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())
		known := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHosts(t, known, srv.Addr(), newKey(t))

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithKnownHosts(known), WithHostKey(srv.hostKey))
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())

		// nor one that is not there
		srv = newTestServer(t)
		srv.Serve(greet)
		host, port = hostPort(t, srv.Addr())
		tr, err = DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithKnownHosts(filepath.Join(t.TempDir(), "no-such-file")), WithHostKey(srv.hostKey))
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())
	})

	// the key that is given is checked, whatever else was said
	t.Run("not switched off", func(t *testing.T) {
		noHome(t)
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithHostKey(newKey(t)))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
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

	// A list without a key is the device that the inventory has nothing on,
	// and not a dial that asked for no check.  It fails before it connects,
	// whatever else was said about the host key.
	t.Run("no key given", func(t *testing.T) {
		var inventory map[string][]ssh.PublicKey

		known := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHosts(t, known, "switch1.example.net:830", newKey(t))

		for name, opts := range map[string][]DialOption{
			"alone":                           {WithHostKey(inventory["switch1"]...)},
			"with the check switched off":     {WithInsecureIgnoreHostKey(), WithHostKey(inventory["switch1"]...)},
			"and the check switched off":      {WithHostKey(), WithInsecureIgnoreHostKey()},
			"with known hosts for the device": {WithKnownHosts(known), WithHostKey()},
		} {
			t.Run(name, func(t *testing.T) {
				noHome(t)
				dialed, err := refused(opts...)
				be.Nonzero(t, err)
				be.In(t, "WithHostKey was given no key", err.Error())
				be.False(t, dialed)
			})
		}

		// a key from another call of the option is one
		noHome(t)
		dialed, err := refused(WithHostKey(), WithHostKey(newKey(t)))
		be.In(t, "nothing is dialed in this test", err.Error())
		be.True(t, dialed)
	})

	// a line of the inventory that is no key, and its error dropped
	t.Run("a nil key", func(t *testing.T) {
		none, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 not-a-key"))
		be.Nonzero(t, err)

		for name, keys := range map[string][]ssh.PublicKey{
			"alone":        {none},
			"behind a key": {newKey(t), none},
		} {
			t.Run(name, func(t *testing.T) {
				noHome(t)
				dialed, err := refused(WithHostKey(keys...))
				be.Nonzero(t, err)
				be.In(t, "WithHostKey was given a nil key", err.Error())
				be.False(t, dialed)
			})
		}
	})

	// A host certificate is given like a key.  One for an RSA key is asked
	// for with the rsa-sha2 signatures, the only ones a current sshd makes.
	t.Run("an rsa host certificate", func(t *testing.T) {
		noHome(t)
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		be.NilErr(t, err)
		rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
		be.NilErr(t, err)
		sha2, err := ssh.NewSignerWithAlgorithms(rsaSigner.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512})
		be.NilErr(t, err)

		cert := &ssh.Certificate{
			Key:         rsaSigner.PublicKey(),
			CertType:    ssh.HostCert,
			KeyId:       "netconf-test",
			ValidBefore: ssh.CertTimeInfinity,
		}
		be.NilErr(t, cert.SignCert(rand.Reader, newSigner(t)))
		hostKey, err := ssh.NewCertSigner(cert, sha2)
		be.NilErr(t, err)

		srv := newTestServer(t)
		srv.config = &ssh.ServerConfig{NoClientAuth: true}
		srv.config.AddHostKey(hostKey)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""), WithHostKey(cert))
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())
	})

	// The key is that of the device.  A jump host is checked against the
	// known hosts as ever.
	t.Run("a jump host is not the device", func(t *testing.T) {
		home, _ := sshHome(t)

		device := newTestServer(t)
		device.Serve(greet)
		host, port := hostPort(t, device.Addr())

		// the known hosts have one jump host in them, and not the device
		jumpAddr, _ := newJump(t, home)

		// a jump host nobody knows is refused, though it has a given key
		unknown := newTestServer(t)
		serveJump(unknown)
		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKey(device.hostKey, unknown.hostKey), WithProxyJump(unknown.Addr()))
		var keyErr *knownhosts.KeyError
		be.True(t, errors.As(err, &keyErr))
		be.Equal(t, 0, len(keyErr.Want))
		be.In(t, "jump host", err.Error())

		// the one that is known lets the dial through to the device, which
		// is held to its key and not to the known hosts
		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithHostKey(device.hostKey), WithProxyJump(jumpAddr))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})
}
