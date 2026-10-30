package netconf

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/lasseh/netconf/internal/xmlutil"
)

// recvLoop reads messages until the transport fails or is closed, then ends
// the session.  It runs for the lifetime of the session so replies and
// interleaved notifications are picked up as they arrive.
func (s *Session) recvLoop() {
	var (
		err     error
		skipped int
	)
	for err == nil {
		var delivered bool
		delivered, err = s.recvMsg()
		switch {
		case delivered:
			skipped = 0
		case err == nil:
			skipped++
			if skipped >= maxSkippedMessages {
				err = fmt.Errorf("ignored %d messages in a row", skipped)
			}
		}
	}

	closing := s.replies.isClosing()
	s.fail(err)

	// Release notification handlers and anything blocked on the transport,
	// so a dead connection doesn't leak goroutines when the user never
	// calls Close.
	s.notif.stop()
	_ = s.closeTransport()

	if !closing {
		s.logger.Warn("session closed unexpectedly", "error", s.Err())
	}

	// last, so the session has nothing left to do or say once it is done
	close(s.done)
}

var (
	rpcReplyName     = xml.Name{Space: NetconfNamespace, Local: "rpc-reply"}
	notificationName = xml.Name{Space: NotificationNamespace, Local: "notification"}
)

// recvMsg reads one message from the transport and dispatches it.  It
// reports whether the message was delivered to someone; a message that is
// neither delivered nor an error was ignored.  An error means the message
// stream is no longer usable and ends the session.
func (s *Session) recvMsg() (delivered bool, err error) {
	r, err := s.msgReader()
	if err != nil {
		return false, err
	}
	// What is no notification is taken for the answer to an operation, a
	// message that nobody can read included: a session that waited for a
	// better one would send nothing more.
	answer := true
	defer func() {
		// Close drains whatever is left of the message.  If that fails the
		// stream is no longer aligned on a message boundary.
		if closeErr := r.Close(); closeErr != nil && err == nil {
			delivered, err = false, fmt.Errorf("failed to drain message: %w", closeErr)
		}
		// all of the answer has arrived: the next operation may go, see send
		if answer && s.replies.turnOver() {
			s.endTurn()
		}
	}()

	// Decode just far enough to see what kind of message this is.
	start, err := r.start()

	var syntaxErr *xml.SyntaxError
	switch {
	case err == nil:
	case errors.Is(err, io.EOF) || errors.As(err, &syntaxErr):
		// The frame holds no XML element (empty, or not XML at all).  That
		// is the peer's mistake in one message, not a broken stream: the
		// reader is still drained to the frame boundary on close.
		s.logger.Warn("skipping malformed message", "error", err)
		return false, nil
	default:
		return false, fmt.Errorf("failed to read message: %w", err)
	}

	switch start.Name {
	case rpcReplyName:
		return s.recvReply(start, r)
	case notificationName:
		answer = false
		return s.recvNotification(start, r)
	default:
		// An unknown message must not kill the session; closing the reader
		// drains the frame so the stream stays aligned.
		s.logger.Warn("skipping unknown message",
			"name", logValue(start.Name.Local),
			"namespace", logValue(start.Name.Space))
		return false, nil
	}
}

// startElement will walk though a xml.Decode until it finds a start element
// and returns it.
func startElement(d *xml.Decoder) (*xml.StartElement, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}

		if start, ok := tok.(xml.StartElement); ok {
			return &start, nil
		}
	}
}

// validAttrName reports whether name can be written as an XML attribute
// name: an NCName, optionally with an NCName prefix.
func validAttrName(name xml.Name) bool {
	prefix, local, ok := strings.Cut(name.Local, ":")
	if !ok {
		prefix, local = "", name.Local
	}
	return xmlutil.ValidNCName(local) && (prefix == "" || xmlutil.ValidNCName(prefix))
}

func getMessageID(attrs []xml.Attr) string {
	for _, attr := range attrs {
		if attr.Name.Local == "message-id" {
			return attr.Value
		}
	}
	return ""
}

