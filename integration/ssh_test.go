//go:build integration

// Package integration runs tests against a real NETCONF device described by
// the NETCONF_DUT_* environment variables, see README.md.  They read, and
// load or commit only when asked to.
package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
	"github.com/lasseh/netconf/rpc"
	"github.com/lasseh/netconf/transport"
	nccmd "github.com/lasseh/netconf/transport/command"
	ncssh "github.com/lasseh/netconf/transport/ssh"
)

func onlyFlavor(t *testing.T, flavors ...string) {
	t.Helper()
	for _, flavor := range flavors {
		if os.Getenv("NETCONF_DUT_FLAVOR") == flavor {
			return
		}
	}
	t.Skipf("test only for flavors '%s'.  Skipping", strings.Join(flavors, ","))
}

// logWriter copies the framed traffic to the test log.
type logWriter struct {
	t      *testing.T
	prefix string
}

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(w.prefix, strconv.Quote(string(p)))
	return len(p), nil
}

func dialOptions(t *testing.T) []ncssh.DialOption {
	t.Helper()

	// Lab devices come and go, so host keys are not verified here.
	opts := []ncssh.DialOption{ncssh.WithInsecureIgnoreHostKey()}

	if port := os.Getenv("NETCONF_DUT_SSHPORT"); port != "" {
		p, err := strconv.Atoi(port)
		be.NilErr(t, err)
		opts = append(opts, ncssh.WithPort(p))
	}
	if user := os.Getenv("NETCONF_DUT_SSHUSER"); user != "" {
		opts = append(opts, ncssh.WithUser(user))
	}
	if config, ok := os.LookupEnv("NETCONF_DUT_SSHCONFIG"); ok {
		opts = append(opts, ncssh.WithSSHConfig(config))
	}
	if pass, ok := os.LookupEnv("NETCONF_DUT_SSHPASS"); ok {
		opts = append(opts, ncssh.WithPassword(pass))
	}
	if command := os.Getenv("NETCONF_DUT_SSHCOMMAND"); command != "" {
		opts = append(opts, ncssh.WithCommand(command))
	}
	if keyFile := os.Getenv("NETCONF_DUT_SSHKEYFILE"); keyFile != "" {
		opts = append(opts, ncssh.WithPrivateKeyFile(keyFile, os.Getenv("NETCONF_DUT_SSHKEYPASS")))
	}
	return opts
}

// dialSSH connects to the device, and returns the context that bounds that
// and the hello exchange after it.
//
// With NETCONF_DUT_COMMAND set the connection is that command and not the
// ssh client of the library: the suite runs over the transport of package
// command.  A test that asks for something of the ssh transport is skipped
// then.
func dialSSH(t *testing.T, opts ...ncssh.DialOption) (context.Context, transport.Transport) {
	t.Helper()

	host := os.Getenv("NETCONF_DUT_SSHHOST")
	if host == "" {
		t.Skip("NETCONF_DUT_SSHHOST not set, skipping test")
	}

	// A lab device that has been left alone for a while can take the better
	// part of a minute to answer its first connection.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	var tr interface {
		transport.Transport
		DebugCapture(input, output io.Writer)
	}
	if command := strings.Fields(os.Getenv("NETCONF_DUT_COMMAND")); len(command) > 0 {
		if len(opts) > 0 {
			t.Skip("the test needs the ssh transport of the library and NETCONF_DUT_COMMAND is set, skipping test")
		}
		started, err := nccmd.Start(exec.CommandContext(context.Background(), command[0], command[1:]...)) //nolint:gosec // the command of whoever runs the suite
		be.NilErr(t, err)
		tr = started
	} else {
		dialed, err := ncssh.DialHost(ctx, host, append(dialOptions(t), opts...)...)
		be.NilErr(t, err)
		tr = dialed
	}

	// The traffic holds whatever the tests read, the whole configuration of
	// the device among it, so it is only logged when asked for.
	if os.Getenv("NETCONF_DUT_TRACE") != "" {
		tr.DebugCapture(logWriter{t, "S: "}, logWriter{t, "C: "})
	}
	return ctx, tr
}

// open opens a session the way the suite does for this device.
//
// A session that Junos starts for "xml-mode netconf need-trailer" ends its
// messages with the trailer the command asks for, also where the device is
// set up for chunked framing and says base:1.1 in its hello.  So such a
// session offers base:1.0 only.
func open(ctx context.Context, tr transport.Transport, opts ...netconf.SessionOption) (*netconf.Session, error) {
	if strings.Contains(os.Getenv("NETCONF_DUT_SSHCOMMAND"), "need-trailer") {
		opts = append(opts, netconf.WithCapability(netconf.CapNetConf10))
	}
	return netconf.Open(ctx, tr, opts...)
}

func setupSSH(t *testing.T, opts ...ncssh.DialOption) *netconf.Session {
	t.Helper()

	ctx, tr := dialSSH(t, opts...)
	session, err := open(ctx, tr)
	be.NilErr(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		be.NilErr(t, session.Close(ctx))

		select {
		case <-session.Done():
		default:
			t.Error("session not done after Close")
		}
		be.NilErr(t, session.Err())
	})
	return session
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSSHDial(t *testing.T) {
	session := setupSSH(t)
	be.Nonzero(t, session.SessionID())
	be.Nonzero(t, session.ServerCaps().Len())
	be.True(t, session.ServerCaps().Has(netconf.CapNetConf10) || session.ServerCaps().Has(netconf.CapNetConf11))

	// What a device advertises is the first thing to compare between two
	// releases of its software.
	t.Logf("the device advertises:\n%s", strings.Join(slices.Sorted(session.ServerCaps().All()), "\n"))
}

func TestSSHGetConfig(t *testing.T) {
	session := setupSSH(t)

	config, err := rpc.GetConfig{Source: rpc.Running}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.Nonzero(t, len(config))
}

func TestSSHGetConfigStream(t *testing.T) {
	session := setupSSH(t)

	msg, err := session.Stream(testCtx(t), rpc.GetConfig{Source: rpc.Running})
	be.NilErr(t, err)

	var reply rpc.GetConfigReply
	be.NilErr(t, msg.Decode(&reply))
	be.Nonzero(t, len(reply.Config))

	// the session is usable again once the stream is consumed
	_, err = rpc.GetConfig{Source: rpc.Running}.Exec(testCtx(t), session)
	be.NilErr(t, err)
}

func TestBadGetConfig(t *testing.T) {
	session := setupSSH(t)

	cfg, err := rpc.GetConfig{Source: "non-exist"}.Exec(testCtx(t), session)
	be.Zero(t, cfg)
	var rpcErr netconf.RPCError
	be.True(t, errors.As(err, &rpcErr))

	// an rpc-error leaves the session usable
	_, err = rpc.GetConfig{Source: rpc.Running}.Exec(testCtx(t), session)
	be.NilErr(t, err)
}

func TestConcurrentOperations(t *testing.T) {
	session := setupSSH(t)
	ctx := testCtx(t)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range cap(errs) {
		wg.Go(func() {
			_, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		be.NilErr(t, err)
	}
}

// turnTransport counts the messages of a session: those begun, those written
// to their end, those read to their end, and those begun while the answer to
// one before was still to be read to its end.
type turnTransport struct {
	transport.Transport

	mu       sync.Mutex
	begun    int // messages a writer was taken for, the hello among them
	written  int // messages written to their end
	answered int // messages read to their end, the hello among them
	together int // messages begun while another was unanswered
	// when the last message was written to its end, and the last one read
	// to its end
	lastWritten, lastAnswered time.Time
}

func (t *turnTransport) MsgWriter() (io.WriteCloser, error) {
	w, err := t.Transport.MsgWriter()
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.begun > t.answered {
		t.together++
	}
	t.begun++
	return &turnWriter{WriteCloser: w, t: t}, nil
}

// turnWriter writes one message.  The session closes it when the message is
// out.
type turnWriter struct {
	io.WriteCloser
	t *turnTransport
}

func (w *turnWriter) Close() error {
	err := w.WriteCloser.Close()
	if err == nil {
		w.t.mu.Lock()
		defer w.t.mu.Unlock()
		w.t.written++
		w.t.lastWritten = time.Now()
	}
	return err
}

func (t *turnTransport) MsgReader() (io.ReadCloser, error) {
	r, err := t.Transport.MsgReader()
	if err != nil {
		return nil, err
	}
	return &turnReader{ReadCloser: r, t: t}, nil
}

func (t *turnTransport) writtenTogether() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.together
}

// log says what went over the transport, for a request that got no answer.
// With as many messages read as begun, no request was on its way.  With
// more begun than written, a write did not come to its end.  With more
// written than read, a request is out and has no answer.
func (t *turnTransport) log(tb testing.TB) {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	tb.Logf("the transport: %d messages begun, %d written to their end, the last %s ago, and %d read to their end, the last %s ago",
		t.begun, t.written, time.Since(t.lastWritten).Round(time.Millisecond),
		t.answered, time.Since(t.lastAnswered).Round(time.Millisecond))
}

// turnReader reads one message.  The session closes it when it has read the
// message to its end.
type turnReader struct {
	io.ReadCloser
	t    *turnTransport
	once sync.Once
}

func (r *turnReader) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(func() {
		r.t.mu.Lock()
		defer r.t.mu.Unlock()
		r.t.answered++
		r.t.lastAnswered = time.Now()
	})
	return err
}

