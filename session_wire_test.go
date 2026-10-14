package netconf

import (
	"context"
	"encoding/xml"
	"errors"
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

// These tests run the session over the real RFC6242 framer with the device
// scripted at the byte level, so they see the errors a real connection
// produces (truncation, hang-ups, bytes outside a frame).

const wireHello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>7</session-id></hello>]]>]]>`

// wireSession opens a session against a device that answers every operation
// with respond, which returns the raw bytes to put on the wire.  Returning ""
// hangs up instead.
func wireSession(t *testing.T, respond func(msgID, req string) string, opts ...SessionOption) *Session {
	t.Helper()
	return wireSessionAfter(t, wireHello, respond, opts...)
}

// wireSessionAfter is wireSession with a device that says hello the way it
// is told to.
func wireSessionAfter(t *testing.T, hello string, respond func(msgID, req string) string, opts ...SessionOption) *Session {
	t.Helper()

	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		if err := dev.WriteRaw(hello); err != nil {
			return
		}
		for {
			req, err := dev.Recv()
			if err != nil {
				return
			}
			raw := respond(netconftest.ExtractMessageID(req), req)
			if raw == "" {
				dev.Hangup()
				return
			}
			if err := dev.WriteRaw(raw); err != nil {
				return
			}
		}
	}()

	opts = append([]SessionOption{WithLogger(slog.New(slog.DiscardHandler))}, opts...)
	sess, err := Open(context.Background(), wire, opts...)
	be.NilErr(t, err)
	return sess
}

func framed(msgID, body string) string {
	return netconftest.Reply(msgID, body) + "]]>]]>"
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// flood sends s to the client over and over, until the client stops taking
// it.
func flood(dev *netconftest.Device, s string) {
	for {
		if dev.WriteRaw(s) != nil {
			return
		}
	}
}

func waitDone(t *testing.T, sess *Session) {
	t.Helper()
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
}

// A connection that drops in the middle of a reply must fail the waiting
// operation instead of leaving it blocked.
func TestTransportDiesMidReply(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string {
		full := framed(msgID, "<data>"+strings.Repeat("x", 500)+"</data>")
		return full[:120] // the rest never arrives
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := sess.ExecReply(context.Background(), NewRPC("<get/>"))
		errCh <- err
	}()

	// Let the receive loop get stuck in the truncated reply, then drop the
	// connection the way a keepalive or a reset would.
	time.Sleep(50 * time.Millisecond)
	be.NilErr(t, sess.tr.Close())

	select {
	case err := <-errCh:
		be.True(t, errors.Is(err, ErrClosed))
	case <-time.After(5 * time.Second):
		t.Fatal("operation still blocked after the transport died")
	}

	waitDone(t, sess)
	be.Nonzero(t, sess.Err())
}

func TestDoneAndErrOnHangup(t *testing.T) {
	sess := wireSession(t, func(_, _ string) string { return "" })

	select {
	case <-sess.Done():
		t.Fatal("session ended before the device hung up")
	default:
	}
	be.NilErr(t, sess.Err())

	_, err := sess.ExecReply(testCtx(t), NewRPC("<get/>"))
	be.True(t, errors.Is(err, ErrClosed))

	waitDone(t, sess)
	be.Nonzero(t, sess.Err())

	// Operations on a dead session fail at once and carry the cause.
	_, err = sess.ExecReply(context.Background(), NewRPC("<get/>"))
	be.True(t, errors.Is(err, ErrClosed))
	be.True(t, errors.Is(err, sess.Err()))

	// Close on a dead session is not an error.
	be.NilErr(t, sess.Close(testCtx(t)))
}

func TestErrNilAfterClose(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, "<ok/>") })

	be.NilErr(t, sess.Close(testCtx(t)))
	waitDone(t, sess)
	be.NilErr(t, sess.Err())

	_, err := sess.ExecReply(context.Background(), NewRPC("<get/>"))
	be.Equal(t, ErrClosed, err)
}

type failingOp struct{}

func (failingOp) MarshalXML(*xml.Encoder, xml.StartElement) error {
	return errors.New("invalid operation")
}

// An operation that fails to encode must not put anything on the wire.
func TestExecEncodeErrorSendsNothing(t *testing.T) {
	var requests []string
	sess := wireSession(t, func(msgID, req string) string {
		requests = append(requests, req)
		return framed(msgID, "<ok/>")
	})

	err := sess.Exec(testCtx(t), failingOp{}, nil)
	be.Nonzero(t, err)
	be.In(t, "invalid operation", err.Error())

	// The session is still usable and the device saw only the good request.
	be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
	be.NilErr(t, sess.Close(testCtx(t)))
	be.Equal(t, 2, len(requests))
	be.In(t, "<get/>", requests[0])
	be.In(t, "close-session", requests[1])
}

func TestExecNestedRPCError(t *testing.T) {
	// Junos nests rpc-error in a results wrapper for its native operations.
	const loadResults = `<load-configuration-results>
