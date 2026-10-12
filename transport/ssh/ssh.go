// Package ssh is the NETCONF over SSH transport of RFC 6242.
//
// DialHost connects the way the OpenSSH client does: known_hosts, ssh-agent,
// key files, jump hosts and optionally an ssh config file.  Dial takes a
// ready-made ssh.ClientConfig instead, and NewTransport an established
// ssh.Client.
//
// The package name collides with golang.org/x/crypto/ssh, so it is usually
// imported under another name:
//
//	import ncssh "github.com/lasseh/netconf/transport/ssh"
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lasseh/netconf/internal/tail"
	"github.com/lasseh/netconf/transport"
)

// alias it to a private type so we can make it private when embedding.
type framer = transport.Framer

// Transport implements RFC6242 for implementing NETCONF protocol over SSH.
type Transport struct {
	// c is the connection to the device
	c *ssh.Client

	// conn is the connection a transport made by Dial or DialHost closes
	// when it is closed: the one to the device or, through jump hosts, the
	// one to the first of them, which all the others run over.  It is nil
	// for a transport on somebody else's connection.
	conn io.Closer

	// release cuts a transport on somebody else's connection off from its
	// channel, see newTransport.  It is nil for a transport with a conn.
	release func()

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}

	// gaveUp is why the keepalive closed the transport, if it did
	gaveUp atomic.Pointer[error]

	*framer
}

// Dial will connect to a ssh server and issues a transport, it's used as a
// convenience function as essentially is the same as
//
//		c, err := ssh.Dial(network, addr, config)
//	 	if err != nil { /* ... handle error ... */ }
//	 	t, err := NewTransport(c)
//
// When the transport is closed the underlying connection is also closed.
func Dial(ctx context.Context, network, addr string, config *ssh.ClientConfig) (*Transport, error) {
	if config == nil {
		return nil, errors.New("ssh client config is required")
	}
	return dial(ctx, network, []hop{{addr: addr, config: config}}, nil, "")
}

// dialFunc makes the first connection of a dial: to the device, or to the
// first jump host.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// hop is one host to log in to on the way to the device: a jump host, or the
// device itself, which is the last.
type hop struct {
	// jump names a jump host in errors.  It is empty for the device.
	jump   string
	addr   string
	config *ssh.ClientConfig

	// offered notes what the login methods of config were asked for, nil
	// when they are not ours to watch
	offered *loginOffer
}

// ErrAuth is what a dial fails with when the login to a host on the way did
// not succeed: to the device, or to a jump host, which the error then
// names.  A wrong password, a key the host does not know and a user it does
// not know all end here, and Junos does not say which it was.  So does a
// host that asks for more than DialHost answers, a second factor.  The error
// says who tried and what was offered, and has the error of the ssh client
// underneath.
var ErrAuth = errors.New("ssh: login failed")

// ErrSubsystemRefused is what a dial fails with when the device let the user
// in and refused to start the netconf subsystem: NETCONF is not enabled on
// it, or not on this port.
var ErrSubsystemRefused = errors.New("ssh: the device refused the netconf subsystem")

// loginOffer is what the ssh client asked the login methods of DialHost for.
// The ssh client calls them from the dial, and its error does not say what
// it tried with.
type loginOffer struct {
	keys        atomic.Int32
	password    atomic.Bool
	interactive atomic.Bool
}

func (o *loginOffer) String() string {
	if o == nil {
		return ""
	}
	var parts []string
	switch keys := o.keys.Load(); keys {
	case 0:
	case 1:
		parts = append(parts, "1 key")
	default:
		parts = append(parts, fmt.Sprintf("%d keys", keys))
	}
	if o.password.Load() || o.interactive.Load() {
		parts = append(parts, "the password")
	}
	if len(parts) == 0 {
		return ", offered nothing"
	}
	return ", offered " + strings.Join(parts, " and ")
}

// login logs in to the hop over conn.  A login that the host refused fails
// with ErrAuth.
func (h hop) login(conn net.Conn) (sshConn ssh.Conn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request, err error) {
	// The ssh client checks the host key and then logs in, and what it
	// fails with does not say how far it got.  So the check says when it
	// has passed: what fails after that is the login.
	var hostKeyAccepted atomic.Bool
	config := *h.config
	if check := config.HostKeyCallback; check != nil {
		config.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			err := check(hostname, remote, key)
			hostKeyAccepted.Store(err == nil)
			return err
		}
	}

	sshConn, chans, reqs, err = ssh.NewClientConn(conn, h.addr, &config)
	var netErr net.Error
	switch {
	case err == nil:
		return sshConn, chans, reqs, nil
	case !hostKeyAccepted.Load(), errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed), errors.As(err, &netErr):
		// not got as far as the login, or the connection ended under it
		return nil, nil, nil, err
	}
	return nil, nil, nil, fmt.Errorf("%w: user %q at %s%s: %w", ErrAuth, config.User, h.addr, h.offered, err)
}