// onWire returns how many bytes a message of size bytes takes over chunked
// framing, cut into chunks by the writer of package transport, the end of
// the chunks included.
func onWire(t *testing.T, size int) int {
	t.Helper()

	var wire bytes.Buffer
	framer := transport.NewFramer(strings.NewReader(""), &wire)
	framer.Upgrade()
	w, err := framer.MsgWriter()
	be.NilErr(t, err)
	_, err = w.Write(bytes.Repeat([]byte{'x'}, size))
	be.NilErr(t, err)
	be.NilErr(t, w.Close())
	return wire.Len()
}

// Two requests made at once over chunked framing, of the sizes that cost a
// session on Junos 26.2.  There the second request was written right behind
// the first, the two together put the end of the chunks of the second
// across byte 8192, and Junos ended the session in 3 of 40 tries.
//
// A session sends such a device one request at a time, and the test holds it
// to that: no request is written before the answer to the one before it has
// been read to its end.  Any other device gets the two as they come, and
// has to answer both.
//
// A pair has a minute.  When it is not answered the test says what went
// over the transport, which tells a request that got no answer from one
// whose write did not end and from one that was never begun.
func TestRequestsAtOnce(t *testing.T) {
	request, answer := "<get-config><source><running/></source></get-config>", "data"
	toJunos := os.Getenv("NETCONF_DUT_FLAVOR") == "junos"
	if toJunos {
		request, answer = "<get-system-uptime-information/>", "system-uptime-information"
	}

	dialCtx, tr := dialSSH(t)
	counted := &turnTransport{Transport: tr}
	session, err := open(dialCtx, counted)
	be.NilErr(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(func() {
		be.NilErr(t, session.Close(ctx))
		cancel()
	})
	if !session.ServerCaps().Has(netconf.CapNetConf11) || !session.ClientCaps().Has(netconf.CapNetConf11) {
		t.Skip("the session does not speak chunks, skipping test")
	}

	// sized returns the request as a message that takes wire bytes over
	// chunks once the session has given it message-id id
	sized := func(id, wire int) *netconf.RPC {
		t.Helper()
		msg := netconf.NewRPC(request)
		numbered := msg.Clone()
		numbered.MessageID = strconv.Itoa(id)
		bare, err := xml.Marshal(numbered)
		be.NilErr(t, err)
		// the headers of its chunks and their end take a few bytes of that
		for size := max(len(bare), wire-64); size <= wire; size++ {
			if onWire(t, size) == wire {
				msg.Operation = request + strings.Repeat(" ", size-len(bare))
				return msg
			}
		}
		t.Fatalf("no message takes %d bytes over chunks", wire)
		return nil
	}

	const (
		end   = len("\n##\n")
		first = 4000 // bytes of the one request on the wire
	)
	pairs := 0
	// where the end of the chunks of the second request begins, counted from
	// the first byte of the first: with three, two and one of its bytes in
	// front of byte 8192
	for _, at := range []int{8189, 8190, 8191} {
		for range 8 {
			pairs++
			pairCtx, done := context.WithTimeout(ctx, time.Minute)
			errs := make(chan error, 2)
			// Whichever of the two goes first, the other ends at that place.
			// The session numbers its requests from 1, so the two have the
			// message-ids 2*pairs-1 and 2*pairs in either order, and take the
			// same bytes together both ways.
			for i, wire := range []int{first, at + end - first} {
				msg := sized(2*pairs-1+i, wire)
				go func() {
					reply, err := session.ExecReply(pairCtx, msg)
					if err == nil {
						_, err = reply.Element(answer)
					}
					errs <- err
				}()
			}
			err := errors.Join(<-errs, <-errs)
			done()
			if err != nil {
				counted.log(t)
				t.Fatalf("pair %d, the end of the second at %d: %v", pairs, at, err)
			}
		}
	}

	together := counted.writtenTogether()
	if toJunos {
		be.Equal(t, 0, together)
	}
	t.Logf("%d pairs of requests, all answered; %d requests were written while another was unanswered", pairs, together)
}

// messageTimeout is the bound on the silence inside a message that
// docs/operations.md names as safe.
const messageTimeout = 10 * time.Minute

// gapTransport measures the silence inside the messages read through it:
// the longest time between two reads that brought bytes, after the first of
// a message.
type gapTransport struct {
	transport.Transport

	longest atomic.Int64 // nanoseconds
	at      atomic.Int64 // how far into its message that was, in bytes
}

func (t *gapTransport) MsgReader() (io.ReadCloser, error) {
	r, err := t.Transport.MsgReader()
	if err != nil {
		return nil, err
	}
	return &gapReader{ReadCloser: r, t: t}, nil
}

// take returns the longest silence since it was last called, and where in
// its message it was.
func (t *gapTransport) take() (gap time.Duration, at int64) {
	return time.Duration(t.longest.Swap(0)), t.at.Swap(0)
}

type gapReader struct {
	io.ReadCloser
	t    *gapTransport
	last time.Time
	read int64
}

func (r *gapReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		now := time.Now()
		if gap := int64(now.Sub(r.last)); !r.last.IsZero() && gap > r.t.longest.Load() {
			r.t.longest.Store(gap)
			r.t.at.Store(r.read)
		}
		r.last = now
		r.read += int64(n)
	}
	return n, err
}

// The silence inside a message.  A session opened with the message timeout
// of docs/operations.md has to outlive what is read here, the whole
// configuration, and for Junos in every format.  The longest silence inside
// each reply is logged with where in the reply it was, and has to stay far
// below the timeout.
//
// Junos sends the start tag of a reply at once and the rest when it has
// done the work, so its silence sits behind the first hundred or so bytes
// and is the time the request took.  With commits allowed the test times
// one: that is the request a bound has to be longer than.
//
// The line break Junos writes behind a reply begins the next message, so
// whatever the test waits for between two requests would count as silence
// inside the next reply.  Nothing is waited for once the measured session is
// open.
func TestMessageTimeout(t *testing.T) {
	// A session that is left alone is not one whose message has stopped.
	// Junos writes a line break behind every message, the hello included,
	// which is the first byte of the next message to the framing and must
	// not start the clock: with a bound of three seconds a session has to be
	// there after seven.
	idleCtx, idleTr := dialSSH(t)
	idle, err := open(idleCtx, idleTr, netconf.WithMessageTimeout(3*time.Second))
	be.NilErr(t, err)
	time.Sleep(7 * time.Second)
	select {
	case <-idle.Done():
		t.Errorf("a session that was left alone has ended: %v", idle.Err())
	default:
	}
	be.NilErr(t, idle.Close(testCtx(t)))

	ctx, tr := dialSSH(t)
	gaps := &gapTransport{Transport: tr}
	session, err := open(ctx, gaps, netconf.WithMessageTimeout(messageTimeout))
	be.NilErr(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		be.NilErr(t, session.Close(ctx))
		be.NilErr(t, session.Err())
	})

	measure := func(name string, op func(ctx context.Context) error) {
		t.Helper()
		gaps.take()
		be.NilErr(t, op(changeCtx(t)))
		gap, at := gaps.take()
		t.Logf("%s: the longest silence inside a message was %s, %d bytes into it", name, gap.Round(time.Millisecond), at)
		if gap > messageTimeout/10 {
			t.Errorf("%s: a silence of %s leaves the message timeout of %s little room", name, gap, messageTimeout)
		}
	}

	measure("get-config", func(ctx context.Context) error {
		_, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
		return err
	})

	if os.Getenv("NETCONF_DUT_FLAVOR") != "junos" {
		return
	}

	for _, format := range []junos.Format{junos.FormatXML, junos.FormatText, junos.FormatSet, junos.FormatJSON} {
		measure("get-configuration as "+string(format), func(ctx context.Context) error {
			_, err := junos.GetConfiguration{Format: format}.Exec(ctx, session)
			return err
		})
	}
	if commitPending(t, session) {
		t.Log("a confirmed commit waits for its confirmation and a commit check would confirm it: none is sent")
		return
	}
	measure("commit check", func(ctx context.Context) error {
		_, err := junos.CommitConfiguration{Check: true}.Exec(ctx, session)
		return err
	})

	if os.Getenv("NETCONF_DUT_CHANGE_COMMIT") == "" {
		return
	}
	_, config := probe()
	probeAbsent(testCtx(t), t, session)
	t.Cleanup(func() {
		measure("commit that takes the group out", func(ctx context.Context) error {
			_, err := junos.Change{Format: junos.FormatSet, Config: "delete groups " + probeGroup}.Apply(ctx, session)
			return err
		})
	})
	measure("commit", func(ctx context.Context) error {
		result, err := junos.Change{Format: junos.FormatSet, Config: config}.Apply(ctx, session)
		if err == nil && !result.Committed {
			err = errors.New("nothing was committed")
		}
		return err
	})
}

