package transport

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// The fuzz targets below treat the framer as a device would: arbitrary bytes
// in, and whatever comes out must be explainable by those bytes.  The seeds
// run as ordinary tests; `make fuzz` searches for new inputs.

// readMessages reads messages off in until the framer reports an error and
// returns the payloads of the complete ones.
func readMessages(t *testing.T, in []byte, chunked bool) [][]byte {
	t.Helper()

	f := NewFramer(bytes.NewReader(in), io.Discard)
	if chunked {
		f.Upgrade()
	}

	var (
		msgs [][]byte
		read int
	)
	// Every message takes at least its framing off the input, so there
	// cannot be more messages than input bytes.
	for range len(in) + 1 {
		r, err := f.MsgReader()
		if err != nil {
			t.Fatalf("MsgReader: %v", err)
		}
		msg, readErr := io.ReadAll(r)
		closeErr := r.Close()

		read += len(msg)
		if read > len(in) {
			t.Fatalf("got %d payload bytes out of %d input bytes", read, len(in))
		}
		if readErr != nil || closeErr != nil {
			return msgs
		}
		msgs = append(msgs, msg)

		// A finished reader stays finished and never shows the next message.
		if n, err := r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, ErrInvalidIO) {
			t.Fatalf("Read after Close = %d, %v", n, err)
		}
	}
	t.Fatalf("more messages than input bytes")
	return nil
}

func FuzzMarkedReader(f *testing.F) {
	f.Add([]byte("<hello/>]]>]]><rpc/>]]>]]>"))
	f.Add([]byte("]]>]]>"))
	f.Add([]byte("a]]>]]]>]]>b]]>]]"))
	f.Add([]byte("<!-- junos --><hello/>]]>]]>\n<!-- session end -->"))
	f.Fuzz(func(t *testing.T, in []byte) {
		msgs := readMessages(t, in, false)

		// The messages, each followed by the delimiter, are exactly the
		// start of the input: nothing is dropped, invented or reordered.
		var framed []byte
		for _, msg := range msgs {
			if bytes.Contains(msg, endOfMsg) {
				t.Fatalf("message contains the delimiter: %q", msg)
			}
			framed = append(framed, msg...)
			framed = append(framed, endOfMsg...)
		}
		if !bytes.HasPrefix(in, framed) {
			t.Fatalf("messages %q do not match input %q", msgs, in)
		}
		// What is left never held a complete message.
		if rest := in[len(framed):]; bytes.Contains(rest, endOfMsg) {
			t.Fatalf("stopped before a complete message: %q", rest)
		}
	})
}

func FuzzChunkedReader(f *testing.F) {
	f.Add(rfcChunkedRPC)
	f.Add([]byte("\n#3\nabc\n##\n\n#1\nx\n##\n"))
	f.Add([]byte("\n##\n"))
	f.Add([]byte("\n#4294967295\nabc"))
	f.Add([]byte("\n#03\nabc\n##\n"))
	f.Add([]byte("\n#3\nab"))
	f.Fuzz(func(t *testing.T, in []byte) {
		msgs := readMessages(t, in, true)

		// Whatever was accepted must read back the same once re-encoded;
		// see FuzzChunkedRoundTrip for the other direction.
		var wire bytes.Buffer
		fr := NewFramer(&wire, &wire)
		fr.Upgrade()
		for _, msg := range msgs {
			w, err := fr.MsgWriter()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(msg); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		}
		again := readMessages(t, wire.Bytes(), true)
		if len(again) != len(msgs) {
			t.Fatalf("re-encoded %d messages, read back %d", len(msgs), len(again))
		}
		for i := range msgs {
			if !bytes.Equal(msgs[i], again[i]) {
				t.Fatalf("message %d: %q became %q", i, msgs[i], again[i])
			}
		}
	})
}

// roundTrip writes msgs through a framer, splitting every write into pieces
// of step bytes, and reads them back through a transport that delivers one
// byte at a time when slow is set.
func roundTrip(t *testing.T, chunked bool, msgs [][]byte, step int, slow bool) [][]byte {
	t.Helper()

	var wire bytes.Buffer
	out := NewFramer(bytes.NewReader(nil), &wire)
	if chunked {
		out.Upgrade()
	}
	for _, msg := range msgs {
		w, err := out.MsgWriter()
		if err != nil {
			t.Fatal(err)
		}
		for rest := msg; len(rest) > 0; {
			n := min(max(step, 1), len(rest))
			if _, err := w.Write(rest[:n]); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	var src io.Reader = bytes.NewReader(wire.Bytes())
	if slow {
		src = iotest.OneByteReader(src)
	}
	in := NewFramer(src, io.Discard)
	if chunked {
		in.Upgrade()
	}

	got := make([][]byte, 0, len(msgs))
	for range msgs {
		r, err := in.MsgReader()
		if err != nil {
			t.Fatal(err)
		}
		msg, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		got = append(got, msg)
	}

	// and nothing follows the last message
	r, err := in.MsgReader()
	if err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(r); len(rest) != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("after the last message: %q, %v", rest, err)
	}
	return got
}

func FuzzChunkedRoundTrip(f *testing.F) {
	f.Add([]byte("<rpc/>"), []byte("<rpc-reply/>"), uint8(0), false)
	f.Add([]byte("\n##\n"), []byte("\n#1\n"), uint8(1), true)
	f.Add([]byte{}, []byte("x"), uint8(3), true)
	f.Fuzz(func(t *testing.T, first, second []byte, step uint8, slow bool) {
		msgs := [][]byte{first, second}
		got := roundTrip(t, true, msgs, int(step), slow)
		for i := range msgs {
			if !bytes.Equal(msgs[i], got[i]) {
				t.Fatalf("message %d: sent %q, got %q", i, msgs[i], got[i])
			}
		}
	})
}

func FuzzMarkedRoundTrip(f *testing.F) {
	f.Add([]byte("<hello/>"), []byte("<rpc/>"), uint8(0), false)
	f.Add([]byte("]]>"), []byte("]]>]]"), uint8(1), true)
	f.Add([]byte("a]]>]]>b"), []byte("x"), uint8(2), false)
	f.Fuzz(func(t *testing.T, first, second []byte, step uint8, slow bool) {
		// A payload the framing cannot carry is refused whole.  It holds
		// the delimiter, or completes it together with the one that ends
		// the message.
		for _, msg := range [][]byte{first, second} {
			framed := append(bytes.Clone(msg), endOfMsg...)
			if bytes.Index(framed, endOfMsg) == len(msg) {
				continue
			}

			var wire bytes.Buffer
			w, err := NewFramer(bytes.NewReader(nil), &wire).MsgWriter()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(msg); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); !errors.Is(err, ErrEndOfMsgInPayload) {
				t.Fatalf("Close(%q) = %v", msg, err)
			}
			if wire.Len() != 0 {
				t.Fatalf("refused message left %q on the wire", wire.Bytes())
			}
			return
		}

		msgs := [][]byte{first, second}
		got := roundTrip(t, false, msgs, int(step), slow)
		for i := range msgs {
			if !bytes.Equal(msgs[i], got[i]) {
				t.Fatalf("message %d: sent %q, got %q", i, msgs[i], got[i])
			}
		}
	})
}