// isNil reports whether conn is no connection: nil, or a nil pointer in the
// interface, which is what a function has when it returns the two values of
// net.DialTCP as they come.  Nothing can be called on either.
func isNil(conn net.Conn) bool {
	if conn == nil {
		return true
	}
	v := reflect.ValueOf(conn)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// dial logs in to every hop, each through the one before it, and starts
// NETCONF on the last: with the netconf subsystem, or with command when
// there is one.  connect makes the connection to the first hop, nil for one
// made here.
func dial(ctx context.Context, network string, hops []hop, connect dialFunc, command string) (*Transport, error) {
	// doing is what the dial is busy with.  When the context ends the dial,
	// that is what its error says: the error of the step itself is then no
	// more than a connection that was closed under it.
	doing := "connecting to " + hops[0].String()
	interrupted := func(err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", doing, ctxErr)
		}
		return err
	}

	var (
		first net.Conn
		err   error
	)
	if connect != nil {
		// the error of somebody else's function does not say what it was for
		first, err = connect(ctx, network, hops[0].addr)
		switch {
		case err != nil:
			// a connection that came with the error is ours all the same
			if !isNil(first) {
				_ = first.Close()
			}
			err = fmt.Errorf("%s: %w", doing, err)
		case isNil(first):
			err = fmt.Errorf("%s: the function given to WithDialer returned no connection", doing)
		}
	} else {
		d := net.Dialer{Timeout: hops[0].config.Timeout}
		first, err = d.DialContext(ctx, network, hops[0].addr)
	}
	if err != nil {
		return nil, interrupted(hops[0].failed(err))
	}

	// Close the connection if the context is cancelled.  This is needed as the
	// ssh library doesn't support contexts for dialing so this approximates a
	// context based cancelation/timeout for the ssh handshake.  Every further
	// hop runs over this connection, so it ends their handshakes as well.
	//
	// Since writing this code, Go 1.20 added DialContext to ssh.ClientConn but it doesn't support a custom configuration
	stop := context.AfterFunc(ctx, func() { _ = first.Close() })

	// fail gives up on the dial.  Closing the first connection takes every
	// client on top of it down too.
	fail := func(err error) (*Transport, error) {
		stop()
		_ = first.Close()
		return nil, interrupted(err)
	}

	var clients []*ssh.Client
	for i, h := range hops {
		conn := first
		if i > 0 {
			doing = fmt.Sprintf("connecting to %s through jump host %s", h, hops[i-1].jump)
			if conn, err = clients[i-1].DialContext(ctx, "tcp", h.addr); err != nil {
				return fail(fmt.Errorf("failed to connect to %s through jump host %s: %w", h.addr, hops[i-1].jump, err))
			}
		}

		doing = "ssh handshake with " + h.String()
		sshConn, chans, reqs, err := h.login(conn)
		if err != nil {
			return fail(h.failed(err))
		}
		clients = append(clients, ssh.NewClient(sshConn, chans, reqs))
	}

	doing = "starting the netconf subsystem on " + hops[len(hops)-1].addr
	if command != "" {
		doing = fmt.Sprintf("running %q on %s", command, hops[len(hops)-1].addr)
	}
	t, err := newTransport(clients[len(clients)-1], clients[0], command)
	if err != nil {
		return fail(err)
	}

	// stop() returning false means the context already fired and the AfterFunc
	// closed (or is closing) the connection, so the transport is dead.
	if !stop() {
		_ = first.Close()
		return nil, interrupted(ctx.Err())
	}

	return t, nil
}

// String names the hop in an error: a jump host by the name it was given,
// the device by its address.
func (h hop) String() string {
	if h.jump != "" {
		return "jump host " + h.jump
	}
	return h.addr
}

// failed says which jump host an error is about.  An error about the device
// is left as it is.
func (h hop) failed(err error) error {
	if h.jump == "" {
		return err
	}
	return fmt.Errorf("jump host %s: %w", h.jump, err)
}

