package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
)

type testServer struct {
	t               *testing.T // Add this field
	listener        net.Listener
	config          *ssh.ServerConfig
	errCh           chan error
	hostKey         ssh.PublicKey
	RejectSubsystem bool

	// IgnoreRequests leaves global requests (e.g. keepalives) unanswered to
	// simulate a peer that went silent.
	IgnoreRequests bool

	// SubsystemAsked, when set, is closed by the request for a subsystem,
	// which then gets no answer: a server that stops answering right there.
	SubsystemAsked chan struct{}

	// SubsystemAnswered, when set, is closed once the request for a
	// subsystem has its answer.
	SubsystemAnswered chan struct{}

	// RejectExec makes the server say no to a command.
	RejectExec bool

	// asked is what the client asked the channel for, a subsystem or a
	// command, by the name of the request
	mu    sync.Mutex
	asked []string
}

// Asked returns what the client asked the channel for: "subsystem netconf"
// or "exec" and the command.
func (s *testServer) Asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	be.NilErr(t, err)

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)

	return &testServer{
		t:        t,
		listener: ln,
		config:   config,
		errCh:    make(chan error, 1),
		hostKey:  signer.PublicKey(),
	}
}

func (s *testServer) Addr() string { return s.listener.Addr().String() }

func (s *testServer) Serve(handler func(ssh.Channel) error) {
	go func() {
		defer close(s.errCh)
		defer func() {
			if err := s.listener.Close(); err != nil {
				// We often ignore "use of closed network connection"
				// but logging it hurts nothing in verbose mode.
				s.t.Logf("testServer listener close: %v", err)
			}
		}()

		conn, err := s.listener.Accept()
		if err != nil {
			s.errCh <- fmt.Errorf("accept: %w", err)
			return
		}
		s.serve(conn, handler)
	}()
}

// CallHome has the server open the connection, to addr, and serve the one
// client it finds there: a device that connects to whoever manages it.
func (s *testServer) CallHome(addr string, handler func(ssh.Channel) error) {
	go func() {
		defer close(s.errCh)

		conn, err := (&net.Dialer{}).DialContext(s.t.Context(), "tcp", addr)
		if err != nil {
			s.errCh <- fmt.Errorf("dial: %w", err)
			return
		}
		s.serve(conn, handler)
	}()
}

// serve is the server on one connection.
func (s *testServer) serve(conn net.Conn, handler func(ssh.Channel) error) {
	_, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		s.errCh <- fmt.Errorf("handshake: %w", err)
		return
	}
	if !s.IgnoreRequests {
		go ssh.DiscardRequests(reqs)
	}

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			err := newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			if err != nil {
				s.t.Logf("failed to reject channel: %v", err)
			}
			continue
		}
		ch, reqs, err := newChannel.Accept()
		if err != nil {
			s.errCh <- fmt.Errorf("channel accept: %w", err)
			return
		}

		go func(in <-chan *ssh.Request) {
			for req := range in {
				// What was asked for is the one string of either request.
				// It is noted when the request has its answer, for a
				// handler that waits for that.
				note := func() {
					var what struct{ Name string }
					_ = ssh.Unmarshal(req.Payload, &what)
					s.mu.Lock()
					s.asked = append(s.asked, req.Type+" "+what.Name)
					s.mu.Unlock()
				}

				switch req.Type {
				case "subsystem":
					// In a real server we'd check payload == "netconf",
					// but for tests accepting any subsystem is fine.
					if s.SubsystemAsked != nil {
						note()
						close(s.SubsystemAsked)
						continue
					}
					if err := req.Reply(!s.RejectSubsystem, nil); err != nil {
						s.t.Logf("failed to reply to subsystem req: %v", err)
					}
					note()
					if s.SubsystemAnswered != nil {
						close(s.SubsystemAnswered)
					}
				case "exec":
					if err := req.Reply(!s.RejectExec, nil); err != nil {
						s.t.Logf("failed to reply to exec req: %v", err)
					}
					note()
				}
			}
		}(reqs)

		if err := handler(ch); err != nil {
			s.errCh <- err
		}
		return
	}
}

func (s *testServer) Wait(t *testing.T) error {
	t.Helper()
	err := <-s.errCh
	return err
}

func TestTransport_Dial_NetworkFailure(t *testing.T) {
	// Try to dial a port that is definitely closed (port 1 on localhost)
	config := &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}

	// Use a short timeout so the test is fast
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	tr, err := Dial(ctx, "tcp", "127.0.0.1:1", config)

	be.Nonzero(t, err)
	be.Zero(t, tr)
	// Assert it's a network error
	be.In(t, "connection refused", err.Error())
}

