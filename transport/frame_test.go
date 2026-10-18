package transport

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/carlmjohnson/be"
)

var (
	rfcChunkedRPC = []byte(`
#4
<rpc
#18
 message-id="102"

#79
     xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <close-session/>
</rpc>
##
`)

	rfcUnchunkedRPC = []byte(`<rpc message-id="102"
     xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <close-session/>
</rpc>`)
)

func TestChunkedReader_readHeader(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantSize uint32
		wantErr  error // If nil, we expect no error
	}{
		// --- Valid Cases ---
		{
			name:     "simpleChunk",
			input:    "\n#100\n",
			wantSize: 100,
		},
		{
			name:     "singleDigit",
			input:    "\n#1\n",
			wantSize: 1,
		},
		{
			name:     "maxChunk",
			input:    "\n#4294967295\n",
			wantSize: math.MaxUint32,
		},
		{
			name:     "endOfChunks",
			input:    "\n##\n",
			wantSize: 0,
		},

		// --- Structural/Syntax Errors ---
		{
			name:    "missingLeadingNewline",
			input:   "x#100\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "missingHash",
			input:   "\n!100\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "incomplete",
			input:   "\n#1", // Less than 4 bytes
			wantErr: io.ErrUnexpectedEOF,
		},
		{
			name:    "missingTralingNewline",
			input:   "\n#123", // 4+ bytes but no ending newline
			wantErr: io.ErrUnexpectedEOF,
		},
		{
			name:  "emptySize",
			input: "\n#\n",
			// neither a size nor the end marker can follow "\n#" this way
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "zeroChunkSize",
			input:   "\n#0\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "manyZeros",
			input:   "\n#000\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "leadingZero",
			input:   "\n#0123\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "oversizedSizeLine",
			input:   "\n#" + strings.Repeat("1", 5000),
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "nonDigit",
			input:   "\n#12a3\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name:    "negativeSize",
			input:   "\n#-5\n",
			wantErr: ErrMalformedChunk,
		},

		// --- Overflow Checks ---
		{
			name: "overflowMultiplication",
			// 5000000000 is > MaxUint32
			// Fails at: chunkSize > MaxUint32/10 check
			input:   "\n#5000000000\n",
			wantErr: ErrMalformedChunk,
		},
		{
			name: "overflowAddition",
			// 4294967296 is MaxUint32 + 1
			// Passes multiplication check (429496729 < Max/10)
			// Fails at: chunkSize > Max - digit check
			input:   "\n#4294967296\n",
			wantErr: ErrMalformedChunk,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &chunkedReader{
				r: bufio.NewReader(strings.NewReader(tt.input)),
			}

			got, err := r.readHeader()
			be.True(t, errors.Is(err, tt.wantErr))
			be.Equal(t, tt.wantSize, got)
		})
	}
}