<rpc-error>
<error-severity>error</error-severity>
<error-message>syntax error</error-message>
<error-info><bad-element>bogus</bad-element></error-info>
</rpc-error>
<load-error-count>1</load-error-count>
</load-configuration-results>`

	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, loadResults) })

	err := sess.Exec(testCtx(t), "<load-configuration/>", nil)
	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, "syntax error", rpcErr.Message)
}

func TestExecReply(t *testing.T) {
	const body = `<rpc-error><error-severity>warning</error-severity><error-message>uncommitted changes</error-message></rpc-error><ok/>`
	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, body) })

	reply, err := sess.ExecReply(testCtx(t), "<commit/>")
	be.NilErr(t, err)
	be.Equal(t, netconftest.Reply("1", body), string(reply.Raw))
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "uncommitted changes", reply.Warnings[0].Message)

	var ok struct {
		OK *struct{} `xml:"ok"`
	}
	be.NilErr(t, reply.Decode(&ok))
	be.Nonzero(t, ok.OK)
}

// The start tag of a message may be arbitrarily large and arrive in pieces.
func TestRecvLargeStartTag(t *testing.T) {
	attr := strings.Repeat("a", 10000)
	sess := wireSession(t, func(msgID, _ string) string {
		return `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" big="` + attr + `" message-id="` + msgID + `"><ok/></rpc-reply>]]>]]>`
	})

	reply, err := sess.ExecReply(testCtx(t), "<get/>")
	be.NilErr(t, err)
	be.In(t, `message-id="1"`, string(reply.Raw))
}

// Closing a session while a streamed reply was never closed must still end
// the session instead of leaking the receive loop.
func TestCloseWithUnclosedStream(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, "<ok/>") })

	msg, err := sess.Stream(testCtx(t), NewRPC("<get/>"))
	be.NilErr(t, err)

	// close-session cannot be answered while the stream holds the receive
	// loop, so Close runs into its deadline and tears the session down.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = sess.Close(ctx)

	waitDone(t, sess)

	_, err = io.ReadAll(msg)
	be.True(t, errors.Is(err, ErrClosed))
}

// The stream wait must also end when the receiver's context fired first and
// the message was never closed.
func TestCloseWithUnclosedStreamAfterCancel(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, "<ok/>") })

	ctx, cancel := context.WithCancel(context.Background())
	_, err := sess.Stream(ctx, NewRPC("<get/>"))
	be.NilErr(t, err)
	cancel()
	time.Sleep(20 * time.Millisecond)

	// Close does not wait for a close-session reply it could never read.
	start := time.Now()
	_ = sess.Close(context.Background())
	be.True(t, time.Since(start) < 5*time.Second)
	waitDone(t, sess)
}