func TestKeepalive(t *testing.T) {
	session := setupSSH(t, ncssh.WithKeepalive(200*time.Millisecond, 3))

	// several keepalive rounds must leave a healthy session alone
	time.Sleep(time.Second)
	select {
	case <-session.Done():
		t.Fatalf("session ended: %v", session.Err())
	default:
	}

	_, err := rpc.GetConfig{Source: rpc.Running}.Exec(testCtx(t), session)
	be.NilErr(t, err)
}

// Notifications, against a device that advertises them.  One session
// subscribes and another logs in, so that the device has something to say.
// What arrives first has to have a time and a content, whatever it is about:
// netopeer2 reports the subscription itself before anything else, and Junos
// OS Evolved a change of state that has nothing to do with the login.
//
// Whether the device goes on answering on the session with the subscription
// is told and not required.  Junos advertises :interleave as soon as it has
// notifications and answers nothing there, <close-session> included, unless
// "system services netconf notification interleave" is set.
func TestNotifications(t *testing.T) {
	if !setupSSH(t).ServerCaps().Has(":notification:1.0") {
		t.Skip("the device does not advertise notifications, skipping test")
	}

	notifs := make(chan netconf.Notification, 4096)
	ctx, tr := dialSSH(t)
	subscribed, err := open(ctx, tr, netconf.WithNotifHandler(func(_ context.Context, msg *netconf.Message) {
		var notif netconf.Notification
		if err := msg.Decode(&notif); err != nil {
			t.Errorf("a notification does not decode: %v", err)
			return
		}
		select {
		case notifs <- notif:
		default:
		}
	}))
	be.NilErr(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := subscribed.Close(ctx); err != nil {
			t.Logf("the session with the subscription was closed without a word from the device: %v", err)
		}
	})

	be.NilErr(t, rpc.CreateSubscription{}.Exec(testCtx(t), subscribed))

	// somebody logs in
	other := setupSSH(t)

	select {
	case notif := <-notifs:
		be.Nonzero(t, notif.EventTime)
		be.Nonzero(t, notif.Content.XMLName.Local)
	case <-time.After(time.Minute):
		t.Fatal("no notification within a minute of another session logging in")
	}

	// On Junos the stream is the system log, with what a commit was told to
	// say about itself, in letters that arrive a byte at a time as in a
	// reply.  Junos OS Evolved has other things in its stream and says
	// nothing about a commit.
	if os.Getenv("NETCONF_DUT_FLAVOR") == "junos" && os.Getenv("NETCONF_DUT_CHANGE_COMMIT") != "" {
		session := commitSession(t)
		_, config := probe()
		const said, letters = "netconf integration test, notified: ", "Tromsø – Ålesund"
		_, err := junos.Change{Format: junos.FormatSet, Config: config, Comment: said + letters}.Apply(changeCtx(t), session)
		be.NilErr(t, err)

		for told, wait := false, time.After(20*time.Second); !told; {
			select {
			case notif := <-notifs:
				if told = bytes.Contains(notif.Content.Inner, []byte(said)); told {
					be.In(t, said+letters, string(notif.Content.Inner))
				}
			case <-wait:
				t.Log("the device has sent no notification about the commit")
				told = true
			}
		}
	}

	// An operation the device refuses is one it has answered.
	answered, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = rpc.GetConfig{Source: "non-exist"}.Exec(answered, subscribed)
	var rpcErr netconf.RPCError
	switch {
	case errors.As(err, &rpcErr):
		t.Log("the device answers operations on the session with the subscription")
	case errors.Is(err, context.DeadlineExceeded):
		t.Log("the device does not answer operations on the session with the subscription")
	default:
		t.Errorf("an operation on the session with the subscription: %v", err)
	}

	// the other session is not bothered by any of it
	_, err = rpc.GetConfig{Source: "non-exist"}.Exec(testCtx(t), other)
	be.True(t, errors.As(err, &rpcErr))
}

func TestJunosRawRPC(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	var info struct {
		Hostname string `xml:"system-information>host-name"`
		Model    string `xml:"system-information>hardware-model"`
		Version  string `xml:"system-information>os-version"`
	}
	be.NilErr(t, session.Exec(testCtx(t), "<get-system-information/>", &info))
	be.Nonzero(t, info.Model)
	be.Nonzero(t, info.Version)

	reply, err := session.ExecReply(testCtx(t), "<get-interface-information><terse/></get-interface-information>")
	be.NilErr(t, err)
	be.In(t, "<interface-information", string(reply.Raw))

	_, err = session.ExecReply(testCtx(t), "<no-such-rpc/>")
	var rpcErr netconf.RPCError
	be.True(t, errors.As(err, &rpcErr))
}

func TestJunosGetConfiguration(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	const filter = `<configuration><system><host-name/></system></configuration>`

	xmlCfg, err := junos.GetConfiguration{Filter: filter}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.True(t, strings.HasPrefix(string(xmlCfg), "<configuration"))

	text, err := junos.GetConfiguration{Format: junos.FormatText, Filter: filter}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.In(t, "system {", string(text))

	set, err := junos.GetConfiguration{Format: junos.FormatSet, Filter: filter, Candidate: true}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.In(t, "set system host-name", string(set))

	inherited, err := junos.GetConfiguration{Format: junos.FormatText, Filter: filter, Inherit: true}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.In(t, "system {", string(inherited))

	jsonCfg, err := junos.GetConfiguration{Format: junos.FormatJSON, Filter: filter}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.True(t, json.Valid(jsonCfg))

	// With chunked framing Junos 26.2 does not finish a reply in a format
	// without indentation: it stops in the middle of it and the session
	// waits for the rest.  So GetConfiguration refuses the two formats on
	// such a session and sends nothing.  A session of its own, with a bound
	// on that wait, says what this device does with the request.
	if session.ServerCaps().Has(netconf.CapNetConf11) && session.ClientCaps().Has(netconf.CapNetConf11) {
		_, err := junos.GetConfiguration{Format: junos.FormatXMLMinified, Filter: filter}.Exec(testCtx(t), session)
		be.Nonzero(t, err)
		be.In(t, "over chunked framing", err.Error())
		be.NilErr(t, session.Err())

		if finishesMinified(t, filter) {
			t.Log("this device finishes a reply without indentation over chunked framing, which Junos 26.2 does not")
		}
		return
	}

	// The two formats without indentation, of the whole configuration: the
	// same content in fewer bytes.  Exec fails when a device answers them in
	// another format, which a release that does not know them does.
	whole := func(format junos.Format) []byte {
		t.Helper()
		cfg, err := junos.GetConfiguration{Format: format}.Exec(testCtx(t), session)
		be.NilErr(t, err)
		return cfg
	}
	indented, minified := whole(junos.FormatXML), whole(junos.FormatXMLMinified)
	be.True(t, strings.HasPrefix(string(minified), "<configuration"))
	be.True(t, len(minified) < len(indented))
	var asIndented, asMinified struct {
		Statements []struct {
			XMLName xml.Name
		} `xml:",any"`
	}
	be.NilErr(t, xml.Unmarshal(indented, &asIndented))
	be.NilErr(t, xml.Unmarshal(minified, &asMinified))
	be.Nonzero(t, len(asMinified.Statements))
	be.DeepEqual(t, asIndented.Statements, asMinified.Statements)

	indented, minified = whole(junos.FormatJSON), whole(junos.FormatJSONMinified)
	be.True(t, json.Valid(minified))
	be.True(t, len(minified) < len(indented))
	t.Logf("the configuration as JSON: %d bytes, without indentation %d", len(indented), len(minified))
}

