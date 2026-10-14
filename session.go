package netconf

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lasseh/netconf/transport"
)

// The XML namespaces of the protocol messages: rpc, rpc-reply and hello are
// in NetconfNamespace, notification is in NotificationNamespace.
const (
	NetconfNamespace      = "urn:ietf:params:xml:ns:netconf:base:1.0"
	NotificationNamespace = "urn:ietf:params:xml:ns:netconf:notification:1.0"
)

var (
	// ErrClosed is returned by operations on a session that is over, and by
	// reads from a message that was closed.  When the session ended for a
	// reason other than Close, that reason is wrapped along with it.
	ErrClosed = errors.New("netconf: session closed")
)

// DefaultHelloTimeout bounds the hello exchange in Open when the caller's
// context has no deadline, so a device that never answers cannot hang Open
// forever.
const DefaultHelloTimeout = 30 * time.Second

// DefaultCloseTimeout bounds Session.Close when the caller's context has no
// deadline, so a server that never answers <close-session> cannot hang Close
// forever.
const DefaultCloseTimeout = 30 * time.Second

// DefaultMaxMessageSize is the default limit on the size of a message
// received from the server.
const DefaultMaxMessageSize = 256 << 20

// maxStartSize bounds how far into a message its start element may be.  The
// bytes before it are buffered whatever becomes of the message, so the bound
// is much tighter than the message size.
const maxStartSize = 1 << 20

// maxLogValue bounds server-supplied strings in log records.
const maxLogValue = 128

// maxSkippedMessages is how many messages in a row the session ignores
// (malformed, unknown or unsolicited) before it gives up on the peer.
const maxSkippedMessages = 100

// Session is a NETCONF session to one device.  It is safe for concurrent use:
// operations issued from multiple goroutines are written one at a time and
// correlated to their replies by message-id.  Over chunked framing to a
// Junos device an operation is written when the one before it has been
// answered, see send.
type Session struct {
	tr        transport.Transport
	sessionID uint64
	seq       atomic.Uint64

	clientCaps CapabilitySet
	serverCaps CapabilitySet

	// byteRefs says that the device writes text the way Junos does, with a
	// character reference for every byte above 127, see joinByteRefs
	byteRefs bool

	// opTimeout bounds an operation whose context has no deadline, zero for
	// no bound of our own
	opTimeout time.Duration
	logger    *slog.Logger

	// maxMessageSize limits buffered messages, negative for no limit
	maxMessageSize int64

	// repair is run on every buffered reply and notification, nil for none
	repair func([]byte) []byte

	// msgTimeout is how long a message that has begun may go without a
	// byte, zero for as long as it takes
	msgTimeout time.Duration

	// trace is the functions told of what goes on, each nil for none
	trace Trace

	// streaming is set while a streamed reply holds the receive loop
	streaming atomic.Bool

	// sendSem serializes outbound messages since the transport only allows
	// one active writer at a time.
	sendSem chan struct{}

	// turn is taken by an operation before it is sent and given back when
	// its answer has arrived whole, on a session that has one operation on
	// its way at a time: over chunked framing to a Junos device, see send.
	// It is nil on every other session.
	turn chan struct{}

	// replies holds the operations awaiting a reply.  It is closed by
	// Close: new operations are refused from then on and the receive loop
	// ending is expected rather than an error.
	replies *replyTable

	// err is why the session ended, nil while it is alive or after Close.
	mu  sync.Mutex
	err error

	// done is closed when the receive loop has exited, i.e. the session is
	// over, whether through Close or a dead transport.
	done chan struct{}

	closeOnce   sync.Once
	trCloseOnce sync.Once
	trCloseErr  error
	// trClosed is closed once the session has closed its transport
	trClosed chan struct{}

	// notif queues notifications for the handler, nil without one
	notif *notifier
}