var chunkedTests = []struct {
	name        string
	input, want []byte
	err         error
}{
	{"normal",
		[]byte("\n#3\nfoo\n##\n"),
		[]byte("foo"),
		nil},
	{"empty frame",
		[]byte("\n##\n"),
		[]byte(""),
		nil},
	{"multichunk",
		[]byte("\n#3\nfoo\n#3\nbar\n##\n"),
		[]byte("foobar"),
		nil},
	{"missing header",
		[]byte("uhoh"),
		[]byte(""),
		ErrMalformedChunk},
	{"empty size",
		[]byte("\n#\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"eof in header",
		[]byte("\n#"),
		[]byte(""),
		io.ErrUnexpectedEOF},
	{"no headler",
		[]byte("\n00\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"malformed header",
		[]byte("\n#big\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"zero len chunk",
		[]byte("\n#0\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"too big chunk",
		[]byte("\n#4294967296\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"leading zeros",
		[]byte("\n#007\nabcdefg\n##\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"oversized size line",
		[]byte("\n#" + strings.Repeat("1", 5000)),
		[]byte(""),
		ErrMalformedChunk},
	{"truncated chunk body",
		[]byte("\n#10\nhello"),
		[]byte("hello"),
		io.ErrUnexpectedEOF},
	{"rfc example rpc", rfcChunkedRPC, rfcUnchunkedRPC, nil},
}

func TestChunkReaderRead(t *testing.T) {
	for _, tc := range chunkedTests {
		t.Run(tc.name, func(t *testing.T) {
			r := &chunkedReader{
				r: bufio.NewReader(bytes.NewReader(tc.input)),
			}

			got, err := io.ReadAll(r)
			be.Equal(t, tc.err, err)
			be.Equal(t, string(tc.want), string(got))

			closeErr := r.Close()
			if tc.err == nil {
				be.NilErr(t, closeErr)
			}
		})
	}
}

func TestChunkWriter(t *testing.T) {
	buf := bytes.Buffer{}
	w := &chunkedWriter{w: bufio.NewWriter(&buf)}

	n, err := w.Write([]byte("foo"))
	be.NilErr(t, err)
	be.Equal(t, 3, n)

	n, err = w.Write([]byte("quux"))
	be.NilErr(t, err)
	be.Equal(t, 4, n)

	err = w.Close()
	be.NilErr(t, err)

	// what is written in pieces goes out in chunks of the writer's making
	want := []byte("\n#7\nfooquux\n##\n")
	be.Equal(t, string(want), buf.String())
}

// A message is written in chunks no larger than writeChunkSize, however
// much of it is written at once.  RFC 6242 lets a chunk be as large as the
// message, but Junos 26.2 ends the session over a chunk of 9800 bytes; it
// takes one of 9500.
func TestChunkWriterSplits(t *testing.T) {
	msg := bytes.Repeat([]byte("0123456789"), 1000) // 10000 bytes

	var buf bytes.Buffer
	w := &chunkedWriter{w: bufio.NewWriter(&buf)}
	n, err := w.Write(msg)
	be.NilErr(t, err)
	be.Equal(t, len(msg), n)
	be.NilErr(t, w.Close())

	// the sizes of the chunks, from their headers
	var sizes []int
	wire := buf.Bytes()
	for len(wire) > 0 && !bytes.Equal(wire, endOfChunks) {
		var size int
		_, err := fmt.Sscanf(string(wire), "\n#%d\n", &size)
		be.NilErr(t, err)
		sizes = append(sizes, size)
		wire = wire[len(fmt.Sprintf("\n#%d\n", size))+size:]
	}
	be.DeepEqual(t, []int{4096, 1808, 4096}, sizes)

	// and what was written reads back
	got, err := io.ReadAll(&chunkedReader{r: bufio.NewReader(bytes.NewReader(buf.Bytes()))})
	be.NilErr(t, err)
	be.Equal(t, string(msg), string(got))
}

// writeSizes notes the size of every write it gets.
type writeSizes []int

func (w *writeSizes) Write(p []byte) (int, error) {
	*w = append(*w, len(p))
	return len(p), nil
}

// A message in chunks goes to the transport in blocks of writeBlock bytes,
// however small its chunks are: a write per chunk is a packet per chunk over
// ssh.  Every block ends on a multiple of endBlock bytes of the message,
// which is where the end of the chunks is kept clear of.
func TestChunkWriterWrites(t *testing.T) {
	var writes writeSizes
	f := NewFramer(strings.NewReader(""), &writes)
	f.Upgrade()

	const size = 1 << 20
	w, err := f.MsgWriter()
	be.NilErr(t, err)
	_, err = w.Write(make([]byte, size))
	be.NilErr(t, err)
	be.NilErr(t, w.Close())

	be.Equal(t, 0, writeBlock%endBlock)
	total := 0
	for i, n := range writes {
		total += n
		if i < len(writes)-1 {
			be.Equal(t, writeBlock, n)
		}
	}
	// the message, a header for each of its chunks and the end of them
	be.True(t, total > size)
	be.Equal(t, total/writeBlock+1, len(writes))
}

// The end of the chunks is kept clear of a multiple of 4096 bytes, counted
// from the beginning of the message.  A device reads a message in blocks,
// and Junos 26.2 does not find the end when one of its blocks ends inside
// it: it ends the session, or waits for good.  The last byte of the message
// in a chunk of its own moves the end, and the message is what it was.
func TestChunkWriterKeepsTheEndWhole(t *testing.T) {
	// the size of a message that takes wire bytes in front of its end,
	// written as one chunk: "\n#" + digits + "\n" + message
	sized := func(wire int) int { return wire - len(fmt.Sprintf("\n#%d\n", wire-7)) }

	for _, tc := range []struct {
		wire  int // in front of the end of the chunks, as one chunk
		split bool
	}{
		{4092, false}, // the end is the last four bytes of the block
		{4093, true},
		{4094, true},
		{4095, true},
		{4096, false}, // and here the first four of the next
		{100, false},
		{8191, true},
		{3 * 4096, false},
		{5*4096 - 2, true},
	} {
		t.Run(strconv.Itoa(tc.wire), func(t *testing.T) {
			msg := bytes.Repeat([]byte("x"), sized(tc.wire))
			msg[len(msg)-1] = '>'
			if len(msg) > writeChunkSize {
				// more than one chunk: the size on the wire is another, and
				// what counts is where the end falls
				msg = msg[:len(msg)-len(fmt.Sprintf("\n#%d\n", writeChunkSize))*(len(msg)/writeChunkSize)]
			}

			var buf bytes.Buffer
			w := &chunkedWriter{w: bufio.NewWriter(&buf)}
			_, err := w.Write(msg)
			be.NilErr(t, err)
			be.NilErr(t, w.Close())

			wire := buf.Bytes()
			be.True(t, bytes.HasSuffix(wire, endOfChunks))
			// no multiple of 4096 falls inside the end of the chunks
			end := len(wire) - len(endOfChunks)
			be.Equal(t, end/4096, (len(wire)-1)/4096)
			if len(msg) <= writeChunkSize {
				be.Equal(t, tc.split, bytes.HasSuffix(wire, []byte("\n#1\n"+string(msg[len(msg)-1:])+"\n##\n")))
			}

			got, err := io.ReadAll(&chunkedReader{r: bufio.NewReader(bytes.NewReader(wire))})
			be.NilErr(t, err)
			be.Equal(t, string(msg), string(got))
		})
	}

	// every size around the blocks, written in pieces of every kind
	for size := 1; size < 3*4096; size++ {
		msg := bytes.Repeat([]byte("y"), size)
		var buf bytes.Buffer
		w := &chunkedWriter{w: bufio.NewWriter(&buf)}
		for rest := msg; len(rest) > 0; {
			n := min(len(rest), 1+size%977)
			_, err := w.Write(rest[:n])
			be.NilErr(t, err)
			rest = rest[n:]
		}
		be.NilErr(t, w.Close())

		wire := buf.Bytes()
		end := len(wire) - len(endOfChunks)
		if end/4096 != (len(wire)-1)/4096 {
			t.Fatalf("a message of %d bytes: the end of the chunks falls across byte %d", size, (len(wire)-1)/4096*4096)
		}
		got, err := io.ReadAll(&chunkedReader{r: bufio.NewReader(bytes.NewReader(wire))})
		be.NilErr(t, err)
		if !bytes.Equal(msg, got) {
			t.Fatalf("a message of %d bytes does not read back", size)
		}
	}
}

var (
	rfcMarkedRPC = []byte(`
<?xml version="1.0" encoding="UTF-8"?>
<rpc message-id="105"
xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <get-config>
    <source><running/></source>
    <config xmlns="http://example.com/schema/1.2/config">
     <users/>
    </config>
  </get-config>
</rpc>
]]>]]>`)
	rfcUnframedRPC = rfcMarkedRPC[:len(rfcMarkedRPC)-6]
)

var framedTests = []struct {
	name        string
	input, want []byte
	err         error
}{
	{"normal",
		[]byte("foo]]>]]>"),
		[]byte("foo"),
		nil},
	{"empty frame",
		[]byte("]]>]]>"),
		[]byte(""),
		nil},
	{"next message",
		[]byte("foo]]>]]>bar]]>]]>"),
		[]byte("foo"), nil},
	{"no delim",
		[]byte("uhohwhathappened"),
		[]byte("uhohwhathappened"),
		io.ErrUnexpectedEOF},
	{"truncated delim",
		[]byte("foo]]>"),
		[]byte("foo"),
		io.ErrUnexpectedEOF},
	{"partial delim",
		[]byte("foo]]>]]bar]]>]]>"),
		[]byte("foo]]>]]bar"),
		nil},
	{"rfc example rpc", rfcMarkedRPC, rfcUnframedRPC, nil},
}

func TestMarkedRead(t *testing.T) {
	for _, tc := range framedTests {
		t.Run(tc.name, func(t *testing.T) {
			r := &markedReader{
				r: bufio.NewReader(bytes.NewReader(tc.input)),
			}
			got, err := io.ReadAll(r)
			be.Equal(t, err, tc.err)
			be.Equal(t, string(tc.want), string(got))

			closeErr := r.Close()
			if tc.err == nil {
				be.NilErr(t, closeErr)
			}
		})
	}
}

func TestMarkedWriter(t *testing.T) {
	buf := bytes.Buffer{}
	w := &markedWriter{w: bufio.NewWriter(&buf), f: &Framer{}}

	n, err := w.Write([]byte("foo"))
	be.NilErr(t, err)
	be.Equal(t, 3, n)

	// nothing goes out before the message is complete
	be.Equal(t, "", buf.String())

	err = w.Close()
	be.NilErr(t, err)

	want := []byte("foo]]>]]>")
	be.Equal(t, string(want), buf.String())
}

// TestChunkedReaderCloseTruncated verifies that closing a reader over a stream
// truncated mid chunk-body reports io.ErrUnexpectedEOF rather than a clean EOF.
func TestChunkedReaderCloseTruncated(t *testing.T) {
	r := &chunkedReader{
		r: bufio.NewReader(bytes.NewReader([]byte("\n#10\nhello"))),
	}
	err := r.Close()
	be.True(t, errors.Is(err, io.ErrUnexpectedEOF))
}

// TestChunkedReaderLatchesEOF verifies that once a chunked message ends, further
// Read calls keep returning io.EOF and never consume the next message.
func TestChunkedReaderLatchesEOF(t *testing.T) {
	const twoMsgs = "\n#3\nfoo\n##\n" + "\n#3\nbar\n##\n"
	f := NewFramer(strings.NewReader(twoMsgs), io.Discard)
	f.Upgrade()

	r1, err := f.MsgReader()
	be.NilErr(t, err)

	got, err := io.ReadAll(r1)
	be.NilErr(t, err)
	be.Equal(t, "foo", string(got))

	// Reads after EOF must stay at EOF without consuming the second message.
	var p [8]byte
	n, err := r1.Read(p[:])
	be.Equal(t, 0, n)
	be.True(t, errors.Is(err, io.EOF))

	be.NilErr(t, r1.Close())

	r2, err := f.MsgReader()
	be.NilErr(t, err)
	got, err = io.ReadAll(r2)
	be.NilErr(t, err)
	be.Equal(t, "bar", string(got))
	be.NilErr(t, r2.Close())
}

// A payload containing the delimiter, however it was split across writes or
// completed by the real delimiter, is refused whole: nothing reaches the peer.
func TestMarkedWriterRejectsDelimiter(t *testing.T) {
	tests := []struct {
		name    string
		writes  []string
		wantErr bool
	}{
		{"single write", []string{"foo]]>]]>bar"}, true},
		{"straddle two writes", []string{"foo]]", ">]]>bar"}, true},
		{"in a later write", []string{"<rpc>", "]]>]]>", "</rpc>"}, true},
		{"completed by the real delimiter", []string{"abc]]>"}, true},
		{"partial then continuation", []string{"foo]]", "xyz"}, false},
		{"ends in a bracket", []string{"abc]"}, false},
		{"ends in four delimiter bytes", []string{"abc]]>]"}, false},
		{"empty", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			f := NewFramer(strings.NewReader(""), &out)

			w, err := f.MsgWriter()
			be.NilErr(t, err)
			for _, s := range tt.writes {
				_, err := w.Write([]byte(s))
				be.NilErr(t, err)
			}
			err = w.Close()

			if tt.wantErr {
				be.True(t, errors.Is(err, ErrEndOfMsgInPayload))
				be.Equal(t, "", out.String())
			} else {
				be.NilErr(t, err)
				be.Equal(t, strings.Join(tt.writes, "")+endOfMsgStr, out.String())
			}

			// the framer is usable for the next message either way
			out.Reset()
			w, err = f.MsgWriter()
			be.NilErr(t, err)
			_, err = w.Write([]byte("<ok/>"))
			be.NilErr(t, err)
			be.NilErr(t, w.Close())
			be.Equal(t, "<ok/>"+endOfMsgStr, out.String())
		})
	}
}

func TestFramerStreamBusy(t *testing.T) {
	// Two empty frames so each acquired reader has one to drain on Close.
	f := NewFramer(strings.NewReader("]]>]]>]]>]]>"), &bytes.Buffer{})

	r1, err := f.MsgReader()
	be.NilErr(t, err)
	_, err = f.MsgReader()
	be.Equal(t, ErrStreamBusy, err)
	be.NilErr(t, r1.Close())

	// After close the reader can be re-acquired.
	r2, err := f.MsgReader()
	be.NilErr(t, err)
	be.NilErr(t, r2.Close())

	w1, err := f.MsgWriter()
	be.NilErr(t, err)
	_, err = f.MsgWriter()
	be.Equal(t, ErrStreamBusy, err)
	be.NilErr(t, w1.Close())

	w2, err := f.MsgWriter()
	be.NilErr(t, err)
	be.NilErr(t, w2.Close())
}

// TestFramerUpgrade round-trips a marked hello then a chunked message over the
// same Framer to verify no bytes are lost across the framing upgrade.
func TestFramerUpgrade(t *testing.T) {
	buf := &bytes.Buffer{}
	f := NewFramer(buf, buf)

	w, err := f.MsgWriter()
	be.NilErr(t, err)
	_, err = io.WriteString(w, "<hello/>")
	be.NilErr(t, err)
	be.NilErr(t, w.Close())

	r, err := f.MsgReader()
	be.NilErr(t, err)
	got, err := io.ReadAll(r)
	be.NilErr(t, err)
	be.Equal(t, "<hello/>", string(got))
	be.NilErr(t, r.Close())

	f.Upgrade()

	w, err = f.MsgWriter()
	be.NilErr(t, err)
	_, err = io.WriteString(w, "<rpc/>")
	be.NilErr(t, err)
	be.NilErr(t, w.Close())

	r, err = f.MsgReader()
	be.NilErr(t, err)
	got, err = io.ReadAll(r)
	be.NilErr(t, err)
	be.Equal(t, "<rpc/>", string(got))
	be.NilErr(t, r.Close())
}

const (
	// Chunk size used for generating synthetic data.
	// 4KB is a reasonable default for many network implementations.
	benchChunkSize = 4096
)

// generateChunkedData creates a valid chunked message of approx totalSize bytes.
func generateChunkedData(totalSize int) []byte {
	buf := bytes.Buffer{}
	// Use a recognizable pattern
	payload := []byte("0123456789abcdef")

	current := 0
	for current < totalSize {
		// Determine size for this specific chunk
		size := min(totalSize-current, benchChunkSize)

		// Write header: \n#<len>\n
		fmt.Fprintf(&buf, "\n#%d\n", size)

		// Write payload repeating the pattern
		for i := range size {
			buf.WriteByte(payload[i%len(payload)])
		}
		current += size
	}

	// Write End-of-Chunks
	buf.Write([]byte("\n##\n"))
	return buf.Bytes()
}

// generateMarkedData creates a valid end-of-message delimited message of approx
// totalSize bytes.
func generateMarkedData(totalSize int) []byte {
	buf := bytes.Buffer{}
	payload := []byte("0123456789abcdef")

	for i := range totalSize {
		buf.WriteByte(payload[i%len(payload)])
	}
	buf.Write(endOfMsg)
	return buf.Bytes()
}

func BenchmarkChunkedRead(b *testing.B) {
	benchmarks := []struct {
		name string
		size int
	}{
		{"Small_200B", 200},
		{"Medium_128KB", 128 * 1024},
		{"Large_10MB", 10 * 1024 * 1024},
	}

	// Pre-allocate a buffer for io.CopyBuffer to avoid measuring allocation overhead
	copyBuf := make([]byte, 32*1024)
	dst := io.Discard

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			data := generateChunkedData(bm.size)
			src := bytes.NewReader(data)

			br := bufio.NewReader(src)

			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				src.Reset(data)
				br.Reset(src)
				r := &chunkedReader{r: br}

				_, err := io.CopyBuffer(dst, r, copyBuf)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMarkedRead(b *testing.B) {
	benchmarks := []struct {
		name string
		size int
	}{
		{"Small_200B", 200},
		{"Medium_128KB", 128 * 1024},
		{"Large_10MB", 10 * 1024 * 1024},
	}

	copyBuf := make([]byte, 32*1024)
	dst := io.Discard

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			data := generateMarkedData(bm.size)
			src := bytes.NewReader(data)

			br := bufio.NewReader(src)

			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				src.Reset(data)
				br.Reset(src)
				r := &markedReader{r: br}

				_, err := io.CopyBuffer(dst, r, copyBuf)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestMarkedReadBoundaries checks that payload resembling the delimiter is
// passed through and that the delimiter is found no matter how the stream is
// split across reads.
func TestMarkedReadBoundaries(t *testing.T) {
	payloads := []string{
		"",
		"]",
		"]]>]]",
		"a]b]]c]]>d]]>]e]]>]]f",
		"<a>x]]></a>",
		strings.Repeat("]", 9000),
		strings.Repeat("ab]]>]", 3000),
		strings.Repeat("x", 10000),
	}

	wrappers := map[string]func(io.Reader) io.Reader{
		"whole":   func(r io.Reader) io.Reader { return r },
		"onebyte": iotest.OneByteReader,
		"half":    iotest.HalfReader,
	}

	for _, payload := range payloads {
		for name, wrap := range wrappers {
			// two messages back to back: the first must not bleed
			stream := payload + endOfMsgStr + "next" + endOfMsgStr
			br := bufio.NewReader(wrap(strings.NewReader(stream)))

			got, err := io.ReadAll(&markedReader{r: br})
			be.NilErr(t, err)
			if string(got) != payload {
				t.Fatalf("%s: payload %.20q...: got %d bytes, want %d", name, payload, len(got), len(payload))
			}

			next, err := io.ReadAll(&markedReader{r: br})
			be.NilErr(t, err)
			be.Equal(t, "next", string(next))
		}
	}
}

func TestMarkedReadTruncated(t *testing.T) {
	for _, stream := range []string{"", "abc", "abc]", "abc]]>]]", "]]>]]"} {
		r := &markedReader{r: bufio.NewReader(strings.NewReader(stream))}
		_, err := io.ReadAll(r)
		be.True(t, errors.Is(err, io.ErrUnexpectedEOF))

		// Close on a truncated message reports the truncation too.
		r = &markedReader{r: bufio.NewReader(strings.NewReader(stream)), f: &Framer{}}
		be.True(t, errors.Is(r.Close(), io.ErrUnexpectedEOF))
	}
}

func TestMarkedCloseDrains(t *testing.T) {
	br := bufio.NewReader(strings.NewReader(strings.Repeat("a]", 5000) + endOfMsgStr + "next" + endOfMsgStr))

	r := &markedReader{r: br, f: &Framer{}}
	buf := make([]byte, 3)
	_, err := io.ReadFull(r, buf)
	be.NilErr(t, err)
	be.NilErr(t, r.Close())

	next, err := io.ReadAll(&markedReader{r: br})
	be.NilErr(t, err)
	be.Equal(t, "next", string(next))
}

// After a chunk header that makes no sense nobody knows where the message
// ends.  The reader must say so and stay that way: reading on to "drain" the
// message waits for ever on a peer that is waiting too.
func TestChunkedReaderStaysFailed(t *testing.T) {
	for _, header := range []string{"\n#0\n", "\n#abc\n", "\n#\n", "\n#-1\n", "x"} {
		t.Run(strings.TrimSpace(header), func(t *testing.T) {
			// the peer sends the header and then nothing, without hanging up
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			go func() { _, _ = io.WriteString(pw, header) }()

			f := NewFramer(pr, io.Discard)
			f.Upgrade()
			r, err := f.MsgReader()
			be.NilErr(t, err)

			_, err = r.Read(make([]byte, 16))
			be.True(t, errors.Is(err, ErrMalformedChunk))
			_, err = r.Read(make([]byte, 16))
			be.True(t, errors.Is(err, ErrMalformedChunk))

			closed := make(chan error, 1)
			go func() { closed <- r.Close() }()
			select {
			case err := <-closed:
				be.True(t, errors.Is(err, ErrMalformedChunk))
			case <-time.After(5 * time.Second):
				t.Fatal("Close is reading on after a malformed chunk")
			}
		})
	}
}
