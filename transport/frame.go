package transport

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

// ErrStreamBusy is returned when a message reader or writer is requested
// while the previous one has not been closed.
var ErrStreamBusy = errors.New("transport: stream is already active")

// Framer is a wrapper used for transports that implement the framing defined in
// RFC6242.  This supports End-of-Message and Chunked framing methods and
// will move from End-of-Message to Chunked framing after the `Upgrade` method
// has been called.
//
// This is not a transport on it's own (missing the `Close` method) and is
// intended to be embedded into other transports.
type Framer struct {
	r io.Reader
	w io.Writer

	br *bufio.Reader
	bw *bufio.Writer

	mu           sync.Mutex
	chunkFraming bool
	activeReader bool
	activeWriter bool
}

// NewFramer return a new Framer to be used against the given io.Reader and io.Writer.
func NewFramer(r io.Reader, w io.Writer) *Framer {
	return &Framer{
		r:  r,
		w:  w,
		br: bufio.NewReader(r),
		bw: bufio.NewWriterSize(w, endBlock),
	}
}

// DebugCapture will copy all *framed* input/output to the the given
// `io.Writers` for sent or recv data.  Either sent of recv can be nil to not
// capture any data.  Useful for displaying to a screen or capturing to a file
// for debugging.
//
// This needs to be called before `MsgReader` or `MsgWriter`.
func (f *Framer) DebugCapture(input, output io.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.activeReader ||
		f.activeWriter ||
		f.bw.Buffered() > 0 ||
		f.br.Buffered() > 0 {
		panic("debug capture added with active reader or writer")
	}

	if input != nil {
		f.br = bufio.NewReader(io.TeeReader(f.r, input))
	}

	if output != nil {
		f.bw = bufio.NewWriterSize(io.MultiWriter(f.w, output), endBlock)
	}
}

// Upgrade will cause the Framer to switch from End-of-Message framing to
// Chunked framing.  This is usually called after netconf exchanged the hello
// messages.
func (f *Framer) Upgrade() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunkFraming = true
}

func (f *Framer) closeReader() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeReader = false
}

func (f *Framer) closeWriter() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeWriter = false
}

// MsgReader returns a reader for the next message, in the framing in effect
// when it is called.  The reader reports io.EOF at the end of the message and
// must be closed before the next one can be read; closing it early skips the
// rest of the message.
func (f *Framer) MsgReader() (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.activeReader {
		return nil, ErrStreamBusy
	}
	f.activeReader = true

	if f.chunkFraming {
		return &chunkedReader{
			r: f.br,
			f: f,
		}, nil
	}
	return &markedReader{
		r: f.br,
		f: f,
	}, nil
}

// MsgWriter returns a writer for one message, in the framing in effect when
// it is called.  Closing the writer ends the message and flushes it.  With
// end-of-message framing nothing is sent before Close, and a message that
// contains the delimiter is refused with ErrEndOfMsgInPayload.
func (f *Framer) MsgWriter() (io.WriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.activeWriter {
		return nil, ErrStreamBusy
	}
	f.activeWriter = true

	if f.chunkFraming {
		return &chunkedWriter{
			w: f.bw,
			f: f,
		}, nil
	}
	return &markedWriter{
		w: f.bw,
		f: f,
	}, nil
}

var endOfChunks = []byte("\n##\n")

// writeChunkSize is the size of the largest chunk that is written.  RFC 6242
// allows a chunk of any size, and a message could go out as one.  Not every
// device takes that: Junos 26.2 ends the session over a chunk of 9800
// bytes, where it takes one of 9500.  A chunk header is a few bytes, so
// small chunks cost next to nothing.
const writeChunkSize = 4096

// ErrMalformedChunk represents a message that invalid as defined in the chunk
// framing in RFC6242.
var ErrMalformedChunk = errors.New("netconf: invalid chunk")