func newSession(tr transport.Transport, opts ...SessionOption) *Session {
	cfg := sessionConfig{
		clientCaps:     DefaultCapabilities,
		logger:         slog.Default(),
		maxMessageSize: DefaultMaxMessageSize,
	}

	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if cfg.maxMessageSize <= 0 {
		cfg.maxMessageSize = -1
	}

	s := &Session{
		tr:         tr,
		clientCaps: NewCapabilitySet(cfg.clientCaps...),
		opTimeout:  cfg.opTimeout,
		logger:     cfg.logger,

		maxMessageSize: cfg.maxMessageSize,
		repair:         cfg.repair,
		msgTimeout:     cfg.msgTimeout,
		trace:          cfg.trace,

		sendSem:  make(chan struct{}, 1),
		replies:  newReplyTable(),
		done:     make(chan struct{}),
		trClosed: make(chan struct{}),
	}
	if cfg.notifHandler != nil {
		s.notif = newNotifier(cfg.notifHandler, cfg.notifQueue, cfg.logger)
		s.notif.trace = cfg.trace.Notification
	}
	return s
}

// Open opens a session on tr by performing the hello exchange.
//
// ctx bounds the exchange: when it fires, Open gives up and returns its
// error.  If it has no deadline, DefaultHelloTimeout is applied.  ctx has no
// say over the session once Open has returned.
//
// An exchange that fails returns an error that matches ErrHello, with a
// *HelloError that has what the device sent: the capabilities of a hello
// that did not make a session, errors in its place or in front of it, text
// outside any element.  A device that sends errors with its hello gets no
// session, though it said hello: Junos does that to a user whose login class
// permits nothing, and then refuses every request.  Warnings alone are
// logged and the session opens.
//
// The session owns the transport from here on.  It is closed when Open
// fails, by Session.Close, and when the session ends on its own.
func Open(ctx context.Context, tr transport.Transport, opts ...SessionOption) (*Session, error) {
	s := newSession(tr, opts...)

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultHelloTimeout)
		defer cancel()
	}

	if err := s.handshake(ctx); err != nil {
		if closeErr := s.closeTransport(); closeErr != nil {
			s.logger.Warn("failed to close transport after handshake error", "error", closeErr)
		}
		return nil, err
	}

	if s.notif != nil {
		go s.notif.run()
	}
	go s.recvLoop()
	return s, nil
}

// handshake performs the hello exchange.  The transport has no deadlines, so
// a context that fires closes the transport to release the blocked exchange.
func (s *Session) handshake(ctx context.Context) error {
	// RFC6241 sec 8.1: both peers must advertise a base capability.  Catch a
	// misconfigured WithCapability before any network I/O.
	if !s.clientCaps.Has(CapNetConf10) && !s.clientCaps.Has(CapNetConf11) {
		return errors.New("client capabilities must include a netconf base capability")
	}

	stop := context.AfterFunc(ctx, func() {
		_ = s.closeTransport()
	})

	err := s.exchangeHello()

	// stop reporting false means the context fired and the transport has
	// been (or is being) closed, even if the exchange itself got through.
	// That is why the exchange failed then, and what the device sent until
	// then is still what it sent.
	if !stop() {
		failed := &HelloError{Err: context.Cause(ctx)}
		var said *HelloError
		if errors.As(err, &said) {
			failed.Capabilities, failed.RPCErrors, failed.Text = said.Capabilities, said.RPCErrors, said.Text
		}
		return failed
	}
	return err
}

// helloLastWords is how long a device that could not be said hello to is
// given to say why.
const helloLastWords = time.Second

