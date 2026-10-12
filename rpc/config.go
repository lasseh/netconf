package rpc

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
	"github.com/lasseh/netconf/internal/xmlutil"
)

// Source selects the configuration source for operations like <copy-config>
// and <validate>: a Datastore, a URL, or an inline Config payload.
type Source interface{ isSource() }

// Target selects the configuration target for operations like <copy-config>:
// a Datastore or a URL.
type Target interface{ isTarget() }

// Datastore represents a NETCONF configuration datastore as defined in
// RFC6241 section 7.1.
type Datastore string

func (Datastore) isSource() {}
func (Datastore) isTarget() {}

// MarshalXML implements xml.Marshaler.  It fails for a name that cannot be
// an XML element name.
func (d Datastore) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if d == "" {
		return fmt.Errorf("datastore name cannot be empty")
	}
	if !xmlutil.ValidNCName(string(d)) {
		return fmt.Errorf("invalid datastore name: %q", d)
	}

	inner := struct {
		Elem string `xml:",innerxml"`
	}{Elem: "<" + string(d) + "/>"}

	// EncodeElement(nil, ...) creates a self-closing tag <running/>
	return e.EncodeElement(&inner, start)
}

const (
	// Running configuration datastore. Required by RFC6241.
	Running Datastore = "running"

	// Candidate configuration configuration datastore.  Supported with the
	// `:candidate` capability defined in RFC6241 section 8.3.
	Candidate Datastore = "candidate"

	// Startup configuration configuration datastore.  Supported with the
	// `:startup` capability defined in RFC6241 section 8.7.
	Startup Datastore = "startup"
)

// URL is a configuration source or target given by its location, for devices
// with the `:url` capability defined in RFC6241 section 8.8.
type URL string

func (URL) isSource() {}
func (URL) isTarget() {}

// MarshalXML implements xml.Marshaler.
func (u URL) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	v := struct {
		URL string `xml:"url"`
	}{string(u)}
	return e.EncodeElement(&v, start)
}

// Config is an inline configuration payload used as an operation source.
// Payload may be a raw XML string, []byte, or any xml-marshalable value.
type Config struct {
	Payload any
}

func (Config) isSource() {}

// MarshalXML encodes the caller's start element (e.g. <source>) containing a
// <config> child wrapping Payload.
func (c Config) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	if err := xmlutil.EncodePayload(e, xml.StartElement{Name: xml.Name{Local: "config"}}, c.Payload); err != nil {
		return err
	}
	return e.EncodeToken(start.End())
}

// payload is caller supplied XML content for an element: a string or []byte
// holding literal XML, or any other value which is marshaled as a child
// element the way encoding/xml would marshal it on its own.
type payload struct {
	v any
}

func (p payload) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return xmlutil.EncodePayload(e, start, p.v)
}

// GetConfig implements the <get-config> rpc operation defined in [RFC6241 7.1].
// `source` is the datastore to query.
//
// [RFC6241 7.1]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.1
type GetConfig struct {
	Source       Datastore
	Filter       Filter
	WithDefaults WithDefaultsMode
}

// MarshalXML implements xml.Marshaler.
func (op GetConfig) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName      xml.Name             `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 get-config"`
		Source       Datastore            `xml:"source"`
		Filter       Filter               `xml:"filter,omitempty"`
		WithDefaults *withDefaultsElement `xml:",omitempty"`
	}{
		Source: op.Source,
		Filter: op.Filter,
	}

	if op.WithDefaults != "" {
		req.WithDefaults = &withDefaultsElement{Mode: op.WithDefaults}
	}

	return e.Encode(&req)
}

// Exec issues the operation on session and returns the configuration: the
// content of the reply's <data> element.
func (op GetConfig) Exec(ctx context.Context, session *netconf.Session) ([]byte, error) {
	return data(ctx, session, op, "get-config")
}

// GetConfigReply is the reply to a <get-config> operation, for decoding a
// reply by hand, such as one streamed with netconf.Session.Stream.
type GetConfigReply struct {
	// Config is the content of the <data> element.
	Config []byte
}