func TestMaxMessageSize(t *testing.T) {
	big := strings.Repeat("x", 4096)

	t.Run("reply over the limit ends the session", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw(wireHello)
			req, _ := dev.Recv()
			_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<data>"+big+"</data>"))
		}()

		sess, err := Open(context.Background(), wire, WithMaxMessageSize(1024), WithLogger(nil))
		be.NilErr(t, err)

		_, err = sess.ExecReply(testCtx(t), NewRPC("<get/>"))
		be.True(t, errors.Is(err, ErrClosed))
		be.True(t, errors.Is(err, ErrMessageTooLarge))
		be.True(t, errors.Is(sess.Err(), ErrMessageTooLarge))
	})

	t.Run("endless reply ends the session", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw(wireHello)
			req, _ := dev.Recv()
			_ = dev.WriteRaw(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="` + netconftest.ExtractMessageID(req) + `">`)
			flood(dev, big) // never terminated
		}()

		sess, err := Open(context.Background(), wire, WithMaxMessageSize(64*1024), WithLogger(nil))
		be.NilErr(t, err)

		_, err = sess.ExecReply(context.Background(), NewRPC("<get/>"))
		be.True(t, errors.Is(err, ErrMessageTooLarge))
		waitDone(t, sess)
	})

	t.Run("streamed reply is not limited", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw(wireHello)
			req, _ := dev.Recv()
			_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<data>"+big+"</data>"))
		}()

		sess, err := Open(context.Background(), wire, WithMaxMessageSize(1024), WithLogger(nil))
		be.NilErr(t, err)

		msg, err := sess.Stream(testCtx(t), NewRPC("<get/>"))
		be.NilErr(t, err)
		body, err := io.ReadAll(msg)
		be.NilErr(t, err)
		be.In(t, big, string(body))
		be.NilErr(t, msg.Close())
	})

	t.Run("hello over the limit", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw("<!--" + big + "-->" + wireHello)
		}()

		_, err := Open(context.Background(), wire, WithMaxMessageSize(1024), WithLogger(nil))
		be.True(t, errors.Is(err, ErrMessageTooLarge))
	})
}

// Frames that hold no XML element are skipped; the session lives on.
func TestRecvSkipsMalformedFrames(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string {
		return "]]>]]>" + "  \n]]>]]>" + "<!-- note -->]]>]]>" + "not xml < at all]]>]]>" +
			`<?xml version="1.0" encoding="us-ascii"?>` + framed(msgID, "<ok/>")
	})

	be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
	be.NilErr(t, sess.Close(testCtx(t)))
}

// A reply without message-id answers the only operation outstanding.
func TestReplyWithoutMessageID(t *testing.T) {
	sess := wireSession(t, func(_, _ string) string {
		return `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error><error-type>rpc</error-type><error-tag>missing-attribute</error-tag><error-severity>error</error-severity></rpc-error></rpc-reply>]]>]]>`
	})

	err := sess.Exec(testCtx(t), "<get/>", nil)
	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, ErrMissingAttribute, rpcErr.Tag)
}

func TestMalformedAttributeNameRejected(t *testing.T) {
	var requests int
	sess := wireSession(t, func(msgID, _ string) string {
		requests++
		return framed(msgID, "<ok/>")
	})

	evil := xml.Attr{Name: xml.Name{Local: `a="1"><kill-session/></rpc><rpc x`}, Value: "v"}
	_, err := sess.ExecReply(testCtx(t), NewRPC("<get/>", WithAttributes(evil)))
	be.Nonzero(t, err)

	good := xml.Attr{Name: xml.Name{Local: "xmlns:ex"}, Value: "urn:example"}
	_, err = sess.ExecReply(testCtx(t), NewRPC("<get/>", WithAttributes(good)))
	be.NilErr(t, err)
	be.NilErr(t, sess.Close(testCtx(t)))
	be.Equal(t, 2, requests) // the good rpc and close-session
}

// Notifications still queued when the session closes are not delivered.
func TestQueuedNotificationsDroppedOnClose(t *testing.T) {
	const notif = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime></notification>]]>]]>`

	wire, dev := netconftest.NewWire(t)
	go func() {
		_, _ = dev.Recv()
		_ = dev.WriteRaw(wireHello + notif + notif + notif)
		req, _ := dev.Recv() // close-session
		_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<ok/>"))
	}()

	var handled atomic.Int32
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	sess, err := Open(context.Background(), wire, WithLogger(nil),
		WithNotifHandler(func(context.Context, *Message) {
			handled.Add(1)
			entered <- struct{}{}
			<-release
		}))
	be.NilErr(t, err)

	<-entered // first handler is running, two are queued
	be.NilErr(t, sess.Close(testCtx(t)))
	close(release)

	time.Sleep(50 * time.Millisecond)
	be.Equal(t, int32(1), handled.Load())
}