// exchangeHello sends the hello of the client and reads that of the device.
// What it fails with is a *HelloError.
func (s *Session) exchangeHello() error {
	clientMsg := helloMessage{
		Capabilities: slices.Collect(s.clientCaps.All()),
	}
	payload, err := xml.Marshal(&clientMsg)
	if err != nil {
		return &HelloError{Err: fmt.Errorf("failed to encode hello message: %w", err)}
	}
	// A device that refuses says why and hangs up, and can be gone before
	// it was said hello to.  So a hello that could not be written is not
	// the end: what the device sent is read all the same, and is in the
	// error.
	writeErr := s.writeMsg(payload)
	if writeErr != nil {
		writeErr = fmt.Errorf("failed to write hello message: %w", writeErr)
		// what a device that hung up had to say is there already.  One
		// that says nothing is not waited for until the context ends.
		defer time.AfterFunc(helloLastWords, func() { _ = s.closeTransport() }).Stop()
	}

	r, err := s.msgReader()
	if err != nil {
		if writeErr != nil {
			return &HelloError{Err: writeErr}
		}
		return &HelloError{Err: fmt.Errorf("failed to get hello message reader: %w", err)}
	}
	defer func() {
		_ = r.Close()
	}()
	r.setLimit(s.maxMessageSize)

	raw, readErr := io.ReadAll(r)
	said, err := readGreeting(raw)
	switch {
	case writeErr != nil:
		if said.hello == nil && len(said.errors) == 0 {
			said.text = strings.TrimSpace(string(raw))
		}
		return said.failed(writeErr)
	case errors.Is(readErr, io.ErrUnexpectedEOF):
		// what arrived of a message that did not end is what the device
		// printed, if anything
		if said.hello == nil && len(said.errors) == 0 {
			said.text = strings.TrimSpace(string(raw))
		}
		return said.failed(fmt.Errorf("the device closed the connection before it said hello, "+
			"as a wrong port does, and a device that has NETCONF not enabled: %w", readErr))
	case readErr != nil:
		return said.failed(fmt.Errorf("failed to read server hello message: %w", readErr))
	case err != nil && said.hello == nil:
		// What is not XML behind a hello is not looked at: a session opens
		// on the hello.  Without one it is why there is none.
		return said.failed(fmt.Errorf("failed to read server hello message: %w", err))
	case said.hello == nil && len(said.errors) > 0:
		return said.failed(errors.New("the device sent errors in place of a hello"))
	case said.hello == nil:
		return said.failed(errors.New("the device sent no hello"))
	case refuses(said.errors):
		return said.failed(errors.New("the device sent errors with its hello"))
	case said.hello.SessionID == 0:
		return said.failed(errors.New("server did not return a session-id"))
	case len(said.hello.Capabilities) == 0:
		return said.failed(errors.New("server did not return any capabilities"))
	}
	if len(said.errors) > 0 {
		s.logger.Warn("the device warned with its hello",
			"count", len(said.errors),
			"first", logValue(said.errors[0].Message))
	}
	serverMsg := *said.hello

	s.serverCaps = NewCapabilitySet(serverMsg.Capabilities...)
	s.sessionID = serverMsg.SessionID
	s.byteRefs = s.serverCaps.Has(junosCapability)

	// RFC6241 sec 8.1: peers must share a common base protocol version.
	base11 := s.serverCaps.Has(CapNetConf11) && s.clientCaps.Has(CapNetConf11)
	base10 := s.serverCaps.Has(CapNetConf10) && s.clientCaps.Has(CapNetConf10)
	if !base11 && !base10 {
		return said.failed(errors.New("no common netconf base capability with server"))
	}

	// both peers speak base:1.1, which means chunked framing from here on
	if base11 {
		s.tr.Upgrade()
		// to Junos one operation is on its way at a time there, see send
		if s.serverCaps.Has(junosCapability) {
			s.turn = make(chan struct{}, 1)
		}
	}

	return nil
}

// SessionID returns the current session ID exchanged in the hello messages.
// Will return 0 if there is no session ID.
func (s *Session) SessionID() uint64 {
	return s.sessionID
}

// ClientCaps will return the capabilities initialized with the session.
func (s *Session) ClientCaps() *CapabilitySet {
	return &s.clientCaps
}

// ServerCaps will return the capabilities returned by the server in
// it's hello message.
func (s *Session) ServerCaps() *CapabilitySet {
	return &s.serverCaps
}

// HasNotifHandler reports whether the session was opened with a handler for
// notifications, see WithNotifHandler.  A session without one skips the
// notifications it is sent, and gives up on a device that sends a hundred
// in a row.
func (s *Session) HasNotifHandler() bool {
	return s.notif != nil
}