// NewTransport will create a new ssh transport as defined in RFC6242 for use
// with netconf.  Unlike Dial, the underlying client will not be automatically
// closed when the transport is closed (however any sessions and subsystems
// are still closed).
//
// Closing the transport releases everything waiting on it at once, also
// when the device has stopped answering.  The channel itself can then only
// be torn down together with the connection, so a goroutine stays behind
// until the client is closed.
func NewTransport(client *ssh.Client) (*Transport, error) {
	return newTransport(client, nil, "")
}

// newTransport starts NETCONF on client: with the netconf subsystem, or with
// command when there is one.  conn is what closing the transport closes, nil
// when the connection is not the transport's.
func newTransport(client *ssh.Client, conn io.Closer, command string) (*Transport, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create ssh session: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// The request is sent as it is, and not with RequestSubsystem, for its
	// answer: a device that says no is another matter than a request that
	// did not get through.
	const subsystem = "netconf"
	request, payload := "subsystem", ssh.Marshal(&struct{ Subsystem string }{subsystem})

	// said is what a command wrote to its standard error, all of it once
	// heard is closed
	var (
		said  *tail.Buffer
		heard = make(chan struct{})
	)
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	if command != "" {
		request, payload = "exec", ssh.Marshal(&struct{ Command string }{command})

		said = &tail.Buffer{}
		go func() {
			_, _ = io.Copy(said, stderr)
			close(heard)
		}()
	} else {
		// Nothing is expected on the standard error of the subsystem.  What
		// a device writes there is read and dropped all the same: left
		// unread it fills the window of the channel, and a device that
		// cannot write there any more does not get to its replies.
		go func() { _, _ = io.Copy(io.Discard, stderr) }()
	}

	ok, err := sess.SendRequest(request, true, payload)
	switch {
	case err != nil && command != "":
		_ = sess.Close()
		return nil, fmt.Errorf("ssh: failed to ask the device to run %q: %w", command, err)
	case err != nil:
		_ = sess.Close()
		return nil, fmt.Errorf("failed to start netconf ssh subsytem: %w", err)
	case !ok && command != "":
		_ = sess.Close()
		return nil, fmt.Errorf("ssh: the device refused to run %q", command)
	case !ok:
		_ = sess.Close()
		return nil, fmt.Errorf("%w: is NETCONF enabled on it, and is this its port?", ErrSubsystemRefused)
	}

	if command != "" {
		// A device that takes the request has not started NETCONF yet: a
		// command that cannot run says so on its standard error and ends.
		// NETCONF says hello without being asked, so the first byte tells
		// the one from the other, and a dial that fails can say why.
		var first [1]byte
		if _, err := io.ReadFull(stdout, first[:]); err != nil {
			_ = sess.Close()
			// its last words may be a moment behind the end of its output
			select {
			case <-heard:
			case <-time.After(time.Second):
			}
			why := said.String()
			if why != "" {
				why = ": " + why
			}
			return nil, fmt.Errorf("ssh: %q ended before it said anything%s: %w", command, why, err)
		}
		// put back in front of the rest, for a framer that has read nothing
		stdout = io.MultiReader(bytes.NewReader(first[:]), stdout)
	}

	t := &Transport{
		c:      client,
		closed: make(chan struct{}),
	}

	if conn != nil {
		t.conn = conn
		t.framer = transport.NewFramer(stdout, stdin)
		return t, nil
	}

	// The connection is the caller's, so closing it is not how this
	// transport gets out of IO stuck on a device that stopped answering,
	// and an ssh channel offers nothing else.  The framer therefore works on
	// two pipes that Close can cut, and the channel behind them is left to
	// two goroutines that end when it does.
	inR, inW := io.Pipe()
	go func() {
		_, err := io.Copy(inW, stdout)
		_ = inW.CloseWithError(err)
	}()

	outR, outW := io.Pipe()
	go func() {
		_, err := io.Copy(stdin, outR)
		// fail writers rather than leave them waiting on a pipe nobody reads
		_ = outR.CloseWithError(err)

		// Everything written before Close has gone out by now, so the
		// channel can be closed.  This waits for a peer that is gone.
		_ = stdin.Close()
		_ = sess.Close()
	}()

	t.release = func() {
		_ = inR.Close()
		_ = outW.Close()
	}
	t.framer = transport.NewFramer(inR, outW)
	return t, nil
}

