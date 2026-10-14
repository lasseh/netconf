package netconf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/transport"
)

// These tests are about the turn an operation takes over chunked framing to
// a Junos device, see Session.send.  The device hands every request to the
// test and answers when the test says so.

// wireHelloJunos11 is the hello of a Junos device that speaks chunks.
const wireHelloJunos11 = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability><capability>urn:ietf:params:netconf:base:1.1</capability><capability>http://xml.juniper.net/netconf/junos/1.0</capability></capabilities><session-id>7</session-id></hello>]]>]]>`

// held is how long a test watches for a request that must not arrive.
const held = 50 * time.Millisecond

// asked is the device of these tests.
type asked struct {
	*netconftest.Device
	// requests has what the device received after the hello, and is closed
	// when the device has lost its connection
	requests chan string
}

// turnWire returns a transport to a device that says hello and from then on
// hands what it receives to the test.
func turnWire(t *testing.T, hello string) (*netconftest.Wire, *asked) {
	t.Helper()

	wire, dev := netconftest.NewWire(t)
	d := &asked{Device: dev, requests: make(chan string, 8)}
	go func() {
		defer close(d.requests)

		said, err := dev.Recv()
		if err != nil {
			return
		}
		if dev.WriteRaw(hello) != nil {
			return
		}
		if strings.Contains(said, CapNetConf11) && strings.Contains(hello, CapNetConf11) {
			dev.Upgrade()
		}
		for {
			req, err := dev.Recv()
			if err != nil {
				return
			}
			d.requests <- req
		}
	}()
	return wire, d
}

func turnSession(t *testing.T, hello string, opts ...SessionOption) (*Session, *asked) {
	t.Helper()

	wire, dev := turnWire(t, hello)
	opts = append([]SessionOption{WithLogger(slog.New(slog.DiscardHandler))}, opts...)
	sess, err := Open(context.Background(), wire, opts...)
	be.NilErr(t, err)
	return sess, dev
}

// next returns the request that arrives at the device next.
func (d *asked) next(t *testing.T) string {
	t.Helper()

	select {
	case req, ok := <-d.requests:
		be.True(t, ok)
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("no request arrived at the device")
		return ""
	}
}

// nothing fails the test when a request arrives at the device within a
// moment.
func (d *asked) nothing(t *testing.T) {
	t.Helper()

	select {
	case req, ok := <-d.requests:
		be.True(t, ok)
		t.Fatalf("a request arrived while the one in front of it was not answered: %s", req)
	case <-time.After(held):
	}
}

// answer writes the whole reply to req.
func (d *asked) answer(t *testing.T, req string) {
	t.Helper()
	be.NilErr(t, d.Send(netconftest.OKReply(netconftest.ExtractMessageID(req))))
}

// begin writes the start tag of the reply to req as a chunk of its own, the
// way Junos sends it when it has the start tag of the request.
func (d *asked) begin(t *testing.T, req string) {
	t.Helper()
	start := `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="` + netconftest.ExtractMessageID(req) + `">`
	be.NilErr(t, d.WriteRaw(chunk(start)))
}

// finish writes the rest of a reply that was begun.
func (d *asked) finish(t *testing.T) {
	t.Helper()
	be.NilErr(t, d.WriteRaw(chunk("<ok/></rpc-reply>")+"\n##\n"))
}

func chunk(s string) string {
	return fmt.Sprintf("\n#%d\n%s", len(s), s)
}

// ask issues op from a goroutine of its own and returns where its error
// arrives.
func ask(ctx context.Context, sess *Session, op string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := sess.ExecReply(ctx, op)
		done <- err
	}()
	return done
}

// Over chunked framing to a Junos device a request is written when the
// answer to the one before it has arrived whole.  The start tag of that
// answer does not do: Junos sends it when it has the start tag of the
// request, see ADR 0006.
func TestOneRequestAtATime(t *testing.T) {
	sess, dev := turnSession(t, wireHelloJunos11)
	ctx := testCtx(t)

	first := ask(ctx, sess, "<first/>")
	one := dev.next(t)
	be.In(t, "<first/>", one)

	second := ask(ctx, sess, "<second/>")
	dev.nothing(t)
	dev.begin(t, one)
	dev.nothing(t)
	dev.finish(t)

	two := dev.next(t)
	be.In(t, "<second/>", two)
	dev.answer(t, two)
	be.NilErr(t, <-first)
	be.NilErr(t, <-second)
}