// finishesMinified reports whether the device finishes the reply to a small
// request for configuration without indentation.  It asks on a session of
// its own, which a message that stops arriving ends after 15 seconds, and
// with ExecReply: GetConfiguration.Exec sends no such request over chunked
// framing.
func finishesMinified(t *testing.T, filter string) bool {
	t.Helper()
	ctx, tr := dialSSH(t)
	session, err := open(ctx, tr, netconf.WithMessageTimeout(15*time.Second))
	be.NilErr(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = session.Close(ctx)
	}()

	_, err = session.ExecReply(testCtx(t), junos.GetConfiguration{Format: junos.FormatXMLMinified, Filter: filter})
	if err == nil {
		return true
	}
	// the session is over, and over a message that stopped
	be.True(t, errors.Is(err, netconf.ErrClosed))
	be.True(t, errors.Is(session.Err(), netconf.ErrMessageStalled))
	t.Logf("a request for configuration without indentation: %v", session.Err())
	return false
}

// The attributes of an element, and the namespaces declared around it.
// Junos says on <configuration> when it was committed, in attributes whose
// prefix the <rpc-reply> declares, and gives every time the seconds behind
// it the same way.  The namespace of that prefix has the release in it.
func TestJunosElementAttributes(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	reply, err := session.ExecReply(testCtx(t), junos.GetConfiguration{
		Filter: `<configuration><system><services><netconf/></services></system></configuration>`,
	})
	be.NilErr(t, err)
	config, err := reply.Element("configuration")
	be.NilErr(t, err)

	var committed string
	for _, attr := range config.Attr {
		t.Logf("configuration has %s in %q", attr.Name.Local, attr.Name.Space)
		if attr.Name.Local == "commit-seconds" {
			be.True(t, strings.HasPrefix(attr.Name.Space, "http://xml.juniper.net/junos/"))
			committed = attr.Value
		}
	}
	seconds, err := strconv.ParseInt(committed, 10, 64)
	be.NilErr(t, err)
	be.True(t, time.Unix(seconds, 0).Before(time.Now().Add(24*time.Hour)))

	// an element cut out of a reply decodes in the namespaces it stood in
	reply, err = session.ExecReply(testCtx(t), "<get-system-uptime-information/>")
	be.NilErr(t, err)
	envelope, err := reply.Element("rpc-reply")
	be.NilErr(t, err)
	result, err := reply.Element("system-uptime-information")
	be.NilErr(t, err)
	declared := func(el netconf.Element, name xml.Name) string {
		for _, attr := range el.Attr {
			if attr.Name == name {
				return attr.Value
			}
		}
		t.Fatalf("no %s among %v", name.Local, el.Attr)
		return ""
	}
	prefix := declared(envelope, xml.Name{Space: "xmlns", Local: "junos"})
	namespace := declared(result, xml.Name{Local: "xmlns"})

	current, err := reply.Element("current-time")
	be.NilErr(t, err)
	var got struct {
		XMLName xml.Name
		Time    struct {
			Attr []xml.Attr `xml:",any,attr"`
		} `xml:"date-time"`
	}
	be.NilErr(t, current.Decode(&got))
	be.Equal(t, xml.Name{Space: namespace, Local: "current-time"}, got.XMLName)
	be.Equal(t, 1, len(got.Time.Attr))
	be.Equal(t, xml.Name{Space: prefix, Local: "seconds"}, got.Time.Attr[0].Name)
}

func TestJunosCompareRollback(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	_, err := junos.CompareRollback{Rollback: 1}.Exec(testCtx(t), session)
	be.NilErr(t, err)
}

func TestJunosCommand(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	out, err := junos.Command("show version").Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.In(t, "Junos", out)

	doc, err := junos.Command("show system information").JSON(testCtx(t), session)
	be.NilErr(t, err)
	be.True(t, json.Valid(doc))

	reply, err := junos.Command("show system uptime").XML(testCtx(t), session)
	be.NilErr(t, err)
	var uptime struct {
		Booted string `xml:"system-booted-time>date-time"`
	}
	be.NilErr(t, junos.Decode(reply, "system-uptime-information", &uptime))
	be.Nonzero(t, uptime.Booted)

	// A command that fails must not read as one that printed nothing.  The
	// one reply that may is a reply with nothing in it, which is how Junos
	// 23.4 answers this one in rfc-compliant mode.
	const missing = junos.Command("show interfaces nonexistent-0/0/0")
	out, err = missing.Exec(testCtx(t), session)
	if err == nil {
		be.Equal(t, "", out)
		reply, err := session.ExecReply(testCtx(t), missing)
		be.NilErr(t, err)
		var body struct {
			Text     string `xml:",chardata"`
			Children []struct {
				XMLName xml.Name
			} `xml:",any"`
		}
		be.NilErr(t, reply.Decode(&body))
		be.Equal(t, "", strings.TrimSpace(body.Text))
		be.Equal(t, 0, len(body.Children))
		t.Log("the device answers a command that fails with a reply that has nothing in it")
	}
}

func TestJunosSystemInformation(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	be.True(t, junos.IsJunos(session))

	info, err := junos.GetSystemInformation{}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.Nonzero(t, info.HostName)
	be.Nonzero(t, info.Model)
	be.Nonzero(t, info.Version)
}

// A device hands out the YANG modules of its release.  `make junos-schema`
// compares two of them with what the library reads, and NETCONF_DUT_YANG
// names a directory to keep them in for it.
func TestJunosSchema(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	const monitoring = "urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring"
	if !session.ServerCaps().Has(monitoring) {
		t.Skip("the device does not advertise ietf-netconf-monitoring, skipping test")
	}

	// the module of the configuration has four megabytes, which takes a lab
	// device a while
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	listed, err := rpc.Get{Filter: rpc.SubtreeFilter(`<netconf-state xmlns="` + monitoring + `"><schemas/></netconf-state>`)}.Exec(ctx, session)
	be.NilErr(t, err)
	var state struct {
		Identifiers []string `xml:"schemas>schema>identifier"`
	}
	be.NilErr(t, xml.Unmarshal(listed, &state))

	dir := os.Getenv("NETCONF_DUT_YANG")
	if dir != "" {
		be.NilErr(t, os.MkdirAll(dir, 0o750))
	}
	read := 0
	for _, identifier := range state.Identifiers {
		// junos-conf-system and junos-rpc-system, with the family in the name
		// on a device of a family that has one
		if !strings.HasSuffix(identifier, "-conf-system") && !strings.HasSuffix(identifier, "-rpc-system") {
			continue
		}
		var schema struct {
			Module string `xml:"data"`
		}
		request := `<get-schema xmlns="` + monitoring + `"><identifier>` + identifier + `</identifier><format>yang</format></get-schema>`
		be.NilErr(t, session.Exec(ctx, request, &schema))
		be.In(t, "module "+identifier+" {", schema.Module)
		be.In(t, `description "Junos: `, schema.Module)
		t.Logf("%s has %d bytes", identifier, len(schema.Module))
		read++

		if dir != "" {
			be.NilErr(t, os.WriteFile(filepath.Join(dir, identifier+".yang"), []byte(schema.Module), 0o600))
		}
	}
	be.Equal(t, 2, read)
}

// A commit check validates the candidate as it is and commits nothing.  It
// does one thing to the device all the same: it confirms a confirmed commit
// that is pending.  So none is sent while one is.
func TestJunosCommitCheck(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)
	skipIfCommitPending(t, session)

	_, err := junos.CommitConfiguration{Check: true}.Exec(testCtx(t), session)
	be.NilErr(t, err)

	be.NilErr(t, rpc.Validate{Source: rpc.Candidate}.Exec(testCtx(t), session))
}

// Who is editing the configuration.  A session that opens a private copy of
// the candidate shows in the list another session asks for, as a private
// one, under its session-id, and is gone from it when the copy is closed.
// The private copy is opened and closed and nothing is loaded into it; like
// the dry run it only runs when asked for.
func TestJunosDatabaseStatus(t *testing.T) {
	onlyFlavor(t, "junos")
	session, other := setupSSH(t), setupSSH(t)
	ctx := testCtx(t)

	// the sessions of this test in the list: others may be editing too
	mine := func(t *testing.T) []junos.EditSession {
		t.Helper()
		all, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, other)
		be.NilErr(t, err)
		t.Logf("%d sessions are editing the configuration", len(all))

		var found []junos.EditSession
		for _, edit := range all {
			if uint64(edit.PID) == session.SessionID() { //nolint:gosec // a process id
				found = append(found, edit)
			}
		}
		return found
	}
	be.Equal(t, 0, len(mine(t)))

	if os.Getenv("NETCONF_DUT_CHANGE_DRYRUN") == "" {
		t.Skip("NETCONF_DUT_CHANGE_DRYRUN not set, skipping the part that opens a private copy")
	}
	be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(ctx, session))
	opened := mine(t)
	be.NilErr(t, junos.CloseConfiguration{}.Exec(ctx, session))

	be.Equal(t, 1, len(opened))
	be.True(t, opened[0].Private)
	be.False(t, opened[0].Exclusive)
	be.Equal(t, "[edit]", opened[0].EditPath)
	be.Nonzero(t, opened[0].User)
	be.True(t, time.Since(opened[0].Start) < time.Hour)
	be.Equal(t, 0, len(mine(t)))
}

