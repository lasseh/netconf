package netconf

import (
	"log/slog"
	"time"
)

type sessionConfig struct {
	clientCaps     []string
	logger         *slog.Logger
	notifHandler   NotifHandler
	notifQueue     NotifQueue
	opTimeout      time.Duration
	maxMessageSize int64
	repair         func([]byte) []byte
	msgTimeout     time.Duration
	trace          Trace
}

// SessionOption configures a session when it is opened.
type SessionOption interface {
	apply(*sessionConfig)
}

type capabilityOpt []string

func (o capabilityOpt) apply(cfg *sessionConfig) {
	cfg.clientCaps = []string(o)
}

// WithCapability sets the capabilities the client advertises in the hello
// exchange, replacing DefaultCapabilities.  They must include a base
// capability (CapNetConf10 or CapNetConf11); leaving out CapNetConf10
// refuses devices without chunked framing.
func WithCapability(capabilities ...string) SessionOption {
	return capabilityOpt(capabilities)
}

type loggerOpt struct {
	logger *slog.Logger
}

func (o loggerOpt) apply(cfg *sessionConfig) {
	cfg.logger = o.logger
}

// WithLogger sets a custom slog.Logger for the session.  If not provided,
// slog.Default() is used; nil turns logging off.
//
// The session logs little.  At warn level: a session that ended without
// Close, a message it skipped because it was malformed or nobody had asked
// for it, and warnings in a reply that Exec had no way to return.  At debug
// level: the late reply to an operation its caller had given up on.
func WithLogger(logger *slog.Logger) SessionOption {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return loggerOpt{logger: logger}
}

type notifHandlerOpt struct {
	handler NotifHandler
}

func (o notifHandlerOpt) apply(cfg *sessionConfig) {
	cfg.notifHandler = o.handler
}

// WithNotifHandler sets the function that receives the notifications of
// the session, see NotifHandler for how they are delivered.  It has to be
// given when the session is opened, before anything subscribes:
// rpc.CreateSubscription fails with ErrNoNotifHandler on a session without
// one.  A notification that arrives all the same, after a subscription made
// with literal XML, is logged and skipped like any message nobody asked for.
func WithNotifHandler(handler NotifHandler) SessionOption {
	return notifHandlerOpt{handler: handler}
}

type notifQueueOpt NotifQueue

func (o notifQueueOpt) apply(cfg *sessionConfig) {
	cfg.notifQueue = NotifQueue(o)
}

// WithNotifQueue bounds the notifications waiting for the handler and says
// what happens when the bound is reached, see NotifQueue.
func WithNotifQueue(limit NotifQueue) SessionOption {
	return notifQueueOpt(limit)
}

type opTimeoutOpt time.Duration

func (o opTimeoutOpt) apply(cfg *sessionConfig) {
	cfg.opTimeout = time.Duration(o)
}

// WithOperationTimeout bounds the operations on the session that bring no
// deadline of their own: the wait for the reply is given up after d, as if
// the context passed to the operation had that timeout.  A context with a
// deadline is taken at its word, whether that is before d or after it, so
// one operation that needs longer, a commit on a large configuration, gets
// its time from its context.  The other side of that: an operation given a
// context with a late deadline, one that belongs to a whole request, is
// not bounded by d.  The default is no bound, so an operation without a
// deadline waits for as long as the device stays connected.
//
// The timeout covers sending the operation and waiting for its reply to
// arrive.  It does not cover reading a reply requested with Stream.
func WithOperationTimeout(d time.Duration) SessionOption {
	return opTimeoutOpt(d)
}

// WithMaxMessageSize limits the size in bytes of a message the session
// buffers: the server hello, replies and notifications.  A larger message
// ends the session with ErrMessageTooLarge, so a misbehaving peer cannot
// exhaust memory.  The default is DefaultMaxMessageSize; zero or less removes
// the limit.  Replies requested with Stream are not buffered and not
// limited.
func WithMaxMessageSize(n int64) SessionOption {
	return maxMessageSizeOpt(n)
}

type maxMessageSizeOpt int64

func (o maxMessageSizeOpt) apply(cfg *sessionConfig) {
	cfg.maxMessageSize = int64(o)
}

type replyRepairOpt func([]byte) []byte

func (o replyRepairOpt) apply(cfg *sessionConfig) {
	cfg.repair = o
}

// WithReplyRepair has the session run fn on every reply and notification it
// buffers, before it reads any of it, and go on with what fn returns.  It is
// for a device whose messages are not XML as they come: StripInvalidChars is
// a function for the usual case, a control character in text the device
// passes on, which fails every way of reading the message with a
// SyntaxError.  Without the option no message is changed.
//
// fn gets the message as the device sent it and may change it in place.
// For a Junos device the session puts text that is not ASCII back together
// after fn has run, so fn sees the character references Junos writes for
// the bytes of such text, see "Text from Junos" in the package
// documentation.  The hello of the device and a reply read with
// Session.Stream do not go through fn.  Neither does a message whose start
// tag cannot be read: the session reads that tag first, to know what the
// message is, and skips a message that has none.
func WithReplyRepair(fn func(msg []byte) []byte) SessionOption {
	return replyRepairOpt(fn)
}

type messageTimeoutOpt time.Duration

func (o messageTimeoutOpt) apply(cfg *sessionConfig) {
	cfg.msgTimeout = time.Duration(o)
}

// WithMessageTimeout ends the session when a message from the device stops
// arriving: one that has begun and of which no further byte comes for d.  A
// message has begun with its first byte that is not white space.  That is
// a byte of the message and not of its framing: a device that sends the
// size of a chunk and then nothing has not begun one.
// The session then ends with ErrMessageStalled, which Session.Err reports,
// and what was waiting on it fails with ErrClosed.  Without the option, or
// with a d of zero, the session waits on such a message for ever:
// operations time out one after the other, Done is never closed, and no
// keepalive notices as long as the device answers that.
//
// The time starts again with every byte, so a message that keeps coming is
// never cut short, however large.  The silence between two messages is not
// timed, and neither is a reply read with Session.Stream.
//
// Mind what a device does between the start of a reply and its end.  Junos
// sends the start tag of a reply at once and the rest when it has done
// what it was asked: to Junos a message that has begun and does not go on
// is a request it is still working on.  So d has to be longer than the
// slowest operation on the session takes, a commit as a rule, and an
// operation that takes longer loses the session and not only its reply.
func WithMessageTimeout(d time.Duration) SessionOption {
	return messageTimeoutOpt(d)
}

type traceOpt struct {
	trace *Trace
}

func (o traceOpt) apply(cfg *sessionConfig) {
	if o.trace != nil {
		cfg.trace = *o.trace
	}
}

// WithTrace has the session report every operation as it starts and as it
// ends, and every notification, to the functions of trace, see Trace.  It
// is the place for a timer, a counter or a log line per operation, also for
// the operations that the typed operations and junos.Change.Apply issue
// underneath.  The functions are taken as they are when the session is
// opened; a nil trace reports nothing.
func WithTrace(trace *Trace) SessionOption {
	return traceOpt{trace: trace}
}
