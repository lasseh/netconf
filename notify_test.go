package netconf

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

func notification(size int) *Message {
	return &Message{body: make([]byte, size)}
}

// The queue takes what its bounds allow and not one notification more,
// whichever bound is reached first.
func TestNotifierBounds(t *testing.T) {
	quiet := slog.New(slog.DiscardHandler)
	idle := func(context.Context, *Message) {}

	t.Run("by count", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{Messages: 3}, quiet)
		for range 3 {
			be.NilErr(t, n.deliver(notification(10)))
		}
		be.True(t, errors.Is(n.deliver(notification(10)), ErrNotificationOverflow))
	})

	t.Run("by size", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{Bytes: 250}, quiet)
		be.NilErr(t, n.deliver(notification(100)))
		be.NilErr(t, n.deliver(notification(100)))
		be.True(t, errors.Is(n.deliver(notification(100)), ErrNotificationOverflow))
		// a smaller one still fits
		be.NilErr(t, n.deliver(notification(50)))
	})

	// However small the bound, a notification that arrives when nothing is
	// waiting gets through: the message size limit is what bounds one.
	t.Run("one is always let in", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{Bytes: 10}, quiet)
		be.NilErr(t, n.deliver(notification(1000)))
		be.True(t, errors.Is(n.deliver(notification(1)), ErrNotificationOverflow))
	})

	t.Run("room again once the handler has taken some", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{Messages: 2}, quiet)
		be.NilErr(t, n.deliver(notification(10)))
		be.NilErr(t, n.deliver(notification(10)))
		be.Nonzero(t, n.next())
		be.NilErr(t, n.deliver(notification(10)))
		be.True(t, errors.Is(n.deliver(notification(10)), ErrNotificationOverflow))
	})

	t.Run("defaults", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{}, quiet)
		be.Equal(t, DefaultNotifQueueMessages, n.limit.Messages)
		be.Equal(t, int64(DefaultNotifQueueBytes), n.limit.Bytes)
	})

	// Dropping keeps what was let in, in order, and says that it dropped.
	t.Run("drop", func(t *testing.T) {
		logs := &logRecorder{}
		n := newNotifier(idle, NotifQueue{Messages: 2, Drop: true}, slog.New(logs))

		first, second := notification(1), notification(2)
		be.NilErr(t, n.deliver(first))
		be.NilErr(t, n.deliver(second))
		for range 5 {
			be.NilErr(t, n.deliver(notification(3)))
		}

		be.True(t, n.next() == first)
		be.True(t, n.next() == second)
		be.True(t, n.next() == nil)
		// reported at the first, second and fourth loss, not at every one
		be.Equal(t, 3, len(logs.Records()))
	})
}

// The queue takes the room of what waits in it.  What went through it
// leaves nothing behind, however long the queue has not been empty.
func TestNotifierStorage(t *testing.T) {
	quiet := slog.New(slog.DiscardHandler)
	idle := func(context.Context, *Message) {}

	// A handler that never catches up: the queue stays full, one
	// notification goes out and one comes in.
	t.Run("a queue that is never empty", func(t *testing.T) {
		const bound, size = 4, 10
		n := newNotifier(idle, NotifQueue{Messages: bound, Drop: true}, quiet)

		var waiting []*Message
		for range bound {
			msg := notification(size)
			be.NilErr(t, n.deliver(msg))
			waiting = append(waiting, msg)
		}
		for range 10000 {
			be.True(t, n.next() == waiting[0])

			msg := notification(size)
			be.NilErr(t, n.deliver(msg))
			waiting = append(waiting[1:], msg)
			// one more than fits is thrown away
			be.NilErr(t, n.deliver(notification(size)))
		}

		be.Equal(t, bound, len(n.queue)-n.head)
		be.Equal(t, int64(bound*size), n.bytes)
		// a few times the bound, where it was a slot for each of the 10000
		be.True(t, cap(n.queue) <= 8*bound)
		for _, msg := range waiting {
			be.True(t, n.next() == msg)
		}
		be.True(t, n.next() == nil)
	})

	t.Run("stop lets go of what waits", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{Messages: 2}, quiet)
		be.NilErr(t, n.deliver(notification(10)))
		be.NilErr(t, n.deliver(notification(10)))

		n.stop()
		be.True(t, n.next() == nil)
		be.Equal(t, int64(0), n.bytes)

		// what arrives afterwards is thrown away, and that is no overflow
		for range 5 {
			be.NilErr(t, n.deliver(notification(10)))
		}
		be.True(t, n.next() == nil)
	})

	t.Run("stop while notifications arrive", func(t *testing.T) {
		n := newNotifier(idle, NotifQueue{}, quiet)
		go n.run()

		arrived := make(chan struct{})
		go func() {
			defer close(arrived)
			for range 1000 {
				_ = n.deliver(notification(1))
			}
		}()
		n.stop()
		<-arrived
		be.True(t, n.next() == nil)
	})
}