// UnmarshalXML implements xml.Unmarshaler.  A reply without a <data> element
// fails with the RPC errors it carries instead, if any.
func (r *GetConfigReply) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var reply struct {
		Errors netconf.RPCErrors `xml:"rpc-error"`
		Data   *struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"data"`
	}
	if err := d.DecodeElement(&reply, &start); err != nil {
		return err
	}

	// An empty configuration comes as an empty <data>; no <data> at all is
	// a reply to something else, most likely an error.  Nothing has looked
	// for those yet when the reply was not obtained through Exec.
	if reply.Data == nil {
		if len(reply.Errors) > 0 {
			return reply.Errors
		}
		return fmt.Errorf("get-config: %w: <data>", netconf.ErrElementNotFound)
	}
	r.Config = reply.Data.Inner
	return nil
}

// DefaultOperation defines the strategies for merging configuration in a
// `<edit-config> operation`.
type DefaultOperation string

const (
	// MergeConfig configuration elements are merged together at the level at
	// which this specified.  Can be used for config elements as well as default.
	MergeConfig DefaultOperation = "merge"

	// ReplaceConfig defines that the incoming config change should replace the
	// existing config at the level which it is specified.  This can be
	// specified on individual config elements or set as the default strategy.
	ReplaceConfig DefaultOperation = "replace"

	// NoneOperation indicates that no default operation should be applied and
	// nothing is applied to the target configuration unless there are
	// operations defined on the configs subelements.
	NoneOperation DefaultOperation = "none"
)

// TestOption defines the behavior for testing configuration before applying it
// in a `<edit-config>` operation.
type TestOption string

const (
	// TestThenSet will validate the configuration and only if is is valid then
	// apply the configuration to the datastore.
	TestThenSet TestOption = "test-then-set"

	// SetOnly will not do any testing before applying it.
	SetOnly TestOption = "set"

	// TestOnly will validate the incoming configuration and return the
	// results without modifying the underlying store.
	TestOnly TestOption = "test-only"
)

// ErrorOption defines the behavior when an error is encountered during a
// `<edit-config>` operation.
type ErrorOption string

const (
	// StopOnError will abort the `<edit-config>` operation on the first error.
	StopOnError ErrorOption = "stop-on-error"

	// ContinueOnError will continue to parse the configuration data even if an
	// error is encountered.  Errors are still recorded and reported in the
	// reply.
	ContinueOnError ErrorOption = "continue-on-error"

	// RollbackOnError will restore the configuration back to before the
	// `<edit-config>` operation took place.  This requires the device to
	// support the `:rollback-on-error` capability.
	RollbackOnError ErrorOption = "rollback-on-error"
)

// EditConfig issues the `<edit-config>` operation defined in [RFC6241 7.2] for
// updating an existing target config datastore.
//
// [RFC6241 7.2]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.2
type EditConfig struct {
	Target           Datastore
	DefaultOperation DefaultOperation
	TestOption       TestOption
	ErrorOption      ErrorOption

	// Config is the configuration to apply: a string or []byte holding
	// literal XML, a URL, or a value that marshals to the configuration
	// element.
	Config any
}

// MarshalXML implements xml.Marshaler.  It fails when Config is nil.
func (rpc EditConfig) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName          xml.Name         `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 edit-config"`
		Target           Datastore        `xml:"target"`
		DefaultOperation DefaultOperation `xml:"default-operation,omitempty"`
		TestOption       TestOption       `xml:"test-option,omitempty"`
		ErrorOption      ErrorOption      `xml:"error-option,omitempty"`

		Config *payload `xml:"config,omitempty"`
		URL    string   `xml:"url,omitempty"`
	}{
		Target:           rpc.Target,
		DefaultOperation: rpc.DefaultOperation,
		TestOption:       rpc.TestOption,
		ErrorOption:      rpc.ErrorOption,
	}

	switch v := rpc.Config.(type) {
	case nil:
		return errors.New("edit-config: config cannot be nil")
	case URL:
		req.URL = string(v)
	case Config:
		// the inline source of copy-config and validate, taken apart so it
		// is not wrapped twice
		if v.Payload == nil {
			return errors.New("edit-config: config cannot be nil")
		}
		req.Config = &payload{v: v.Payload}
	default:
		req.Config = &payload{v: v}
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc EditConfig) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "edit-config")
}

// CopyConfig issues the `<copy-config>` operation as defined in [RFC6241 7.3]
// for copying an entire config to/from a source and target datastore.
//
// A `<config>` element defining a full config can be used as the source.
//
// If a device supports the `:url` capability than a [URL] object can be used
// for the source or target datastore.
//
// [RFC6241 7.3] https://www.rfc-editor.org/rfc/rfc6241.html#section-7.3
type CopyConfig struct {
	Source       Source
	Target       Target
	WithDefaults WithDefaultsMode
}

