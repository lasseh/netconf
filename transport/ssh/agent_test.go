package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// testAgent serves an ssh-agent holding one fresh key and returns its socket
// and that key.
func testAgent(t *testing.T) (sock string, pub ssh.PublicKey) {
	t.Helper()

	edPub, priv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	keyring := agent.NewKeyring()
	be.NilErr(t, keyring.Add(agent.AddedKey{PrivateKey: priv}))
	pub, err = ssh.NewPublicKey(edPub)
	be.NilErr(t, err)

	return agentSocket(t, func(conn net.Conn) { _ = agent.ServeAgent(keyring, conn) }), pub
}

// agentSocket returns a unix socket on which serve gets every connection.
func agentSocket(t *testing.T, serve func(net.Conn)) (sock string) {
	t.Helper()

	// t.TempDir is too long a path for a unix socket on some systems
	dir, err := os.MkdirTemp("", "agent")
	be.NilErr(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s")

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	be.NilErr(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	return sock
}

// holdsSignature is an agent that lists its keys and does not sign until it
// is released: one that waits for somebody to confirm the use of the key, or
// to touch it.
type holdsSignature struct {
	agent.Agent
	release <-chan struct{}
}

func (a holdsSignature) Sign(ssh.PublicKey, []byte) (*ssh.Signature, error) {
	<-a.release
	return nil, errors.New("nobody confirmed the use of the key")
}

// An agent that does not answer must not hold a dial past its context.  The
// login waits for the agent then, and not for the connection to the device,
// which is all that the end of the context used to close.
func TestDialHost_AgentThatDoesNotAnswer(t *testing.T) {
	_, otherPub := writeKey(t, "")

	tests := []struct {
		name  string
		agent func(t *testing.T, release <-chan struct{}) (sock string, accepted ssh.PublicKey)
	}{
		{
			name: "it never lists its keys",
			agent: func(t *testing.T, release <-chan struct{}) (string, ssh.PublicKey) {
				return agentSocket(t, func(conn net.Conn) {
					<-release
					_ = conn.Close()
				}), otherPub
			},
		},
		{
			name: "it lists a key and holds the signature",
			agent: func(t *testing.T, release <-chan struct{}) (string, ssh.PublicKey) {
				edPub, priv, err := ed25519.GenerateKey(rand.Reader)
				be.NilErr(t, err)
				keyring := agent.NewKeyring()
				be.NilErr(t, keyring.Add(agent.AddedKey{PrivateKey: priv}))
				pub, err := ssh.NewPublicKey(edPub)
				be.NilErr(t, err)
				return agentSocket(t, func(conn net.Conn) {
					_ = agent.ServeAgent(holdsSignature{keyring, release}, conn)
				}), pub
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			sock, accepted := tc.agent(t, release)
			t.Setenv("SSH_AUTH_SOCK", sock)
			t.Setenv("HOME", t.TempDir()) // no default identities

			srv := newTestServer(t)
			requireKey(srv, "admin", accepted)
			srv.Serve(greet)
			host, port := hostPort(t, srv.Addr())

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			dialed := make(chan error, 1)
			go func() {
				_, err := DialHost(ctx, host, WithPort(port), WithUser("admin"), WithInsecureIgnoreHostKey())
				dialed <- err
			}()

			select {
			case err := <-dialed:
				be.True(t, errors.Is(err, context.DeadlineExceeded))
			case <-time.After(3 * time.Second):
				t.Fatal("the dial still waits for the agent, 3 s after its context ended")
			}
		})
	}
}

// A key file and an agent are two sources of keys for one kind of
// authentication.  A key from either must get in, whichever the device
// accepts: the agent's keys were never offered once a key file had loaded.
func TestDialHost_KeyFilesAndAgent(t *testing.T) {
	fileKey, filePub := writeKey(t, "")

	tests := []struct {
		name   string
		accept string // the key the server takes: "file" or "agent"
		opts   func(config string) []DialOption
	}{
		{
			name:   "key file and agent, the key file is accepted",
			accept: "file",
			opts:   func(string) []DialOption { return []DialOption{WithPrivateKeyFile(fileKey, ""), WithAgent()} },
		},
		{
			name:   "key file and agent, the agent's key is accepted",
			accept: "agent",
			opts:   func(string) []DialOption { return []DialOption{WithPrivateKeyFile(fileKey, ""), WithAgent()} },
		},
		{
			name:   "identity file from the ssh config and the agent, the agent's key is accepted",
			accept: "agent",
			opts:   func(config string) []DialOption { return []DialOption{WithSSHConfig(config)} },
		},
		{
			name:   "agent only",
			accept: "agent",
			opts:   func(string) []DialOption { return []DialOption{WithAgent()} },
		},
		{
			name:   "nothing asked for, the agent is there",
			accept: "agent",
			opts:   func(string) []DialOption { return nil },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sock, agentPub := testAgent(t)
			t.Setenv("SSH_AUTH_SOCK", sock)
			t.Setenv("HOME", t.TempDir()) // no default identities

			srv := newTestServer(t)
			want := agentPub
			if tc.accept == "file" {
				want = filePub
			}
			requireKey(srv, "admin", want)
			srv.Serve(greet)
			host, port := hostPort(t, srv.Addr())

			config := filepath.Join(t.TempDir(), "config")
			be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil, "Host %s\n    IdentityFile %s\n", host, fileKey), 0o600))

			opts := append([]DialOption{WithPort(port), WithUser("admin"), WithInsecureIgnoreHostKey()}, tc.opts(config)...)
			tr, err := DialHost(t.Context(), host, opts...)
			be.NilErr(t, err)
			readGreeting(t, tr)
			be.NilErr(t, tr.Close())
			_ = srv.Wait(t)
		})
	}
}