// A whole change as a dry run in a private copy of the candidate: a group
// that nothing applies is loaded there, compared and checked, and the copy
// is closed.  Nothing is committed and the candidate itself is not touched,
// which its diff before and after has to show.  It is the one test here that
// loads anything, so it only runs when asked for.
func TestJunosChangeDryRun(t *testing.T) {
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_DRYRUN") == "" {
		t.Skip("NETCONF_DUT_CHANGE_DRYRUN not set, skipping test")
	}
	session := setupSSH(t)
	skipIfCommitPending(t, session)

	before, err := junos.CompareRollback{}.Exec(testCtx(t), session)
	be.NilErr(t, err)

	// a check can take its time
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := junos.Change{
		Format:  junos.FormatSet,
		Config:  "set groups netconf-lib-probe system location building probe",
		Private: true,
		DryRun:  true,
	}.Apply(ctx, session)
	be.NilErr(t, err)
	be.False(t, result.Committed)
	be.In(t, "netconf-lib-probe", result.Diff)

	// the diff of a change is a patch, and a change loads it to the same end
	patched, err := junos.Change{Action: junos.LoadPatch, Config: result.Diff, Private: true, DryRun: true}.Apply(ctx, session)
	be.NilErr(t, err)
	be.False(t, patched.Committed)
	be.Equal(t, result.Diff, patched.Diff)

	after, err := junos.CompareRollback{}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	be.Equal(t, before, after)
}

// Text that is not ASCII, and text with the characters XML and JSON escape,
// loaded into a private copy of the candidate and read back from it in every
// format.  Junos sends each byte of such a letter as a character reference
// of its own and reads two escapes of a JSON string only, and neither may
// show.  A statement that looks like the error element of Junos is read back
// too.  Nothing is committed and the copy is closed at the end; like the dry
// run this loads, so it only runs when asked for.
func TestJunosTextRoundTrip(t *testing.T) {
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_DRYRUN") == "" {
		t.Skip("NETCONF_DUT_CHANGE_DRYRUN not set, skipping test")
	}
	session := setupSSH(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// without the copy the loads below would go into the candidate itself
	be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(ctx, session))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		be.NilErr(t, junos.CloseConfiguration{}.Exec(ctx, session))
	}()

	const statement = "groups " + probeGroup + " system location building"
	asXML := func(building string) string {
		return "<configuration><groups><name>" + probeGroup + "</name><system><location><building>" +
			building + "</building></location></system></groups></configuration>"
	}
	asJSON := func(building string) string {
		doc, err := json.Marshal(map[string]any{"configuration": map[string]any{"groups": []any{map[string]any{
			"name":   probeGroup,
			"system": map[string]any{"location": map[string]any{"building": building}},
		}}}})
		be.NilErr(t, err)
		return string(doc)
	}
	load := func(t *testing.T, format junos.Format, config string) {
		t.Helper()
		_, err := junos.LoadConfiguration{Format: format, Config: config}.Exec(ctx, session)
		be.NilErr(t, err)
	}

	read := func(t *testing.T, format junos.Format) []byte {
		t.Helper()
		out, err := junos.GetConfiguration{
			Format:    format,
			Candidate: true,
			Filter:    "<configuration><groups><name>" + probeGroup + "</name></groups></configuration>",
		}.Exec(ctx, session)
		be.NilErr(t, err)
		return out
	}
	// decoded is the value in the two formats that have an encoding to undo
	decoded := func(t *testing.T) (fromXML, fromJSON string) {
		t.Helper()
		var inXML struct {
			Building string `xml:"groups>system>location>building"`
		}
		be.NilErr(t, xml.Unmarshal(read(t, junos.FormatXML), &inXML))

		var inJSON struct {
			Configuration struct {
				Groups []struct {
					System struct {
						Location struct {
							Building string `json:"building"`
						} `json:"location"`
					} `json:"system"`
				} `json:"groups"`
			} `json:"configuration"`
		}
		be.NilErr(t, json.Unmarshal(read(t, junos.FormatJSON), &inJSON))
		be.Equal(t, 1, len(inJSON.Configuration.Groups))
		return inXML.Building, inJSON.Configuration.Groups[0].System.Location.Building
	}

	// a letter of two bytes in UTF-8, signs of three, and one of four
	const letters = "Tromsø – Ålesund 日本 😀"
	for _, tc := range []struct {
		format junos.Format
		config func(building string) string
	}{
		{junos.FormatSet, func(building string) string { return "set " + statement + ` "` + building + `"` }},
		{junos.FormatText, func(building string) string {
			return "groups { " + probeGroup + ` { system { location { building "` + building + `"; } } } }`
		}},
		{junos.FormatXML, asXML},
		{junos.FormatJSON, asJSON},
	} {
		t.Run("letters loaded as "+string(tc.format), func(t *testing.T) {
			// a value of its own, so that it is this load that is read back
			want := letters + " as " + string(tc.format)
			load(t, tc.format, tc.config(want))

			fromXML, fromJSON := decoded(t)
			be.Equal(t, want, fromXML)
			be.Equal(t, want, fromJSON)
			be.In(t, `building "`+want+`";`, string(read(t, junos.FormatText)))
			be.In(t, "set "+statement+` "`+want+`"`, string(read(t, junos.FormatSet)))

			diff, err := junos.CompareRollback{}.Exec(ctx, session)
			be.NilErr(t, err)
			be.In(t, `building "`+want+`";`, diff)
		})
	}

	// A character reference that names a letter, the way another program may
	// have written it, is stored as one byte.  Junos sends that back as it
	// came, next to the bytes of a letter in UTF-8.
	t.Run("a letter stored as one byte", func(t *testing.T) {
		load(t, junos.FormatXML, asXML("x&#229;åx"))

		fromXML, fromJSON := decoded(t)
		be.Equal(t, "xååx", fromXML)
		be.Equal(t, "xååx", fromJSON)
		be.In(t, `building "xååx";`, string(read(t, junos.FormatText)))
	})

	// encoding/json writes <, > and & as escapes, which Junos does not read
	t.Run("what encoding/json escapes", func(t *testing.T) {
		const want = `R&D <lab> "quoted" back\slash å`
		doc := asJSON(want)
		be.In(t, `R\u0026D \u003clab\u003e`, doc)
		load(t, junos.FormatJSON, doc)

		fromXML, fromJSON := decoded(t)
		be.Equal(t, want, fromXML)
		be.Equal(t, want, fromJSON)
	})

	// The syslog severity error is an element with the name and the
	// namespace of the error element of Junos.  The file it is for is called
	// rpc-error, so that the reply is gone through for errors.
	t.Run("a severity called error", func(t *testing.T) {
		load(t, junos.FormatSet, "set groups "+probeGroup+" system syslog file rpc-error any error")
		be.In(t, "<error/>", string(read(t, junos.FormatXML)))
	})
}