// Close will close the underlying transport.  If the connection was created
// with Dial or DialHost then the underlying ssh.Client is closed as well,
// and with it the connections to the jump hosts.  If not only the sessions
// is closed.  Close is safe to call more than once and from multiple
// goroutines; every call returns the result of the first.
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)
		t.closeErr = t.close()
	})
	return t.closeErr
}

// close must not wait for the peer: it is how a session gives up on a device
// that stopped answering.  Closing the channel politely does wait, because
// x/crypto/ssh sends the channel's EOF and close behind any write still in
// flight, and a write to a peer that stopped reading stays in flight.
func (t *Transport) close() error {
	// A connection the transport made is the transport's to close, which
	// takes the channel with it and releases everything blocked on either.
	//
	// Through jump hosts it is the connection to the first of them and no
	// other: that one is a network connection, closed without a word to
	// anybody, and everything on top of it ends with it.  The connection to
	// the device is a channel on the last jump host, and closing a channel
	// waits like closing the netconf channel does.
	if t.conn != nil {
		if err := t.conn.Close(); err != nil {
			return fmt.Errorf("failed to close ssh connection: %w", err)
		}
		return nil
	}

	t.release()
	return nil
}

// ErrKeepalive is what reading from and writing to a transport fail with
// once its keepalive has closed it, see WithKeepalive.  A session on such a
// transport ends with it: errors.Is(session.Err(), ErrKeepalive).
var ErrKeepalive = errors.New("ssh keepalive gave up on the device")

// MsgReader returns a reader for the next message, like the one of the
// Framer.  Once the keepalive has closed the transport, what the reader
// fails with says so.
func (t *Transport) MsgReader() (io.ReadCloser, error) {
	r, err := t.framer.MsgReader()
	if err != nil {
		return nil, t.because(err)
	}
	return msgReader{r, t}, nil
}

// MsgWriter returns a writer for a new message, like the one of the Framer.
// Once the keepalive has closed the transport, what the writer fails with
// says so.
func (t *Transport) MsgWriter() (io.WriteCloser, error) {
	w, err := t.framer.MsgWriter()
	if err != nil {
		return nil, t.because(err)
	}
	return msgWriter{w, t}, nil
}

// msgReader and msgWriter are the reader and the writer of a message, with
// errors that name the keepalive when it closed the transport under them.
type (
	msgReader struct {
		io.ReadCloser
		t *Transport
	}
	msgWriter struct {
		io.WriteCloser
		t *Transport
	}
)

func (r msgReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	// the end of a message is not a failure
	if err != nil && err != io.EOF { //nolint:errorlint // what a reader ends with, never wrapped
		err = r.t.because(err)
	}
	return n, err
}

// Close reads the message to its end.  What that fails with is explained
// like the error of a Read: the session closes a message that nobody reads
// without reading it first.
func (r msgReader) Close() error {
	if err := r.ReadCloser.Close(); err != nil {
		return r.t.because(err)
	}
	return nil
}

func (w msgWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err != nil {
		err = w.t.because(err)
	}
	return n, err
}

func (w msgWriter) Close() error {
	if err := w.WriteCloser.Close(); err != nil {
		return w.t.because(err)
	}
	return nil
}

// because returns why IO on the transport failed with err: the keepalive, if
// it closed the transport, and err as it is otherwise.
func (t *Transport) because(err error) error {
	if gaveUp := t.gaveUp.Load(); gaveUp != nil {
		return *gaveUp
	}
	return err
}

// keepalive probes the peer every interval and closes the transport once
// maxMissed consecutive probes went unanswered.  It returns when the
// transport is closed.
func (t *Transport) keepalive(interval time.Duration, maxMissed int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	missed := 0
	for {
		select {
		case <-t.closed:
			return
		case <-ticker.C:
		}

		// SendRequest has no deadline, so bound each probe by the interval.
		// The buffered channel lets an abandoned probe finish on its own
		// once the connection is closed.
		reply := make(chan error, 1)
		go func() {
			_, _, err := t.c.SendRequest("keepalive@openssh.com", true, nil)
			reply <- err
		}()

		select {
		case <-t.closed:
			return
		case err := <-reply:
			if err != nil {
				_ = t.Close()
				return
			}
			missed = 0
		case <-time.After(interval):
			missed++
			if missed >= maxMissed {
				gaveUp := fmt.Errorf("%w: %d probes in a row went unanswered for %s each", ErrKeepalive, missed, interval)
				t.gaveUp.Store(&gaveUp)
				_ = t.Close()
				return
			}
		}
	}
}
