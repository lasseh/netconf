package ssh

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/msgio"
)

// silent listens for one connection and says nothing on it, like a host
// that has stopped answering.  The channel it returns is closed once the
// client has sent its ssh version: the dial is in the handshake by then.
func silent(t *testing.T) (addr string, reached <-chan struct{}) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// the client speaks first: its version, on a line of its own
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		close(got)
		_, _ = io.Copy(io.Discard, conn)
	}()
	return ln.Addr().String(), got
}

// stallingJump is a jump host that lets the client in and does not answer
// when it is asked to connect on.  The channel it returns is closed once it
// has been asked.
func stallingJump(t *testing.T, home string) (addr string, reached <-chan struct{}) {
	t.Helper()

	srv := newTestServer(t)
	trust(t, home, srv.Addr(), srv.hostKey)
	t.Cleanup(func() { _ = srv.listener.Close() })

	got := make(chan struct{})
	go func() {
		conn, err := srv.listener.Accept()
		_ = srv.listener.Close()
		if err != nil {
			return
		}
		sc, chans, reqs, err := ssh.NewServerConn(conn, srv.config)
		if err != nil {
			return
		}
		defer func() { _ = sc.Close() }()
		go ssh.DiscardRequests(reqs)

		// asked to connect on, it neither does nor refuses
		var once sync.Once
		for range chans {
			once.Do(func() { close(got) })
		}
	}()
	return srv.Addr(), got
}

// A dial that the context ends says what it was doing and for which host,
// and still is the error of the context.  Every case stops the dial at one
// step: the other end tells the test that the dial has got there, and the
// test cancels then.
func TestDial_Interrupted(t *testing.T) {
	insecure := &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()}

	tests := []struct {
		name string
		// setup returns the dial, what tells that it has reached the step,
		// and the step as the error names it
		setup func(t *testing.T) (dial func(context.Context) error, reached <-chan struct{}, doing string)
	}{
		{"the handshake with the device", func(t *testing.T) (func(context.Context) error, <-chan struct{}, string) {
			addr, reached := silent(t)
			return func(ctx context.Context) error {
				_, err := Dial(ctx, "tcp", addr, insecure)
				return err
			}, reached, "ssh handshake with " + addr
		}},
		{"the handshake with a jump host", func(t *testing.T) (func(context.Context) error, <-chan struct{}, string) {
			home, _ := sshHome(t)
			addr, reached := silent(t)
			// the file has to be there for the dial to get as far as the host
			trust(t, home, "192.0.2.1:22", newTestServer(t).hostKey)
			return func(ctx context.Context) error {
				_, err := DialHost(ctx, "127.0.0.1", WithPort(1), WithPassword(""), WithInsecureIgnoreHostKey(), WithProxyJump(addr))
				return err
			}, reached, "ssh handshake with jump host 127.0.0.1"
		}},
		{"the way on from a jump host", func(t *testing.T) (func(context.Context) error, <-chan struct{}, string) {
			home, _ := sshHome(t)
			addr, reached := stallingJump(t, home)
			return func(ctx context.Context) error {
				_, err := DialHost(ctx, "127.0.0.1", WithPort(1), WithPassword(""), WithInsecureIgnoreHostKey(), WithProxyJump(addr))
				return err
			}, reached, "connecting to 127.0.0.1:1 through jump host 127.0.0.1"
		}},
		{"the netconf subsystem", func(t *testing.T) (func(context.Context) error, <-chan struct{}, string) {
			srv := newTestServer(t)
			srv.SubsystemAsked = make(chan struct{})
			srv.Serve(func(ch ssh.Channel) error {
				_, _ = io.Copy(io.Discard, ch)
				return nil
			})
			return func(ctx context.Context) error {
				_, err := Dial(ctx, "tcp", srv.Addr(), insecure)
				return err
			}, srv.SubsystemAsked, "starting the netconf subsystem on " + srv.Addr()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dial, reached, doing := tc.setup(t)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// a dial that nothing ends would not return
			dialed := make(chan error, 1)
			go func() { dialed <- dial(ctx) }()

			recvWithin(t, reached, "the dial to get to "+tc.name)
			cancel()

			err := recvWithin(t, dialed, "the dial to give up")
			be.True(t, errors.Is(err, context.Canceled))
			be.Equal(t, doing+": context canceled", err.Error())
		})
	}

	// nothing has answered yet when the first connection is being made
	t.Run("the first connection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := Dial(ctx, "tcp", "127.0.0.1:1", insecure)
		be.True(t, errors.Is(err, context.Canceled))
		be.Equal(t, "connecting to 127.0.0.1:1: context canceled", err.Error())
	})

	t.Run("a deadline", func(t *testing.T) {
		addr, reached := silent(t)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		dialed := make(chan error, 1)
		go func() {
			_, err := Dial(ctx, "tcp", addr, insecure)
			dialed <- err
		}()

		err := recvWithin(t, dialed, "the dial to give up")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		// on a machine that is slow enough the deadline is over before the
		// connection is made
		select {
		case <-reached:
			be.Equal(t, "ssh handshake with "+addr+": context deadline exceeded", err.Error())
		default:
		}
	})
}

