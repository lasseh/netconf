// Package transport defines what a NETCONF session needs from the connection
// it runs over, and implements the message framing of RFC 6242 for transports
// built on a byte stream.
//
// The ssh and tls packages below it are the transports most programs use.
// To write another one, embed a Framer around the connection, add Close, and
// run the transporttest suite against the result.
package transport

import (
	"errors"
	"io"
)

var (
	// ErrInvalidIO is returned when a write or read operation is called on
	// message io.Reader or a message io.Writer when they are no longer valid.
	// (i.e a new reader or writer has been obtained).
	ErrInvalidIO = errors.New("netconf: read/write on invalid io")
)

// Transport is used for a netconf.Session to talk to the device. It is message
// oriented to allow for framing and other details to happen on a per message
// basis.
//
// Implementations must honour the following contract, which the session layer
// (and the transporttest conformance suite) rely on:
//
//   - Single active reader/writer: at most one message reader and one message
//     writer may be active at a time. Acquiring a second reader while one is
//     active (or a second writer while one is active) returns ErrStreamBusy.
//     The reader and writer are independent: a reader and a writer may be
//     active simultaneously.
//
//   - Release on close: closing a reader or writer releases it so that the
//     next MsgReader/MsgWriter succeeds. Read/Write calls made after the
//     reader/writer has been closed return ErrInvalidIO.
//
//   - EOF latch: a reader returns io.EOF exactly at the end of the current
//     message and keeps returning io.EOF on every subsequent Read. End-of-
//     message must never bleed into the next message: the bytes of one message
//     are never observable through the reader of another.
//
//   - Close is cancel: Close must unblock any in-flight Read or Write (and any
//     goroutine blocked acquiring a reader/writer). This is the transport's
//     only cancellation mechanism because some underlying transports (e.g. SSH
//     channels) have no read/write deadline support. After Close, MsgReader and
//     MsgWriter may fail either immediately or on first use, but must not block
//     indefinitely.
type Transport interface {
	// MsgReader returns a reader for the next message.
	// The caller must close the reader when done.
	MsgReader() (io.ReadCloser, error)

	// MsgWriter returns a writer for a new message. Closing it will finalize
	// the message framing and flush to the underlying transport.
	MsgWriter() (io.WriteCloser, error)

	// Upgrade switches from end-of-message to chunked framing (RFC6242 sec
	// 4.1).  The session calls it once, after a hello exchange in which both
	// peers advertised base:1.1, and every message after that is chunked.
	// Framer implements it for every transport that embeds one.  A
	// transport that does not frame messages on a byte stream has nothing
	// to switch and does nothing here.
	Upgrade()

	// Close closes the transport and releases everything waiting on it.
	Close() error
}