// A reply without a message-id answers the request that was sent.  One that
// waits for its turn is not in question, the device has seen nothing of it,
// and it goes out when the turn is over.
func TestReplyWithoutMessageIDWhileAnotherWaits(t *testing.T) {
	const refusal = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error><error-type>rpc</error-type><error-tag>malformed-message</error-tag><error-severity>error</error-severity></rpc-error></rpc-reply>`

	sess, dev := turnSession(t, wireHelloJunos11)
	ctx := testCtx(t)

	first := ask(ctx, sess, "<first/>")
	dev.next(t)
	second := ask(ctx, sess, "<second/>")
	dev.nothing(t)

	be.NilErr(t, dev.Send(refusal))
	var rpcErrs RPCErrors
	be.True(t, errors.As(<-first, &rpcErrs))

	two := dev.next(t)
	be.In(t, "<second/>", two)
	dev.answer(t, two)
	be.NilErr(t, <-second)
}

// Which sessions have one request on its way at a time: those over chunked
// framing to a device that advertises the Junos capability.  On any other
// the second request is written while the first waits for its answer.
func TestWhoTakesTurns(t *testing.T) {
	tests := []struct {
		name  string
		hello string
		opts  []SessionOption
		turns bool
	}{
		{name: "junos over chunked framing", hello: wireHelloJunos11, turns: true},
		{name: "junos over end-of-message framing", hello: wireHelloJunos11, opts: []SessionOption{WithCapability(CapNetConf10)}},
		{name: "another device over chunked framing", hello: wireHello11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess, dev := turnSession(t, tt.hello, tt.opts...)
			ctx := testCtx(t)
			be.Equal(t, tt.turns, sess.turn != nil)

			first := ask(ctx, sess, "<first/>")
			one := dev.next(t)
			second := ask(ctx, sess, "<second/>")
			if tt.turns {
				dev.nothing(t)
				dev.answer(t, one)
			}
			two := dev.next(t)
			be.In(t, "<second/>", two)
			if !tt.turns {
				dev.answer(t, one)
			}
			dev.answer(t, two)
			be.NilErr(t, <-first)
			be.NilErr(t, <-second)
		})
	}
}

// slowWrites is a transport whose writers can be made to return from Close
// only when told to, with the message long out.
type slowWrites struct {
	*netconftest.Wire
	slow    atomic.Bool
	release chan struct{}
}

func (s *slowWrites) MsgWriter() (io.WriteCloser, error) {
	w, err := s.Wire.MsgWriter()
	if err != nil || !s.slow.Load() {
		return w, err
	}
	return slowWriter{WriteCloser: w, release: s.release}, nil
}

type slowWriter struct {
	io.WriteCloser
	release <-chan struct{}
}

func (w slowWriter) Close() error {
	err := w.WriteCloser.Close()
	<-w.release
	return err
}

func slowSession(t *testing.T) (*Session, *slowWrites, *asked) {
	t.Helper()

	wire, dev := turnWire(t, wireHelloJunos11)
	tr := &slowWrites{Wire: wire, release: make(chan struct{})}
	sess, err := Open(context.Background(), tr, WithLogger(slog.New(slog.DiscardHandler)))
	be.NilErr(t, err)
	return sess, tr, dev
}

// A request whose caller gave up once it was on its way keeps its turn: the
// next one waits for the answer that nobody reads.
func TestTurnOutlastsItsCaller(t *testing.T) {
	tests := []struct {
		name string
		// writing has the caller give up before the write has returned
		writing bool
	}{
		{name: "the caller gives up when the request is written"},
		{name: "the caller gives up while the request is written", writing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess, tr, dev := slowSession(t)

			tr.slow.Store(tt.writing)
			ctx, cancel := context.WithCancel(context.Background())
			first := ask(ctx, sess, "<first/>")
			one := dev.next(t)
			cancel()
			be.True(t, errors.Is(<-first, context.Canceled))
			tr.slow.Store(false)
			close(tr.release)

			second := ask(testCtx(t), sess, "<second/>")
			dev.nothing(t)
			dev.answer(t, one)

			two := dev.next(t)
			be.In(t, "<second/>", two)
			dev.answer(t, two)
			be.NilErr(t, <-second)
		})
	}
}

// A request whose context fires while it waits for its turn is not sent, and
// the turn goes to the next.
func TestTurnNotTakenInTime(t *testing.T) {
	sess, dev := turnSession(t, wireHelloJunos11)

	first := ask(testCtx(t), sess, "<first/>")
	one := dev.next(t)

	ctx, cancel := context.WithTimeout(context.Background(), held)
	defer cancel()
	_, err := sess.ExecReply(ctx, "<second/>")
	be.True(t, errors.Is(err, context.DeadlineExceeded))

	dev.answer(t, one)
	be.NilErr(t, <-first)

	third := ask(testCtx(t), sess, "<third/>")
	three := dev.next(t)
	be.In(t, "<third/>", three)
	dev.answer(t, three)
	be.NilErr(t, <-third)
}

// A request that waits for its turn when the session ends fails like any
// other.
func TestTurnOnASessionThatEnds(t *testing.T) {
	sess, dev := turnSession(t, wireHelloJunos11)
	ctx := testCtx(t)

	first := ask(ctx, sess, "<first/>")
	dev.next(t)
	second := ask(ctx, sess, "<second/>")
	dev.nothing(t)

	dev.Hangup()
	be.True(t, errors.Is(<-first, ErrClosed))
	be.True(t, errors.Is(<-second, ErrClosed))
}

// A reply read with Stream has arrived whole when its reader is closed.
func TestTurnOfAStreamedReply(t *testing.T) {
	sess, dev := turnSession(t, wireHelloJunos11)
	ctx := testCtx(t)

	type streamed struct {
		msg *Message
		err error
	}
	began := make(chan streamed, 1)
	go func() {
		msg, err := sess.Stream(ctx, "<first/>")
		began <- streamed{msg, err}
	}()
	dev.answer(t, dev.next(t))
	stream := <-began
	be.NilErr(t, stream.err)

	second := ask(ctx, sess, "<second/>")
	dev.nothing(t)
	be.NilErr(t, stream.msg.Close())

	two := dev.next(t)
	be.In(t, "<second/>", two)
	dev.answer(t, two)
	be.NilErr(t, <-second)
}

// A message that nobody can read ends the turn like an answer does: a
// session that waited for a better one would send nothing more.
func TestTurnAfterAMessageThatIsNotXML(t *testing.T) {
	sess, dev := turnSession(t, wireHelloJunos11)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := ask(ctx, sess, "<first/>")
	dev.next(t)
	be.NilErr(t, dev.WriteRaw(chunk("no such request")+"\n##\n"))

	second := ask(testCtx(t), sess, "<second/>")
	two := dev.next(t)
	be.In(t, "<second/>", two)
	dev.answer(t, two)
	be.NilErr(t, <-second)

	cancel()
	be.True(t, errors.Is(<-first, context.Canceled))
}

// A notification is no answer: the turn stays where it is.
func TestTurnAndANotification(t *testing.T) {
	const notification = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-12T09:00:00Z</eventTime><event/></notification>`

	notified := make(chan struct{}, 1)
	sess, dev := turnSession(t, wireHelloJunos11, WithNotifHandler(func(context.Context, *Message) {
		notified <- struct{}{}
	}))
	ctx := testCtx(t)

	first := ask(ctx, sess, "<first/>")
	one := dev.next(t)
	second := ask(ctx, sess, "<second/>")
	be.NilErr(t, dev.Send(notification))
	<-notified
	dev.nothing(t)
	dev.answer(t, one)

	two := dev.next(t)
	be.In(t, "<second/>", two)
	dev.answer(t, two)
	be.NilErr(t, <-first)
	be.NilErr(t, <-second)
}