// Done returns a channel that is closed when the session is over, either
// because Close was called or because the transport died.  Long-lived callers
// use it to notice a dead session and reconnect.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Err reports why the session ended.  It returns nil while the session is
// alive and after a session ended by Close.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// fail records why the session is ending.  Only the first reason counts:
// what it sets off afterwards, such as the read that fails once the
// transport is closed, is a consequence.  After Close there is nothing to
// explain.
func (s *Session) fail(err error) {
	if s.replies.isClosing() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// stalled ends the session over a message that stopped arriving.  Closing
// the transport is what gets the receive loop off its read.
func (s *Session) stalled() {
	s.fail(s.errStalled())
	_ = s.closeTransport()
}

// errStalled is what a session ends with over a message that stopped
// arriving.
func (s *Session) errStalled() error {
	return fmt.Errorf("%w: no byte of it for %s", ErrMessageStalled, s.msgTimeout)
}

// closedErr is the error for an operation that cannot complete because the
// session is over.
func (s *Session) closedErr() error {
	if err := s.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return ErrClosed
}

func (s *Session) closeTransport() error {
	s.trCloseOnce.Do(func() {
		s.trCloseErr = s.tr.Close()
		close(s.trClosed)
	})
	return s.trCloseErr
}

// buffered returns a message that was read whole the way it is handed on:
// repaired, when the session was opened with a function for that, and from
// a Junos device with its text put back together.
func (s *Session) buffered(msg []byte) []byte {
	if s.repair != nil {
		msg = s.repair(msg)
	}
	if s.byteRefs {
		return joinByteRefs(msg)
	}
	return msg
}

// msgReader returns a reader for the next inbound message.  Its limit is
// still to be set.
func (s *Session) msgReader() (*boundedReader, error) {
	r, err := s.tr.MsgReader()
	if err != nil {
		return nil, err
	}
	return &boundedReader{r: r, left: -1, closeTransport: s.closeTransport}, nil
}

// send writes one message to the transport, one sender at a time.
//
// A write can only be interrupted by closing the transport, so it runs aside
// and a caller whose context fires is released while the write finishes (or
// fails) on its own; the next sender waits for it.
//
// Over chunked framing to a Junos device the message waits for its turn
// first: until the answer to the message before it has arrived whole.  Junos
// 26.2 has ended the session over a request that was written right behind
// another, where the two together put the end of the chunks of the second
// across byte 8192.  The writer keeps that end clear of such a place counted
// from the beginning of its own message only.  Requests that each went out
// when the one before it was answered were all answered.  The beginning of a
// reply would not do for that: Junos sends the start tag of a reply when it
// has the start tag of the request.  A message whose context fires while it
// waits for its turn is not sent.
func (s *Session) send(ctx context.Context, req *pendingReply, payload []byte) error {
	if err := s.takeTurn(ctx); err != nil {
		return err
	}

	select {
	case s.sendSem <- struct{}{}:
	case <-s.done:
		s.endTurn()
		return s.closedErr()
	case <-ctx.Done():
		s.endTurn()
		return ctx.Err()
	}

	// From here on the server may answer, even if we stop waiting.  The turn
	// stays taken until it has, whoever waits for the answer.
	s.replies.markSent(req)

	result := make(chan error, 1)
	go func() {
		defer func() { <-s.sendSem }()

		err := s.writeMsg(payload)
		switch {
		case err == nil:
		case errors.Is(err, errNotSent):
			s.replies.notSent(req)
			// no answer will come to end the turn
			s.endTurn()
		default:
			// Part of the message may be out, so the outbound stream is no
			// longer aligned.  The session is over; say so the same way
			// every other operation will.
			s.fail(fmt.Errorf("failed to write message: %w", err))
			_ = s.closeTransport()
			<-s.done
			err = s.closedErr()
		}
		result <- err
	}()

	select {
	case err := <-result:
		return err
	case <-s.done:
		return s.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// takeTurn waits until no operation is on its way or being answered, on a
// session that has one at a time, see send.
func (s *Session) takeTurn(ctx context.Context) error {
	if s.turn == nil {
		return nil
	}
	select {
	case s.turn <- struct{}{}:
		return nil
	case <-s.done:
		return s.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// endTurn lets the next operation be sent: the one whose turn it was has
// been answered, or did not go out.  It does nothing when no turn is taken.
func (s *Session) endTurn() {
	select {
	case <-s.turn:
	default:
	}
}

// errNotSent marks a write error that left nothing on the wire.
var errNotSent = errors.New("message not sent")

// writeMsg writes payload as one framed message.  The caller must be the
// only writer.
func (s *Session) writeMsg(payload []byte) error {
	w, err := s.tr.MsgWriter()
	if err != nil {
		return fmt.Errorf("failed to get message writer: %w", err)
	}

	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		return err
	}

	if err := w.Close(); err != nil {
		// The framing refuses a payload it cannot carry before emitting
		// any of it.
		if errors.Is(err, transport.ErrEndOfMsgInPayload) {
			return fmt.Errorf("%w: %w", errNotSent, err)
		}
		return fmt.Errorf("failed to flush message: %w", err)
	}
	return nil
}

func (s *Session) nextMessageID() string {
	return strconv.FormatUint(s.seq.Add(1), 10)
}

// Stream issues an operation like ExecReply does and returns the reply as a
// stream read directly from the transport, for replies too large to hold in
// memory.
//
// The session cannot process anything else until the returned Message is
// closed (Decode closes it for you).  A message that is never closed stalls
// every other operation and notification on this session.
//
// The context covers the wait for the reply to begin.  Reading it is not
// bounded by anything, and closing the message reads what is left of the
// reply first, so there is one way to give up on a reply part of the way:
// close the session.
//
// Nobody looks inside a streamed reply.  A device that answers with an
// rpc-error sends it in place of the data, for the reader to find, and text
// that is not ASCII is the way a Junos device writes it, see "Text from
// Junos" in the package documentation.
func (s *Session) Stream(ctx context.Context, op any) (*Message, error) {
	msg, traced, err := s.do(ctx, asRPC(op), true, false)
	if err != nil {
		return nil, err
	}
	// the beginning of the reply is all the session sees of it
	s.traceDone(traced, 0, nil)
	return msg, nil
}

// asRPC returns the rpc message for an operation: the operation itself when
// the caller has built the message, for the sake of its attributes.
func asRPC(op any) *RPC {
	if rpc, ok := op.(*RPC); ok {
		return rpc
	}
	return NewRPC(op)
}

// do sends an operation and waits for its reply to arrive.  The operation is
// reported to the trace of the session from the moment there is a message to
// send: traced is for the report of its end, which do makes itself when it
// fails and leaves to its caller, who knows what the reply holds, when it
// does not.
func (s *Session) do(ctx context.Context, rpc *RPC, stream, internal bool) (msg *Message, traced OperationInfo, err error) {
	// The bound of the session is for a caller that brings none.  A context
	// with a deadline is taken at its word, before the bound or after it.
	if _, ok := ctx.Deadline(); !ok && s.opTimeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opTimeout)
		defer cancel()
	}

	// A context that is over before we start must not get the rpc out: the
	// wait for the send slot below is a draw it could lose.
	if err := ctx.Err(); err != nil {
		return nil, traced, err
	}

	// The caller keeps its message, and ours gets a message-id of the
	// session's.  One the caller chose could be that of a request given up
	// on, whose reply may still come, or the next number of the session.
	rpc = rpc.Clone()
	rpc.MessageID = s.nextMessageID()

	// encoding/xml writes attribute names verbatim, so a malformed name
	// would rewrite the message.
	for _, attr := range rpc.Attributes {
		if !validAttrName(attr.Name) {
			return nil, traced, fmt.Errorf("invalid rpc attribute name %q", attr.Name.Local)
		}
	}

	// Encode before touching the transport: an operation that fails to
	// marshal must not leave a truncated message on the wire.
	payload, err := xml.Marshal(rpc)
	if err != nil {
		return nil, traced, fmt.Errorf("failed to encode rpc: %w", err)
	}

	traced = s.traceStart(rpc.MessageID, payload)
	defer func() {
		if err != nil {
			s.traceDone(traced, 0, err)
		}
	}()

	req := &pendingReply{
		msg:    make(chan *Message, 1),
		ctx:    ctx,
		stream: stream,
	}
	select {
	case <-s.done:
		return nil, traced, s.closedErr()
	default:
	}
	if err := s.replies.register(rpc.MessageID, req, internal); err != nil {
		return nil, traced, err
	}
	// Forget the operation if we stop waiting before its reply arrives.
	defer s.replies.forget(rpc.MessageID, req)

	if err := s.send(ctx, req, payload); err != nil {
		return nil, traced, err
	}

	select {
	case msg := <-req.msg:
		return msg, traced, nil
	case <-ctx.Done():
		return nil, traced, ctx.Err()
	case <-s.done:
		// The reply may have been delivered just before the session ended.
		select {
		case msg := <-req.msg:
			return msg, traced, nil
		default:
			return nil, traced, s.closedErr()
		}
	}
}

// ExecReply issues an rpc message with op as the body and returns the
// buffered reply.  op is either a value that marshals to the operation's XML
// or a string or []byte holding the literal XML.  To put attributes on the
// <rpc> element pass the message itself, built with NewRPC.
//
// A context that fires stops the wait for the reply, not the operation: the
// rpc may already be on its way and still be carried out by the device.
//
// RPC errors in the reply are returned as an RPCErrors error, except for
// those with warning severity which are available as Reply.Warnings.  The
// reply is returned along with RPCErrors, so what else the device sent can
// still be read.  It is also returned along with a SyntaxError, the error of
// a reply that has the text of an error in it and is not XML: Reply.Raw is
// what the device sent.  The RPC errors that stand in front of where such a
// reply stops being XML are in that error too, so errors.As and errors.Is
// find the refusal of the device.  With any other error the reply is nil.
func (s *Session) ExecReply(ctx context.Context, op any) (*Reply, error) {
	return s.exec(ctx, op, false)
}

func (s *Session) exec(ctx context.Context, op any, internal bool) (*Reply, error) {
	msg, traced, err := s.do(ctx, asRPC(op), false, internal)
	if err != nil {
		return nil, err
	}
	reply, err := parseReply(msg.body)
	// the errors of the reply, alone or in front of where it is not XML
	var rpcErrs RPCErrors
	if errors.As(err, &rpcErrs) {
		for i := range rpcErrs {
			rpcErrs[i].request = traced.Name
		}
	}
	if reply != nil {
		for i := range reply.Warnings {
			reply.Warnings[i].request = traced.Name
		}
	}
	s.traceDone(traced, len(msg.body), err)
	return reply, err
}

// Exec issues a rpc message with `op` as the body and decodes the response into
// a pointer at `reply`. The Reply must include the full <rpc-reply> structure.
// See ExecReply for how op is encoded and RPC errors are reported.
//
// Exec has nowhere to return warnings, so it logs that there were some.  Use
// ExecReply to get them.
func (s *Session) Exec(ctx context.Context, op, reply any) error {
	r, err := s.ExecReply(ctx, op)
	if err != nil {
		return err
	}
	if len(r.Warnings) > 0 {
		s.logger.Warn("operation succeeded with warnings",
			"count", len(r.Warnings),
			"first", logValue(strings.TrimSpace(r.Warnings[0].Message)))
	}
	if reply == nil {
		return nil
	}
	return r.Decode(reply)
}

// Close will gracefully close the sessions first by sending a `close-session`
// operation to the remote and then closing the underlying transport.  If the
// given context has no deadline, DefaultCloseTimeout is applied so an
// unresponsive server cannot hang Close forever.
//
// The session is over when Close returns, whatever it returns.  An error
// says the close was not a clean one: the device refused close-session or
// did not acknowledge it before the context fired.  Close is safe to call
// more than once and on a session that has already ended; only the first
// call can return an error.
//
// Operations started after Close has been called fail with ErrClosed.
func (s *Session) Close(ctx context.Context) error {
	var closeErr error

	s.closeOnce.Do(func() {
		// From here on new operations are refused and the server hanging up
		// is the expected end of the session.
		s.replies.close()

		s.notif.stop()

		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, DefaultCloseTimeout)
			defer cancel()
		}

		type closeSession struct {
			XMLName xml.Name `xml:"close-session"`
		}

		// Report a close-session failure but still close the underlying
		// transport.  That includes a device that did not acknowledge it in
		// time: the session ends all the same, but not cleanly.  Errors
		// caused by the remote hanging up on us or an already-dead session
		// are expected here and not reported.  With a streamed reply still
		// open the reply to close-session could never be read, so don't
		// wait for one.
		if !s.streaming.Load() {
			if _, err := s.exec(ctx, &closeSession{}, true); err != nil &&
				!errors.Is(err, ErrClosed) &&
				!errors.Is(err, io.EOF) {
				closeErr = fmt.Errorf("close-session: %w", err)
			}
		}

		// Close the connection and ignore errors if the remote side hung up first.
		if err := s.closeTransport(); err != nil &&
			!errors.Is(err, net.ErrClosed) &&
			!errors.Is(err, io.EOF) &&
			!errors.Is(err, syscall.EPIPE) {
			closeErr = errors.Join(closeErr, err)
		}

		// A closed transport ends the receive loop (close-is-cancel); wait
		// for it so no goroutine outlives a completed Close.
		<-s.done
	})

	return closeErr
}
