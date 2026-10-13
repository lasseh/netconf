package netconf

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// NotifHandler is called for each notification the server sends.
//
// Notifications are handed over one at a time, in the order received, on a
// goroutine of their own: a handler may take its time and may issue
// operations on the same session.  The context is cancelled when the session
// ends.  While a handler runs, later notifications wait in a queue, see
// WithNotifQueue.
type NotifHandler func(ctx context.Context, msg *Message)

// ErrNoNotifHandler is what subscribing to notifications fails with on a
// session that was opened without WithNotifHandler: there is nowhere to
// deliver them.
var ErrNoNotifHandler = errors.New("netconf: the session has no notification handler, see WithNotifHandler")

// ErrNotificationOverflow ends a session whose notification handler does not
// keep up with the notifications the server sends: more were waiting for it
// than the queue may hold.  See WithNotifQueue.
var ErrNotificationOverflow = errors.New("netconf: notification handler cannot keep up")

// The defaults for the notifications waiting for the handler.
const (
	DefaultNotifQueueMessages = 10000
	DefaultNotifQueueBytes    = 64 << 20
)

// NotifQueue bounds the notifications that wait while the handler is busy.
// The queue is full when either bound is reached.
type NotifQueue struct {
	// Messages is how many notifications may wait.  Zero means
	// DefaultNotifQueueMessages.
	Messages int

	// Bytes is how much they may take up together.  Zero means
	// DefaultNotifQueueBytes.  A single notification is let in whatever its
	// size, which WithMaxMessageSize bounds.
	Bytes int64

	// Drop says what happens to a notification that does not fit.  By
	// default the session ends with ErrNotificationOverflow: the receiver
	// has lost events and gets to know.  With Drop the notification is
	// thrown away, the loss is logged and the session carries on.
	Drop bool
}

// notifier is the queue between the receive loop, which must never wait for
// a handler, and the handler.
type notifier struct {
	handler NotifHandler
	limit   NotifQueue
	logger  *slog.Logger

	// ctx is the handler's context, cancelled when the session ends
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	queue   []*Message
	head    int // the next message to hand over
	bytes   int64
	dropped int
	// stopped says that nothing is handed over any more, see stop
	stopped bool

	// wake tells the dispatcher that the queue is no longer empty
	wake chan struct{}

	// trace is told of every notification and whether there was room for
	// it, nil for nobody
	trace func(size int, dropped bool)
}

// report tells the trace of a notification.
func (n *notifier) report(size int64, dropped bool) {
	if n.trace != nil {
		n.trace(int(size), dropped)
	}
}

func newNotifier(handler NotifHandler, limit NotifQueue, logger *slog.Logger) *notifier {
	if limit.Messages <= 0 {
		limit.Messages = DefaultNotifQueueMessages
	}
	if limit.Bytes <= 0 {
		limit.Bytes = DefaultNotifQueueBytes
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &notifier{
		handler: handler,
		limit:   limit,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
	}
}

// deliver queues msg for the handler without waiting for it.  It fails when
// the queue is full and the session is to end over that.
func (n *notifier) deliver(msg *Message) error {
	size := int64(len(msg.body))

	n.mu.Lock()
	if n.stopped {
		n.mu.Unlock()
		n.report(size, true)
		return nil
	}
	waiting := len(n.queue) - n.head
	if waiting >= n.limit.Messages || (waiting > 0 && n.bytes+size > n.limit.Bytes) {
		if !n.limit.Drop {
			n.mu.Unlock()
			n.report(size, true)
			return ErrNotificationOverflow
		}
		n.dropped++
		dropped := n.dropped
		n.mu.Unlock()
		n.report(size, true)

		// say so when it starts, and after that ever less often
		if dropped&(dropped-1) == 0 {
			n.logger.Warn("dropping notifications, the handler does not keep up", "dropped", dropped)
		}
		return nil
	}
	n.queue = append(n.queue, msg)
	n.bytes += size
	n.mu.Unlock()
	n.report(size, false)

	select {
	case n.wake <- struct{}{}:
	default:
	}
	return nil
}

// next takes the oldest waiting message off the queue.
func (n *notifier) next() *Message {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.head == len(n.queue) {
		return nil
	}
	msg := n.queue[n.head]
	n.queue[n.head] = nil
	n.head++
	n.bytes -= int64(len(msg.body))

	// Move what waits to the front once it is no more than what was taken
	// from in front of it.  The queue then takes the room of what waits,
	// not of everything that went through it since it was last empty.
	if waiting := len(n.queue) - n.head; n.head >= waiting {
		copy(n.queue, n.queue[n.head:])
		clear(n.queue[waiting:])
		n.queue, n.head = n.queue[:waiting], 0
	}
	return msg
}

// run hands the queued notifications to the handler until the session ends.
// What is still waiting then is dropped.
func (n *notifier) run() {
	for {
		msg := n.next()
		if msg == nil {
			select {
			case <-n.wake:
				continue
			case <-n.ctx.Done():
				return
			}
		}
		if n.ctx.Err() != nil {
			return
		}
		n.handler(n.ctx, msg)
	}
}

// stop ends the dispatch and lets go of what waits.  A handler that is
// running is told through its context and not waited for.  A notification
// that arrives afterwards is thrown away, which is no overflow.
func (n *notifier) stop() {
	if n == nil {
		return
	}
	n.cancel()

	n.mu.Lock()
	defer n.mu.Unlock()
	n.stopped = true
	n.queue, n.head, n.bytes = nil, 0, 0
}
