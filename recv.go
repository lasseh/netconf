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
	r.quiet, r.stalled = s.msgTimeout, s.stalled
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
		if answer {
			s.endTurn()
		}
	}()

	// Decode just far enough to see what kind of message this is, keeping
	// every byte the decoder consumed in head so the message can still be
	// delivered whole.  What comes before the start element is buffered
	// whatever becomes of the message, hence the tighter limit.
	r.setLimit(maxStartSize)
	if s.maxMessageSize >= 0 {
		r.setLimit(min(maxStartSize, s.maxMessageSize))
	}
	head := new(bytes.Buffer)
	peel := xml.NewDecoder(io.TeeReader(r, head))
	// Only the start element is looked at here, and its name is ASCII
	// whatever encoding the message declares.
	peel.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	start, err := startElement(peel)
	r.setLimit(s.maxMessageSize)

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
		return s.recvReply(start, head, r)
	case notificationName:
		answer = false
		return s.recvNotification(start, head, r)
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

// recvReply delivers an rpc-reply to the operation waiting for it.  head
// holds the part of the message already read from body.
func (s *Session) recvReply(start *xml.StartElement, head *bytes.Buffer, body *boundedReader) (bool, error) {
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
		if _, err := head.ReadFrom(body); err != nil {
			return false, fmt.Errorf("failed to read rpc-reply: %w", err)
		}
		// all of it has arrived: what is done with it is not its silence
		if body.untimed() {
			return false, s.errStalled()
		}
		msg.body = s.buffered(head.Bytes())
		msg.reader = io.NopCloser(bytes.NewReader(msg.body))
		req.msg <- msg
		return true, nil
	}

	// Streaming reply (Stream): hand the live transport reader to the
	// caller and hold the receive loop until it is given back.  A stream is
	// not buffered and so not limited, and read at the pace of the caller.
	body.setLimit(-1)
	if body.untimed() {
		return false, s.errStalled()
	}
	reader := &streamReader{
		r:      io.MultiReader(head, body),
		closer: body,
		done:   make(chan struct{}),
	}
	msg.reader = reader

	s.streaming.Store(true)
	defer s.streaming.Store(false)
	req.msg <- msg

	return true, s.awaitStream(req, reader, body)
}

// awaitStream blocks until the streamed reply handed to req is finished
// with: closed by its receiver, abandoned by a receiver whose context fired,
// or cut short by the session closing its transport.  body is what the
// stream reads the rest of the reply from.
func (s *Session) awaitStream(req *pendingReply, reader *streamReader, body *boundedReader) error {
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
				body.timed(s.msgTimeout)
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
	r      io.Reader
	closer io.Closer

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
		err = r.closer.Close()
		close(r.done)
	})
	return err
}

// recvNotification buffers a notification and queues it for the handler.
// head holds the part of the message already read, body reads the rest.
func (s *Session) recvNotification(start *xml.StartElement, head *bytes.Buffer, body *boundedReader) (bool, error) {
	if s.notif == nil {
		s.logger.Warn("skipping a notification: the session has no handler, see WithNotifHandler")
		return false, nil
	}

	// Buffer the notification so the receive loop is free to continue and
	// handlers can safely issue RPCs on this session.
	if _, err := head.ReadFrom(body); err != nil {
		return false, fmt.Errorf("failed to read notification: %w", err)
	}
	if body.untimed() {
		return false, s.errStalled()
	}
	whole := s.buffered(head.Bytes())
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
