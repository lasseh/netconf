//go:build unix

package tls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
)

// A TLS 1.3 device can ask the client to update its keys, and crypto/tls answers
// from inside Read, under the lock that tls.Conn.Close needs for its close_notify.
// With a device that stops reading, that answer sits in a socket write with no
// deadline.  crypto/tls has no call to send a KeyUpdate, so the device of these
// tests takes its traffic secret from its own key log and writes the records on
// the raw socket.
const (
	// stuckAfter is how long a call gets before it counts as stuck.  It is
	// past the five seconds crypto/tls gives close_notify.
	stuckAfter = 7 * time.Second

	hello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities>` +
		`<capability>urn:ietf:params:netconf:base:1.0</capability></capabilities>` +
		`<session-id>7</session-id></hello>]]>]]>`
	replyStart = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1">`
)

// floodingDevice is the server side of a TLS 1.3 connection after the handshake.
type floodingDevice struct {
	conn   net.Conn
	aead   cipher.AEAD
	iv     [12]byte
	seq    uint64
	secret []byte
	sent   atomic.Int64

	// stalled closes when the client has stopped reading: a write to it ran
	// out of time.
	stalled chan struct{}
}

// expandLabel is HKDF-Expand-Label of RFC 8446 section 7.1 with an empty context.
func expandLabel(t *testing.T, secret []byte, label string, length int) []byte {
	t.Helper()
	full := "tls13 " + label
	info := binary.BigEndian.AppendUint16(nil, uint16(length)) //nolint:gosec // a small constant
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, 0)
	out, err := hkdf.Expand(sha256.New, secret, string(info), length)
	be.NilErr(t, err)
	return out
}

func (d *floodingDevice) setSecret(t *testing.T, secret []byte) {
	t.Helper()
	block, err := aes.NewCipher(expandLabel(t, secret, "key", 16))
	be.NilErr(t, err)
	d.aead, err = cipher.NewGCM(block)
	be.NilErr(t, err)
	copy(d.iv[:], expandLabel(t, secret, "iv", 12))
	d.secret, d.seq = secret, 0
}

// record seals one TLS 1.3 record holding content of the inner type typ.
func (d *floodingDevice) record(content []byte, typ byte) []byte {
	inner := append(append([]byte(nil), content...), typ)
	header := []byte{0x17, 0x03, 0x03, 0, 0}
	binary.BigEndian.PutUint16(header[3:], uint16(len(inner)+d.aead.Overhead())) //nolint:gosec // a short record
	nonce := d.iv
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], d.seq)
	for i := range seq {
		nonce[4+i] ^= seq[i]
	}
	d.seq++
	return d.aead.Seal(header, nonce[:], inner, header[:5:5]) //nolint:gosec // the nonce is the one of RFC 8446
}

// say sends s as one record of application data.
func (d *floodingDevice) say(t *testing.T, s string) {
	t.Helper()
	_, err := d.conn.Write(d.record([]byte(s), 23))
	be.NilErr(t, err)
}

// flood sends 16 KeyUpdates that ask for an answer and a byte of application data,
// which resets the count of records crypto/tls allows without any, until stop
// closes or the client stops reading.
func (d *floodingDevice) flood(t *testing.T, stop <-chan struct{}) {
	t.Helper()
	keyUpdate := []byte{24, 0, 0, 1, 1}
	var out []byte
	for {
		select {
		case <-stop:
			return
		default:
		}
		out = out[:0]
		for range 16 {
			out = append(out, d.record(keyUpdate, 22)...)
			d.setSecret(t, expandLabel(t, d.secret, "traffic upd", 32))
		}
		out = append(out, d.record([]byte(" "), 23)...)
		_ = d.conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := d.conn.Write(out); err != nil {
			close(d.stalled)
			return
		}
		d.sent.Add(16)
	}
}

// keyLog keeps what the KeyLogWriter of a handshake writes.
type keyLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (k *keyLog) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.b.Write(p)
}

func (k *keyLog) serverSecret(t *testing.T) []byte {
	t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	for line := range strings.SplitSeq(k.b.String(), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "SERVER_TRAFFIC_SECRET_0" {
			secret, err := hex.DecodeString(f[2])
			be.NilErr(t, err)
			return secret
		}
	}
	t.Fatal("no SERVER_TRAFFIC_SECRET_0 in the key log")
	return nil
}

