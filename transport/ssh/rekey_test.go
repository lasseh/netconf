package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"

	"github.com/lasseh/netconf"
)

// A device can get a key exchange that the client started stuck for good.  It
// stops reading its socket, so the client's KEXINIT is never answered, and then
// sends what makes the client write, which is held until the key exchange ends.
// Rejecting a channel open is such a write.  With enough of them queued the read
// loop of x/crypto stops, and from then on no goroutine touches the socket, so
// closing it wakes nobody.
const (
	// rekeyThreshold is low to keep the tests small.  On a real connection it
	// is 1 GiB for chacha20-poly1305 and more for the others.
	rekeyThreshold = 64 << 10

	// rekeySettle is how long x/crypto gets to take in what a device sent.
	rekeySettle = 200 * time.Millisecond

	// rekeyDeadline is the deadline a test gives a call.
	rekeyDeadline = 300 * time.Millisecond
)

// rekeyOpens are the channel opens of the tests: 98 leave a way out, and 99 stop
// the read loop.
var rekeyOpens = []int{98, 99}

// deaf is the socket of a device that can stop reading.
type deaf struct {
	net.Conn
	armed  atomic.Bool
	resume chan struct{}
}

func (d *deaf) Read(p []byte) (int, error) {
	n, err := d.Conn.Read(p)
	if d.armed.Load() {
		<-d.resume
	}
	return n, err
}

// rekeyDevice is a device, or a jump host, that can get a key exchange stuck.
type rekeyDevice struct {
	t      *testing.T
	ln     net.Listener
	key    ssh.PublicKey
	config *ssh.ServerConfig

	// answers says that it answers the request for the subsystem, and greets
	// that it then says hello and answers operations.
	answers, greets bool

	socket  *deaf
	conn    *ssh.ServerConn
	ready   chan struct{} // socket and conn are set
	asked   chan struct{} // the subsystem was asked for
	once    sync.Once
	closing chan struct{}
}

func newRekeyDevice(t *testing.T, answers, greets bool) *rekeyDevice {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	be.NilErr(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	be.NilErr(t, err)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	be.NilErr(t, err)

	d := &rekeyDevice{
		t: t, ln: ln, key: signer.PublicKey(),
		config:  &ssh.ServerConfig{NoClientAuth: true},
		answers: answers, greets: greets,
		ready: make(chan struct{}), asked: make(chan struct{}), closing: make(chan struct{}),
	}
	d.config.Ciphers = []string{ssh.CipherChaCha20Poly1305}
	d.config.RekeyThreshold = 1 << 40 // it never asks for a key exchange itself
	d.config.AddHostKey(signer)

	go d.serve()
	t.Cleanup(func() {
		close(d.closing)
		_ = ln.Close()
		if d.socket != nil {
			_ = d.socket.Close()
		}
	})
	return d
}

func (d *rekeyDevice) serve() {
	conn, err := d.ln.Accept()
	_ = d.ln.Close()
	if err != nil {
		return
	}
	d.socket = &deaf{Conn: conn, resume: d.closing}
	sc, chans, reqs, err := ssh.NewServerConn(d.socket, d.config)
	if err != nil {
		return
	}
	d.conn = sc
	close(d.ready)
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, in, err := nc.Accept()
			if err != nil {
				return
			}
			go d.requests(ch, in)
		case "direct-tcpip":
			d.forward(nc)
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func (d *rekeyDevice) requests(ch ssh.Channel, in <-chan *ssh.Request) {
	for req := range in {
		if req.Type != "subsystem" {
			_ = req.Reply(false, nil)
			continue
		}
		d.once.Do(func() { close(d.asked) })
		if !d.answers {
			continue
		}
		_ = req.Reply(true, nil)
		if d.greets {
			_, _ = io.WriteString(ch, hello)
			go answerOperations(ch)
		}
	}
}

var messageID = regexp.MustCompile(`message-id="([^"]+)"`)

// answerOperations answers every operation with ok.
func answerOperations(ch ssh.Channel) {
	var seen []byte
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		seen = append(seen, buf[:n]...)
		for {
			end := bytes.Index(seen, []byte("]]>]]>"))
			if end < 0 {
				break
			}
			if m := messageID.FindSubmatch(seen[:end]); m != nil {
				_, _ = fmt.Fprintf(ch, `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="%s"><ok/></rpc-reply>]]>]]>`, m[1])
			}
			seen = seen[end+6:]
		}
	}
}

// forward connects a jump host to the next hop.
func (d *rekeyDevice) forward(nc ssh.NewChannel) {
	var to struct {
		Host  string
		Port  uint32
		From  string
		FromP uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &to); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(to.Host, fmt.Sprint(to.Port))) //nolint:noctx // the test ends it
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "no route")
		return
	}
	ch, in, err := nc.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(in)
	go func() { _, _ = io.Copy(target, ch); _ = target.Close() }()
	go func() { _, _ = io.Copy(ch, target); _ = ch.Close() }()
}

