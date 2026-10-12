package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// greet is a server handler that sends one message and waits for the client
// to hang up.
func greet(ch ssh.Channel) error {
	if _, err := io.WriteString(ch, "hi]]>]]>"); err != nil {
		return err
	}
	_, err := io.Copy(io.Discard, ch)
	return err
}

func hostPort(t *testing.T, addr string) (host string, port int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	be.NilErr(t, err)
	port, err = strconv.Atoi(portStr)
	be.NilErr(t, err)
	return host, port
}

// writeKey generates a private key file and returns its path and public key.
func writeKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)

	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	be.NilErr(t, err)

	path := filepath.Join(t.TempDir(), "id_ed25519")
	be.NilErr(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))

	sshPub, err := ssh.NewPublicKey(pub)
	be.NilErr(t, err)
	return path, sshPub
}

func requireKey(srv *testServer, user string, want ssh.PublicKey) {
	srv.config.NoClientAuth = false
	srv.config.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if conn.User() != user || !bytes.Equal(key.Marshal(), want.Marshal()) {
			return nil, fmt.Errorf("unexpected user %q or key", conn.User())
		}
		return nil, nil
	}
}

func readGreeting(t *testing.T, tr *Transport) {
	t.Helper()
	r, err := tr.MsgReader()
	be.NilErr(t, err)
	msg, err := io.ReadAll(r)
	be.NilErr(t, err)
	be.Equal(t, "hi", string(msg))
	be.NilErr(t, r.Close())
}

// What a device writes to the standard error of the netconf subsystem is
// read and dropped.  Left unread it fills the window of the channel, and a
// device that cannot write there any more does not get to its replies.
func TestDialHost_SubsystemStderr(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(func(ch ssh.Channel) error {
		// more than the window of a channel, which is 2 MiB
		if _, err := ch.Stderr().Write(make([]byte, 3<<20)); err != nil {
			return err
		}
		return greet(ch)
	})
	host, port := hostPort(t, srv.Addr())

	tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey())
	be.NilErr(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	type greeting struct {
		msg []byte
		err error
	}
	said := make(chan greeting, 1)
	go func() {
		r, err := tr.MsgReader()
		if err != nil {
			said <- greeting{err: err}
			return
		}
		msg, err := io.ReadAll(r)
		said <- greeting{msg, err}
	}()

	select {
	case got := <-said:
		be.NilErr(t, got.err)
		be.Equal(t, "hi", string(got.msg))
	case <-time.After(3 * time.Second):
		t.Fatal("the device does not get to its hello: nobody reads what it wrote to standard error")
	}
}

func TestDialHost_Password(t *testing.T) {
	srv := newTestServer(t)
	srv.config.NoClientAuth = false
	srv.config.PasswordCallback = func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if conn.User() != "admin" || string(password) != "secret" {
			return nil, fmt.Errorf("bad credentials")
		}
		return nil, nil
	}
	srv.Serve(greet)

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port),
		WithUser("admin"),
		WithPassword("secret"),
		WithInsecureIgnoreHostKey(),
	)
	be.NilErr(t, err)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())
	be.NilErr(t, srv.Wait(t))
}

func TestDialHost_PrivateKeyFile(t *testing.T) {
	for _, passphrase := range []string{"", "hunter2"} {
		t.Run("passphrase="+passphrase, func(t *testing.T) {
			keyPath, pub := writeKey(t, passphrase)

			srv := newTestServer(t)
			requireKey(srv, "admin", pub)
			srv.Serve(greet)

			host, port := hostPort(t, srv.Addr())
			tr, err := DialHost(t.Context(), host,
				WithPort(port),
				WithUser("admin"),
				WithPrivateKeyFile(keyPath, passphrase),
				WithInsecureIgnoreHostKey(),
			)
			be.NilErr(t, err)
			readGreeting(t, tr)
			be.NilErr(t, tr.Close())
		})
	}
}

func TestDialHost_EncryptedKeyWithoutPassphrase(t *testing.T) {
	keyPath, _ := writeKey(t, "hunter2")
	_, err := DialHost(t.Context(), "127.0.0.1",
		WithPrivateKeyFile(keyPath, ""),
		WithInsecureIgnoreHostKey(),
	)
	be.Nonzero(t, err)
}

