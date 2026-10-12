package netconf

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

// traced records what a session reports to its trace.
type traced struct {
	mu     sync.Mutex
	events []string
	starts []OperationInfo
	dones  []OperationResult
	// what OperationDone was told about the operation
	doneOps []OperationInfo
	notifs  []string
}

func (r *traced) trace() *Trace {
	return &Trace{
		OperationStart: func(op OperationInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, "start "+op.Name)
			r.starts = append(r.starts, op)
		},
		OperationDone: func(op OperationInfo, result OperationResult) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, "done "+op.Name)
			r.doneOps = append(r.doneOps, op)
			r.dones = append(r.dones, result)
		},
		Notification: func(size int, dropped bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if dropped {
				r.notifs = append(r.notifs, "dropped")
			} else {
				r.notifs = append(r.notifs, "delivered")
			}
			if size == 0 {
				r.notifs = append(r.notifs, "without a size")
			}
		},
	}
}

func (r *traced) snapshot() (events []string, starts []OperationInfo, dones []OperationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...), append([]OperationInfo(nil), r.starts...), append([]OperationResult(nil), r.dones...)
}

// Every operation is reported as it starts and as it ends, once each and in
// that order, whichever way it was issued and whatever became of it.
func TestTraceOperations(t *testing.T) {
	const refused = `<rpc-error><error-type>protocol</error-type><error-tag>lock-denied</error-tag><error-severity>error</error-severity></rpc-error>`
	respond := func(msgID, req string) string {
		switch {
		case strings.Contains(req, "<slow/>"):
			return framed("no-such-id", "<ok/>")
		case strings.Contains(req, "<lock"):
			return framed(msgID, refused)
		case strings.Contains(req, "<die/>"):
			return ""
		}
		return framed(msgID, "<data>x</data>")
	}

	t.Run("exec, exec reply and stream", func(t *testing.T) {
		var rec traced
		sess := wireSession(t, respond, WithTrace(rec.trace()))
		before := time.Now()

		var data struct {
			Data string `xml:"data"`
		}
		be.NilErr(t, sess.Exec(testCtx(t), "<get/>", &data))
		reply, err := sess.ExecReply(testCtx(t), "<get-config><source><running/></source></get-config>")
		be.NilErr(t, err)
		msg, err := sess.Stream(testCtx(t), "<get-interface-information/>")
		be.NilErr(t, err)
		_, err = io.ReadAll(msg)
		be.NilErr(t, err)
		be.NilErr(t, msg.Close())

		events, starts, dones := rec.snapshot()
		be.AllEqual(t, []string{
			"start get", "done get",
			"start get-config", "done get-config",
			"start get-interface-information", "done get-interface-information",
		}, events)

		for i, op := range starts {
			be.Equal(t, uint64(7), op.SessionID)
			be.DeepEqual(t, op, rec.doneOps[i])
			be.True(t, !op.Start.Before(before) && !op.Start.After(time.Now()))
			be.True(t, dones[i].Duration >= 0)
			be.NilErr(t, dones[i].Err)
		}
		be.Equal(t, "1", starts[0].MessageID)
		be.Equal(t, "2", starts[1].MessageID)
		be.Equal(t, `<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="2"><get-config><source><running/></source></get-config></rpc>`, string(starts[1].Request))

		// the size is that of the reply, and unknown for one that is streamed
		be.Equal(t, len(reply.Raw), dones[1].ReplySize)
		be.Equal(t, len(reply.Raw), dones[0].ReplySize)
		be.Equal(t, 0, dones[2].ReplySize)
	})

	t.Run("an operation the device refuses", func(t *testing.T) {
		var rec traced
		sess := wireSession(t, respond, WithTrace(rec.trace()))

		reply, err := sess.ExecReply(testCtx(t), `<lock><target><candidate/></target></lock>`)
		be.True(t, errors.Is(err, ErrLockDenied))

		events, _, dones := rec.snapshot()
		be.AllEqual(t, []string{"start lock", "done lock"}, events)
		be.True(t, errors.Is(dones[0].Err, ErrLockDenied))
		be.Equal(t, len(reply.Raw), dones[0].ReplySize)
	})

	t.Run("an operation that times out", func(t *testing.T) {
		var rec traced
		sess := wireSession(t, respond, WithTrace(rec.trace()))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := sess.ExecReply(ctx, "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))

		events, _, dones := rec.snapshot()
		be.AllEqual(t, []string{"start slow", "done slow"}, events)
		be.True(t, errors.Is(dones[0].Err, context.DeadlineExceeded))
		be.True(t, dones[0].Duration >= 30*time.Millisecond)
		be.Equal(t, 0, dones[0].ReplySize)

		// a reply that was to be streamed and never began
		streamCtx, cancelStream := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancelStream()
		_, err = sess.Stream(streamCtx, "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		events, _, dones = rec.snapshot()
		be.AllEqual(t, []string{"start slow", "done slow", "start slow", "done slow"}, events)
		be.True(t, errors.Is(dones[1].Err, context.DeadlineExceeded))

		// an operation whose context is over already never becomes a
		// message, and is not reported
		_, err = sess.Stream(ctx, "<slow/>")
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		events, _, _ = rec.snapshot()
		be.Equal(t, 4, len(events))
	})

	t.Run("an operation cut short by the end of the session", func(t *testing.T) {
		var rec traced
		sess := wireSession(t, respond, WithTrace(rec.trace()))

		_, err := sess.ExecReply(testCtx(t), "<die/>")
		be.True(t, errors.Is(err, ErrClosed))

		events, _, dones := rec.snapshot()
		be.AllEqual(t, []string{"start die", "done die"}, events)
		be.True(t, errors.Is(dones[0].Err, ErrClosed))

		// an operation on a session that is over is one that failed
		_, err = sess.ExecReply(testCtx(t), "<get/>")
		be.True(t, errors.Is(err, ErrClosed))
		events, _, _ = rec.snapshot()
		be.Equal(t, 4, len(events))
		be.Equal(t, "done get", events[3])
	})

	t.Run("the close of the session", func(t *testing.T) {
		var rec traced
		sess := wireSession(t, respond, WithTrace(rec.trace()))
		be.NilErr(t, sess.Close(testCtx(t)))

		events, _, _ := rec.snapshot()
		be.AllEqual(t, []string{"start close-session", "done close-session"}, events)
	})

	t.Run("a function that is nil is not called", func(t *testing.T) {
		var done []string
		for _, trace := range []*Trace{
			nil,
			{},
			{OperationDone: func(op OperationInfo, _ OperationResult) { done = append(done, op.Name) }},
			{OperationStart: func(OperationInfo) {}},
		} {
			sess := wireSession(t, respond, WithTrace(trace))
			be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))
		}
		be.AllEqual(t, []string{"get"}, done)
	})
}

