package transport

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/carlmjohnson/be"
)

// A session with Junos 26.2R1.7 (vJunos-router) that has rfc-compliant and
// version-1.1 set under the NETCONF service, as the device sent it in a
// lab: its hello, and the replies to eleven requests in chunks.  Nothing in
// the chunks was touched.
//
// Junos makes a chunk of every line it writes.  The first is the start tag
// of the reply, 139 bytes here, which it sends at once; the rest follows
// when it has done what it was asked.  A lock is answered in three chunks
// of 139, 9 and 15 bytes, a request it does not know in eleven.
func TestFramerJunosChunked(t *testing.T) {
	raw, err := os.ReadFile("testdata/junos-chunked-session.raw")
	be.NilErr(t, err)

	// the sizes of the replies, without their framing
	sizes := []int{163, 163, 444, 370, 337, 154, 154, 386, 235, 155, 164}
	const lockReply = `<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="1">` + "\n<nc:ok/>\n</nc:rpc-reply>"

	readers := map[string]func(io.Reader) io.Reader{
		"as it came":         func(r io.Reader) io.Reader { return r },
		"one byte at a time": iotest.OneByteReader,
	}
	for name, reader := range readers {
		t.Run(name, func(t *testing.T) {
			f := NewFramer(reader(bytes.NewReader(raw)), io.Discard)
			next := func() string {
				t.Helper()
				r, err := f.MsgReader()
				be.NilErr(t, err)
				msg, err := io.ReadAll(r)
				be.NilErr(t, err)
				be.NilErr(t, r.Close())
				return string(msg)
			}

			// the hello ends with the end-of-message mark, whatever comes after
			hello := next()
			be.True(t, strings.HasPrefix(hello, "<!-- No zombies were killed during the creation of this user interface -->\n"))
			be.In(t, "<nc:capability>urn:ietf:params:netconf:base:1.1</nc:capability>", hello)
			be.True(t, strings.HasSuffix(hello, "</nc:hello>\n"))

			f.Upgrade()
			for i, size := range sizes {
				msg := next()
				be.Equal(t, size, len(msg))
				be.True(t, strings.HasPrefix(msg, `<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="`+strconv.Itoa(i+1)+`">`+"\n"))
				be.True(t, strings.HasSuffix(msg, "</nc:rpc-reply>"))
				if i == 0 {
					be.Equal(t, lockReply, msg)
				}
			}

			// and that is all the device sent
			r, err := f.MsgReader()
			be.NilErr(t, err)
			_, err = io.ReadAll(r)
			be.Equal(t, io.ErrUnexpectedEOF, err)
		})
	}
}