func TestDialHost_KnownHosts(t *testing.T) {
	newKnownHosts := func(t *testing.T, addr string, key ssh.PublicKey) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "known_hosts")
		line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
		be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
		return path
	}

	t.Run("known key", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(greet)

		host, port := hostPort(t, srv.Addr())
		tr, err := DialHost(t.Context(), host,
			WithPort(port),
			WithPassword(""),
			WithKnownHosts(newKnownHosts(t, srv.Addr(), srv.hostKey)),
		)
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})

	t.Run("changed key", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(greet)

		_, otherKey := writeKey(t, "")
		host, port := hostPort(t, srv.Addr())
		_, err := DialHost(t.Context(), host,
			WithPort(port),
			WithPassword(""),
			WithKnownHosts(newKnownHosts(t, srv.Addr(), otherKey)),
		)
		be.Nonzero(t, err)
	})

	t.Run("unknown host", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(greet)

		host, port := hostPort(t, srv.Addr())
		_, err := DialHost(t.Context(), host,
			WithPort(port),
			WithPassword(""),
			WithKnownHosts(newKnownHosts(t, "192.0.2.1:830", srv.hostKey)),
		)
		be.Nonzero(t, err)
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := DialHost(t.Context(), "127.0.0.1",
			WithPassword(""),
			WithKnownHosts(filepath.Join(t.TempDir(), "nope")),
		)
		be.Nonzero(t, err)
	})
}

func TestDialHost_SSHConfig(t *testing.T) {
	keyPath, pub := writeKey(t, "")

	srv := newTestServer(t)
	requireKey(srv, "bob", pub)
	srv.Serve(greet)
	host, port := hostPort(t, srv.Addr())

	config := filepath.Join(t.TempDir(), "config")
	be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil, `
Host switch1
    HostName %s
    User bob
    IdentityFile %s
    IdentityFile /does/not/exist
`, host, keyPath), 0o600))

	t.Run("alias", func(t *testing.T) {
		tr, err := DialHost(t.Context(), "switch1",
			WithPort(port),
			WithSSHConfig(config),
			WithInsecureIgnoreHostKey(),
		)
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})

	t.Run("missing config", func(t *testing.T) {
		_, err := DialHost(t.Context(), "switch1",
			WithSSHConfig(filepath.Join(t.TempDir(), "nope")),
			WithInsecureIgnoreHostKey(),
		)
		be.Nonzero(t, err)
	})
}

// The ssh config says where the known hosts of a host are, unless the caller
// does.
func TestDialHost_SSHConfigKnownHosts(t *testing.T) {
	tests := []struct {
		name string
		// files is the UserKnownHostsFile of the host: good is a file with
		// its key, other one with somebody else's and missing is not there
		files func(good, other, missing string) string
		// option is what the caller asks for with WithKnownHosts, if anything
		option func(good, other string) string
		fails  string
	}{
		{
			name:  "the file of the config",
			files: func(good, _, _ string) string { return good },
		},
		{
			name:  "a file in the home directory",
			files: func(string, string, string) string { return "~/.ssh/known_hosts_work" },
		},
		{
			name:  "next to a file that is not there",
			files: func(good, _, missing string) string { return missing + " " + good },
		},
		{
			name:  "no file of the config is there",
			files: func(_, _, missing string) string { return missing },
			fails: "none of it exists",
		},
		{
			name:  "a file without the host",
			files: func(_, other, _ string) string { return other },
			fails: "key is unknown",
		},
		{
			name:   "the caller's file instead, without the host",
			files:  func(good, _, _ string) string { return good },
			option: func(_, other string) string { return other },
			fails:  "key is unknown",
		},
		{
			name:   "the caller's file instead, with the host",
			files:  func(_, other, _ string) string { return other },
			option: func(good, _ string) string { return good },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := sshHome(t)
			srv := newTestServer(t)
			srv.Serve(greet)
			host, port := hostPort(t, srv.Addr())

			dir := t.TempDir()
			write := func(path, addr string, key ssh.PublicKey) string {
				line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
				be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
				return path
			}
			good := write(filepath.Join(dir, "good"), srv.Addr(), srv.hostKey)
			write(filepath.Join(home, ".ssh", "known_hosts_work"), srv.Addr(), srv.hostKey)
			_, otherKey := writeKey(t, "")
			other := write(filepath.Join(dir, "other"), "192.0.2.1:830", otherKey)
			missing := filepath.Join(dir, "missing")

			config := filepath.Join(dir, "config")
			be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil, "Host switch1\n    HostName %s\n    UserKnownHostsFile %s\n",
				host, tc.files(good, other, missing)), 0o600))

			opts := []DialOption{WithPort(port), WithPassword(""), WithSSHConfig(config)}
			if tc.option != nil {
				opts = append(opts, WithKnownHosts(tc.option(good, other)))
			}
			tr, err := DialHost(t.Context(), "switch1", opts...)
			if tc.fails != "" {
				be.Nonzero(t, err)
				be.In(t, tc.fails, err.Error())
				return
			}
			be.NilErr(t, err)
			readGreeting(t, tr)
			be.NilErr(t, tr.Close())
		})
	}
}