// The name of an operation is that of its element, however the operation
// was given.
func TestTraceOperationName(t *testing.T) {
	type lock struct {
		XMLName struct{} `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 lock"`
	}
	for want, op := range map[string]any{
		"get":                  "<get/>",
		"get-configuration":    `<get-configuration compare="rollback" rollback="0" format="text"/>`,
		"commit-configuration": []byte("\n  <commit-configuration>\n<check/></commit-configuration>"),
		"lock":                 lock{},
		"kill-session":         `<nc:kill-session xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0"><nc:session-id>7</nc:session-id></nc:kill-session>`,
		"command":              `<!-- as the CLI prints it --><?pi x?><command format="text">show version</command>`,
		"":                     "not an element",
	} {
		var rec traced
		sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, "<ok/>") }, WithTrace(rec.trace()))
		_, err := sess.ExecReply(testCtx(t), op)
		be.NilErr(t, err)

		_, starts, _ := rec.snapshot()
		be.Equal(t, 1, len(starts))
		be.Equal(t, want, starts[0].Name)
	}
}

// A notification is reported when it is queued for the handler, and when
// the queue has no room for it.
func TestTraceNotifications(t *testing.T) {
	var rec traced
	release := make(chan struct{})
	started := make(chan struct{}, 1)

	sess := notifyingSession(t, 3,
		WithTrace(rec.trace()),
		WithNotifQueue(NotifQueue{Messages: 1, Drop: true}),
		WithNotifHandler(func(ctx context.Context, _ *Message) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}),
	)
	defer close(release)
	<-started

	// an operation is answered behind the notifications
	be.NilErr(t, sess.Exec(testCtx(t), "<get/>", nil))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	// The first goes to the handler, which keeps it, and one more can wait.
	// Which of the other two finds the room depends on when the handler
	// takes the first: one of them at least finds none.
	be.Equal(t, 3, len(rec.notifs))
	be.Equal(t, "delivered", rec.notifs[0])
	be.True(t, slices.Contains(rec.notifs[1:], "dropped"))
}
