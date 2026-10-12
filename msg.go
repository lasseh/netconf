package netconf

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// RPC maps the xml value of <rpc> in RFC6241.
type RPC struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc"`

	// Managed by the session.  Will be overwritten when sent on the wire.
	MessageID string `xml:"message-id,attr"`

	// User-defined attributes (e.g. xmlns:ex="...").  Per RFC6241 sec 7.3, these
	// must be preserved and reflected in the associated <rpc-reply>.
	Attributes []xml.Attr `xml:",attr"`

	// The inner XML of the RPC message (e.g. <get-config>, <edit-config>)
	Operation any `xml:",innerxml"` // The operation payload (e.g. <get-config>)
}

// RPCOption represents an option that can be passed to NewRPC to customize the
// created RPC message.
type RPCOption interface {
	apply(*RPC)
}

type attributesOpt []xml.Attr

func (o attributesOpt) apply(rpc *RPC) {
	rpc.Attributes = append(rpc.Attributes, o...)
}

// WithAttributes adds the given XML attributes to be added to the <rpc>
// element.  NETCONF servers are required to reflect these attributes in the
// associated <rpc-reply>.
func WithAttributes(attrs ...xml.Attr) RPCOption { return attributesOpt(attrs) }

// NewRPC creates a new RPC message with the given operation as the body.
func NewRPC(op any, opts ...RPCOption) *RPC {
	rpc := &RPC{
		Operation: op,
	}
	for _, opt := range opts {
		opt.apply(rpc)
	}
	return rpc
}

// Clone does a shallow copy of the RPC.  Operation is not deeply copied.
func (r *RPC) Clone() *RPC {
	return &RPC{
		MessageID:  r.MessageID,
		Attributes: slices.Clone(r.Attributes),
		Operation:  r.Operation,
	}
}

// RPCReply maps the envelope of an <rpc-reply> in RFC6241, for embedding in
// a struct a reply is decoded into.  The RPC errors of a reply are not part
// of it: Exec and ExecReply return them as an error.
type RPCReply struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc-reply"`

	// The message-id must match that of the associated <rpc>
	MessageID string `xml:"message-id,attr"`

	// Additional attributes on the <rpc-reply>.
	Attributes []xml.Attr `xml:",attr"`
}

// ErrSeverity represents the error-severity of a NETCONF RPC error.  The
// severities, types and tags are defined in RFC6241 sec 4.3 and appendix A.
type ErrSeverity string

// The severities of an RPC error.  Only SevError fails an operation by the
// RFC; this package also fails on anything that is not SevWarning.
const (
	SevError   ErrSeverity = "error"
	SevWarning ErrSeverity = "warning"
)

// ErrType represents the error-type of a NETCONF RPC error.
type ErrType string

// The layers an RPC error can come from.
const (
	ErrTypeTransport ErrType = "transport"
	ErrTypeRPC       ErrType = "rpc"
	ErrTypeProtocol  ErrType = "protocol"
	ErrTypeApp       ErrType = "application"
)

// ErrTag represents the error-tag of a NETCONF RPC error.
//
// A tag can be given to errors.Is to ask whether an operation failed with
// it:
//
//	if errors.Is(err, netconf.ErrLockDenied) { ... }
type ErrTag string //nolint:errname // a tag is a target for errors.Is, not an error anybody returns

// Error is here to make a tag a target for errors.Is, see ErrTag.
func (t ErrTag) Error() string {
	return "netconf error tag " + string(t)
}

