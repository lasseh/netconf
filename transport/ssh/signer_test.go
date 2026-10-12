package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
)

// newSigner returns a key that exists in memory only, as one that came from
// a vault does.
func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	be.NilErr(t, err)
	return signer
}

// A private key that is no file: one from a vault, or a certificate signed
// for the session.  WithSigner logs in with it, and it is the login then:
// nothing else is looked for.
func TestDialHost_Signer(t *testing.T) {
	// nothing else to log in with: no key file in the home, no agent
	alone := func(t *testing.T) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("SSH_AUTH_SOCK", "")
	}

	t.Run("a key from memory", func(t *testing.T) {
		alone(t)
		signer := newSigner(t)
		srv := newTestServer(t)
		requireKey(srv, "admin", signer.PublicKey())
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithSigner(signer), WithInsecureIgnoreHostKey())
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})

	t.Run("a certificate", func(t *testing.T) {
		alone(t)
		authority, key := newSigner(t), newSigner(t)
		cert := &ssh.Certificate{
			Key:             key.PublicKey(),
			CertType:        ssh.UserCert,
			KeyId:           "netconf-test",
			ValidPrincipals: []string{"admin"},
			ValidBefore:     ssh.CertTimeInfinity,
		}
		be.NilErr(t, cert.SignCert(rand.Reader, authority))
		signer, err := ssh.NewCertSigner(cert, key)
		be.NilErr(t, err)

		// a server that knows the authority and no key
		srv := newTestServer(t)
		checker := &ssh.CertChecker{
			IsUserAuthority: func(auth ssh.PublicKey) bool {
				return bytes.Equal(auth.Marshal(), authority.PublicKey().Marshal())
			},
			UserKeyFallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				return nil, errors.New("only certificates here")
			},
		}
		srv.config.NoClientAuth = false
		srv.config.PublicKeyCallback = checker.Authenticate
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithSigner(signer), WithInsecureIgnoreHostKey())
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())

		// the key without the certificate is not let in
		srv = newTestServer(t)
		srv.config.NoClientAuth = false
		srv.config.PublicKeyCallback = checker.Authenticate
		srv.Serve(greet)
		host, port = hostPort(t, srv.Addr())
		_, err = DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithSigner(key), WithInsecureIgnoreHostKey())
		be.True(t, errors.Is(err, ErrAuth))
	})

	// A signer is a login that was named, like a key file: the identity
	// files and the agent are for a dial that names none.
	t.Run("in place of the identity files and the agent", func(t *testing.T) {
		_, homePub := sshHome(t)
		sock, agentPub := testAgent(t)
		t.Setenv("SSH_AUTH_SOCK", sock)

		signer := newSigner(t)
		srv := newTestServer(t)
		seen := recordAuth(srv, nil) // lets nobody in, so every key there is gets tried
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithSigner(signer), WithInsecureIgnoreHostKey())
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, "offered 1 key", err.Error())
		be.True(t, seen.saw(signer.PublicKey()))
		be.False(t, seen.saw(homePub))
		be.False(t, seen.saw(agentPub))
	})

	// A list without a signer still names the login.  The vault had nothing
	// for the device, and the keys of whoever runs the program are not
	// offered in its place.
	t.Run("no signer given", func(t *testing.T) {
		var vault map[string][]ssh.Signer

		// refused dials with opts where an identity file and an agent are
		// there to find, for a dial that has to fail before it connects, and
		// reports whether a connection was asked for.
		refused := func(t *testing.T, opts ...DialOption) (dialed bool, err error) {
			t.Helper()
			sshHome(t)
			sock, _ := testAgent(t)
			t.Setenv("SSH_AUTH_SOCK", sock)

			opts = append(opts, WithUser("admin"), WithInsecureIgnoreHostKey(),
				WithDialer(func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, errors.New("nothing is dialed in this test")
				}))
			_, err = DialHost(t.Context(), "switch1.example.net", opts...)
			return dialed, err
		}

		t.Run("alone", func(t *testing.T) {
			dialed, err := refused(t, WithSigner(vault["switch1"]...))
			be.Nonzero(t, err)
			be.In(t, "WithSigner was given no signer", err.Error())
			be.False(t, dialed)
		})

		t.Run("a nil signer", func(t *testing.T) {
			var none ssh.Signer
			dialed, err := refused(t, WithSigner(newSigner(t), none))
			be.Nonzero(t, err)
			be.In(t, "WithSigner was given a nil signer", err.Error())
			be.False(t, dialed)
		})

		// the password next to it is the login then
		t.Run("with a password", func(t *testing.T) {
			_, homePub := sshHome(t)
			sock, agentPub := testAgent(t)
			t.Setenv("SSH_AUTH_SOCK", sock)

			srv := newTestServer(t)
			seen := recordAuth(srv, homePub) // would let the identity file in
			srv.config.PasswordCallback = func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }
			srv.Serve(greet)
			host, port := hostPort(t, srv.Addr())

			tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"),
				WithSigner(vault["switch1"]...), WithPassword("secret"), WithInsecureIgnoreHostKey())
			be.NilErr(t, err)
			be.NilErr(t, tr.Close())
			be.False(t, seen.saw(homePub))
			be.False(t, seen.saw(agentPub))
		})
	})

	// with a key file and the agent, when those are asked for: one method,
	// every key in it
	t.Run("next to a key file and the agent", func(t *testing.T) {
		alone(t)
		sock, agentPub := testAgent(t)
		t.Setenv("SSH_AUTH_SOCK", sock)
		keyFile, filePub := writeKey(t, "")
		first, second := newSigner(t), newSigner(t)

		srv := newTestServer(t)
		seen := recordAuth(srv, nil) // lets nobody in, so every key is tried
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"),
			WithSigner(first), WithSigner(second), WithPrivateKeyFile(keyFile, ""), WithAgent(), WithInsecureIgnoreHostKey())
		be.True(t, errors.Is(err, ErrAuth))
		be.In(t, "offered 4 keys", err.Error())
		// the signers first and in their order, then the file, then the agent
		want := [][]byte{first.PublicKey().Marshal(), second.PublicKey().Marshal(), filePub.Marshal(), agentPub.Marshal()}
		seen.mu.Lock()
		defer seen.mu.Unlock()
		be.DeepEqual(t, want, seen.keys)
	})

	// The signer is for the device.  A jump host is logged in to the way
	// ssh does it, and never sees it.
	t.Run("not for a jump host", func(t *testing.T) {
		home, homePub := sshHome(t)
		signer := newSigner(t)

		jumpSrv := newTestServer(t)
		trust(t, home, jumpSrv.Addr(), jumpSrv.hostKey)
		jumpSeen := recordAuth(jumpSrv, homePub)
		serveJump(jumpSrv)

		device := newTestServer(t)
		deviceSeen := recordAuth(device, signer.PublicKey())
		device.Serve(greet)
		_, port := hostPort(t, device.Addr())

		tr, err := DialHost(t.Context(), "127.0.0.1", WithPort(port), WithUser("admin"),
			WithSigner(signer), WithInsecureIgnoreHostKey(), WithProxyJump(jumpSrv.Addr()))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())

		be.False(t, jumpSeen.saw(signer.PublicKey()))
		be.True(t, jumpSeen.saw(homePub))
		be.True(t, deviceSeen.saw(signer.PublicKey()))
		be.False(t, deviceSeen.saw(homePub))
	})
}