// A patch, a file and the rescue configuration, each loaded into a private
// copy of the candidate, which is closed again.  Nothing is committed.
//
// The patch is what a load changed, taken with a compare and loaded back
// into a copy that was put back: the copy then differs from the device by
// that same patch.  The file is one the test writes to /var/tmp on the
// device and deletes again, which is why that part is for a lab device and
// runs with the commit tests.  The rescue configuration is loaded where the
// device has one, and where it has none the refusal is what is looked at.
func TestJunosLoadPatchURLRescue(t *testing.T) {
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_DRYRUN") == "" {
		t.Skip("NETCONF_DUT_CHANGE_DRYRUN not set, skipping test")
	}
	session := setupSSH(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(ctx, session))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		be.NilErr(t, junos.CloseConfiguration{}.Exec(ctx, session))
	}()
	compare := func(t *testing.T) string {
		t.Helper()
		diff, err := junos.CompareRollback{}.Exec(ctx, session)
		be.NilErr(t, err)
		return diff
	}
	putBack := func(t *testing.T) {
		t.Helper()
		_, err := junos.Rollback(0).Exec(ctx, session)
		be.NilErr(t, err)
		be.Equal(t, "", compare(t))
	}

	t.Run("a patch", func(t *testing.T) {
		value, config := probe()
		_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: config}.Exec(ctx, session)
		be.NilErr(t, err)
		patch := compare(t)
		be.In(t, value, patch)
		putBack(t)

		_, err = junos.LoadConfiguration{Action: junos.LoadPatch, Config: patch}.Exec(ctx, session)
		be.NilErr(t, err)
		be.Equal(t, patch, compare(t))
		putBack(t)

		// a patch that takes out what is not there loads nothing, and
		// Junos has only warnings for it
		gone := "[edit groups]\n-   " + probeGroup + " {\n-       system {\n-           location building " + value + ";\n-       }\n-   }"
		reply, err := junos.LoadConfiguration{Action: junos.LoadPatch, Config: gone}.Exec(ctx, session)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		be.Nonzero(t, len(reply.Warnings))
		t.Logf("a patch that does not apply: %v", err)
		putBack(t)
	})

	t.Run("a file", func(t *testing.T) {
		if os.Getenv("NETCONF_DUT_CHANGE_COMMIT") == "" {
			t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping the part that writes a file to the device")
		}
		const path = "/var/tmp/" + probeGroup + ".conf"
		value, _ := probe()
		text := "groups {\n    " + probeGroup + " {\n        system {\n            location {\n                building " + value + ";\n            }\n        }\n    }\n}\n"

		_, err := session.ExecReply(ctx, "<file-put><delete-if-exist/><encoding>base64</encoding><filename>"+path+"</filename><file-contents>"+
			base64.StdEncoding.EncodeToString([]byte(text))+"</file-contents></file-put>")
		be.NilErr(t, err)
		defer func() {
			// whatever became of the test and its context, the file goes
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, err := session.ExecReply(ctx, "<file-delete><path>"+path+"</path></file-delete>")
			be.NilErr(t, err)
		}()

		_, err = junos.LoadConfiguration{Format: junos.FormatText, URL: path}.Exec(ctx, session)
		be.NilErr(t, err)
		be.In(t, value, compare(t))
		putBack(t)

		_, err = junos.LoadConfiguration{Format: junos.FormatText, URL: path + ".not-there"}.Exec(ctx, session)
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.In(t, "Cannot open configuration file", rpcErr.Message)
		putBack(t)
	})

	t.Run("the rescue configuration", func(t *testing.T) {
		_, err := junos.LoadRescue{}.Exec(ctx, session)
		var rpcErr netconf.RPCError
		switch {
		case err == nil:
			t.Log("the device has a rescue configuration, and it was loaded")
			_, err := junos.Rollback(0).Exec(ctx, session)
			be.NilErr(t, err)
		case errors.As(err, &rpcErr):
			t.Logf("the device has no rescue configuration: %s", rpcErr.Message)
			be.In(t, "Cannot open configuration file", rpcErr.Message)
		default:
			t.Errorf("load rescue: %v", err)
		}
	})
}

// The tests from here to TestJunosErrorDetail commit, and only run when
// asked for.  What they commit is a configuration group that nothing
// applies, and they take it out again: the device runs the same as before,
// with a longer commit history.  They are meant for a lab device.  A group
// of that name that the device runs already is not theirs, and they fail
// without touching it, see probeAbsent.

// probeGroup is the configuration group these tests work with.
const probeGroup = "netconf-lib-probe"

// commitSession returns a session for a test that commits.  The probe group
// must not be there when the test begins, and is taken out of the
// configuration when the test is over.
func commitSession(t *testing.T) *netconf.Session {
	t.Helper()
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_COMMIT") == "" {
		t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
	}

	session := setupSSH(t)
	probeAbsent(testCtx(t), t, session)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, err := junos.Change{Format: junos.FormatSet, Config: "delete groups " + probeGroup}.Apply(ctx, session)
		be.NilErr(t, err)
	})
	return session
}

// changeCtx bounds a test that commits.  A commit takes its time, and a
// confirmed commit that is not confirmed takes two minutes to go away.
func changeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// probe returns a value for the probe group that no run had before, so that
// loading it always is a change, and the set command that loads it.
func probe() (value, config string) {
	value = "run-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	return value, "set groups " + probeGroup + " system location building " + value
}

// activeProbe returns the probe group as the device runs it, as set
// commands, and nothing when it is not there.
func activeProbe(ctx context.Context, t *testing.T, session *netconf.Session) string {
	t.Helper()
	set, err := junos.GetConfiguration{
		Format: junos.FormatSet,
		Filter: "<configuration><groups><name>" + probeGroup + "</name></groups></configuration>",
	}.Exec(ctx, session)
	be.NilErr(t, err)
	return string(set)
}

// commitPending reports whether a confirmed commit waits for its
// confirmation on the device.  A commit check confirms such a commit,
// whoever made it and from whatever session, so a test that is only to
// look does not send one then.
func commitPending(t *testing.T, session *netconf.Session) bool {
	t.Helper()
	history, err := junos.GetCommitInformation{}.Exec(testCtx(t), session)
	be.NilErr(t, err)
	return len(history) > 0 && history[0].Pending
}

// skipIfCommitPending skips a test that has Junos check a commit when a
// confirmed commit is pending, see commitPending.
func skipIfCommitPending(t *testing.T, session *netconf.Session) {
	t.Helper()
	if commitPending(t, session) {
		t.Skip("a confirmed commit waits for its confirmation and a commit check would confirm it, skipping test")
	}
}

// commits returns the commit history of the device, newest first.
func commits(ctx context.Context, t *testing.T, session *netconf.Session) []junos.Commit {
	t.Helper()
	history, err := junos.GetCommitInformation{}.Exec(ctx, session)
	be.NilErr(t, err)
	be.Nonzero(t, len(history))
	return history
}

// The commit history as values: newest first, numbered from 0 the way
// rollback counts, each entry with who committed, through what and when.
// The time is that of the seconds Junos gives, so it is right whatever time
// zone the device prints.
func TestJunosCommitInformation(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	history := commits(testCtx(t), t, session)
	t.Logf("%d commits, the newest through %q", len(history), history[0].Client)
	for i, commit := range history {
		be.Equal(t, i, commit.Sequence)
		be.Nonzero(t, commit.User)
		be.Nonzero(t, commit.Client)
		be.False(t, commit.Time.IsZero())
		be.True(t, commit.Time.Before(time.Now().Add(24*time.Hour)))
		if i > 0 {
			be.True(t, !commit.Time.After(history[i-1].Time))
		}
	}

	// the newest is what the configuration says about itself
	reply, err := session.ExecReply(testCtx(t), junos.GetConfiguration{
		Filter: `<configuration><system><services><netconf/></services></system></configuration>`,
	})
	be.NilErr(t, err)
	config, err := reply.Element("configuration")
	be.NilErr(t, err)
	seconds := ""
	for _, attr := range config.Attr {
		if attr.Name.Local == "commit-seconds" {
			seconds = attr.Value
		}
	}
	be.Equal(t, strconv.FormatInt(history[0].Time.Unix(), 10), seconds)
}

// A change under the lock on the candidate: the device runs what was
// loaded, the commit carries the comment, letters outside ASCII included,
// and the same change again finds nothing to do and commits nothing.
func TestJunosChangeCommit(t *testing.T) {
	session := commitSession(t)
	ctx := changeCtx(t)

	value, config := probe()
	change := junos.Change{
		Format:  junos.FormatSet,
		Config:  config,
		Comment: `netconf integration test <` + value + `> & "quoted", Tromsø – Ålesund`,
	}
	result, err := change.Apply(ctx, session)
	be.NilErr(t, err)
	be.True(t, result.Committed)
	be.In(t, value, result.Diff)
	be.In(t, value, activeProbe(ctx, t, session))

	latest := commits(ctx, t, session)[0]
	be.Equal(t, change.Comment, latest.Comment)
	be.Equal(t, "netconf", latest.Client)
	be.False(t, latest.Pending)

	// and the CLI prints it the same
	printed, err := junos.Command("show system commit").Exec(ctx, session)
	be.NilErr(t, err)
	be.In(t, change.Comment, printed)

	result, err = change.Apply(ctx, session)
	be.NilErr(t, err)
	be.False(t, result.Committed)
	be.Equal(t, "", result.Diff)
	be.Equal(t, latest, commits(ctx, t, session)[0])
}

// A commit that has every process read the whole configuration again, like
// the CLI's "commit full".
func TestJunosChangeCommitFull(t *testing.T) {
	session := commitSession(t)
	ctx := changeCtx(t)

	value, config := probe()
	result, err := junos.Change{Format: junos.FormatSet, Config: config, Full: true, Comment: "netconf integration test, commit full"}.Apply(ctx, session)
	be.NilErr(t, err)
	be.True(t, result.Committed)
	be.In(t, value, activeProbe(ctx, t, session))
	for i := range result.Warnings {
		t.Logf("warning of the commit: %v", result.Warnings[i])
	}

	latest := commits(ctx, t, session)[0]
	be.Equal(t, "netconf integration test, commit full", latest.Comment)
	t.Logf("what Junos notes on the commit: %q", latest.Note)
}