// A late, id-less error for an operation that was given up on must not be
// taken as the answer to the next one.
func TestIDLessReplyNotRoutedToNextOperation(t *testing.T) {
	const idLessError = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error><error-type>rpc</error-type><error-tag>malformed-message</error-tag><error-severity>error</error-severity></rpc-error></rpc-reply>]]>]]>`

	wire, dev := netconftest.NewWire(t)
	gotA := make(chan struct{})
	go func() {
		_, _ = dev.Recv()
		_ = dev.WriteRaw(wireHello)

		_, _ = dev.Recv() // A: not answered in time
		close(gotA)

		reqB, _ := dev.Recv()
		// A's late error arrives first, then B's real answer
		_ = dev.WriteRaw(idLessError + framed(netconftest.ExtractMessageID(reqB), "<ok/>"))
	}()

	sess, err := Open(context.Background(), wire, WithLogger(nil))
	be.NilErr(t, err)

	ctxA, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = sess.Exec(ctxA, "<a/>", nil)
	be.True(t, errors.Is(err, context.DeadlineExceeded))
	<-gotA

	be.NilErr(t, sess.Exec(testCtx(t), "<commit/>", nil))
}

// A message the session ignores is still held to the size limit while it is
// drained.
func TestEndlessIgnoredMessageEndsSession(t *testing.T) {
	for name, start := range map[string]string{
		"unknown element":   "<foo>",
		"unsolicited reply": `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="999">`,
		"not xml":           "<<",
	} {
		t.Run(name, func(t *testing.T) {
			wire, dev := netconftest.NewWire(t)
			go func() {
				_, _ = dev.Recv()
				_ = dev.WriteRaw(wireHello + start)
				flood(dev, strings.Repeat("x", 4096))
			}()

			sess, err := Open(context.Background(), wire, WithMaxMessageSize(64*1024), WithLogger(nil))
			be.NilErr(t, err)

			waitDone(t, sess)
			be.True(t, errors.Is(sess.Err(), ErrMessageTooLarge))
		})
	}
}

func TestEndlessHello(t *testing.T) {
	wire, dev := netconftest.NewWire(t)
	go func() {
		_, _ = dev.Recv()
		_ = dev.WriteRaw("<!--")
		flood(dev, strings.Repeat("x", 4096))
	}()

	// long before the hello exchange gives up, the size limit ends this
	_, err := Open(context.Background(), wire, WithMaxMessageSize(64*1024), WithLogger(nil))
	be.True(t, errors.Is(err, ErrMessageTooLarge))
}

// A peer cannot keep a session alive on messages it ignores.
func TestTooManyIgnoredMessagesEndSession(t *testing.T) {
	wire, dev := netconftest.NewWire(t)
	go func() {
		_, _ = dev.Recv()
		_ = dev.WriteRaw(wireHello + strings.Repeat("]]>]]>", maxSkippedMessages+5))
	}()

	sess, err := Open(context.Background(), wire, WithLogger(nil))
	be.NilErr(t, err)
	waitDone(t, sess)
	be.Nonzero(t, sess.Err())
}

// An operation caught by the transport dying reports ErrClosed whichever
// side notices first, the write or the receive loop.
func TestOperationRacingTransportDeath(t *testing.T) {
	for range 50 {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			_ = dev.WriteRaw(wireHello)
		}()

		sess, err := Open(context.Background(), wire, WithLogger(nil))
		be.NilErr(t, err)

		go func() { _ = wire.Close() }()
		_, err = sess.ExecReply(testCtx(t), NewRPC("<get/>"))
		be.True(t, errors.Is(err, ErrClosed))
		waitDone(t, sess)
	}
}

// The message-id a caller puts in a request is not sent.  Two requests that
// bring the same one, and the first is given up on: the late answer to the
// first goes to nobody, and the second gets its own.  Over chunked framing
// to Junos the second waits for that late answer before it goes out.
func TestMessageIDOfTheCaller(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hello string
		turn  bool
	}{
		{name: "end-of-message framing", hello: wireHello},
		{name: "junos over chunked framing", hello: wireHelloJunos11, turn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, dev := turnSession(t, tc.hello)
			ctx := testCtx(t)

			// "1" is also the first number of the session
			ctxA, cancelA := context.WithCancel(ctx)
			first := make(chan error, 1)
			go func() {
				_, err := sess.ExecReply(ctxA, &RPC{MessageID: "1", Operation: "<get-config/>"})
				first <- err
			}()
			one := dev.next(t)
			cancelA()
			be.True(t, errors.Is(<-first, context.Canceled))

			type outcome struct {
				reply *Reply
				err   error
			}
			second := make(chan outcome, 1)
			go func() {
				reply, err := sess.ExecReply(ctx, &RPC{MessageID: "1", Operation: "<commit/>"})
				second <- outcome{reply, err}
			}()
			var two string
			if tc.turn {
				dev.nothing(t)
			} else {
				two = dev.next(t)
			}

			be.NilErr(t, dev.Send(netconftest.Reply(netconftest.ExtractMessageID(one), "<data>the answer to get-config</data>")))
			if tc.turn {
				two = dev.next(t)
			}
			be.In(t, "<commit/>", two)
			be.Unequal(t, netconftest.ExtractMessageID(one), netconftest.ExtractMessageID(two))
			select {
			case got := <-second:
				t.Fatalf("the commit was answered before the device answered it: %v", got.err)
			case <-time.After(held):
			}

			dev.answer(t, two)
			got := <-second
			be.NilErr(t, got.err)
			be.In(t, "<ok/>", string(got.reply.Raw))
		})
	}
}

// failingWrites is a transport whose writes fail once the first messages are
// out.
type failingWrites struct {
	*netconftest.Wire
	good atomic.Int32 // messages that still go through
	err  error
}

func (f *failingWrites) MsgWriter() (io.WriteCloser, error) {
	if f.good.Add(-1) >= 0 {
		return f.Wire.MsgWriter()
	}
	return failedWriter{f.err}, nil
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }
func (failedWriter) Close() error                { return nil }

// A failed write ends the session, and is why it ended.  What it sets off,
// the transport being closed and the read failing in turn, is a consequence
// and must not be reported as the cause.
func TestWriteErrorIsWhySessionEnded(t *testing.T) {
	errWrite := errors.New("no route to host")

	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err == nil {
			_ = dev.WriteRaw(wireHello)
		}
	}()

	tr := &failingWrites{Wire: wire, err: errWrite}
	tr.good.Store(1) // the hello
	sess, err := Open(context.Background(), tr, WithLogger(slog.New(slog.DiscardHandler)))
	be.NilErr(t, err)

	_, err = sess.ExecReply(testCtx(t), "<get/>")
	be.True(t, errors.Is(err, ErrClosed))
	be.True(t, errors.Is(err, errWrite))

	waitDone(t, sess)
	be.True(t, errors.Is(sess.Err(), errWrite))

	// and every later operation says the same
	_, err = sess.ExecReply(testCtx(t), "<get/>")
	be.True(t, errors.Is(err, ErrClosed))
	be.True(t, errors.Is(err, errWrite))
}

// The context given to Open bounds the hello exchange and cancels it.
// Either way the transport is closed.
func TestOpenContext(t *testing.T) {
	// silentDevice takes the client's hello and never sends its own.
	silentDevice := func(t *testing.T) (*netconftest.Wire, <-chan error) {
		wire, dev := netconftest.NewWire(t)
		closed := make(chan error, 1)
		go func() {
			if _, err := dev.Recv(); err != nil {
				closed <- err
				return
			}
			// the next read ends when the client closes the transport
			_, err := dev.Recv()
			closed <- err
		}()
		return wire, closed
	}
	transportClosed := func(t *testing.T, closed <-chan error) {
		t.Helper()
		select {
		case err := <-closed:
			be.Nonzero(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("transport was not closed")
		}
	}

	t.Run("cancelled", func(t *testing.T) {
		wire, closed := silentDevice(t)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)

		_, err := Open(ctx, wire)
		be.True(t, errors.Is(err, context.Canceled))
		be.In(t, "hello exchange", err.Error())
		transportClosed(t, closed)
	})

	t.Run("deadline", func(t *testing.T) {
		wire, closed := silentDevice(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		_, err := Open(ctx, wire)
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		transportClosed(t, closed)
	})

	t.Run("already cancelled", func(t *testing.T) {
		wire, _ := netconftest.NewWire(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := Open(ctx, wire)
		be.True(t, errors.Is(err, context.Canceled))
	})

	t.Run("the session outlives the context", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			if _, err := dev.Recv(); err != nil {
				return
			}
			_ = dev.WriteRaw(wireHello)
			for {
				req, err := dev.Recv()
				if err != nil {
					return
				}
				_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<ok/>"))
			}
		}()

		ctx, cancel := context.WithCancel(context.Background())
		sess, err := Open(ctx, wire, WithLogger(slog.New(slog.DiscardHandler)))
		be.NilErr(t, err)
		cancel()

		be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
		be.NilErr(t, sess.Close(testCtx(t)))
	})
}

// WithOperationTimeout bounds the wait for a reply for callers that bring no
// deadline of their own, and leaves the session usable.  A context with a
// deadline is taken at its word, whether that is before the bound or after.
func TestOperationTimeout(t *testing.T) {
	// The device answers everything except <slow/>, and takes its time over
	// <late/>: longer than the bound of the session, as a commit can.
	const late = 150 * time.Millisecond
	respond := func(msgID, req string) string {
		switch {
		case strings.Contains(req, "<slow/>"):
			return framed("no-such-id", "<ok/>")
		case strings.Contains(req, "<late/>"):
			time.Sleep(late)
		}
		return framed(msgID, "<ok/>")
	}

	t.Run("bounds an operation", func(t *testing.T) {
		sess := wireSession(t, respond, WithOperationTimeout(30*time.Millisecond))

		start := time.Now()
		_, err := sess.ExecReply(context.Background(), "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		be.True(t, time.Since(start) < 5*time.Second)

		be.NilErr(t, sess.Exec(context.Background(), "<get/>", nil))
		be.NilErr(t, sess.Close(testCtx(t)))
	})

	t.Run("a shorter deadline of the caller wins", func(t *testing.T) {
		sess := wireSession(t, respond, WithOperationTimeout(time.Hour))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := sess.ExecReply(ctx, "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
	})

	t.Run("a longer deadline of the caller wins", func(t *testing.T) {
		sess := wireSession(t, respond, WithOperationTimeout(30*time.Millisecond))

		start := time.Now()
		be.NilErr(t, sess.Exec(testCtx(t), "<late/>", nil))
		be.True(t, time.Since(start) >= late)

		// and without a deadline the bound of the session is back
		_, err := sess.ExecReply(context.Background(), "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
	})

	// A context that can be cancelled and has no deadline brings no bound of
	// its own, so the one of the session applies.
	t.Run("a context without a deadline gets the bound", func(t *testing.T) {
		sess := wireSession(t, respond, WithOperationTimeout(30*time.Millisecond))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := sess.ExecReply(ctx, "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
	})

	t.Run("off by default", func(t *testing.T) {
		sess := wireSession(t, respond)

		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		_, err := sess.ExecReply(ctx, "<slow/>")
		be.True(t, errors.Is(err, context.Canceled))
	})

	// The timeout is for the reply to arrive.  Reading a streamed reply is
	// the caller's business and takes as long as it takes.
	t.Run("does not cut a streamed reply short", func(t *testing.T) {
		sess := wireSession(t, respond, WithOperationTimeout(30*time.Millisecond))

		msg, err := sess.Stream(context.Background(), NewRPC("<get/>"))
		be.NilErr(t, err)
		time.Sleep(60 * time.Millisecond)

		body, err := io.ReadAll(msg)
		be.NilErr(t, err)
		be.In(t, "<ok/>", string(body))
		be.NilErr(t, msg.Close())
		be.NilErr(t, sess.Close(testCtx(t)))
	})
}

// Exec cannot return the warnings of a successful reply, and the typed
// operations are built on it.  They must not vanish: a commit that went
// through with a warning is worth knowing about.
func TestExecLogsWarnings(t *testing.T) {
	const warned = `<rpc-error><error-severity>warning</error-severity><error-message>