// MarshalXML implements xml.Marshaler.  It fails when Source or Target is
// nil.
func (rpc CopyConfig) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if rpc.Source == nil {
		return fmt.Errorf("copy-config: source cannot be nil")
	}
	if rpc.Target == nil {
		return fmt.Errorf("copy-config: target cannot be nil")
	}

	req := struct {
		XMLName      xml.Name             `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 copy-config"`
		Source       Source               `xml:"source"`
		Target       Target               `xml:"target"`
		WithDefaults *withDefaultsElement `xml:",omitempty"`
	}{
		Source: rpc.Source,
		Target: rpc.Target,
	}

	if rpc.WithDefaults != "" {
		req.WithDefaults = &withDefaultsElement{Mode: rpc.WithDefaults}
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc CopyConfig) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "copy-config")
}

// DeleteConfig represents the `<delete-config>` operation defined in
// [RFC6241 7.4] for deleting a configuration datastore.
//
// [RFC6241 7.4]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.4
type DeleteConfig struct {
	Target Datastore
}

// MarshalXML implements xml.Marshaler.
func (rpc DeleteConfig) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 delete-config"`
		Target  any      `xml:"target"`
	}{
		Target: rpc.Target,
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc DeleteConfig) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "delete-config")
}

// Lock represents the `<lock>` operation defined in [RFC6241 7.5] for
// locking a configuration datastore.
//
// [RFC6241 7.5]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.5
type Lock struct {
	Target Datastore
}

// MarshalXML implements xml.Marshaler.
func (rpc Lock) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName xml.Name  `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 lock"`
		Target  Datastore `xml:"target"`
	}{
		Target: rpc.Target,
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc Lock) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "lock")
}

// Unlock represents the `<unlock>` operation defined in [RFC6241 7.6] for
// releasing a lock this session holds on a configuration datastore.
//
// [RFC6241 7.6]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.6
type Unlock struct {
	Target Datastore
}

// MarshalXML implements xml.Marshaler.
func (rpc Unlock) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName xml.Name  `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 unlock"`
		Target  Datastore `xml:"target"`
	}{
		Target: rpc.Target,
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc Unlock) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "unlock")
}

// PartialLock locks specific subtrees of the running datastore using XPath
// expressions as defined in RFC 5717.
type PartialLock struct {
	Select []string
}

// MarshalXML implements xml.Marshaler.  It fails when Select is empty.
func (rpc PartialLock) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if len(rpc.Select) == 0 {
		return fmt.Errorf("partial-lock: must have at least one lock selection (xpath)")
	}

	type selectExpr struct {
		XMLName xml.Name `xml:"select"`
		Expr    string   `xml:",chardata"`
	}

	selects := make([]selectExpr, len(rpc.Select))
	for i, s := range rpc.Select {
		selects[i] = selectExpr{Expr: s}
	}

	req := struct {
		XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:partial-lock:1.0 partial-lock"`
		Select  []selectExpr
	}{
		Select: selects,
	}

	return e.Encode(&req)
}

// PartialLockReply is the response from a successful partial-lock operation.
type PartialLockReply struct {
	netconf.RPCReply
	LockID      uint32   `xml:"lock-id"`
	LockedNodes []string `xml:"locked-node"`
}

// Exec issues the operation on session and returns the lock that was
// granted.
func (rpc PartialLock) Exec(ctx context.Context, session *netconf.Session) (*PartialLockReply, error) {
	const name = "partial-lock"

	var resp PartialLockReply
	if err := session.Exec(ctx, rpc, &resp); err != nil {
		return nil, operr.Named(name, err)
	}
	// lock ids start at 1, so zero is a reply that granted nothing
	if resp.LockID == 0 {
		return nil, operr.Named(name, fmt.Errorf("%w: <lock-id>", netconf.ErrElementNotFound))
	}

	return &resp, nil
}

// PartialUnlock releases a partial lock acquired by PartialLock.
type PartialUnlock struct {
	LockID uint32
}

// MarshalXML implements xml.Marshaler.  It fails when LockID is zero.
func (rpc PartialUnlock) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if rpc.LockID == 0 {
		return fmt.Errorf("partial-unlock: lock id is not set")
	}

	req := struct {
		XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:partial-lock:1.0 partial-unlock"`
		LockID  uint32   `xml:"lock-id"`
	}{
		LockID: rpc.LockID,
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc PartialUnlock) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "partial-unlock")
}

// Validate represents the `<validate>` operation defined in
// [RFC6241 8.6.4.1] for checking a configuration for syntax and semantic
// errors without applying it.
//
// Device must support the `:validate` capability.
//
// [RFC6241 8.6.4.1]: https://www.rfc-editor.org/rfc/rfc6241.html#section-8.6.4.1
type Validate struct {
	Source Source
}