type chunkedReader struct {
	f         *Framer
	r         *bufio.Reader
	chunkLeft uint32

	// err is how the message ended: io.EOF at its end, or what went wrong.
	// Either way it is final.  After bad framing nobody knows where the
	// next message starts, so reading on, which could wait for ever on a
	// peer that expects an answer, is not an option.
	err error
}

func (r *chunkedReader) readHeader() (uint32, error) {
	// A header is "\n#" and then a size, or "\n##\n" at the end of the
	// message.  Look at no more bytes than it takes to tell: a peer that
	// sent something else may be waiting for an answer, not sending more.
	var marker []byte
	for n, want := range []byte("\n#") {
		var err error
		if marker, err = r.r.Peek(n + 1); err != nil {
			return 0, unexpectedEOF(err)
		}
		if marker[n] != want {
			return 0, ErrMalformedChunk
		}
	}
	marker, err := r.r.Peek(3)
	if err != nil {
		return 0, unexpectedEOF(err)
	}

	if marker[2] == '#' {
		marker, err = r.r.Peek(4)
		if err != nil {
			return 0, unexpectedEOF(err)
		}
		if marker[3] != '\n' {
			return 0, ErrMalformedChunk
		}

		// Discard the end-of-chunks marker
		if _, err := r.r.Discard(4); err != nil {
			return 0, err
		}

		return 0, nil // Signal end of message with 0 chunk size
	}
	if marker[2] < '1' || marker[2] > '9' {
		return 0, ErrMalformedChunk
	}

	// Discard the "\n#" preamble
	if _, err := r.r.Discard(2); err != nil {
		return 0, err
	}

	line, err := r.r.ReadSlice('\n')
	if err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		// A size line larger than the read buffer is hostile, not a
		// transient bufio condition; treat it as a malformed chunk.
		if errors.Is(err, bufio.ErrBufferFull) {
			return 0, ErrMalformedChunk
		}
		return 0, err
	}

	// Cut off the '\n' from the end for parsing
	digits := line[:len(line)-1]

	// If the line was just "\n", digits is empty (chunks must have size)
	if len(digits) == 0 {
		return 0, ErrMalformedChunk
	}

	// RFC6242 ABNF: chunk-size = pos-digit *DIGIT. Leading zeros (and a bare
	// "0") are not allowed.
	if digits[0] == '0' {
		return 0, ErrMalformedChunk
	}

	var chunkSize uint32
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, ErrMalformedChunk
		}

		if chunkSize > math.MaxUint32/10 {
			return 0, ErrMalformedChunk
		}
		chunkSize *= 10

		digit := uint32(c - '0')
		if chunkSize > math.MaxUint32-digit {
			return 0, ErrMalformedChunk
		}
		chunkSize += digit
	}

	return chunkSize, nil
}

// nextChunk makes sure there is chunk data left to consume, reading the next
// chunk header when the current chunk is used up.  It returns io.EOF at the
// end of the message, an error when the message is broken, and keeps doing
// so.
func (r *chunkedReader) nextChunk() error {
	if r.err != nil {
		return r.err
	}
	if r.chunkLeft > 0 {
		return nil
	}

	n, err := r.readHeader()
	if err != nil {
		r.err = err
		return err
	}
	// Zero chunk size indicates "End of Chunks" (\n##\n)
	if n == 0 {
		r.err = io.EOF
		return io.EOF
	}
	r.chunkLeft = n
	return nil
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.r == nil {
		return 0, ErrInvalidIO
	}

	if err := r.nextChunk(); err != nil {
		return 0, err
	}

	// chunkLeft may exceed what an int holds on 32-bit platforms
	toRead := len(p)
	if uint64(toRead) > uint64(r.chunkLeft) {
		toRead = int(r.chunkLeft)
	}
	n, err := r.r.Read(p[:toRead])
	r.chunkLeft -= uint32(n)

	// EOF mid chunk-body means the chunk was truncated, not a clean end.
	if err != nil {
		r.err = unexpectedEOF(err)
	}
	return n, r.err
}