statement not found
</error-message></rpc-error><ok/>`

	logs := &logRecorder{}
	sess := wireSession(t, func(msgID, _ string) string {
		return framed(msgID, warned)
	}, WithLogger(slog.New(logs)))

	// ExecReply hands the warnings over, so there is nothing to log
	reply, err := sess.ExecReply(testCtx(t), "<commit/>")
	be.NilErr(t, err)
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, 0, len(logs.Records()))

	be.NilErr(t, sess.Exec(testCtx(t), "<commit/>", nil))
	be.AllEqual(t, []string{"operation succeeded with warnings"}, logs.Records())
}

// A timeout computed from a deadline goes negative once the budget is spent.
// That must mean "too late", not "no limit".
func TestSpentTimeouts(t *testing.T) {
	t.Run("hello", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() { _, _ = dev.Recv() }() // never answers

		ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
		defer cancel()

		start := time.Now()
		_, err := Open(ctx, wire)
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		be.True(t, time.Since(start) < 5*time.Second)
	})

	t.Run("operation", func(t *testing.T) {
		sess := wireSession(t, func(msgID, _ string) string {
			return framed(msgID, "<ok/>")
		}, WithOperationTimeout(-time.Second))

		_, err := sess.ExecReply(context.Background(), "<get/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
	})
}

// A device that does not acknowledge <close-session> in time still gets the
// session closed, but Close says that it was not a clean one.
func TestCloseNotAcknowledged(t *testing.T) {
	sess := wireSession(t, func(msgID, req string) string {
		if strings.Contains(req, "<close-session") {
			return framed("no-such-id", "<ok/>")
		}
		return framed(msgID, "<ok/>")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := sess.Close(ctx)
	be.True(t, errors.Is(err, context.DeadlineExceeded))
	be.In(t, "close-session", err.Error())

	// closed all the same, on purpose, and a second Close has nothing to add
	waitDone(t, sess)
	be.NilErr(t, sess.Err())
	be.NilErr(t, sess.Close(testCtx(t)))
}

// Closing a streamed reply from another goroutine than the one reading it
// waits for that read; it must not race with it.  Run under the race
// detector.
func TestMessageCloseDuringRead(t *testing.T) {
	// the reply starts and never ends
	sess := wireSession(t, func(msgID, _ string) string {
		return `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="` + msgID + `"><data>`
	})

	msg, err := sess.Stream(testCtx(t), NewRPC("<get/>"))
	be.NilErr(t, err)

	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(msg)
		read <- err
	}()
	closed := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		closed <- msg.Close()
	}()

	// the only way out of a reply that never ends is the session
	time.Sleep(50 * time.Millisecond)
	be.NilErr(t, sess.Close(testCtx(t)))

	for _, ch := range []chan error{read, closed} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("reader or closer still blocked after the session was closed")
		}
	}
}

// A context that is over before the operation starts must not get the rpc out.  The
// wait for the send slot was a draw the context lost half the time.
func TestDeadContextSendsNothing(t *testing.T) {
	var received atomic.Int32
	sess := wireSession(t, func(msgID, _ string) string {
		received.Add(1)
		return framed(msgID, "<ok/>")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 50 {
		_, err := sess.ExecReply(ctx, NewRPC("<edit-config/>"))
		be.True(t, errors.Is(err, context.Canceled))
	}

	// a round trip, so anything sent before it has reached the device
	be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
	be.Equal(t, int32(1), received.Load())
}

// Replies to operations the caller stopped waiting for are the device doing
// its job.  However many arrive in a row, they are not a reason to drop it.
func TestLateRepliesDoNotEndSession(t *testing.T) {
	const abandoned = maxSkippedMessages + 10

	wire, dev := netconftest.NewWire(t)
	release := make(chan struct{})
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		_ = dev.WriteRaw(wireHello)

		// take every operation, answer none until told to
		var ids []string
		for range abandoned {
			req, err := dev.Recv()
			if err != nil {
				return
			}
			ids = append(ids, netconftest.ExtractMessageID(req))
		}
		<-release
		for _, id := range ids {
			if dev.WriteRaw(framed(id, "<ok/>")) != nil {
				return
			}
		}
		for {
			req, err := dev.Recv()
			if err != nil {
				return
			}
			_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<ok/>"))
		}
	}()

	sess, err := Open(context.Background(), wire, WithLogger(slog.New(slog.DiscardHandler)))
	be.NilErr(t, err)

	for range abandoned {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
		_, err := sess.ExecReply(ctx, "<get/>")
		cancel()
		be.True(t, errors.Is(err, context.DeadlineExceeded))
	}
	close(release)

	// the session reads all the late replies and is still there for this
	be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
	be.NilErr(t, sess.Err())
}

// After a chunk header that makes no sense nobody knows where the next
// message starts.  The session has to end there, also when the device then
// says nothing more: it used to wait for the rest of the message for ever.
func TestMalformedChunkEndsSession(t *testing.T) {
	for _, header := range []string{"\n#0\n", "\n#abc\n", "\n#\n"} {
		wire, dev := netconftest.NewWire(t)
		go func() {
			if _, err := dev.Recv(); err != nil {
				return
			}
			_ = dev.WriteRaw(wireHello11)
			dev.Upgrade()
			if _, err := dev.Recv(); err != nil {
				return
			}
			_ = dev.WriteRaw(header)
			// and then silence
		}()

		sess, err := Open(context.Background(), wire, WithLogger(slog.New(slog.DiscardHandler)))
		be.NilErr(t, err)

		_, err = sess.ExecReply(testCtx(t), "<get/>")
		be.True(t, errors.Is(err, ErrClosed))
		waitDone(t, sess)
		be.True(t, errors.Is(sess.Err(), transport.ErrMalformedChunk))
	}
}

// A message that base:1.0 framing cannot carry is refused whole: the caller
// gets the reason, the device gets nothing, and the session carries on as if
// the operation had never been tried.
func TestRefusedMessage(t *testing.T) {
	const idLessError = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error><error-severity>error</error-severity><error-message>malformed</error-message></rpc-error></rpc-reply>]]>]]>`

	var received []string
	sess := wireSession(t, func(msgID, req string) string {
		received = append(received, req)
		if strings.Contains(req, "<bad-reply/>") {
			return idLessError
		}
		return framed(msgID, "<ok/>")
	})

	_, err := sess.ExecReply(testCtx(t), "<load><text>]]>]]></text></load>")
	be.True(t, errors.Is(err, transport.ErrEndOfMsgInPayload))
	be.False(t, errors.Is(err, ErrClosed))

	// Nothing was sent, so no reply is outstanding: a reply without a
	// message-id now can only answer the next operation.
	_, err = sess.ExecReply(testCtx(t), "<bad-reply/>")
	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, "malformed", rpcErr.Message)

	be.NilErr(t, sess.Close(testCtx(t)))
	be.Equal(t, 2, len(received)) // the second operation and close-session
	be.In(t, "<bad-reply/>", received[0])
}