// hello is the least a device says to open a session.
const hello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities>` +
	`<capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>1</session-id></hello>]]>]]>`

// A session whose transport the keepalive closed says so: it is the one way
// to tell a device that went silent from a connection that broke.
func TestKeepalive_EndsSessionWithCause(t *testing.T) {
	srv := newTestServer(t)
	srv.IgnoreRequests = true
	srv.Serve(func(ch ssh.Channel) error {
		if _, err := io.WriteString(ch, hello); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, ch)
		return nil
	})

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port), WithPassword(""), WithInsecureIgnoreHostKey(),
		WithKeepalive(20*time.Millisecond, 3),
	)
	be.NilErr(t, err)

	session, err := netconf.Open(t.Context(), tr)
	be.NilErr(t, err)

	recvWithin(t, session.Done(), "the keepalive to end the session")
	be.True(t, errors.Is(session.Err(), ErrKeepalive))
	be.In(t, "ssh keepalive gave up on the device: 3 probes in a row went unanswered for 20ms each", session.Err().Error())

	// and so does every operation after that
	_, err = session.ExecReply(t.Context(), "<get/>")
	be.True(t, errors.Is(err, netconf.ErrClosed))
	be.True(t, errors.Is(err, ErrKeepalive))
}

// The device goes silent inside a message that the session only reads to
// get past it, a notification to a session without a handler.  The keepalive
// is still what ended the session.
func TestKeepalive_EndsSessionInsideAMessageNobodyReads(t *testing.T) {
	srv := newTestServer(t)
	srv.IgnoreRequests = true
	srv.Serve(func(ch ssh.Channel) error {
		const begun = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-10T11:52:01Z</eventTime><event>`
		if _, err := io.WriteString(ch, hello+begun); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, ch)
		return nil
	})

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port), WithPassword(""), WithInsecureIgnoreHostKey(),
		WithKeepalive(20*time.Millisecond, 3),
	)
	be.NilErr(t, err)

	session, err := netconf.Open(t.Context(), tr)
	be.NilErr(t, err)

	recvWithin(t, session.Done(), "the keepalive to end the session")
	be.True(t, errors.Is(session.Err(), ErrKeepalive))
}

// A transport on somebody else's connection is closed another way, and says
// the same.
func TestKeepalive_OnAClientOfTheCaller(t *testing.T) {
	srv := newTestServer(t)
	srv.IgnoreRequests = true
	srv.Serve(func(ch ssh.Channel) error {
		_, _ = io.Copy(io.Discard, ch)
		return nil
	})

	client, err := ssh.Dial("tcp", srv.Addr(), &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	be.NilErr(t, err)
	t.Cleanup(func() { _ = client.Close() })

	tr, err := NewTransport(client)
	be.NilErr(t, err)
	go tr.keepalive(20*time.Millisecond, 2)

	read := make(chan error, 1)
	go func() {
		r, err := tr.MsgReader()
		if err == nil {
			_, err = io.ReadAll(r)
		}
		read <- err
	}()
	be.True(t, errors.Is(recvWithin(t, read, "the keepalive to close the transport"), ErrKeepalive))
}

// A message that was read to its end stays what it is: the end of a message
// is not what the keepalive did.
func TestKeepalive_LeavesTheEndOfAMessage(t *testing.T) {
	gaveUp := ErrKeepalive
	tr := &Transport{}
	tr.gaveUp.Store(&gaveUp)

	got, err := io.ReadAll(msgio.Reader(io.NopCloser(strings.NewReader("hi")), tr.because))
	be.NilErr(t, err)
	be.Equal(t, "hi", string(got))
}

// A transport that its owner closed has no keepalive to blame.
func TestKeepalive_NotNamedAfterClose(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(greet)

	host, port := hostPort(t, srv.Addr())
	tr, err := DialHost(t.Context(), host,
		WithPort(port), WithPassword(""), WithInsecureIgnoreHostKey(),
		WithKeepalive(time.Hour, 1),
	)
	be.NilErr(t, err)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())

	r, err := tr.MsgReader()
	if err == nil {
		_, err = io.ReadAll(r)
	}
	be.Nonzero(t, err)
	be.False(t, errors.Is(err, ErrKeepalive))
}
