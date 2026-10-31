package ssh

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"

	"github.com/lasseh/netconf"
)

// stuckAfter is how long a call gets before it counts as stuck.
const stuckAfter = 5 * time.Second

// unreadable are channel requests that x/crypto cannot parse, whose request
// stream an ssh.Session stops reading at.
var unreadable = []struct {
	name    string
	kind    string
	payload []byte
}{
	{"exit-status of one byte", "exit-status", []byte{0}},
	{"exit-status of three bytes", "exit-status", []byte{1, 2, 3}},
	{"exit-signal that does not unmarshal", "exit-signal", []byte{0xff}},
}

// flood sends a request nobody can read, and then more of them than the
// channel has room for: x/crypto queues 16, and the read loop of the
// connection waits for room for the next.
func flood(ch ssh.Channel, kind string, payload []byte) {
	_, _ = ch.SendRequest(kind, false, payload)
	for range 17 {
		_, _ = ch.SendRequest("noise@example.net", false, nil)
	}
}

// within fails the test when f has not returned after stuckAfter.
func within(t *testing.T, doing string, f func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(stuckAfter):
		t.Fatalf("%s: still blocked after %s", doing, stuckAfter)
	}
}

func dialDevice(ctx context.Context, t *testing.T, srv *testServer) (*Transport, error) {
	t.Helper()
	host, port := hostPort(t, srv.Addr())
	return DialHost(ctx, host, WithPort(port), WithUser("admin"), WithPassword(""), WithHostKey(srv.hostKey))
}

// Close releases a read after the device has sent requests that x/crypto
// cannot read, on every way to a transport.
func TestChannelRequests_CloseReleasesARead(t *testing.T) {
	ways := []struct {
		name string
		open func(t *testing.T, srv *testServer) *Transport
	}{
		{"DialHost", func(t *testing.T, srv *testServer) *Transport {
			tr, err := dialDevice(t.Context(), t, srv)
			be.NilErr(t, err)
			return tr
		}},
		{"Dial", func(t *testing.T, srv *testServer) *Transport {
			tr, err := Dial(t.Context(), "tcp", srv.Addr(), &ssh.ClientConfig{
				User: "admin", HostKeyCallback: ssh.FixedHostKey(srv.hostKey),
			})
			be.NilErr(t, err)
			return tr
		}},
		{"NewTransport", func(t *testing.T, srv *testServer) *Transport {
			client, err := ssh.Dial("tcp", srv.Addr(), &ssh.ClientConfig{
				User: "admin", HostKeyCallback: ssh.FixedHostKey(srv.hostKey),
			})
			be.NilErr(t, err)
			t.Cleanup(func() { _ = client.Close() })
			tr, err := NewTransport(client)
			be.NilErr(t, err)
			return tr
		}},
	}

	for _, way := range ways {
		for _, tc := range unreadable {
			t.Run(way.name+" "+tc.name, func(t *testing.T) {
				srv := newTestServer(t)
				srv.SubsystemAnswered = make(chan struct{})
				sent, release := make(chan struct{}), make(chan struct{})
				defer close(release)
				srv.Serve(func(ch ssh.Channel) error {
					<-srv.SubsystemAnswered
					flood(ch, tc.kind, tc.payload)
					close(sent)
					<-release
					return nil
				})

				tr := way.open(t, srv)
				<-sent
				time.Sleep(100 * time.Millisecond)

				read := make(chan struct{})
				go func() {
					defer close(read)
					if r, err := tr.MsgReader(); err == nil {
						_, _ = io.ReadAll(r)
					}
				}()
				time.Sleep(50 * time.Millisecond)

				within(t, "Close", func() { _ = tr.Close() })
				within(t, "the read", func() { <-read })
			})
		}
	}
}

// The context of DialHost holds when the device does this before it answers
// the request for the subsystem.
func TestChannelRequests_DialHostDeadline(t *testing.T) {
	for _, tc := range unreadable {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			srv.SubsystemAsked = make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			srv.Serve(func(ch ssh.Channel) error {
				<-srv.SubsystemAsked
				flood(ch, tc.kind, tc.payload)
				<-release
				return nil
			})

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			within(t, "DialHost", func() {
				tr, err := dialDevice(ctx, t, srv)
				if err == nil {
					_ = tr.Close()
				}
				be.True(t, err != nil)
			})
		})
	}
}

// Closing the client gets NewTransport out of a wait for that same answer.
func TestChannelRequests_NewTransportReleasedByTheClient(t *testing.T) {
	srv := newTestServer(t)
	srv.SubsystemAsked = make(chan struct{})
	sent, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	srv.Serve(func(ch ssh.Channel) error {
		<-srv.SubsystemAsked
		flood(ch, "exit-status", []byte{0})
		close(sent)
		<-release
		return nil
	})

	client, err := ssh.Dial("tcp", srv.Addr(), &ssh.ClientConfig{
		User: "admin", HostKeyCallback: ssh.FixedHostKey(srv.hostKey),
	})
	be.NilErr(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := NewTransport(client)
		done <- err
	}()
	<-sent
	time.Sleep(100 * time.Millisecond)

	_ = client.Close()
	within(t, "NewTransport", func() { be.True(t, <-done != nil) })
}

// The context of Open holds when the device does this in place of its hello.
func TestChannelRequests_OpenDeadline(t *testing.T) {
	srv := newTestServer(t)
	srv.SubsystemAnswered = make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	srv.Serve(func(ch ssh.Channel) error {
		<-srv.SubsystemAnswered
		flood(ch, "exit-status", []byte{0})
		<-release
		return nil
	})

	tr, err := dialDevice(t.Context(), t, srv)
	be.NilErr(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	within(t, "Open", func() {
		_, err := netconf.Open(ctx, tr)
		be.True(t, err != nil)
	})
}

// The context of Session.Close holds when the device does this during a
// session, and the session ends.
func TestChannelRequests_SessionCloseDeadline(t *testing.T) {
	srv := newTestServer(t)
	srv.SubsystemAnswered = make(chan struct{})
	opened, sent, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	srv.Serve(func(ch ssh.Channel) error {
		<-srv.SubsystemAnswered
		if _, err := io.WriteString(ch, hello); err != nil {
			return err
		}
		go func() { _, _ = io.Copy(io.Discard, ch) }()
		<-opened
		flood(ch, "exit-status", []byte{0})
		close(sent)
		<-release
		return nil
	})

	tr, err := dialDevice(t.Context(), t, srv)
	be.NilErr(t, err)
	session, err := netconf.Open(t.Context(), tr)
	be.NilErr(t, err)
	close(opened)
	<-sent
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	within(t, "Session.Close", func() { _ = session.Close(ctx) })
	within(t, "Done", func() { <-session.Done() })
}