func TestDialHost_NoAuth(t *testing.T) {
	// no agent and a home directory without identity files
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err := DialHost(t.Context(), "127.0.0.1", WithInsecureIgnoreHostKey())
	be.Nonzero(t, err)

	_, err = DialHost(t.Context(), "127.0.0.1", WithAgent(), WithInsecureIgnoreHostKey())
	be.Nonzero(t, err)
}

func TestDialHost_KeepaliveClosesDeadPeer(t *testing.T) {
	srv := newTestServer(t)
	srv.IgnoreRequests = true
	srv.Serve(func(ch ssh.Channel) error {
		_, err := io.Copy(io.Discard, ch)
		return err
	})

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port),
		WithPassword(""),
		WithInsecureIgnoreHostKey(),
		WithKeepalive(20*time.Millisecond, 2),
	)
	be.NilErr(t, err)

	// A read blocked on the silent peer must be released by the keepalive
	// closing the transport.
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

	select {
	case err := <-done:
		// and the read says what happened to it
		be.True(t, errors.Is(err, ErrKeepalive))
		be.Equal(t, "ssh keepalive gave up on the device: 2 probes in a row went unanswered for 20ms each", err.Error())
	case <-time.After(5 * time.Second):
		t.Fatal("keepalive did not close the transport")
	}

	// so does a write, and whoever asks for a reader or a writer after that
	_, err = tr.MsgReader()
	be.True(t, errors.Is(err, ErrKeepalive))
	w, err := tr.MsgWriter()
	if err == nil {
		if _, err = io.WriteString(w, "<rpc/>"); err == nil {
			err = w.Close()
		}
	}
	be.True(t, errors.Is(err, ErrKeepalive))
}

func TestDialHost_KeepaliveLeavesHealthyPeer(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(greet)

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port),
		WithPassword(""),
		WithInsecureIgnoreHostKey(),
		WithKeepalive(10*time.Millisecond, 1),
	)
	be.NilErr(t, err)

	time.Sleep(100 * time.Millisecond)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())
}

func TestTransport_CloseIsIdempotent(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(greet)

	config := &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	tr, err := Dial(context.Background(), "tcp", srv.Addr(), config)
	be.NilErr(t, err)

	be.NilErr(t, tr.Close())
	be.NilErr(t, tr.Close())
}

func TestDialHost_KnownHostsPort22Entry(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(greet)
	host, port := hostPort(t, srv.Addr())

	// known_hosts as written by a plain "ssh host": no port
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{host}, srv.hostKey)
	be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))

	tr, err := DialHost(t.Context(), host, WithPort(port), WithPassword(""), WithKnownHosts(path))
	be.NilErr(t, err)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())
}

func TestDialHost_KnownHostsPort22EntryWrongKey(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(greet)
	host, port := hostPort(t, srv.Addr())

	_, otherKey := writeKey(t, "")
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{host}, otherKey)
	be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))

	_, err := DialHost(t.Context(), host, WithPort(port), WithPassword(""), WithKnownHosts(path))
	be.Nonzero(t, err)
}

func TestDialHost_KeyboardInteractive(t *testing.T) {
	srv := newTestServer(t)
	srv.config.NoClientAuth = false
	srv.config.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		answers, err := challenge("", "", []string{"Password: "}, []bool{false})
		if err != nil {
			return nil, err
		}
		if len(answers) != 1 || answers[0] != "secret" {
			return nil, fmt.Errorf("bad credentials")
		}
		return nil, nil
	}
	srv.Serve(greet)

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port), WithUser("admin"), WithPassword("secret"), WithInsecureIgnoreHostKey())
	be.NilErr(t, err)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())
}

// Identity files from the ssh config do not switch off the agent.
func TestDialHost_SSHConfigIdentityKeepsAgent(t *testing.T) {
	keyPath, _ := writeKey(t, "")
	config := filepath.Join(t.TempDir(), "config")
	be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil, "Host switch1\n    IdentityFile %s\n", keyPath), 0o600))

	// a socket path nobody listens on: the agent is tried and skipped
	cfg := dialConfig{
		sshConfig: &config,
		env:       environment{home: t.TempDir(), agentSock: filepath.Join(t.TempDir(), "agent.sock")},
	}
	file, err := cfg.loadSSHConfig()
	be.NilErr(t, err)
	_, _, err = cfg.applySSHConfig(file, "switch1")
	be.NilErr(t, err)
	be.Equal(t, 0, len(cfg.keyFiles))
	be.Equal(t, 1, len(cfg.identityFiles))

	methods, closeAgent, err := cfg.authMethods()
	be.NilErr(t, err)
	closeAgent()
	be.Equal(t, 1, len(methods))

	// requiring the agent makes its absence an error
	cfg.agent = true
	_, _, err = cfg.authMethods()
	be.Nonzero(t, err)
}