// stall stops reading the socket, has the client start a key exchange, and sends
// opens channel opens that it has to reject.
func (d *rekeyDevice) stall(opens int) {
	d.socket.armed.Store(true)

	// what counts towards the client's threshold and begins no message
	payload := make([]byte, 32<<10)
	for sent := 0; sent < rekeyThreshold+2*len(payload); sent += len(payload) {
		_, _, _ = d.conn.SendRequest("x@example.net", false, payload)
	}
	time.Sleep(rekeySettle)

	for range opens {
		go func() { _, _, _ = d.conn.OpenChannel("x@example.net", nil) }()
	}
	time.Sleep(rekeySettle)
}

func (d *rekeyDevice) clientConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            "admin",
		HostKeyCallback: ssh.FixedHostKey(d.key),
		Config:          ssh.Config{RekeyThreshold: rekeyThreshold},
	}
}

func (d *rekeyDevice) dial(ctx context.Context) (*Transport, error) {
	return dial(ctx, "tcp", []hop{{addr: d.ln.Addr().String(), config: d.clientConfig()}}, nil, "")
}

func TestStuckKeyExchange_CloseReleasesARead(t *testing.T) {
	for _, opens := range rekeyOpens {
		t.Run(fmt.Sprint(opens, " opens"), func(t *testing.T) {
			t.Parallel()
			d := newRekeyDevice(t, true, false)
			tr, err := d.dial(t.Context())
			be.NilErr(t, err)
			<-d.asked
			<-d.ready
			d.stall(opens)

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

func TestStuckKeyExchange_SessionCloseDeadline(t *testing.T) {
	for _, opens := range rekeyOpens {
		t.Run(fmt.Sprint(opens, " opens"), func(t *testing.T) {
			t.Parallel()
			d := newRekeyDevice(t, true, true)
			tr, err := d.dial(t.Context())
			be.NilErr(t, err)
			session, err := netconf.Open(t.Context(), tr)
			be.NilErr(t, err)
			<-d.ready
			d.stall(opens)

			ctx, cancel := context.WithTimeout(t.Context(), rekeyDeadline)
			defer cancel()
			within(t, "Session.Close", func() { _ = session.Close(ctx) })
			within(t, "Done", func() { <-session.Done() })
		})
	}
}

func TestStuckKeyExchange_DialDeadline(t *testing.T) {
	for _, opens := range rekeyOpens {
		t.Run(fmt.Sprint(opens, " opens"), func(t *testing.T) {
			t.Parallel()
			d := newRekeyDevice(t, false, false)
			go func() {
				<-d.ready
				<-d.asked
				d.stall(opens)
			}()

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			within(t, "the dial", func() {
				tr, err := d.dial(ctx)
				if err == nil {
					_ = tr.Close()
				}
				be.True(t, err != nil)
			})
		})
	}
}

func TestStuckKeyExchange_OpenDeadline(t *testing.T) {
	for _, opens := range rekeyOpens {
		t.Run(fmt.Sprint(opens, " opens"), func(t *testing.T) {
			t.Parallel()
			d := newRekeyDevice(t, true, false)
			tr, err := d.dial(t.Context())
			be.NilErr(t, err)
			<-d.asked
			<-d.ready

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := netconf.Open(ctx, tr)
				done <- err
			}()
			d.stall(opens)
			within(t, "Open", func() { be.True(t, <-done != nil) })
		})
	}
}

// A message that stops arriving ends the session in the same way.
func TestStuckKeyExchange_MessageTimeout(t *testing.T) {
	t.Parallel()
	d := newRekeyDevice(t, true, true)
	tr, err := d.dial(t.Context())
	be.NilErr(t, err)
	session, err := netconf.Open(t.Context(), tr, netconf.WithMessageTimeout(100*time.Millisecond))
	be.NilErr(t, err)
	<-d.ready
	d.stall(99)

	// the session ends by itself or at the close, and neither may hang
	ctx, cancel := context.WithTimeout(t.Context(), rekeyDeadline)
	defer cancel()
	within(t, "Session.Close", func() { _ = session.Close(ctx) })
	within(t, "Done", func() { <-session.Done() })
}

// Through a jump host, with the stall on either end.
func TestStuckKeyExchange_ThroughAJumpHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		onJump bool
	}{{"on the jump host", true}, {"on the device", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jump := newRekeyDevice(t, false, false)
			device := newRekeyDevice(t, true, true)
			tr, err := dial(t.Context(), "tcp", []hop{
				{jump: "jump", addr: jump.ln.Addr().String(), config: jump.clientConfig()},
				{addr: device.ln.Addr().String(), config: device.clientConfig()},
			}, nil, "")
			be.NilErr(t, err)
			session, err := netconf.Open(t.Context(), tr)
			be.NilErr(t, err)

			on := device
			if tc.onJump {
				on = jump
			}
			<-on.ready
			on.stall(99)

			ctx, cancel := context.WithTimeout(t.Context(), rekeyDeadline)
			defer cancel()
			within(t, "Session.Close", func() { _ = session.Close(ctx) })
			within(t, "Done", func() { <-session.Done() })
		})
	}
}