func TestTransport_Dial_AuthFailure(t *testing.T) {
	srv := newTestServer(t)
	// Force the server to require a password, but provide none
	srv.config.NoClientAuth = false
	srv.config.PasswordCallback = func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		return nil, fmt.Errorf("password rejected")
	}

	// We don't need srv.Serve here because the handshake will fail
	// before the handler is ever reached.
	// But we must start the listener loop:
	srv.Serve(func(ssh.Channel) error { return nil })

	config := &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		// No Auth methods provided = Auth Failure
	}

	tr, err := Dial(context.Background(), "tcp", srv.Addr(), config)

	be.Zero(t, tr)
	be.True(t, errors.Is(err, ErrAuth))

	// For some reason ErrorsIs doesn't work here despite ssh.ErrNoAuth existing.
	err = srv.Wait(t)
	be.True(t, err != nil && strings.Contains(err.Error(), "no auth passed yet"))
}

func TestTransport_DialContextCancel(t *testing.T) {
	// Standard hanging listener pattern (no changes needed here)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)
	defer func() {
		if err := ln.Close(); err != nil {
			t.Logf("failed to close listener: %v", err)
		}
	}()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			if _, err := io.Copy(io.Discard, conn); err != nil {
				t.Logf("failed to copy from conn: %v", err)
			}
		}
	}()

	config := &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// a dial that the context does not end would never return
	dialed := make(chan error, 1)
	go func() {
		_, err := Dial(ctx, "tcp", ln.Addr().String(), config)
		dialed <- err
	}()

	be.True(t, errors.Is(recvWithin(t, dialed, "the dial to give up"), context.DeadlineExceeded))
}

func TestTransport_Dial_SubsystemFails(t *testing.T) {
	srv := newTestServer(t)
	srv.RejectSubsystem = true

	srv.Serve(func(ch ssh.Channel) error {
		_, err := io.ReadAll(ch)
		return err
	})

	config := &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}

	tr, err := Dial(context.Background(), "tcp", srv.Addr(), config)

	// Dial should fail because the subsystem request was rejected
	be.True(t, errors.Is(err, ErrSubsystemRefused))
	be.Zero(t, tr)

	// Ensure the server finishes cleanly (client should close connection on error)
	be.NilErr(t, srv.Wait(t))
}

// A refused subsystem leaves nothing open on a client that stays: the channel
// NewTransport opened for it is closed again.  The server reads the channel
// until the client closes it.
func TestNewTransport_SubsystemRefusedClosesChannel(t *testing.T) {
	srv := newTestServer(t)
	srv.RejectSubsystem = true
	srv.Serve(func(ch ssh.Channel) error {
		_, err := io.ReadAll(ch)
		return err
	})

	client, err := ssh.Dial("tcp", srv.Addr(), &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	be.NilErr(t, err)
	t.Cleanup(func() { _ = client.Close() })

	tr, err := NewTransport(client)
	be.True(t, errors.Is(err, ErrSubsystemRefused))
	be.Zero(t, tr)
	be.NilErr(t, recvWithin(t, srv.errCh, "the server to see the channel closed"))
}

// A context that ends just as the dial succeeds: the dial either returns a
// transport that works, or fails with the error of the context and closes
// its connection.  It never returns a transport whose connection the end of
// the context has closed.
func TestTransport_DialContextEndsAfterSubsystem(t *testing.T) {
	failed := 0
	for i := range 20 {
		srv := newTestServer(t)
		srv.SubsystemAnswered = make(chan struct{})
		srv.Serve(func(ch ssh.Channel) error {
			_, err := io.ReadAll(ch)
			return err
		})

		ctx, cancel := context.WithCancel(t.Context())
		// the end of the context moves through the window, a little
		// later each time
		go func() {
			<-srv.SubsystemAnswered
			time.Sleep(time.Duration(i) * 20 * time.Microsecond)
			cancel()
		}()

		tr, err := Dial(ctx, "tcp", srv.Addr(), &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()})
		if err != nil {
			be.True(t, errors.Is(err, context.Canceled))
			be.Zero(t, tr)
			failed++
		} else {
			w, err := tr.MsgWriter()
			be.NilErr(t, err)
			_, err = io.WriteString(w, "<hello/>")
			be.NilErr(t, err)
			be.NilErr(t, w.Close())
			be.NilErr(t, tr.Close())
		}
		be.NilErr(t, recvWithin(t, srv.errCh, "the server to see the connection closed"))
		cancel()
	}
	t.Logf("%d of 20 dials failed with the context", failed)
}