// connectFlooding connects a client to a device.  Both ends have small socket
// buffers, so that the answers fill them with few records.
func connectFlooding(t *testing.T) (*floodingDevice, *Transport) {
	t.Helper()
	cert, err := generateSelfSignedCert()
	be.NilErr(t, err)
	keys := &keyLog{}

	listen := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) //nolint:gosec // a socket
		}); err != nil {
			return err
		}
		return setErr
	}}
	ln, err := listen.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	be.NilErr(t, err)
	defer func() { _ = ln.Close() }()

	devices := make(chan *floodingDevice, 1)
	go func() {
		defer close(devices)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		srv := tls.Server(raw, &tls.Config{
			Certificates:           []tls.Certificate{cert},
			MinVersion:             tls.VersionTLS13,
			SessionTicketsDisabled: true,
			KeyLogWriter:           keys,
		})
		if err := srv.HandshakeContext(context.Background()); err != nil {
			return
		}
		if srv.ConnectionState().CipherSuite != tls.TLS_AES_128_GCM_SHA256 {
			return
		}
		d := &floodingDevice{conn: raw, stalled: make(chan struct{})}
		d.setSecret(t, keys.serverSecret(t))
		devices <- d
	}()

	raw, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", ln.Addr().String())
	be.NilErr(t, err)
	tcp, ok := raw.(*net.TCPConn)
	be.True(t, ok)
	be.NilErr(t, tcp.SetWriteBuffer(4096))
	client := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec // a test device
	be.NilErr(t, client.HandshakeContext(t.Context()))

	device, ok := <-devices
	if !ok {
		t.Fatal("the device did not come up, or the handshake chose another cipher")
	}
	t.Cleanup(func() { _ = device.conn.Close() })
	return device, NewTransport(client)
}

// waitForTheStall returns once the read path of the client sits in a socket write,
// with the lock of tls.Conn.Close held.
func waitForTheStall(t *testing.T, d *floodingDevice) {
	t.Helper()
	select {
	case <-d.stalled:
	case <-time.After(30 * time.Second):
		t.Fatalf("the client kept reading, %d KeyUpdates sent", d.sent.Load())
	}

	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "crypto/tls.(*Conn).handleKeyUpdate") && strings.Contains(g, "waitWrite") {
			return
		}
	}
	t.Fatalf("the client stopped reading somewhere else, %d KeyUpdates sent", d.sent.Load())
}

func within(t *testing.T, doing string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(stuckAfter):
		t.Fatalf("%s: still blocked after %s", doing, stuckAfter)
	}
}

func TestKeyUpdateStall_CloseReleasesARead(t *testing.T) {
	device, tr := connectFlooding(t)
	stop := make(chan struct{})
	defer close(stop)
	go device.flood(t, stop)

	read := make(chan struct{})
	go func() {
		defer close(read)
		if r, err := tr.MsgReader(); err == nil {
			_, _ = io.ReadAll(r)
		}
	}()
	waitForTheStall(t, device)

	within(t, "Close", func() { _ = tr.Close() })
	within(t, "the read", func() { <-read })
}

// Open reaches this with no option set.
func TestKeyUpdateStall_OpenContext(t *testing.T) {
	device, tr := connectFlooding(t)
	stop := make(chan struct{})
	defer close(stop)
	go device.flood(t, stop)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		_, err := netconf.Open(ctx, tr)
		opened <- err
	}()
	waitForTheStall(t, device)

	cancel()
	within(t, "Open", func() { be.True(t, <-opened != nil) })
}

func TestKeyUpdateStall_MessageTimeout(t *testing.T) {
	device, tr := connectFlooding(t)
	device.say(t, hello)
	session, err := netconf.Open(t.Context(), tr, netconf.WithMessageTimeout(300*time.Millisecond))
	be.NilErr(t, err)

	go func() { _, _ = session.ExecReply(t.Context(), "<get/>") }()
	time.Sleep(200 * time.Millisecond) // the request is out, and in no Write

	stop := make(chan struct{})
	defer close(stop)
	device.say(t, replyStart)
	go device.flood(t, stop)
	waitForTheStall(t, device)

	within(t, "Done", func() { <-session.Done() })
}

func TestKeyUpdateStall_SessionCloseContext(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream ", stream), func(t *testing.T) {
			device, tr := connectFlooding(t)
			device.say(t, hello)
			session, err := netconf.Open(t.Context(), tr)
			be.NilErr(t, err)

			messages := make(chan *netconf.Message, 1)
			go func() {
				if !stream {
					_, _ = session.ExecReply(t.Context(), "<get/>")
					return
				}
				if msg, err := session.Stream(t.Context(), "<get/>"); err == nil {
					messages <- msg
				}
			}()
			time.Sleep(200 * time.Millisecond) // the request is out, and in no Write

			stop := make(chan struct{})
			defer close(stop)
			device.say(t, replyStart)
			if stream {
				msg := <-messages
				go func() { _, _ = io.Copy(io.Discard, msg) }()
			}
			go device.flood(t, stop)
			waitForTheStall(t, device)

			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			var closeErr error
			within(t, "Session.Close", func() { closeErr = session.Close(ctx) })
			if stream {
				// with a stream open no close-session is sent, and nothing is
				// waited for
				be.NilErr(t, closeErr)
			}
		})
	}
}

// A close with nothing in the way still says goodbye.
func TestClose_SendsCloseNotify(t *testing.T) {
	srv := newTestServer(t)
	seen := make(chan error, 1)
	srv.Serve(func(conn net.Conn) error {
		_, err := io.Copy(io.Discard, conn)
		seen <- err
		return nil
	})

	tr, err := Dial(t.Context(), "tcp", srv.Addr(), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a test device
	be.NilErr(t, err)
	be.NilErr(t, tr.Close())

	// io.Copy has no error at a close_notify, and has one for a socket that went away
	within(t, "the server", func() { be.NilErr(t, <-seen) })
}