// refusing is a transport that can refuse a message the way end-of-message
// framing refuses one with the delimiter in it: nothing of it is sent.
type refusing struct {
	*netconftest.Wire
	refuse atomic.Bool
}

func (r *refusing) MsgWriter() (io.WriteCloser, error) {
	if r.refuse.Load() {
		return refusedWriter{}, nil
	}
	return r.Wire.MsgWriter()
}

type refusedWriter struct{}

func (refusedWriter) Write(p []byte) (int, error) { return len(p), nil }
func (refusedWriter) Close() error                { return transport.ErrEndOfMsgInPayload }

// A request that did not go out has no answer to wait for, and gives its
// turn back.
func TestTurnOfARequestThatWasNotSent(t *testing.T) {
	wire, dev := turnWire(t, wireHelloJunos11)
	tr := &refusing{Wire: wire}
	sess, err := Open(context.Background(), tr, WithLogger(slog.New(slog.DiscardHandler)))
	be.NilErr(t, err)

	tr.refuse.Store(true)
	_, err = sess.ExecReply(testCtx(t), "<first/>")
	be.True(t, errors.Is(err, transport.ErrEndOfMsgInPayload))
	tr.refuse.Store(false)

	second := ask(testCtx(t), sess, "<second/>")
	two := dev.next(t)
	be.In(t, "<second/>", two)
	dev.answer(t, two)
	be.NilErr(t, <-second)
}