// The error tags of RFC6241 appendix A.
const (
	ErrInUse                 ErrTag = "in-use"
	ErrInvalidValue          ErrTag = "invalid-value"
	ErrTooBig                ErrTag = "too-big"
	ErrMissingAttribute      ErrTag = "missing-attribute"
	ErrBadAttribute          ErrTag = "bad-attribute"
	ErrUnknownAttribute      ErrTag = "unknown-attribute"
	ErrMissingElement        ErrTag = "missing-element"
	ErrBadElement            ErrTag = "bad-element"
	ErrUnknownElement        ErrTag = "unknown-element"
	ErrUnknownNamespace      ErrTag = "unknown-namespace"
	ErrAccessDenied          ErrTag = "access-denied"
	ErrLockDenied            ErrTag = "lock-denied"
	ErrResourceDenied        ErrTag = "resource-denied"
	ErrRollbackFailed        ErrTag = "rollback-failed"
	ErrDataExists            ErrTag = "data-exists"
	ErrDataMissing           ErrTag = "data-missing"
	ErrOperationNotSupported ErrTag = "operation-not-supported"
	ErrOperationFailed       ErrTag = "operation-failed"
	ErrPartialOperation      ErrTag = "partial-operation"
	ErrMalformedMessage      ErrTag = "malformed-message"
)

// RPCError maps the xml value of <rpc-error> in RFC6241 sec 6.4.1.
//
// Values are kept without the white space devices put around them.  Path is
// whatever the device calls the place of the error: an XPath expression by
// the RFC, a configuration mode banner such as "[edit system ntp]" on Junos.
// What the fields do not cover is available through Detail.
//
// Junos also reports errors in an element of its own, <xnm:error>, which has
// a message and neither type nor tag.  Such an error is an RPCError with the
// severity error and that message: errors.Is finds no tag in it.  Detail
// gives the rest, the source-daemon among it.
type RPCError struct {
	Type     ErrType     `xml:"error-type"`
	Tag      ErrTag      `xml:"error-tag"`
	Severity ErrSeverity `xml:"error-severity"`
	AppTag   string      `xml:"error-app-tag,omitempty"`
	Path     string      `xml:"error-path,omitempty"`
	Message  string      `xml:"error-message,omitempty"`
	Info     RawXML      `xml:"error-info,omitempty"`

	// inner is the content of the <rpc-error> as received
	inner []byte
	// request is the name of the request the error answers
	request string
}

// UnmarshalXML implements xml.Unmarshaler.
func (e *RPCError) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type fields RPCError
	var v struct {
		fields
		Inner []byte `xml:",innerxml"`
	}
	if err := d.DecodeElement(&v, &start); err != nil {
		return err
	}

	*e = RPCError(v.fields)
	e.inner = v.Inner

	// devices pad values with line breaks (Junos)
	e.Type = ErrType(strings.TrimSpace(string(e.Type)))
	e.Tag = ErrTag(strings.TrimSpace(string(e.Tag)))
	e.Severity = ErrSeverity(strings.TrimSpace(string(e.Severity)))
	e.AppTag = strings.TrimSpace(e.AppTag)
	e.Path = strings.TrimSpace(e.Path)
	e.Message = strings.TrimSpace(e.Message)
	return nil
}

// Detail returns the text of the element called name anywhere inside the
// error, and whether there is one.  That covers what RFC6241 puts in
// error-info (bad-element, bad-attribute, bad-namespace, and session-id
// naming the holder of a lock) as well as elements a device adds: Junos names
// the process that reported an error in source-daemon.
func (e RPCError) Detail(name string) (string, bool) {
	for _, raw := range [][]byte{e.inner, e.Info} {
		found, err := findElements(raw, name, true)
		if err == nil && len(found) > 0 {
			return strings.TrimSpace(found[0].Text), true
		}
	}
	return "", false
}

// Error describes the error with where it is, when the device says so:
//
//	netconf error: application invalid-value: [edit system ntp] 'server 192.0.2.1': server address is a local address
func (e RPCError) Error() string {
	parts := make([]string, 0, 4)

	if e.Severity == SevWarning {
		parts = append(parts, "netconf warning")
	} else {
		parts = append(parts, "netconf error")
	}

	if kind := strings.TrimSpace(string(e.Type) + " " + string(e.Tag)); kind != "" {
		parts = append(parts, kind)
	}

	where := strings.TrimSpace(e.Path)
	if bad, ok := e.Detail("bad-element"); ok && bad != "" {
		where = strings.TrimSpace(where + " '" + bad + "'")
	}
	if where != "" {
		parts = append(parts, where)
	}

	// trimmed again for errors that were not decoded but put together
	message := strings.TrimSpace(e.Message)
	if message == "" {
		// Junos has errors without one, and then names who reported it
		if daemon, ok := e.Detail("source-daemon"); ok && daemon != "" {
			message = "reported by " + daemon
		}
	}
	if message != "" {
		parts = append(parts, message)
	}
	return strings.Join(parts, ": ")
}