func TestDialHost_WithClientConfig(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(greet)
	host, port := hostPort(t, srv.Addr())

	var seenUser string
	tr, err := DialHost(t.Context(), host,
		WithPort(port), WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey(),
		WithClientConfig(func(c *ssh.ClientConfig) {
			seenUser = c.User
			c.ClientVersion = "SSH-2.0-netconf-test"
		}))
	be.NilErr(t, err)
	be.Equal(t, "admin", seenUser)
	be.NilErr(t, tr.Close())
}

func TestDial_NilConfig(t *testing.T) {
	_, err := Dial(t.Context(), "tcp", "127.0.0.1:1", nil)
	be.Nonzero(t, err)
}

func TestDialHost_KnownHostsCertAuthorityPort22Entry(t *testing.T) {
	// the server presents a host certificate signed by a CA
	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	caSigner, err := ssh.NewSignerFromKey(caPriv)
	be.NilErr(t, err)

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	be.NilErr(t, err)

	srv := newTestServer(t)
	host, port := hostPort(t, srv.Addr())

	cert := &ssh.Certificate{
		Key:             hostSigner.PublicKey(),
		CertType:        ssh.HostCert,
		ValidPrincipals: []string{host},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	be.NilErr(t, cert.SignCert(rand.Reader, caSigner))
	certSigner, err := ssh.NewCertSigner(cert, hostSigner)
	be.NilErr(t, err)

	srv.config = &ssh.ServerConfig{NoClientAuth: true}
	srv.config.AddHostKey(certSigner)
	srv.Serve(greet)

	// the CA is trusted for the host by name, without a port
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := "@cert-authority " + knownhosts.Line([]string{host}, caSigner.PublicKey())
	be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))

	tr, err := DialHost(t.Context(), host, WithPort(port), WithPassword(""), WithKnownHosts(path))
	be.NilErr(t, err)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())
}

func TestPasswordPrompt(t *testing.T) {
	t.Run("single hidden prompt", func(t *testing.T) {
		answers, err := passwordPrompt("secret")("", "", []string{"Password: "}, []bool{false})
		be.NilErr(t, err)
		be.Equal(t, 1, len(answers))
		be.Equal(t, "secret", answers[0])
	})

	t.Run("informational round", func(t *testing.T) {
		answers, err := passwordPrompt("secret")("", "banner", nil, nil)
		be.NilErr(t, err)
		be.Equal(t, 0, len(answers))
	})

	t.Run("echoed prompt", func(t *testing.T) {
		_, err := passwordPrompt("secret")("", "", []string{"Username: "}, []bool{true})
		be.Nonzero(t, err)
	})

	t.Run("several prompts", func(t *testing.T) {
		_, err := passwordPrompt("secret")("", "", []string{"Password: ", "Code: "}, []bool{false, false})
		be.Nonzero(t, err)
	})

	t.Run("asked again", func(t *testing.T) {
		prompt := passwordPrompt("secret")
		_, err := prompt("", "", []string{"Password: "}, []bool{false})
		be.NilErr(t, err)
		_, err = prompt("", "", []string{"Password: "}, []bool{false})
		be.Nonzero(t, err)
	})
}

// With nothing configured, the keys in the home directory are offered.
func TestAuthMethods_DefaultIdentities(t *testing.T) {
	home := t.TempDir()
	keyPath, _ := writeKey(t, "")
	key, err := os.ReadFile(keyPath)
	be.NilErr(t, err)
	be.NilErr(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	be.NilErr(t, os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), key, 0o600))

	cfg := dialConfig{env: environment{home: home}}
	methods, closeAgent, err := cfg.authMethods()
	be.NilErr(t, err)
	closeAgent()
	be.Equal(t, 1, len(methods))

	// an explicit password switches the implicit keys off
	password := "secret"
	cfg.password = &password
	methods, closeAgent, err = cfg.authMethods()
	be.NilErr(t, err)
	closeAgent()
	be.Equal(t, 2, len(methods)) // password and keyboard-interactive
}

func TestAuthMethods_NothingAvailable(t *testing.T) {
	cfg := dialConfig{env: environment{home: t.TempDir()}}
	_, _, err := cfg.authMethods()
	be.Nonzero(t, err)

	cfg.agent = true
	_, _, err = cfg.authMethods()
	be.Nonzero(t, err)
}

func TestExpandHome(t *testing.T) {
	env := environment{home: "/home/me"}
	for path, want := range map[string]string{
		"~":              "/home/me",
		"~/.ssh/config":  "/home/me/.ssh/config",
		"/etc/ssh/known": "/etc/ssh/known",
		"relative":       "relative",
		"~other/x":       "~other/x",
	} {
		got, err := env.expandHome(path)
		be.NilErr(t, err)
		be.Equal(t, want, got)
	}

	_, err := environment{}.expandHome("~/x")
	be.Nonzero(t, err)
}