// A device can have answered a request whole while the write of it has not
// returned.  A request that has its turn then, waits for the writer and gives
// up there, gives the turn back.
func TestTurnGivenBackBehindAWrite(t *testing.T) {
	sess, tr, dev := slowSession(t)

	tr.slow.Store(true)
	first := ask(testCtx(t), sess, "<first/>")
	dev.answer(t, dev.next(t))

	ctx, cancel := context.WithTimeout(context.Background(), held)
	defer cancel()
	_, err := sess.ExecReply(ctx, "<second/>")
	be.True(t, errors.Is(err, context.DeadlineExceeded))

	tr.slow.Store(false)
	close(tr.release)
	be.NilErr(t, <-first)

	third := ask(testCtx(t), sess, "<third/>")
	three := dev.next(t)
	be.In(t, "<third/>", three)
	dev.answer(t, three)
	be.NilErr(t, <-third)
}

// A Trace is told of an operation before it waits for its turn, so the wait
// is part of the duration it is told at the end.
func TestTurnInATrace(t *testing.T) {
	type traced struct {
		name string
		took time.Duration
	}
	started := make(chan string, 2)
	done := make(chan traced, 2)
	sess, dev := turnSession(t, wireHelloJunos11, WithTrace(&Trace{
		OperationStart: func(op OperationInfo) { started <- op.Name },
		OperationDone: func(op OperationInfo, result OperationResult) {
			done <- traced{op.Name, result.Duration}
		},
	}))
	ctx := testCtx(t)

	first := ask(ctx, sess, "<first/>")
	one := dev.next(t)
	be.Equal(t, "first", <-started)
	second := ask(ctx, sess, "<second/>")
	be.Equal(t, "second", <-started)
	dev.nothing(t)
	dev.answer(t, one)
	dev.answer(t, dev.next(t))
	be.NilErr(t, <-first)
	be.NilErr(t, <-second)

	for range 2 {
		if op := <-done; op.name == "second" {
			be.True(t, op.took >= held)
		}
	}
}

// Close waits its turn with close-session, like any operation.
func TestCloseWaitsItsTurn(t *testing.T) {
	t.Run("the request in front is answered", func(t *testing.T) {
		sess, dev := turnSession(t, wireHelloJunos11)
		ctx := testCtx(t)

		first := ask(ctx, sess, "<first/>")
		one := dev.next(t)
		closed := make(chan error, 1)
		go func() { closed <- sess.Close(ctx) }()
		dev.nothing(t)
		dev.answer(t, one)

		bye := dev.next(t)
		be.In(t, "<close-session", bye)
		dev.answer(t, bye)
		be.NilErr(t, <-closed)
		be.NilErr(t, <-first)
	})

	// close-session is not sent then, and the session ends all the same
	t.Run("the request in front is never answered", func(t *testing.T) {
		sess, dev := turnSession(t, wireHelloJunos11)

		first := ask(context.Background(), sess, "<first/>")
		dev.next(t)

		ctx, cancel := context.WithTimeout(context.Background(), held)
		defer cancel()
		err := sess.Close(ctx)
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		be.In(t, "close-session", err.Error())

		waitDone(t, sess)
		be.True(t, errors.Is(<-first, ErrClosed))
		_, more := <-dev.requests
		be.False(t, more)
	})
}