// MarshalXML implements xml.Marshaler.  It fails when Source is nil.
func (rpc Validate) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if rpc.Source == nil {
		return fmt.Errorf("validate: source cannot be nil")
	}

	req := struct {
		XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 validate"`
		Source  Source   `xml:"source"`
	}{
		Source: rpc.Source,
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc Validate) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "validate")
}

// Commit represents the `<commit>` operation defined in [RFC6241 8.3.4.1]
// for committing candidate configuration to the running datastore, with the
// confirmed-commit parameters of section 8.4.5.1.
//
// [RFC6241 8.3.4.1]: https://www.rfc-editor.org/rfc/rfc6241.html#section-8.3.4.1
type Commit struct {
	// Confirmed makes this a confirmed commit: the device goes back to the
	// previous configuration unless a follow-up commit confirms it within
	// the confirm timeout.
	//
	// Device must support the `:confirmed-commit` capability.
	Confirmed bool

	// ConfirmTimeout is how long the device waits for the confirmation of a
	// confirmed commit.  Zero leaves it to the device, 600 seconds by the
	// RFC.  The protocol counts in seconds; the time is rounded up to the
	// next one.
	ConfirmTimeout time.Duration

	// Persist is sent as <persist>.  It lets a confirmed commit outlive its
	// session, and is the token that a later commit or <cancel-commit> names
	// it with.  It needs Confirmed.
	//
	// Device must support the `:confirmed-commit:1.1` capability.
	Persist string

	// PersistID is sent as <persist-id>.  It names a confirmed commit that
	// was made with a token, from any session: without Confirmed to confirm
	// it, with Confirmed to make a further confirmed commit, which gives it
	// more time.
	//
	// netopeer2 2.8 refuses it next to Confirmed, and takes a further
	// confirmed commit that has the token of the first in Persist again.
	//
	// Device must support the `:confirmed-commit:1.1` capability.
	PersistID string
}

// MarshalXML implements xml.Marshaler.  It fails when ConfirmTimeout is
// negative, and when ConfirmTimeout or Persist is set without Confirmed.
func (rpc Commit) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if rpc.ConfirmTimeout != 0 && !rpc.Confirmed {
		return fmt.Errorf("commit: confirm-timeout requires Confirmed to be set")
	}
	if rpc.ConfirmTimeout < 0 {
		return fmt.Errorf("commit: confirm-timeout cannot be negative")
	}
	if rpc.Persist != "" && !rpc.Confirmed {
		return fmt.Errorf("commit: persist requires Confirmed to be set")
	}

	req := struct {
		XMLName        xml.Name   `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 commit"`
		Confirmed      ExtantBool `xml:"confirmed,omitempty"`
		ConfirmTimeout int64      `xml:"confirm-timeout,omitempty"`
		Persist        string     `xml:"persist,omitempty"`
		PersistID      string     `xml:"persist-id,omitempty"`
	}{
		Confirmed:      ExtantBool(rpc.Confirmed),
		ConfirmTimeout: int64(math.Ceil(rpc.ConfirmTimeout.Seconds())),
		Persist:        rpc.Persist,
		PersistID:      rpc.PersistID,
	}

	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc Commit) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "commit")
}

// CancelCommit represents the `<cancel-commit>` operation defined in
// [RFC6241 8.4.4.1] for reverting a confirmed commit before its timeout.
//
// Device must support the `:confirmed-commit:1.1` capability.
//
// [RFC6241 8.4.4.1]: https://www.rfc-editor.org/rfc/rfc6241.html#section-8.4.4.1
type CancelCommit struct {
	// PersistID is the id given to the confirmed commit, required when it
	// is cancelled from another session.
	PersistID string
}

// MarshalXML implements xml.Marshaler.
func (rpc CancelCommit) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 cancel-commit"`
		PersistID string   `xml:"persist-id,omitempty"`
	}{
		PersistID: rpc.PersistID,
	}
	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc CancelCommit) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "cancel-commit")
}

// DiscardChanges represents the `<discard-changes>` operation defined in
// [RFC6241 8.3.4.2] for reverting the candidate configuration to the current
// running configuration.
//
// This operation discards any uncommitted changes by resetting the candidate
// configuration with the content of the running configuration.
//
// Device must support the `:candidate` capability.
//
// [RFC6241 8.3.4.2]: https://www.rfc-editor.org/rfc/rfc6241.html#section-8.3.4.2
type DiscardChanges struct{}

// MarshalXML implements xml.Marshaler.
func (rpc DiscardChanges) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 discard-changes"`
	}{}
	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc DiscardChanges) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "discard-changes")
}
