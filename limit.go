package netconf

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// ErrMessageTooLarge ends a session whose peer sent a message larger than
// the session accepts, see WithMaxMessageSize.
var ErrMessageTooLarge = errors.New("message exceeds the maximum message size")

// ErrMessageStalled ends a session whose peer began a message and then sent
// nothing more of it for the time the session allows, see
// WithMessageTimeout.
var ErrMessageStalled = errors.New("netconf: the device stopped in the middle of a message")

// boundedReader is one inbound message on its way from the transport to
// whoever gets it: start reads as far as its start element, then whole reads
// the rest or live hands the rest over, and Close releases the transport
// reader, once, however far that went.
//
// The message is read under a size limit.  The limit holds for everything
// taken off the transport for the message, including what Close drains when
// the message is not read to its end, so a peer cannot keep the session busy
// with a message that never ends.
//
// Once the limit is exceeded the message stream cannot be realigned without
// reading on, so the transport is closed instead.
type boundedReader struct {
	r io.ReadCloser

	// head holds what was read of the message and is not handed on yet
	head bytes.Buffer

	// limit is the size the message may have, negative for no limit.  left
	// is how many more bytes it may have right now, negative for no limit.
	limit int64
	left  int64
	read  int64

	exceeded       bool
	closed         bool
	closeTransport func() error

	// handOn makes a message read whole what is handed on
	handOn func([]byte) []byte

	// stalled is called when the message is given up, see quiet, with what
	// the session ends with.  timeout is the quiet a message is allowed.
	stalled func(error)
	timeout time.Duration

	// mu guards the watch for a message that stops arriving, which the
	// reader of the message and the timer both look at: what follows
	mu sync.Mutex
	// quiet is how long the message may go without a byte once it has
	// begun, zero for as long as it takes.  When it has, the message is
	// given up.
	quiet time.Duration
	// begun says that the message has begun: a byte of it has arrived that
	// is not white space
	begun bool
	// last is when the last byte arrived, and timer runs from then
	last  time.Time
	timer *time.Timer
	// gaveUp says that the message was given up as stalled
	gaveUp bool
}

// arrived notes that the message has moved on by p.
//
// White space in front of a message is not the message.  A device may write
// some behind the end of each one, as Junos does with a line break, and to
// the framing that belongs to the next message, which may be hours away.
func (b *boundedReader) arrived(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.quiet <= 0 {
		return
	}
	if !b.begun {
		if len(bytes.TrimLeft(p, " \t\r\n")) == 0 {
			return
		}
		b.begun = true
	}
	b.watch()
}

// watch has the timer run from now.  The caller holds mu.
func (b *boundedReader) watch() {
	b.last = time.Now()
	if b.timer == nil {
		b.timer = time.AfterFunc(b.quiet, b.silent)
		return
	}
	b.timer.Reset(b.quiet)
}

// silent is what the timer runs: it gives the message up, unless the message
// is done with or has moved on.  A timer that has fired is not stopped by
// the byte that arrives as it does, so the time of that byte decides, and
// whether the message is given up is settled here and in untimed, under mu.
func (b *boundedReader) silent() {
	b.mu.Lock()
	if b.quiet <= 0 || b.gaveUp {
		b.mu.Unlock()
		return
	}
	if left := b.quiet - time.Since(b.last); left > 0 {
		b.timer.Reset(left)
		b.mu.Unlock()
		return
	}
	b.gaveUp = true
	b.mu.Unlock()

	b.stalled(b.errStalled())
}

// errStalled is what a session ends with over a message that stopped
// arriving.
func (b *boundedReader) errStalled() error {
	return fmt.Errorf("%w: no byte of it for %s", ErrMessageStalled, b.timeout)
}

// timed begins the watch for a message that stops arriving, from now.  It
// is for a message that was handed out untimed and has been taken back.
func (b *boundedReader) timed() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.quiet = b.timeout; b.quiet > 0 {
		b.watch()
	}
}

// untimed ends the watch for a message that stops arriving: for a message
// that is done with, and for one whose reader is handed to somebody who
// reads at a pace of their own.  It reports whether the message was given
// up before that.  The session is ending over it then, and the message is
// not to be handed on as if it had arrived in time.
func (b *boundedReader) untimed() (gaveUp bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.quiet = 0
	if b.timer != nil {
		b.timer.Stop()
	}
	return b.gaveUp
}

// setLimit sets the total size the message may have, counting what was read
// already.  A negative total removes the limit.
func (b *boundedReader) setLimit(total int64) {
	if total < 0 {
		b.left = -1
		return
	}
	b.left = max(total-b.read, 0)
}

// start reads the message as far as its start element, keeping every byte
// the decoder consumed in head so the message can still be delivered whole.
// What comes before the start element is buffered whatever becomes of the
// message, hence the tighter limit.
func (b *boundedReader) start() (*xml.StartElement, error) {
	b.setLimit(maxStartSize)
	if b.limit >= 0 {
		b.setLimit(min(maxStartSize, b.limit))
	}
	peel := xml.NewDecoder(io.TeeReader(b, &b.head))
	// Only the start element is looked at here, and its name is ASCII
	// whatever encoding the message declares.
	peel.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	start, err := startElement(peel)
	b.setLimit(b.limit)
	return start, err
}

// whole reads the rest of the message and returns all of it the way it is
// handed on.  what names the message in the error of a read that failed.
func (b *boundedReader) whole(what string) ([]byte, error) {
	if _, err := b.head.ReadFrom(b); err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", what, err)
	}
	// all of it has arrived: what is done with it is not its silence
	if b.untimed() {
		return nil, b.errStalled()
	}
	return b.handOn(b.head.Bytes()), nil
}

// live returns the reader of the message for somebody who reads it at a pace
// of their own.  A stream is not buffered and so not limited.
func (b *boundedReader) live() (*streamReader, error) {
	b.setLimit(-1)
	if b.untimed() {
		return nil, b.errStalled()
	}
	return &streamReader{
		r:    io.MultiReader(&b.head, b),
		body: b,
		done: make(chan struct{}),
	}, nil
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.left < 0 {
		n, err := b.r.Read(p)
		b.read += int64(n)
		if n > 0 {
			b.arrived(p[:n])
		}
		return n, err
	}

	if b.left == 0 {
		// At the limit: only the end of the message may follow.
		var probe [1]byte
		if n, err := b.r.Read(probe[:]); n == 0 {
			return 0, err
		}
		b.exceeded = true
		return 0, ErrMessageTooLarge
	}

	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	b.read += int64(n)
	if n > 0 {
		b.arrived(p[:n])
	}
	return n, err
}

// Close releases the transport reader, first reading the rest of the message
// so the stream stays aligned on a message boundary.  Closing it again does
// nothing: a transport does not have to take a second Close of a reader.
func (b *boundedReader) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true

	// what is drained here is still part of the message, and is read
	// through b so that it is timed like the rest, with a limit or without
	defer b.untimed()

	if !b.exceeded {
		_, err := io.Copy(io.Discard, b)
		if err != nil && !errors.Is(err, ErrMessageTooLarge) {
			// the transport reader reports its own state on Close
			err = nil
		}
		if err != nil {
			_ = b.closeTransport()
			_ = b.r.Close()
			return err
		}
		return b.r.Close()
	}

	if b.exceeded {
		_ = b.closeTransport()
		_ = b.r.Close()
		return ErrMessageTooLarge
	}
	return b.r.Close()
}
