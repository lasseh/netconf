package netconf

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

// endless never reaches the end of its message.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

type frame struct {
	io.Reader
	closed bool
}

func (f *frame) Close() error {
	f.closed = true
	return nil
}

func bounded(r io.Reader, limit int64) (*boundedReader, *frame, *int) {
	f := &frame{Reader: r}
	transportCloses := new(int)
	b := &boundedReader{r: f, left: -1, closeTransport: func() error {
		*transportCloses++
		return nil
	}}
	b.setLimit(limit)
	return b, f, transportCloses
}

func TestBoundedReader(t *testing.T) {
	t.Run("message of exactly the limit", func(t *testing.T) {
		b, f, closes := bounded(strings.NewReader("12345"), 5)
		got, err := io.ReadAll(b)
		be.NilErr(t, err)
		be.Equal(t, "12345", string(got))
		be.NilErr(t, b.Close())
		be.True(t, f.closed)
		be.Equal(t, 0, *closes)
	})

	t.Run("message over the limit", func(t *testing.T) {
		b, f, closes := bounded(strings.NewReader("123456"), 5)
		_, err := io.ReadAll(b)
		be.True(t, errors.Is(err, ErrMessageTooLarge))

		// the rest is not drained; the transport is closed instead
		be.True(t, errors.Is(b.Close(), ErrMessageTooLarge))
		be.True(t, f.closed)
		be.Equal(t, 1, *closes)
	})

	t.Run("no limit", func(t *testing.T) {
		b, _, closes := bounded(strings.NewReader("123456"), -1)
		got, err := io.ReadAll(b)
		be.NilErr(t, err)
		be.Equal(t, "123456", string(got))
		be.NilErr(t, b.Close())
		be.Equal(t, 0, *closes)
	})

	t.Run("unread rest within the limit is drained", func(t *testing.T) {
		src := strings.NewReader("1234567890")
		b, _, closes := bounded(src, 64)
		_, err := io.ReadFull(b, make([]byte, 3))
		be.NilErr(t, err)
		be.NilErr(t, b.Close())
		be.Equal(t, 0, src.Len())
		be.Equal(t, 0, *closes)
	})

	t.Run("draining an endless message stops at the limit", func(t *testing.T) {
		b, f, closes := bounded(endless{}, 1<<16)
		_, err := io.ReadFull(b, make([]byte, 3))
		be.NilErr(t, err)
		be.True(t, errors.Is(b.Close(), ErrMessageTooLarge))
		be.True(t, f.closed)
		be.Equal(t, 1, *closes)
	})

	t.Run("limit counts what was read before it was set", func(t *testing.T) {
		b, _, _ := bounded(strings.NewReader("1234567890"), -1)
		_, err := io.ReadFull(b, make([]byte, 6))
		be.NilErr(t, err)
		b.setLimit(8)
		_, err = io.ReadAll(b)
		be.True(t, errors.Is(err, ErrMessageTooLarge))
	})
}

// fed is a message that arrives a piece at a time, when the test sends one.
type fed chan string

func (f fed) Read(p []byte) (int, error) {
	piece, ok := <-f
	if !ok {
		return 0, io.EOF
	}
	return copy(p, piece), nil
}

// The watch of WithMessageTimeout settles once whether a message is given up
// as stalled.  One that is done with first is never given up, and one that
// is given up first is not taken for a message that arrived in time.
func TestBoundedReaderStall(t *testing.T) {
	// watched is the reader of a message that the test feeds, with what
	// tells that the message was given up
	watched := func(quiet time.Duration) (*boundedReader, fed, chan struct{}) {
		message, gaveUp := make(fed), make(chan struct{}, 1)
		b, _, _ := bounded(message, -1)
		b.quiet, b.stalled = quiet, func() { gaveUp <- struct{}{} }
		return b, message, gaveUp
	}
	// read has piece arrive and reads it
	read := func(t *testing.T, b *boundedReader, message fed, piece string) {
		t.Helper()
		go func() { message <- piece }()
		buf := make([]byte, 64)
		n, err := b.Read(buf)
		be.NilErr(t, err)
		be.Equal(t, piece, string(buf[:n]))
	}
	given := func(gaveUp chan struct{}) bool {
		select {
		case <-gaveUp:
			return true
		default:
			return false
		}
	}
	awaitGivenUp := func(t *testing.T, gaveUp chan struct{}) {
		t.Helper()
		select {
		case <-gaveUp:
		case <-time.After(5 * time.Second):
			t.Fatal("the message was not given up")
		}
	}

	t.Run("done with before the time is up", func(t *testing.T) {
		b, message, gaveUp := watched(time.Hour)
		read(t, b, message, "<rpc-reply/>")
		be.False(t, b.untimed())

		// a timer that fired as the message ended finds nothing to give up
		b.silent()
		be.False(t, given(gaveUp))
		be.False(t, b.untimed())
	})

	// A timer that has fired is not stopped by the byte that arrives as it
	// does.  The message has moved on all the same.
	t.Run("a byte arrives as the time is up", func(t *testing.T) {
		b, message, gaveUp := watched(time.Hour)
		read(t, b, message, "<rpc-reply>")

		b.silent()
		be.False(t, given(gaveUp))
		be.False(t, b.untimed())
	})

	// The rest may still arrive before the transport is closed.  The
	// message is one that was given up all the same.
	t.Run("given up before the rest arrives", func(t *testing.T) {
		b, message, gaveUp := watched(10 * time.Millisecond)
		read(t, b, message, "<rpc-reply>")
		awaitGivenUp(t, gaveUp)

		read(t, b, message, "</rpc-reply>")
		be.True(t, b.untimed())

		// and given up once
		b.silent()
		be.False(t, given(gaveUp))
	})

	t.Run("white space has not begun a message", func(t *testing.T) {
		b, message, gaveUp := watched(10 * time.Millisecond)
		read(t, b, message, "\n \r\n\t")
		be.True(t, b.timer == nil)

		read(t, b, message, "<")
		awaitGivenUp(t, gaveUp)
	})

	// for a message that was handed out and has been taken back
	t.Run("timed again", func(t *testing.T) {
		b, message, gaveUp := watched(time.Hour)
		read(t, b, message, "<rpc-reply>")
		be.False(t, b.untimed())

		b.timed(10 * time.Millisecond)
		awaitGivenUp(t, gaveUp)
		be.True(t, b.untimed())
	})
}