// recvReply delivers an rpc-reply to the operation waiting for it.
func (s *Session) recvReply(start *xml.StartElement, body *boundedReader) (bool, error) {
	msgID := getMessageID(start.Attr)

	req, late := s.replies.resolve(msgID)
	if req == nil {
		if late {
			// The answer to an operation whose caller stopped waiting.
			// That is the device doing its job, not a message to hold
			// against it.
			s.logger.Debug("dropping late rpc-reply", "message-id", logValue(msgID))
			return true, nil
		}
		s.logger.Warn("unexpected rpc-reply", "message-id", logValue(msgID))
		return false, nil
	}

	msg := &Message{
		messageID:  &msgID,
		Attributes: start.Attr,
	}

	if !req.stream {
		// Buffer the reply so the receive loop never waits on the caller;
		// an abandoned reply is simply dropped.
		whole, err := body.whole("rpc-reply")
		if err != nil {
			return false, err
		}
		msg.body = whole
		msg.reader = io.NopCloser(bytes.NewReader(msg.body))
		req.msg <- msg
		return true, nil
	}

	// Streaming reply (Stream): hand the live transport reader to the
	// caller and hold the receive loop until it is given back.
	reader, err := body.live()
	if err != nil {
		return false, err
	}
	msg.reader = reader

	s.streaming.Store(true)
	defer s.streaming.Store(false)
	req.msg <- msg

	return true, s.awaitStream(req, reader)
}

// awaitStream blocks until the streamed reply handed to req is finished
// with: closed by its receiver, abandoned by a receiver whose context fired,
// or cut short by the session closing its transport.
func (s *Session) awaitStream(req *pendingReply, reader *streamReader) error {
	ctxDone := req.ctx.Done()
	for {
		select {
		case <-reader.done:
			return nil
		case <-ctxDone:
			// The receiver may never collect the message from the channel,
			// so reclaim and close it ourselves rather than blocking the
			// receive loop forever.  If it did take the message it owns
			// Close and we keep waiting for it.
			select {
			case abandoned := <-req.msg:
				// Nobody reads it at a pace of their own any more: closing
				// it reads the rest, which is waited for like the rest of
				// any message.
				reader.body.timed()
				return abandoned.Close()
			default:
				ctxDone = nil
			}
		case <-s.trClosed:
			// Nothing more can be read, so take the reader back instead of
			// waiting for a Close that may never come.
			_ = reader.Close()
			return ErrClosed
		}
	}
}

// streamReader is the reader of a streamed reply.  Closing it releases the
// receive loop.
type streamReader struct {
	r    io.Reader
	body *boundedReader

	// mu keeps Close, which may come from the session, from pulling the
	// transport reader out from under a Read in progress
	mu   sync.Mutex
	done chan struct{}
	once sync.Once
}

func (r *streamReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Reject reads after Close: the transport reader is back with the
	// receive loop, so a stale read could otherwise observe the next
	// message's bytes.
	select {
	case <-r.done:
		return 0, ErrClosed
	default:
	}
	return r.r.Read(p)
}

func (r *streamReader) Close() error {
	var err error
	r.once.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		err = r.body.Close()
		close(r.done)
	})
	return err
}

// recvNotification buffers a notification and queues it for the handler.
func (s *Session) recvNotification(start *xml.StartElement, body *boundedReader) (bool, error) {
	if s.notif == nil {
		s.logger.Warn("skipping a notification: the session has no handler, see WithNotifHandler")
		return false, nil
	}

	// Buffer the notification so the receive loop is free to continue and
	// handlers can safely issue RPCs on this session.
	whole, err := body.whole("notification")
	if err != nil {
		return false, err
	}
	msg := &Message{
		body:       whole,
		reader:     io.NopCloser(bytes.NewReader(whole)),
		Attributes: start.Attr,
	}

	if err := s.notif.deliver(msg); err != nil {
		return false, err
	}
	return true, nil
}

// logValue shortens a server-supplied string for logging.
func logValue(s string) string {
	if len(s) > maxLogValue {
		return s[:maxLogValue] + "..."
	}
	return s
}