// A confirmed commit and its confirmation.  Verify runs with the new
// configuration in effect and the way back still open, and nothing is
// pending once Apply has returned.
func TestJunosChangeConfirmed(t *testing.T) {
	session := commitSession(t)
	ctx := changeCtx(t)

	value, config := probe()
	var pending junos.Commit
	result, err := junos.Change{
		Format:  junos.FormatSet,
		Config:  config,
		Confirm: time.Minute,
		Verify: func(ctx context.Context) error {
			if !strings.Contains(activeProbe(ctx, t, session), value) {
				return errors.New("the change is not in effect")
			}
			pending = commits(ctx, t, session)[0]
			return nil
		},
	}.Apply(ctx, session)
	be.NilErr(t, err)
	be.True(t, result.Committed)
	be.True(t, pending.Pending)
	be.Equal(t, 0, pending.Sequence)

	for _, commit := range commits(ctx, t, session) {
		be.False(t, commit.Pending)
	}
	be.In(t, value, activeProbe(ctx, t, session))
}

// A commit whose answer never arrives, and what the history says about it
// afterwards.  The commit is told a token in its comment, the context of
// the change ends while Junos is at the commit, and Apply can only say that
// it does not know.  A second session finds the token in the history: the
// commit was carried out.
//
// The same with a confirmed commit: found, and still waiting.  The second
// session confirms it with a commit check, as any session can, and the
// token is found once more, in the same entry, which waits no longer.
//
// The context of the change ends 150 ms after the commit was sent.  A
// device that commits faster than that answers in time.  The outcome is
// known then and there is nothing to find out, so the test is skipped.
func TestJunosCommitUnknown(t *testing.T) {
	session := commitSession(t)
	other := setupSSH(t)
	ctx := changeCtx(t)

	// cutOff applies the change with a context that ends a moment after the
	// commit has been sent: Review is the last thing before it.
	cutOff := func(t *testing.T, change junos.Change) {
		t.Helper()
		const moment = 150 * time.Millisecond
		applyCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var cut *time.Timer
		change.Review = func(context.Context, string) error {
			cut = time.AfterFunc(moment, cancel)
			return nil
		}
		if change.Confirm > 0 {
			// Verify is called once the confirmed commit has been answered.
			// It was not cut off then, and its confirmation is not to be.
			change.Verify = func(context.Context) error {
				cut.Stop()
				return nil
			}
		}
		result, err := change.Apply(applyCtx, session)
		// ErrNotConfirmed is the context ending between the answer to the
		// confirmed commit and Verify: the commit was answered all the same
		if err == nil || errors.Is(err, junos.ErrNotConfirmed) {
			t.Skipf("the device answered the commit within %s: its outcome is known, skipping test", moment)
		}
		be.True(t, errors.Is(err, junos.ErrCommitUnknown))
		be.True(t, errors.Is(err, context.Canceled))
		be.False(t, result.Committed)
	}
	// found waits for the token to show in the history: the commit may
	// still be running when it is first asked for.
	found := func(t *testing.T, token string) junos.Commit {
		t.Helper()
		for start := time.Now(); ; time.Sleep(2 * time.Second) {
			commit, ok := junos.FindCommit(commits(ctx, t, other), token)
			if ok {
				return commit
			}
			if time.Since(start) > 2*time.Minute {
				t.Fatalf("no commit with %q in the history", token)
			}
		}
	}
	t.Run("a commit", func(t *testing.T) {
		value, config := probe()
		token := "token-" + value
		cutOff(t, junos.Change{Format: junos.FormatSet, Config: config, Comment: "netconf integration test, " + token})

		_, none := junos.FindCommit(commits(ctx, t, other), "token-that-no-commit-has")
		be.False(t, none)

		commit := found(t, token)
		be.Equal(t, 0, commit.Sequence)
		be.False(t, commit.Pending)
		be.Equal(t, "", commit.Note)
		// the newest entry is what the device runs
		be.In(t, value, activeProbe(ctx, t, other))
	})

	t.Run("a confirmed commit", func(t *testing.T) {
		value, config := probe()
		token := "token-" + value
		cutOff(t, junos.Change{
			Format:  junos.FormatSet,
			Config:  config,
			Comment: "netconf integration test, " + token,
			Confirm: 5 * time.Minute,
		})

		commit := found(t, token)
		be.Equal(t, 0, commit.Sequence)
		be.True(t, commit.Pending)
		be.Nonzero(t, commit.Note)
		t.Logf("Junos notes on the pending commit: %q", commit.Note)
		be.In(t, value, activeProbe(ctx, t, other))

		// Any session confirms it, and a commit check does: that commits
		// nothing of what else may be in the candidate, and makes no entry
		// of its own in the history.
		entries := len(commits(ctx, t, other))
		_, err := junos.CommitConfiguration{Check: true}.Exec(ctx, other)
		be.NilErr(t, err)

		history := commits(ctx, t, other)
		be.Equal(t, entries, len(history))
		confirmed := found(t, token)
		be.Equal(t, 0, confirmed.Sequence)
		be.False(t, confirmed.Pending)
		be.Equal(t, commit.Note, confirmed.Note)
		be.Equal(t, commit.Time, confirmed.Time)
		be.In(t, value, activeProbe(ctx, t, other))
		for _, entry := range history {
			be.False(t, entry.Pending)
		}
	})
}

// A confirmed commit that Verify turns down is not confirmed: the change is
// in effect when Apply returns, and Junos takes it back by itself.
func TestJunosChangeNotConfirmed(t *testing.T) {
	session := commitSession(t)
	ctx := changeCtx(t)

	value, config := probe()
	turnedDown := errors.New("turned down by the test")
	result, err := junos.Change{
		Format:  junos.FormatSet,
		Config:  config,
		Confirm: time.Minute,
		Verify:  func(context.Context) error { return turnedDown },
	}.Apply(ctx, session)
	be.True(t, errors.Is(err, junos.ErrNotConfirmed))
	be.True(t, errors.Is(err, turnedDown))
	be.False(t, result.Committed)
	be.In(t, value, activeProbe(ctx, t, session))

	// Junos takes its time: a vJunos-router went back 109 seconds after a
	// commit to confirm within one minute.
	for start := time.Now(); strings.Contains(activeProbe(ctx, t, session), value); time.Sleep(5 * time.Second) {
		if time.Since(start) > 3*time.Minute {
			t.Fatal("the commit that was not confirmed is still in effect")
		}
	}

	// the candidate went back with it, so nothing is left for whoever
	// commits next
	diff, err := junos.CompareRollback{}.Exec(ctx, session)
	be.NilErr(t, err)
	be.Equal(t, "", diff)
}

// A change in a private copy of the candidate.
func TestJunosChangePrivate(t *testing.T) {
	session := commitSession(t)
	ctx := changeCtx(t)

	value, config := probe()
	result, err := junos.Change{Format: junos.FormatSet, Config: config, Private: true}.Apply(ctx, session)
	be.NilErr(t, err)
	be.True(t, result.Committed)
	be.In(t, value, result.Diff)
	be.In(t, value, activeProbe(ctx, t, session))
}

// Two private copies that change the same statement.  Junos commits nothing
// for the one that comes second and calls that a warning, so the absence of
// errors must not read as a commit that went through.
func TestJunosChangePrivateConflict(t *testing.T) {
	session := commitSession(t)
	other := setupSSH(t)
	ctx := changeCtx(t)

	// the statement has to be there for the two to disagree about
	_, config := probe()
	_, err := junos.Change{Format: junos.FormatSet, Config: config}.Apply(ctx, session)
	be.NilErr(t, err)

	first, firstConfig := probe()
	second, secondConfig := probe()
	result, err := junos.Change{
		Format:  junos.FormatSet,
		Config:  secondConfig,
		Private: true,
		// this copy is open and loaded when somebody else commits
		Review: func(ctx context.Context, _ string) error {
			result, err := junos.Change{Format: junos.FormatSet, Config: firstConfig, Private: true}.Apply(ctx, other)
			if err == nil && !result.Committed {
				err = errors.New("the other change was not committed")
			}
			return err
		},
	}.Apply(ctx, session)
	be.Nonzero(t, err)
	be.False(t, result.Committed)
	be.True(t, errors.Is(err, netconf.ErrElementNotFound))
	be.False(t, errors.Is(err, junos.ErrCommitUnknown))
	be.In(t, "statement does not match patch", err.Error())

	active := activeProbe(ctx, t, session)
	be.In(t, first, active)
	be.False(t, strings.Contains(active, second))
}

