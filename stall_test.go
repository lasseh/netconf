package netconf

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

// stallingDevice returns a session to a device that says hello and then
// does what script tells it with each request.
func stallingDevice(t *testing.T, script func(dev *netconftest.Device, msgID string), opts ...SessionOption) *Session {
	t.Helper()

	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		if err := dev.WriteRaw(wireHello); err != nil {
			return
		}
		for {
			req, err := dev.Recv()
			if err != nil {
				return
			}
			script(dev, netconftest.ExtractMessageID(req))
		}
	}()

	sess, err := Open(context.Background(), wire, append([]SessionOption{WithLogger(slog.New(slog.DiscardHandler))}, opts...)...)
	be.NilErr(t, err)
	return sess
}

// A device that sends the start of a message and then nothing holds the
// receive loop on a read.  With WithMessageTimeout the session ends over
// it, and says why.
func TestMessageTimeout(t *testing.T) {
	const bound = 100 * time.Millisecond
	half := func(msgID string) string {
		return `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="` + msgID + `"><data><configuration>`
	}

	t.Run("a message that stops halfway ends the session", func(t *testing.T) {
		sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
			_ = dev.WriteRaw(half(msgID))
		}, WithMessageTimeout(bound))

		// two operations wait: the one whose reply stalls, and one behind it
		errs := make(chan error, 2)
		for range cap(errs) {
			go func() {
				_, err := sess.ExecReply(context.Background(), "<get-config/>")
				errs <- err
			}()
		}

		start := time.Now()
		select {
		case <-sess.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the session goes on waiting for the rest of the message")
		}
		be.True(t, time.Since(start) >= bound)
		be.True(t, errors.Is(sess.Err(), ErrMessageStalled))
		be.In(t, bound.String(), sess.Err().Error())

		for range cap(errs) {
			err := <-errs
			be.True(t, errors.Is(err, ErrClosed))
			be.True(t, errors.Is(err, ErrMessageStalled))
		}
	})

	// The time is for the silence, not for the message: one that keeps
	// coming is read however long it takes.
	t.Run("a message that arrives slowly is delivered", func(t *testing.T) {
		const pieces, pause = 12, bound / 4
		sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
			_ = dev.WriteRaw(half(msgID))
			for range pieces {
				time.Sleep(pause)
				_ = dev.WriteRaw("<x/>")
			}
			_ = dev.WriteRaw("</configuration></data></rpc-reply>]]>]]>")
		}, WithMessageTimeout(bound))

		start := time.Now()
		reply, err := sess.ExecReply(testCtx(t), "<get-config/>")
		be.NilErr(t, err)
		be.True(t, time.Since(start) > bound)
		be.Equal(t, pieces, strings.Count(string(reply.Raw), "<x/>"))
		be.NilErr(t, sess.Err())
	})

	// Between two messages a session waits for as long as it takes.
	t.Run("silence between messages is not timed", func(t *testing.T) {
		sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
			_ = dev.WriteRaw(framed(msgID, "<ok/>"))
		}, WithMessageTimeout(bound))

		be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
		time.Sleep(3 * bound)
		be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
		be.NilErr(t, sess.Err())
	})

	// Junos writes a line break behind the end of every message, which is
	// the first byte of the next one to the framing.  A message has not begun
	// with it: a session that is left alone after a reply, or after the
	// hello, stays.
	t.Run("white space behind a message is not the next one", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			if _, err := dev.Recv(); err != nil {
				return
			}
			if dev.WriteRaw(wireHello+"\n") != nil {
				return
			}
			for {
				req, err := dev.Recv()
				if err != nil {
					return
				}
				if dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<ok/>")+"\n \r\n\t") != nil {
					return
				}
			}
		}()
		sess, err := Open(context.Background(), wire, WithLogger(slog.New(slog.DiscardHandler)), WithMessageTimeout(bound))
		be.NilErr(t, err)

		time.Sleep(3 * bound)
		be.NilErr(t, sess.Err())
		be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
		time.Sleep(3 * bound)
		be.NilErr(t, sess.Err())
		be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))

		// and a message that stops behind its white space and one byte more
		// has begun
		go func() { _ = dev.WriteRaw("<") }()
		select {
		case <-sess.Done():
			be.True(t, errors.Is(sess.Err(), ErrMessageStalled))
		case <-time.After(5 * time.Second):
			t.Fatal("the session goes on waiting for the rest of the message")
		}
	})

	// A reply read with Stream is the caller's to read, at the caller's
	// pace.
	t.Run("a streamed reply is not timed", func(t *testing.T) {
		sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
			_ = dev.WriteRaw(half(msgID))
			time.Sleep(3 * bound)
			_ = dev.WriteRaw("</configuration></data></rpc-reply>]]>]]>")
		}, WithMessageTimeout(bound))

		msg, err := sess.Stream(testCtx(t), "<get-config/>")
		be.NilErr(t, err)
		time.Sleep(2 * bound)
		body, err := io.ReadAll(msg)
		be.NilErr(t, err)
		be.In(t, "</rpc-reply>", string(body))
		be.NilErr(t, msg.Close())

		be.NilErr(t, sess.Err())
		be.NilErr(t, sess.Close(testCtx(t)))
	})

	// What nobody waits for is read to its end like any other message.
	t.Run("a notification that stops halfway", func(t *testing.T) {
		sess := stallingDevice(t, func(dev *netconftest.Device, _ string) {
			_ = dev.WriteRaw(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>`)
		}, WithMessageTimeout(bound), WithNotifHandler(func(context.Context, *Message) {}))

		_, err := sess.ExecReply(context.Background(), "<get/>")
		be.True(t, errors.Is(err, ErrMessageStalled))
		be.True(t, errors.Is(sess.Err(), ErrMessageStalled))
	})

	// A message nobody reads, a notification on a session without a
	// handler, is read to its end all the same, and is timed like any
	// other: with a size limit and without one.
	for name, limit := range map[string]int64{"with the size limit": 1 << 20, "without a size limit": 0} {
		t.Run("a skipped message that arrives slowly, "+name, func(t *testing.T) {
			const pieces, pause = 12, bound / 4
			sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
				_ = dev.WriteRaw(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-11T12:00:00Z</eventTime><event>`)
				for range pieces {
					time.Sleep(pause)
					_ = dev.WriteRaw("<x/>")
				}
				_ = dev.WriteRaw("</event></notification>]]>]]>" + framed(msgID, "<ok/>"))
			}, WithMessageTimeout(bound), WithMaxMessageSize(limit))

			_, err := sess.ExecReply(testCtx(t), "<get/>")
			be.NilErr(t, err)
			be.NilErr(t, sess.Err())
		})
	}

	// a session that is closed has nothing to say about a message
	t.Run("close has the last word", func(t *testing.T) {
		sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
			_ = dev.WriteRaw(half(msgID))
		}, WithMessageTimeout(time.Hour))

		ctx, cancel := context.WithTimeout(context.Background(), bound)
		defer cancel()
		_, err := sess.ExecReply(ctx, "<get-config/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))

		closeCtx, cancelClose := context.WithTimeout(context.Background(), bound)
		defer cancelClose()
		_ = sess.Close(closeCtx)
		be.NilErr(t, sess.Err())
	})

	t.Run("off by default", func(t *testing.T) {
		sess := stallingDevice(t, func(dev *netconftest.Device, msgID string) {
			_ = dev.WriteRaw(half(msgID))
		})

		ctx, cancel := context.WithTimeout(context.Background(), 3*bound)
		defer cancel()
		_, err := sess.ExecReply(ctx, "<get-config/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		select {
		case <-sess.Done():
			t.Fatalf("the session ended: %v", sess.Err())
		default:
		}
	})
}