// Is makes errors.Is(err, tag) report whether this is an error with that
// tag, see ErrTag.
//
// Not every device says in the tag what went wrong.  A target with a method
// MatchRPCError(RPCError) bool is asked whether this is the error it
// stands for: that is how a package for the devices of one vendor matches
// an error by its message, see junos.ErrPermissionDenied.
func (e RPCError) Is(target error) bool {
	switch target := target.(type) {
	case ErrTag:
		return target == e.Tag
	case interface{ MatchRPCError(RPCError) bool }:
		return target.MatchRPCError(e)
	}
	return false
}

// Request returns the name of the request this error answers: the element
// the <rpc> held, without a prefix.  Exec and ExecReply set it on the
// errors and the warnings of a reply.  It is empty for an error from
// anywhere else: the hello, a reply read with Stream, a value made by hand.
// A device may name the request in the error,
// and then the two together say what the error means: Junos answers a
// request it does not know with a syntax error whose bad element is the
// request.
func (e RPCError) Request() string { return e.request }

// RPCErrors represents a list of RPCError returned from a NETCONF rpc-reply.
type RPCErrors []RPCError

func (errs RPCErrors) Error() string {
	if len(errs) == 0 {
		return ""
	}

	if len(errs) == 1 {
		return errs[0].Error()
	}

	var sb strings.Builder
	sb.WriteString("multiple netconf errors:\n")
	for i := range errs {
		if i > 0 {
			sb.WriteRune('\n')
		}
		sb.WriteString(errs[i].Error())
	}
	return sb.String()
}

func (errs RPCErrors) Unwrap() error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	unboxedErrs := make([]error, len(errs))
	for i := range errs {
		unboxedErrs[i] = errs[i]
	}
	return errors.Join(unboxedErrs...)
}

// Notification maps the xml value of <notification> in RFC5277.
//
// Message.Decode fills one from the message a notification handler is given.
// To encoding/xml it is a struct like any other: one that embeds it, with
// fields of its own for what the device sends, decodes as a whole.
type Notification struct {
	XMLName   xml.Name  `xml:"urn:ietf:params:xml:ns:netconf:notification:1.0 notification"`
	EventTime time.Time `xml:"eventTime"`

	// Content is what the notification reports: the element next to
	// eventTime, which is whatever the device has to say.
	Content NotificationContent `xml:",any"`
}

// receivedNotification is a Notification that keeps its content as the
// device sent it, which is what Message.Decode decodes a Notification as.
// The method is not on Notification: a struct that embeds a type with an
// UnmarshalXML is decoded by that method alone, and its own fields stay
// empty.
type receivedNotification Notification

// UnmarshalXML implements xml.Unmarshaler.
func (n *receivedNotification) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v struct {
		Notification
		Inner []byte `xml:",innerxml"`
	}
	if err := d.DecodeElement(&v, &start); err != nil {
		return err
	}
	*n = receivedNotification(v.Notification)
	// encoding/xml checks a name it reaches through an embedded struct and
	// does not set it
	n.XMLName = start.Name

	// The content as it was written, for its Decode: the last of the
	// elements that are not the time, which is the one encoding/xml has
	// left in Content.
	children, err := elements(v.Inner)
	if err != nil {
		return err
	}
	contents := 0
	for _, child := range slices.Backward(children) {
		if child.name == "eventTime" {
			continue
		}
		contents++
		if contents == 1 {
			// a copy: Inner, which the caller may append to and write
			// into, is a piece of the same buffer
			n.Content.raw = bytes.Clone(child.raw)
			n.Content.ns = (*scope)(nil).inside(start.Attr)
		}
	}
	// encoding/xml has the name and the content of the last element, and
	// the attributes of every one
	if contents > 1 {
		var last struct {
			Attr []xml.Attr `xml:",any,attr"`
		}
		if err := decodeIn(n.Content.ns, n.Content.raw, &last); err != nil {
			return err
		}
		n.Content.Attr = last.Attr
	}
	return nil
}