// A jump host that gets stuck during the login to the device, which goes over a
// channel on it: the context of the dial holds there as well.
func TestStuckKeyExchange_DuringTheLoginThroughAJumpHost(t *testing.T) {
	t.Parallel()
	jump := newRekeyDevice(t, false, false)
	device, reached := silent(t)
	go func() {
		<-jump.ready
		<-reached
		jump.stall(99)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	within(t, "the dial", func() {
		_, err := dial(ctx, "tcp", []hop{
			{jump: "jump", addr: jump.ln.Addr().String(), config: jump.clientConfig()},
			{addr: device, config: &ssh.ClientConfig{User: "admin", HostKeyCallback: ssh.InsecureIgnoreHostKey()}}, //nolint:gosec // a test device
		}, nil, "")
		be.True(t, err != nil)
	})
}

// The last thing a session writes reaches the device, on the pipes the framer
// works on: what Session.Close sends is not cut off by the close behind it.
func TestCloseSessionReachesTheDevice(t *testing.T) {
	srv := newTestServer(t)
	srv.SubsystemAnswered = make(chan struct{})
	got := make(chan string, 1)
	srv.Serve(func(ch ssh.Channel) error {
		<-srv.SubsystemAnswered
		if _, err := io.WriteString(ch, hello); err != nil {
			return err
		}
		var seen []byte
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			seen = append(seen, buf[:n]...)
			if bytes.Contains(seen, []byte("close-session")) && bytes.HasSuffix(seen, []byte("]]>]]>")) {
				m := messageID.FindSubmatch(seen[bytes.Index(seen, []byte("]]>]]>"))+6:])
				got <- "close-session"
				if len(m) == 2 {
					_, _ = fmt.Fprintf(ch, `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="%s"><ok/></rpc-reply>]]>]]>`, m[1])
				}
				return nil
			}
			if err != nil {
				return nil
			}
		}
	})

	tr, err := dialDevice(t.Context(), t, srv)
	be.NilErr(t, err)
	session, err := netconf.Open(t.Context(), tr)
	be.NilErr(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), stuckAfter)
	defer cancel()
	be.NilErr(t, session.Close(ctx))
	be.Equal(t, "close-session", <-got)
}
