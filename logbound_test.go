package netconf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/transport"
)

// logged is a log to look into.
type logged struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logged) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitFor returns the line of the log that holds what, once there is one.
func (l *logged) waitFor(t *testing.T, what string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.Lines(l.String()) {
			if strings.Contains(line, what) {
				return line
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no %q in the log: %s", what, l.String())
	return ""
}

// A frame costs at most maxLogValue bytes of device text in a log record, whichever
// record it is.
func TestLogRecordOfAMalformedMessageIsBounded(t *testing.T) {
	long := strings.Repeat("n", 1<<18)
	for _, tc := range []struct{ name, frame, record string }{
		{"an end tag with a long name", "</" + long + ">", "skipping malformed message"},
		// this one ends the session
		{"an XML declaration with a long version", `<?xml version="` + long + `"?><a/>`, "session closed unexpectedly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, dev := netconftest.NewWire(t)
			go func() {
				_, _ = dev.Recv()
				_ = dev.WriteRaw(wireHello)
				_ = dev.WriteRaw(tc.frame + "]]>]]>")
			}()

			log := &logged{}
			sess, err := Open(context.Background(), wire, WithLogger(slog.New(slog.NewTextHandler(log, nil))))
			be.NilErr(t, err)
			t.Cleanup(func() { _ = sess.Close(testCtx(t)) })

			line := log.waitFor(t, tc.record)
			t.Logf("the record is %d bytes", len(line))
			be.True(t, len(line) < maxLogValue+256)
		})
	}
}

// failingTransport has reads that fail with err.
type failingTransport struct {
	transport.Transport
	err error
}

func (f *failingTransport) MsgReader() (io.ReadCloser, error) { return nil, f.err }

// The record of a session that ended holds what the others hold, and Err all of it.
func TestLogRecordOfASessionThatEndedIsBounded(t *testing.T) {
	long := errors.New(strings.Repeat("e", 1<<18))
	tt := netconftest.NewTransport(t, echoHandler)
	ft := &failingTransport{Transport: tt}

	log := &logged{}
	sess := newSession(ft, WithLogger(slog.New(slog.NewTextHandler(log, nil))))
	ft.err = long
	go sess.recvLoop()

	waitDone(t, sess)
	line := log.waitFor(t, "session closed unexpectedly")
	t.Logf("the record is %d bytes", len(line))
	be.True(t, len(line) < maxLogValue+256)
	be.True(t, errors.Is(sess.Err(), long))
}

func TestLogValue(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "plain", "plain"},
		{"a control character", "a\x1b[31mb", "a[31mb"},
		{"a control character of the upper range", "a\u0085b\u009fc", "a bc"},
		{"line ends", "a\nb\r\nc\td", "a b c d"},
		{"letters stay", "Tromsø", "Tromsø"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be.Equal(t, tc.want, logValue(tc.in))
		})
	}

	long := logValue(strings.Repeat("x", maxLogValue+1))
	be.Equal(t, strings.Repeat("x", maxLogValue)+"...", long)

	// cut in the middle of a letter
	cut := logValue(strings.Repeat("x", maxLogValue-1) + "øø")
	be.True(t, strings.ToValidUTF8(cut, "") == cut)
}