// NotificationContent is the element of a notification that says what
// happened: a <netconf-config-change> of RFC6470, or an element of the
// device's own, such as the <syslog> Junos sends.
type NotificationContent struct {
	// XMLName is the name of the element.  It is empty for a notification
	// that has nothing but its eventTime.
	XMLName xml.Name

	// Attr is the attributes of the element, as encoding/xml reports them,
	// see Element.Attr.
	Attr []xml.Attr `xml:",any,attr"`

	// Inner is what the element contains, as the device sent it.
	Inner []byte `xml:",innerxml"`

	// raw is the element as the device sent it, and ns what the
	// <notification> around it declares.  Both are there for the content of
	// a Notification that Message.Decode filled.
	raw []byte
	ns  *scope
}

// Decode decodes the content into v, which must match its element the way
// it would for xml.Unmarshal.  The namespaces that the <notification>
// declares are in force, as they were in the message.
//
// That holds for the content of a Notification that Message.Decode filled.
// A content that came another way, one decoded with encoding/xml alone or
// put together by hand, is decoded as the element XMLName names with Inner
// in it.  Of Attr it has the namespaces that are declared there, which what
// is in Inner may use, and no other attribute.
func (c NotificationContent) Decode(v any) error {
	if c.XMLName.Local == "" {
		return errors.New("netconf: the notification has no content")
	}

	raw := c.raw
	if raw == nil {
		var err error
		if raw, err = c.again(); err != nil {
			return err
		}
	}
	if err := decodeIn(c.ns, raw, v); err != nil {
		return fmt.Errorf("failed to decode notification: %w", err)
	}
	return nil
}

// again returns the element around what it contained, for a content that
// has not got it as the device sent it.  The namespaces are declared the way
// Attr says they were: the element keeps the prefix it had when the default
// namespace is another one, so that what is in Inner reads as it did.
//
// One case is a guess.  An element that declares a prefix for its own
// namespace and no default namespace was written with that prefix, or
// without one in a default namespace it inherited: encoding/xml hands over
// the same for both.  It is taken for the second, which is what a content
// inside a notification is, and what it contains without a prefix is put
// into the namespace of the element.
func (c NotificationContent) again() ([]byte, error) {
	var (
		declared   []xml.Attr // the namespaces Attr declares
		byDefault  string     // the default namespace among them
		hasDefault bool
		prefix     string // a prefix for the namespace of the element
	)
	for _, attr := range c.Attr {
		switch {
		case attr.Name.Space == "" && attr.Name.Local == xmlns:
			byDefault, hasDefault = attr.Value, true
		case attr.Name.Space == xmlns:
			declared = append(declared, attr)
			if prefix == "" && attr.Value == c.XMLName.Space {
				prefix = attr.Name.Local
			}
		}
	}

	name := c.XMLName.Local
	switch {
	case hasDefault && byDefault != c.XMLName.Space && prefix != "":
		name = prefix + ":" + name
		declared = append(declared, xml.Attr{Name: xml.Name{Local: xmlns}, Value: byDefault})
	case c.XMLName.Space != "":
		declared = append(declared, xml.Attr{Name: xml.Name{Local: xmlns}, Value: c.XMLName.Space})
	}

	var doc bytes.Buffer
	doc.WriteString("<" + name)
	for _, attr := range declared {
		doc.WriteString(" ")
		if attr.Name.Space != "" {
			doc.WriteString(attr.Name.Space + ":")
		}
		doc.WriteString(attr.Name.Local + `="`)
		if err := xml.EscapeText(&doc, []byte(attr.Value)); err != nil {
			return nil, err
		}
		doc.WriteString(`"`)
	}
	doc.WriteString(">")
	doc.Write(c.Inner)
	doc.WriteString("</" + name + ">")
	return doc.Bytes(), nil
}

// helloMessage maps the xml value of the <hello> message in RFC6241 sec 3.3.
type helloMessage struct {
	XMLName      xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 hello"`
	SessionID    uint64   `xml:"session-id,omitempty"`
	Capabilities []string `xml:"capabilities>capability"`
}