// A candidate that somebody else has: under their lock, or holding changes
// they have not committed.  A change is refused either way and loads
// nothing.
func TestJunosChangeCandidateTaken(t *testing.T) {
	session := commitSession(t)
	other := setupSSH(t)
	ctx := changeCtx(t)

	_, config := probe()
	change := junos.Change{Format: junos.FormatSet, Config: config}
	private := change
	private.Private = true

	t.Run("locked", func(t *testing.T) {
		be.NilErr(t, rpc.Lock{Target: rpc.Candidate}.Exec(ctx, other))
		defer func() { be.NilErr(t, rpc.Unlock{Target: rpc.Candidate}.Exec(ctx, other)) }()

		for _, c := range []junos.Change{change, private} {
			_, err := c.Apply(ctx, session)
			be.True(t, errors.Is(err, netconf.ErrLockDenied))
			be.False(t, errors.Is(err, junos.ErrCandidateModified))

			// the error names the session that has the lock
			var rpcErr netconf.RPCError
			be.True(t, errors.As(err, &rpcErr))
			holder, _ := rpcErr.Detail("session-id")
			be.Equal(t, strconv.FormatUint(other.SessionID(), 10), holder)
		}
	})

	t.Run("modified", func(t *testing.T) {
		const left = "set groups " + probeGroup + " system location building left-behind"
		_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: left}.Exec(ctx, other)
		be.NilErr(t, err)
		defer func() {
			_, err := junos.Rollback(0).Exec(ctx, other)
			be.NilErr(t, err)
		}()
		before, err := junos.CompareRollback{}.Exec(ctx, other)
		be.NilErr(t, err)
		be.In(t, "left-behind", before)

		for _, c := range []junos.Change{change, private} {
			_, err := c.Apply(ctx, session)
			be.True(t, errors.Is(err, junos.ErrCandidateModified))
			be.False(t, errors.Is(err, netconf.ErrLockDenied))
		}

		after, err := junos.CompareRollback{}.Exec(ctx, other)
		be.NilErr(t, err)
		be.Equal(t, before, after)
	})
}

// A change that waits for the lock.  One session holds the lock on the
// candidate for some seconds.  The change of the other is refused at once
// without LockWait, gives up with a short one, with the holder named, and
// with a long one gets the lock when it is given back.
//
// The change is a dry run and nothing is committed.  It loads into the
// candidate itself under its lock, though, and puts it back, so it runs
// with the tests for a lab device.
func TestJunosChangeLockWait(t *testing.T) {
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_COMMIT") == "" {
		t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
	}
	session, other := setupSSH(t), setupSSH(t)
	skipIfCommitPending(t, session)
	ctx := changeCtx(t)
	latest := commits(ctx, t, session)[0]

	const held = 10 * time.Second
	be.NilErr(t, rpc.Lock{Target: rpc.Candidate}.Exec(ctx, other))
	locked := time.Now()
	released := make(chan error, 1)
	go func() {
		time.Sleep(held)
		released <- rpc.Unlock{Target: rpc.Candidate}.Exec(ctx, other)
	}()

	_, config := probe()
	change := junos.Change{Format: junos.FormatSet, Config: config, DryRun: true}
	holder := strconv.FormatUint(other.SessionID(), 10)

	_, err := change.Apply(ctx, session)
	be.True(t, errors.Is(err, netconf.ErrLockDenied))

	change.LockWait = 2 * time.Second
	_, err = change.Apply(ctx, session)
	be.True(t, errors.Is(err, netconf.ErrLockDenied))
	var rpcErr netconf.RPCError
	be.True(t, errors.As(err, &rpcErr))
	named, _ := rpcErr.Detail("session-id")
	be.Equal(t, holder, named)
	be.In(t, "gave up after waiting 2s", err.Error())

	change.LockWait = time.Minute
	result, err := change.Apply(ctx, session)
	be.NilErr(t, err)
	be.NilErr(t, <-released)
	be.True(t, time.Since(locked) >= held)
	be.False(t, result.Committed)
	be.In(t, probeGroup, result.Diff)

	// nothing was committed and nothing is left in the candidate
	be.Equal(t, latest, commits(ctx, t, session)[0])
	diff, err := junos.CompareRollback{}.Exec(ctx, session)
	be.NilErr(t, err)
	be.Equal(t, "", diff)
}

// The ephemeral database, its default instance.  What is committed there is
// no part of the configuration and makes no entry in the commit history; the
// session that has the database open reads it as its candidate.  What goes
// in is a prefix list that nothing uses, and it is taken out again.  Like the
// other tests that commit, this one only runs when asked for.
func TestJunosEphemeral(t *testing.T) {
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_COMMIT") == "" {
		t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
	}
	session, other := setupSSH(t), setupSSH(t)
	ctx := changeCtx(t)

	const list = "policy-options prefix-list " + probeGroup
	read := func(t *testing.T, session *netconf.Session, candidate bool) string {
		t.Helper()
		set, err := junos.GetConfiguration{
			Format:    junos.FormatSet,
			Candidate: candidate,
			Filter:    "<configuration><policy-options><prefix-list><name>" + probeGroup + "</name></prefix-list></policy-options></configuration>",
		}.Exec(ctx, session)
		be.NilErr(t, err)
		return strings.TrimSpace(string(set))
	}
	latest := commits(ctx, t, other)[0]

	be.NilErr(t, junos.OpenConfiguration{Ephemeral: true}.Exec(ctx, session))
	// a list of that name that is there already is not the test's to take out
	if before := read(t, session, true); before != "" {
		be.NilErr(t, junos.CloseConfiguration{}.Exec(ctx, session))
		t.Fatalf("the ephemeral database has a prefix list named %q that this test did not make: %s", probeGroup, before)
	}
	defer func() {
		// whatever became of the test, the list does not stay in the database
		_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: "delete " + list}.Exec(ctx, session)
		be.NilErr(t, err)
		_, err = junos.CommitConfiguration{}.Exec(ctx, session)
		be.NilErr(t, err)
		be.Equal(t, "", read(t, session, true))
		be.NilErr(t, junos.CloseConfiguration{}.Exec(ctx, session))
	}()

	_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: "set " + list + " 192.0.2.0/24"}.Exec(ctx, session)
	be.NilErr(t, err)
	be.Equal(t, "set "+list+" 192.0.2.0/24", read(t, session, true))

	// The database has no diff.  Junos 26.2 says so; 23.4 answers with what
	// it could not read of the configuration file.
	_, err = junos.CompareRollback{}.Exec(ctx, session)
	var noDiff netconf.RPCError
	be.True(t, errors.As(err, &noDiff))
	if !strings.Contains(err.Error(), "Unsupported operation config compare in ephemeral database") {
		be.In(t, "/config/juniper.conf", err.Error())
	}

	// what else the database does not do, Junos refuses in so many words
	for refused, try := range map[string]func() error{
		`"commit confirmed" is not supported with ephemeral`: func() error {
			_, err := junos.CommitConfiguration{Confirm: time.Minute}.Exec(ctx, session)
			return err
		},
		"statement creation failed": func() error {
			_, config := probe()
			_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: config}.Exec(ctx, session)
			return err
		},
	} {
		err := try()
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.In(t, refused, err.Error())
	}

	_, err = junos.CommitConfiguration{}.Exec(ctx, session)
	be.NilErr(t, err)
	be.Equal(t, "set "+list+" 192.0.2.0/24", read(t, session, true))

	// the configuration does not have it, and neither does its history
	be.Equal(t, "", read(t, other, false))
	be.Equal(t, latest, commits(ctx, t, other)[0])
}

// Requests for what is not there are errors with a message, whichever
// element Junos says so in.  The first is answered with an <rpc-error> by
// Junos and with the error element of Junos inside the result by Junos OS
// Evolved, the second with that element under <rpc-reply> by both.  Asked
// for in JSON, the error is in the document.
func TestJunosErrorDetail(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)

	for _, rpc := range []struct{ name, args string }{
		{"get-arp-table-information", "<interface>nonexistent.0</interface>"},
		{"get-bgp-neighbor-information", "<neighbor-address>not-an-address</neighbor-address>"},
	} {
		reply, err := session.ExecReply(testCtx(t), "<"+rpc.name+">"+rpc.args+"</"+rpc.name+">")
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Nonzero(t, rpcErr.Message)
		be.Equal(t, strings.TrimSpace(rpcErr.Message), rpcErr.Message)

		// Junos names the process that found the error.  To a session in
		// rfc-compliant mode, which shows in the nc prefix of the reply, it
		// only does so for the errors of a commit.
		if !bytes.Contains(reply.Raw, []byte("<nc:rpc-reply")) {
			daemon, ok := rpcErr.Detail("source-daemon")
			be.True(t, ok)
			be.Nonzero(t, daemon)
		}

		reply, err = session.ExecReply(testCtx(t), "<"+rpc.name+` format="json">`+rpc.args+"</"+rpc.name+">")
		if err == nil {
			_, err = junos.JSON(reply)
		}
		be.Nonzero(t, err)
	}
}
