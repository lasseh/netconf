package netconf

import (
	"bytes"
	"time"
)

// Trace is a set of functions a session calls as it works, for timing,
// counting and logging what goes over it, in the manner of
// net/http/httptrace.  Give it to Open with WithTrace.  A function that is
// nil is not called.
//
// The functions are called on the path of what they report: OperationStart
// and OperationDone by the goroutine that issued the operation, before it is
// sent and before its result is returned, and Notification by the one that
// reads from the device, where it holds up every reply behind it.  They have
// to return quickly and must not block.  Operations on one session can run
// at the same time, and so can the functions.
//
// A function must not call Close on the session.  Close reports its own
// close-session to OperationDone, where a second Close waits for the first
// for ever, and a Close from Notification waits for the goroutine it is
// called on.  To end a session over what a function saw, tell another
// goroutine.  A function must not panic either: the panic goes through the
// session, which can be left with its transport open, or with a reply that
// nobody reads and every later operation waits behind.
type Trace struct {
	// OperationStart is called when an operation is about to be sent.  Over
	// chunked framing to a Junos device that is before the operation waits
	// for the one in front of it to be answered, see Session.
	OperationStart func(OperationInfo)

	// OperationDone is called when the operation is over: its reply has
	// arrived and was looked at for errors, or the wait for it has ended.
	// It is called once for every OperationStart.
	OperationDone func(OperationInfo, OperationResult)

	// Notification is called for a notification that is on its way to the
	// handler, with its size in bytes: when it is queued, not when the
	// handler runs.  dropped says that it was thrown
	// away: the queue of the handler had no room for it, see WithNotifQueue,
	// or the session is closing.  A session without a handler reports none.
	Notification func(size int, dropped bool)
}

// OperationInfo is what a Trace is told about an operation.
type OperationInfo struct {
	// SessionID is the session-id of the session the operation is on.
	SessionID uint64

	// MessageID is the message-id the operation was sent with.
	MessageID string

	// Name is the name of the operation: that of its element, without a
	// prefix.  It is "get-configuration" for a compare too, which Junos has
	// as that element with other attributes, and empty for an operation
	// that is no element.
	Name string

	// Request is the <rpc> message as it is sent, with whatever secrets the
	// operation carries: the configuration of a load, with its password
	// hashes and keys.  It must not be changed.
	Request []byte

	// Start is when the operation was handed to the session to be sent.
	Start time.Time
}

// OperationResult is what a Trace is told about the end of an operation.
type OperationResult struct {
	// Duration is the time from Start to the reply, or to the moment the
	// wait for it ended.  For a reply read with Session.Stream it is the
	// time to the beginning of the reply, which is all the session sees of
	// it.  The time an operation waited for the one in front of it to be
	// answered, see OperationStart, is part of it.
	Duration time.Duration

	// ReplySize is the size of the reply in bytes.  It is zero when none
	// arrived, and for a reply read with Session.Stream.
	ReplySize int

	// Err is what the session made of the operation: the error the wait
	// for the reply ended with, or the RPC errors of the reply, nil for
	// neither.  What is found wrong with a reply after that is not in it: a
	// struct that Session.Exec could not decode it into, an <ok/> that a
	// typed operation missed.  A streamed reply is not looked at for
	// errors.
	Err error
}

// traceStart reports that an operation is about to be sent.  The returned
// info is for traceDone.
func (s *Session) traceStart(msgID string, payload []byte) OperationInfo {
	if s.trace.OperationStart == nil && s.trace.OperationDone == nil {
		// the name is for the errors of the reply, see RPCError.Request
		return OperationInfo{Name: operationName(payload)}
	}
	op := OperationInfo{
		SessionID: s.sessionID,
		MessageID: msgID,
		Name:      operationName(payload),
		Request:   payload,
		Start:     time.Now(),
	}
	if s.trace.OperationStart != nil {
		s.trace.OperationStart(op)
	}
	return op
}

// traceDone reports that the operation op is over.
func (s *Session) traceDone(op OperationInfo, replySize int, err error) {
	if s.trace.OperationDone == nil {
		return
	}
	s.trace.OperationDone(op, OperationResult{
		Duration:  time.Since(op.Start),
		ReplySize: replySize,
		Err:       err,
	})
}

// operationName returns the name of the operation in an <rpc> message: the
// name of the first element inside the <rpc>, without its prefix.
func operationName(rpc []byte) string {
	// what the <rpc> contains
	end := bytes.IndexByte(rpc, '>')
	if end < 0 {
		return ""
	}
	body := rpc[end+1:]

	for {
		start := bytes.IndexByte(body, '<')
		if start < 0 {
			return ""
		}
		body = body[start:]
		// a comment or a processing instruction in front of the operation
		if skip := unparsed(body); skip > 1 {
			body = body[skip:]
			continue
		}
		break
	}

	name := body[1:]
	if i := bytes.IndexAny(name, " \t\r\n/>"); i >= 0 {
		name = name[:i]
	}
	if i := bytes.IndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	// the end tag of the <rpc> means that there was no element
	if len(name) == 0 || len(body) > 1 && body[1] == '/' {
		return ""
	}
	return string(name)
}