func (r *chunkedReader) Close() error {
	if r.r == nil {
		return nil
	}
	defer func() {
		r.r = nil
		r.f.closeReader()
	}()

	// Drain the rest of the chunks until we hit the end-of-chunks marker
	for {
		if err := r.nextChunk(); err != nil {
			if err == io.EOF { //nolint:errorlint // set by nextChunk itself
				return nil
			}
			return err
		}

		// Protect against int overflow on 32-bit systems
		toDiscard := math.MaxInt
		if uint64(r.chunkLeft) < uint64(math.MaxInt) {
			toDiscard = int(r.chunkLeft)
		}
		n, err := r.r.Discard(toDiscard)
		r.chunkLeft -= uint32(n)
		if err != nil {
			// EOF while draining a chunk body means it was truncated.
			r.err = unexpectedEOF(err)
			return r.err
		}
	}
}

// chunkedWriter writes a message in chunks.  It keeps the end of what it
// was given back until more comes or the message is closed, as how the
// last chunks are cut is decided at the end.
type chunkedWriter struct {
	f *Framer
	w *bufio.Writer
	// sent is how many bytes the message has taken on the wire so far
	sent int
	// held is the end of the message so far, not written yet: less than two
	// chunks of it
	held []byte
}

func (w *chunkedWriter) Write(p []byte) (int, error) {
	if w.w == nil {
		return 0, ErrInvalidIO
	}

	written := 0
	for len(p) > 0 {
		n := min(2*writeChunkSize-len(w.held), len(p))
		w.held = append(w.held, p[:n]...)
		p = p[n:]
		written += n

		// two chunks are held: the first can go
		if len(w.held) == 2*writeChunkSize {
			if err := w.chunk(w.held[:writeChunkSize]); err != nil {
				return written, err
			}
			w.held = append(w.held[:0], w.held[writeChunkSize:]...)
		}
	}
	return written, nil
}

// chunk writes data as one chunk.
func (w *chunkedWriter) chunk(data []byte) error {
	header, err := fmt.Fprintf(w.w, "\n#%d\n", len(data))
	w.sent += header
	if err != nil {
		return err
	}
	// Note: we are not checking for a partial writes as bufio.Writer will
	// only return a short write if the underlying writer returns an error.
	n, err := w.w.Write(data)
	w.sent += n
	return err
}

func (w *chunkedWriter) Close() error {
	if w.w == nil {
		return nil
	}
	defer func() {
		w.w = nil
		w.f.closeWriter()
	}()

	// what is held goes out as a last chunk of writeChunkSize at most, with
	// a smaller one in front of it for the rest
	if rest := len(w.held) - writeChunkSize; rest > 0 {
		if err := w.chunk(w.held[:rest]); err != nil {
			return err
		}
		w.held = w.held[rest:]
	}

	// The end of the chunks is kept in one block of the message.  A device
	// reads in blocks, and a write of ours ends where the buffer is full:
	// both fall on a multiple of endBlock bytes into the message.  Junos
	// 26.2 does not find an end that such a place cuts in two, and ends the
	// session or waits for good.  The last byte of the message in a chunk
	// of its own moves the end past that place, by the header of that
	// chunk, and the message is what it was.
	if last := len(w.held); last > 0 {
		end := w.sent + len(fmt.Sprintf("\n#%d\n", last)) + last
		cut := last
		if end%endBlock > endBlock-len(endOfChunks) && last > 1 {
			cut = last - 1
		}
		if err := w.chunk(w.held[:cut]); err != nil {
			return err
		}
		if cut < last {
			if err := w.chunk(w.held[cut:]); err != nil {
				return err
			}
		}
	}

	// write the end-of-chunks marker
	if _, err := w.w.Write(endOfChunks); err != nil {
		return err
	}
	return w.w.Flush()
}

// endBlock is the size of the blocks that the end of the chunks is kept
// whole in.  It is the size of the write buffer of a Framer, so that a
// write to the transport ends on a multiple of it, and the read buffer of
// Junos is a multiple of it.
const endBlock = 4096