const testNotification = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime></notification>]]>]]>`

// notifyingSession opens a session to a device that sends count
// notifications right after the hello exchange and answers every operation
// with <ok/>.
func notifyingSession(t *testing.T, count int, opts ...SessionOption) *Session {
	t.Helper()

	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		if dev.WriteRaw(wireHello+strings.Repeat(testNotification, count)) != nil {
			return
		}
		for {
			req, err := dev.Recv()
			if err != nil {
				return
			}
			if dev.WriteRaw(framed(netconftest.ExtractMessageID(req), "<ok/>")) != nil {
				return
			}
		}
	}()

	sess, err := Open(context.Background(), wire, append([]SessionOption{WithLogger(slog.New(slog.DiscardHandler))}, opts...)...)
	be.NilErr(t, err)
	return sess
}

// A handler that does not keep up loses the session, and is told why: events
// were lost, and the receiver must know.
func TestNotificationOverflowEndsSession(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	sess := notifyingSession(t, 50,
		WithNotifHandler(func(ctx context.Context, _ *Message) {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}),
		WithNotifQueue(NotifQueue{Messages: 5}),
	)

	waitDone(t, sess)
	be.True(t, errors.Is(sess.Err(), ErrNotificationOverflow))

	_, err := sess.ExecReply(testCtx(t), "<get/>")
	be.True(t, errors.Is(err, ErrClosed))
	be.True(t, errors.Is(err, ErrNotificationOverflow))
}

// With Drop the session outlives a handler that is stuck, and operations
// keep working while notifications are thrown away, also operations issued
// by the handler itself.
func TestNotificationDropKeepsSession(t *testing.T) {
	inHandler := make(chan error, 1)
	release := make(chan struct{})

	var sess *Session
	ready := make(chan struct{})
	sess = notifyingSession(t, 50,
		WithNotifHandler(func(ctx context.Context, _ *Message) {
			<-ready
			select {
			case inHandler <- sess.Exec(ctx, "<get/>", nil):
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}),
		WithNotifQueue(NotifQueue{Messages: 2, Drop: true}),
	)
	close(ready)

	select {
	case err := <-inHandler:
		be.NilErr(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("operation issued by the handler did not return while the queue was full")
	}

	be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
	be.NilErr(t, sess.Err())

	close(release)
	be.NilErr(t, sess.Close(testCtx(t)))
}

// Close tells a running handler through its context and does not wait for
// it: a handler that ignores the context cannot hold up Close.
func TestCloseDoesNotWaitForHandler(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck)
	started := make(chan struct{}, 1)

	sess := notifyingSession(t, 1, WithNotifHandler(func(context.Context, *Message) {
		started <- struct{}{}
		<-stuck
	}))
	<-started

	closed := make(chan error, 1)
	go func() { closed <- sess.Close(testCtx(t)) }()
	select {
	case err := <-closed:
		be.NilErr(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close is waiting for a notification handler")
	}
}