// Message is a message read as a stream, envelope included: the reply
// returned by Session.Stream, or a notification handed to the notification
// handler.
//
// A notification is fully buffered and does not have to be closed (Close
// remains safe to call).  A reply from Session.Stream MUST BE closed, with
// Close or by Decode, which closes it for you, before the session can
// process anything else.
//
// A notification from a Junos device has the characters of its text back,
// like a Reply.  A reply from Session.Stream is as the device sent it.
type Message struct {
	// Additional XML attributes from the envelope (rpc-reply or notification)
	Attributes []xml.Attr // Any other attributes on the envelope

	reader    io.ReadCloser
	body      []byte  // the whole message when it was buffered
	messageID *string // cached message-id attribute
	closed    atomic.Bool
}

// MessageID returns the message-id attribute from the rpc-reply envelope.  If
// the message doesn't have a message-id attribute (for example it is a
// notification), an empty string is returned.
func (d *Message) MessageID() string {
	// check for a cached value first
	if d.messageID != nil {
		return *d.messageID
	}

	for _, attr := range d.Attributes {
		if attr.Name.Local == "message-id" {
			d.messageID = &attr.Value
			return attr.Value
		}
	}

	return ""
}

// Read reads the message, envelope included.  It returns io.EOF at the end
// of the message and ErrClosed once the message was closed.
func (d *Message) Read(p []byte) (n int, err error) {
	if d.closed.Load() {
		return 0, ErrClosed
	}
	if d.reader == nil {
		return 0, io.EOF
	}
	return d.reader.Read(p)
}

// Close releases the message.  It is required for a reply from
// Session.Stream, where it lets the session go on to the next message, and
// optional otherwise.  Reads after Close fail with ErrClosed.
//
// Closing a streamed reply that was not read to its end reads the rest of it
// first, and waits for a Read in progress.  To drop a reply without that,
// close the session.
func (d *Message) Close() error {
	if d.closed.Swap(true) {
		return nil
	}
	if d.reader == nil {
		return nil
	}
	return d.reader.Close()
}

// Decode will decode the response XML into the provided value v and then close
// the message releasing the session to process new messages.
//
// A *Notification gets its Content as the device sent it, see
// NotificationContent.Decode.
func (d *Message) Decode(v any) (err error) {
	defer func() {
		err = errors.Join(err, d.Close())
	}()

	if notif, ok := v.(*Notification); ok {
		v = (*receivedNotification)(notif)
	}

	dec := xml.NewDecoder(d)
	if err := dec.Decode(v); err != nil {
		// a message that was buffered is there to say where
		if d.body != nil {
			err = located(d.body, dec.InputOffset(), err)
		}
		return fmt.Errorf("failed to decode response: %w", err)
	}

	return err
}

// Reply is a buffered rpc-reply as returned by Session.ExecReply.
type Reply struct {
	// Raw is the complete <rpc-reply> document as received from the server,
	// including the envelope and its namespace declarations.
	//
	// One thing differs from what a Junos device sent: text that is not
	// ASCII, which Junos writes a byte at a time, has its characters back.
	// See "Text from Junos" in the package documentation.
	Raw []byte

	// Warnings are the RPC errors of warning severity carried in the reply.
	// They are informational and never fail an operation.
	Warnings RPCErrors
}

// Decode decodes the reply into v, which must match the full <rpc-reply>
// structure.
func (r *Reply) Decode(v any) error {
	d := xml.NewDecoder(bytes.NewReader(r.Raw))
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("failed to decode response: %w", located(r.Raw, d.InputOffset(), err))
	}
	return nil
}

const rpcErrorName = "rpc-error"

// junosErrorTag is how the error element of Junos begins.  It is told by
// how it is written and not by its namespace: that is the namespace of the
// Junos configuration too, where the severities of syslog are elements
// called error and warning, and Junos OS Evolved 23.2 does not declare the
// prefix at all.  A statement of the configuration is written without one.
const junosErrorTag = "<xnm:error"