const endOfMsgStr = "]]>]]>"

var endOfMsg = []byte(endOfMsgStr)

// ErrEndOfMsgInPayload is returned when closing an end-of-message framed
// writer whose payload contains the delimiter, which would corrupt the frame.
// Nothing of the message was sent.
var ErrEndOfMsgInPayload = errors.New("netconf: message body contains end-of-message delimiter")

type markedReader struct {
	f   *Framer
	r   *bufio.Reader
	eof bool
}

func (r *markedReader) Read(p []byte) (int, error) {
	if r.r == nil {
		return 0, ErrInvalidIO
	}

	if r.eof {
		return 0, io.EOF
	}

	if len(p) == 0 {
		return 0, nil
	}

	// Wait for data, then work on whatever is buffered: everything up to
	// the first ']' is certainly payload and can be handed out in one go.
	if _, err := r.r.Peek(1); err != nil {
		return 0, unexpectedEOF(err)
	}
	buf, _ := r.r.Peek(r.r.Buffered())

	var n int
	switch i := bytes.IndexByte(buf, endOfMsg[0]); {
	case i < 0:
		n = copy(p, buf)
	case i > 0:
		n = copy(p, buf[:i])
	default:
		// A ']' is either payload or the start of the delimiter.  A message
		// always ends in the delimiter, so that many bytes must follow.
		head, err := r.r.Peek(len(endOfMsg))
		if bytes.Equal(head, endOfMsg) {
			if _, err := r.r.Discard(len(endOfMsg)); err != nil {
				return 0, err
			}
			r.eof = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, unexpectedEOF(err)
		}
		p[0] = endOfMsg[0]
		n = 1
	}

	if _, err := r.r.Discard(n); err != nil {
		return 0, err
	}
	return n, nil
}

// unexpectedEOF maps the stream ending inside a message to
// io.ErrUnexpectedEOF; a message only ends at its delimiter.
func unexpectedEOF(err error) error {
	if err == io.EOF { //nolint:errorlint // bufio returns io.EOF unwrapped
		return io.ErrUnexpectedEOF
	}
	return err
}

func (r *markedReader) Close() error {
	if r.r == nil {
		return nil
	}
	defer func() {
		r.r = nil
		r.f.closeReader()
	}()

	// If we have already read to the end, nothing to do
	if r.eof {
		return nil
	}

	// Drain the rest of the message
	var scratch [512]byte
	for {
		if _, err := r.Read(scratch[:]); err != nil {
			if err == io.EOF { //nolint:errorlint // set by Read itself
				return nil
			}
			return err
		}
	}
}

// markedWriter collects a message and sends it on Close.  The whole payload
// has to be checked for the end-of-message delimiter before any of it goes
// out, since a message that contains it cannot be taken back.
type markedWriter struct {
	f *Framer
	w *bufio.Writer

	buf bytes.Buffer
}

func (w *markedWriter) Write(p []byte) (int, error) {
	if w.w == nil {
		return 0, ErrInvalidIO
	}
	return w.buf.Write(p)
}

// Close sends the message.  A payload the framing cannot carry is not sent
// at all and reported as ErrEndOfMsgInPayload.
func (w *markedWriter) Close() error {
	if w.w == nil {
		return nil
	}
	defer func() {
		w.w = nil
		w.f.closeWriter()
	}()

	// The delimiter must only appear where we put it: at the very end.  It
	// would appear earlier if the payload held it, or ended in its first
	// half: "]]>" followed by "]]>]]>" reads as a message ending three
	// bytes too soon.
	payload := w.buf.Bytes()
	if bytes.Contains(payload, endOfMsg) || bytes.HasSuffix(payload, endOfMsg[:len(endOfMsg)/2]) {
		return ErrEndOfMsgInPayload
	}

	if _, err := w.w.Write(payload); err != nil {
		return err
	}
	if _, err := w.w.Write(endOfMsg); err != nil {
		return err
	}
	return w.w.Flush()
}