// The reclaim that ADR 0002 says not to remove: a streamed reply whose caller
// left before collecting it is taken back, or the receive loop waits on a
// message nobody will ever close.
func TestAwaitStreamReclaimsAbandonedReply(t *testing.T) {
	wire, _ := netconftest.NewWire(t)
	sess := newSession(wire)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := &pendingReply{msg: make(chan *Message, 1), ctx: ctx, stream: true}

	body, _, _ := bounded(strings.NewReader("<rpc-reply/>"), -1)
	reader := &streamReader{r: body, closer: body, done: make(chan struct{})}
	req.msg <- &Message{reader: reader}

	returned := make(chan error, 1)
	go func() { returned <- sess.awaitStream(req, reader, body) }()

	select {
	case err := <-returned:
		be.NilErr(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop is still waiting for a reply nobody has")
	}
	select {
	case <-reader.done:
	default:
		t.Fatal("the abandoned reply was not closed")
	}
}

// A streamed reply that is taken back is nobody's to read at a pace of their
// own any more.  Closing it reads the rest, and a device that stops inside
// that rest ends the session like any other message that stalls.
func TestAwaitStreamTimesAbandonedReply(t *testing.T) {
	wire, _ := netconftest.NewWire(t)
	sess := newSession(wire, WithMessageTimeout(50*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := &pendingReply{msg: make(chan *Message, 1), ctx: ctx, stream: true}

	// the rest of the reply never comes: the device says nothing
	body, err := sess.msgReader()
	be.NilErr(t, err)
	body.stalled = sess.stalled
	reader := &streamReader{r: body, closer: body, done: make(chan struct{})}
	req.msg <- &Message{reader: reader}

	returned := make(chan error, 1)
	go func() { returned <- sess.awaitStream(req, reader, body) }()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop waits for the rest of a reply nobody reads, in spite of the message timeout")
	}
	be.True(t, errors.Is(sess.Err(), ErrMessageStalled))
}

// The caller of an operation can give up once the receive loop has found out
// whose reply is coming in, and before the loop has all of it to hand over.
// Nobody takes the reply then.  The loop must not wait for somebody to: the
// session would never read another message.
func TestReplyHandedToNobody(t *testing.T) {
	wire, dev := netconftest.NewWire(t)

	opened := make(chan *Session, 1)
	go func() {
		session, err := Open(context.Background(), wire, WithLogger(slog.New(slog.DiscardHandler)))
		if err != nil {
			t.Errorf("open: %v", err)
		}
		opened <- session
	}()
	_, err := dev.Recv()
	be.NilErr(t, err)
	be.NilErr(t, dev.WriteRaw(wireHello))
	session := <-opened
	if session == nil {
		t.FailNow()
	}

	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan error, 1)
	go func() {
		_, err := session.ExecReply(ctx, "<get/>")
		gaveUp <- err
	}()
	req, err := dev.Recv()
	be.NilErr(t, err)
	id := netconftest.ExtractMessageID(req)

	// The reply begins.  The loop takes the operation off its list and reads
	// on for the rest of the reply, which is not there yet.
	be.NilErr(t, dev.WriteRaw(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="`+id+`">`))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		session.replies.mu.Lock()
		_, listed := session.replies.pending[id]
		session.replies.mu.Unlock()
		if !listed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the receive loop did not get to the reply")
		}
	}

	// the caller gives up, and the rest of the reply arrives for nobody
	cancel()
	be.True(t, errors.Is(<-gaveUp, context.Canceled))
	be.NilErr(t, dev.WriteRaw(`<ok/></rpc-reply>]]>]]>`))

	// the session goes on
	go func() {
		req, err := dev.Recv()
		if err != nil {
			return
		}
		_ = dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<ok/>"))
	}()
	_, err = session.ExecReply(testCtx(t), "<get/>")
	be.NilErr(t, err)
}

// What comes before the start element of a message is buffered whatever
// becomes of the message, so it has a limit of its own, far below the
// message size.
func TestStartElementLimit(t *testing.T) {
	sess := wireSession(t, func(_, _ string) string {
		return "<!--" + strings.Repeat("x", maxStartSize+1) + "-->" + framed("1", "<ok/>")
	}, WithMaxMessageSize(0))

	_, err := sess.ExecReply(testCtx(t), "<get/>")
	be.True(t, errors.Is(err, ErrClosed))
	waitDone(t, sess)
	be.True(t, errors.Is(sess.Err(), ErrMessageTooLarge))
}

// A handler waiting on its context is released when the device is gone, not
// only when Close is called.
func TestNotificationContextCancelledOnHangup(t *testing.T) {
	const notif = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime></notification>]]>]]>`

	started, released := make(chan struct{}), make(chan struct{})
	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		_ = dev.WriteRaw(wireHello + notif)
		// a notification still waiting for its turn is dropped when the
		// session ends, so let the handler get to it first
		<-started
		dev.Hangup()
	}()

	_, err := Open(context.Background(), wire,
		WithLogger(slog.New(slog.DiscardHandler)),
		WithNotifHandler(func(ctx context.Context, _ *Message) {
			close(started)
			<-ctx.Done()
			close(released)
		}),
	)
	be.NilErr(t, err)

	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("handler context still alive after the device hung up")
	}
}