// parseReply builds a Reply from a buffered rpc-reply, returning its RPC
// errors as an error unless they are all warnings.  The reply comes with
// them: what else the device sent can still be read.  It also comes with the
// error of a reply that cannot be gone through, for what the device sent.
// The RPC errors in front of where such a reply cannot be gone through are
// in that error: the device refused, whatever became of the rest.
//
// RFC6241 puts <rpc-error> directly under <rpc-reply>, but devices also nest
// it in result wrappers (Junos: <commit-results>, <load-configuration-results>)
// so errors are collected at any depth.  They are collected in any namespace
// too: an error written inside a result element inherits the namespace of
// that element.  Everything that is not explicitly a warning fails the
// operation, including errors from non-conforming servers that omit
// <error-severity>.
//
// Junos answers some requests with an error element of its own, <xnm:error>,
// in place of an <rpc-error>, and puts it under <rpc-reply> or inside the
// result.  It is collected like an <rpc-error>.
func parseReply(raw []byte) (*Reply, error) {
	reply := &Reply{Raw: raw}

	// Most replies carry no error at all; don't tokenise those.
	if !bytes.Contains(raw, []byte(rpcErrorName)) && !bytes.Contains(raw, []byte(junosErrorTag)) {
		return reply, nil
	}

	var rpcErrors RPCErrors
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		begin := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return reply, refusedAnd(rpcErrors, fmt.Errorf("failed to parse rpc-reply: %w", located(raw, d.InputOffset(), err)))
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		var rpcErr RPCError
		switch {
		case start.Name.Local == rpcErrorName:
			if err := d.DecodeElement(&rpcErr, &start); err != nil {
				return reply, refusedAnd(rpcErrors, fmt.Errorf("failed to parse rpc-error: %w", located(raw, d.InputOffset(), err)))
			}
		case isJunosError(start, raw[begin:]):
			if err := rpcErr.decodeJunos(d, start); err != nil {
				return reply, refusedAnd(rpcErrors, fmt.Errorf("failed to parse xnm:error: %w", located(raw, d.InputOffset(), err)))
			}
		default:
			continue
		}
		if rpcErr.Severity == SevWarning {
			reply.Warnings = append(reply.Warnings, rpcErr)
		} else {
			rpcErrors = append(rpcErrors, rpcErr)
		}
	}

	if len(rpcErrors) > 0 {
		return reply, rpcErrors
	}
	return reply, nil
}

// refusedAnd returns err, the error of a reply that cannot be gone through
// to its end, behind the RPC errors that were read in front of it, and err
// alone when there are none.
func refusedAnd(rpcErrors RPCErrors, err error) error {
	if len(rpcErrors) == 0 {
		return err
	}
	return fmt.Errorf("%w, and %w", rpcErrors, err)
}

// isJunosError reports whether the element that start begins is the error
// element of Junos.  tag is the reply from where the element begins.
func isJunosError(start xml.StartElement, tag []byte) bool {
	return start.Name.Local == "error" && bytes.HasPrefix(tag, []byte(junosErrorTag))
}

// decodeJunos reads the error element of Junos that start begins into e.
func (e *RPCError) decodeJunos(d *xml.Decoder, start xml.StartElement) error {
	var v struct {
		Message string `xml:"message"`
		Inner   []byte `xml:",innerxml"`
	}
	if err := d.DecodeElement(&v, &start); err != nil {
		return err
	}

	*e = RPCError{Severity: SevError, Message: strings.TrimSpace(v.Message), inner: v.Inner}
	return nil
}

// RawXML is a helper type for getting innerxml content as a byte slice.
type RawXML []byte

// UnmarshalXML implements xml.Unmarshaler.
func (x *RawXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var inner struct {
		Data []byte `xml:",innerxml"`
	}

	if err := d.DecodeElement(&inner, &start); err != nil {
		return err
	}

	*x = inner.Data
	return nil
}

// MarshalXML implements xml.Marshaler.
func (x RawXML) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	inner := struct {
		Data []byte `xml:",innerxml"`
	}{
		Data: []byte(x),
	}
	return e.EncodeElement(&inner, start)
}
