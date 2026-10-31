package netconf

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

// budgetSession opens a session whose handler stays busy, on a device that says
// hello and then sends notification until the queue drops one.
func budgetSession(t *testing.T, hello, notification string, budget int64, opts ...SessionOption) *Session {
	t.Helper()

	wire, dev := netconftest.NewWire(t)
	go func() {
		_, _ = dev.Recv()
		_ = dev.WriteRaw(hello)
		flood(dev, notification+"]]>]]>")
	}()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	handler := func(context.Context, *Message) { <-release }

	opts = append(opts, WithNotifHandler(handler), WithNotifQueue(NotifQueue{Bytes: budget, Drop: true}), WithLogger(nil))
	sess, err := Open(context.Background(), wire, opts...)
	be.NilErr(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sess.Close(ctx)
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sess.notif.mu.Lock()
		dropped := sess.notif.dropped
		sess.notif.mu.Unlock()
		if dropped > 0 {
			return sess
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the queue never filled up")
	return nil
}

func notificationOf(attrs, body string) string {
	return `<notification xmlns="` + NotificationNamespace + `"` + attrs + `>` +
		`<eventTime>2026-10-31T12:00:00Z</eventTime><x>` + body + `</x></notification>`
}

// What waits holds what the budget says, also when a repair or the joining of
// byte references made a body shorter than the buffer it was read into.
func TestNotificationBudgetCountsWhatABodyHolds(t *testing.T) {
	const budget = 1 << 20

	for _, tc := range []struct {
		name  string
		hello string
		body  string
		opts  []SessionOption
	}{
		{name: "a plain session", hello: wireHello, body: strings.Repeat("x", 8192)},
		{name: "a Junos hello", hello: junosHello, body: strings.Repeat("&#195;&#165;", 4096)},
		{
			name: "a repair", hello: wireHello, body: strings.Repeat("x\x01\x01\x01\x01\x01\x01\x01", 1024),
			opts: []SessionOption{WithReplyRepair(StripInvalidChars)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := budgetSession(t, tc.hello, notificationOf("", tc.body), budget, tc.opts...)

			sess.notif.mu.Lock()
			defer sess.notif.mu.Unlock()
			held := 0
			for _, msg := range sess.notif.queue[sess.notif.head:] {
				held += cap(msg.body)
			}
			t.Logf("held %d of a budget of %d", held, budget)
			be.True(t, held <= budget+budget/4)
		})
	}
}

// Attributes on the start tag take far more memory than the text they are in.
func TestNotificationBudgetCountsAttributes(t *testing.T) {
	for _, tc := range []struct {
		attrs  int
		budget int64
	}{
		{171, 8 << 20},
		{342, 8 << 20},
		// one of these takes about 14 MiB, whatever its text says
		{200_000, 64 << 20},
	} {
		t.Run(fmt.Sprint(tc.attrs, " attributes"), func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			raw := notificationOf(strings.Repeat(` a=""`, tc.attrs), "")
			sess := budgetSession(t, wireHello, raw, tc.budget)

			runtime.GC()
			runtime.ReadMemStats(&after)
			held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			t.Logf("%d bytes held against a budget of %d", held, tc.budget)

			// and the one the handler has, which is not in the queue any more
			sess.notif.mu.Lock()
			one := queueCost(sess.notif.queue[sess.notif.head])
			sess.notif.mu.Unlock()
			be.True(t, held < tc.budget*12/10+one)
		})
	}
}

// One attribute with a long value is charged for that value.
func TestNotificationBudgetCountsALongValue(t *testing.T) {
	const budget = 8 << 20

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	raw := notificationOf(` a="`+strings.Repeat("v", maxStartSize-1024)+`"`, "")
	sess := budgetSession(t, wireHello, raw, budget)

	runtime.GC()
	runtime.ReadMemStats(&after)
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d bytes held against a budget of %d", held, budget)
	be.True(t, held < budget*12/10+int64(len(raw))*2)
	runtime.KeepAlive(sess)
}

// A notification with nothing on its start tag takes no more of the budget than
// its text.
func TestNotificationBudgetOfAPlainNotification(t *testing.T) {
	msg := &Message{body: make([]byte, 100)}
	be.Equal(t, int64(100), queueCost(msg))
}
