// Package rpc provides the standard NETCONF operations: those of RFC 6241,
// event notifications (RFC 5277), partial locks (RFC 5717) and with-defaults
// (RFC 6243).
//
// Every operation is a struct that marshals to its XML and has an Exec
// method to issue it on a session and interpret the reply:
//
//	err := rpc.Lock{Target: rpc.Candidate}.Exec(ctx, session)
//
// An error from Exec begins with the name of the operation, "lock: netconf
// error: protocol lock-denied: ...", and has the error of the session
// underneath: errors.Is finds an error tag such as netconf.ErrLockDenied in
// it, and errors.As the netconf.RPCErrors of the device.
//
// An operation can also be handed to netconf.Session.ExecReply to get the
// buffered reply, including its warnings. Buffered Junos replies undergo
// text normalization, and a configured reply repair may change the bytes;
// use netconf.Session.Stream when the original message bytes are needed.
package rpc

import (
	"context"
	"encoding/xml"
	"fmt"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
)

// ExecOK issues the operation via session.Exec and confirms the server
// replied with <ok/>.  It is exported so operation types outside this
// package (e.g. vendor-specific RPCs) can reuse the same reply handling.
//
// The <ok/> counts wherever it is in the reply: RFC6241 puts it directly
// under <rpc-reply>, Junos nests it in <load-configuration-results>.  name
// is the operation's name, which every error it returns begins with.
func ExecOK(ctx context.Context, session *netconf.Session, op any, name string) error {
	var ok okAnywhere
	if err := session.Exec(ctx, op, &ok); err != nil {
		return operr.Named(name, err)
	}
	if !ok {
		return operr.Named(name, fmt.Errorf("%w: <ok>", netconf.ErrElementNotFound))
	}
	return nil
}

// okAnywhere decodes a reply into whether it holds an <ok> element, at any
// depth.
type okAnywhere bool

func (o *okAnywhere) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	for depth := 1; depth > 0; {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			depth++
			if tok.Name.Local == "ok" {
				*o = true
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// ExtantBool is a boolean carried by the presence of an empty element, such
// as <ok/> or <confirmed/>, rather than by its content.
type ExtantBool bool

// MarshalXML implements xml.Marshaler.  A false value writes nothing.
func (b ExtantBool) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if !b {
		return nil
	}
	// This produces a empty start/end tag (i.e <tag></tag>) vs a self-closing
	// tag (<tag/>) which should be the same in XML, however I know certain
	// vendors may have issues with this format. We may have to process this
	// after xml encoding.
	//
	// See https://github.com/golang/go/issues/21399
	// or https://github.com/golang/go/issues/26756 for a different hack.
	return e.EncodeElement(struct{}{}, start)
}

// UnmarshalXML implements xml.Unmarshaler.  The element being there makes
// the value true.
func (b *ExtantBool) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	*b = true
	return d.Skip()
}

// Get represents the `<get>` operation defined in [RFC6241 7.7] for
// retrieving the running configuration together with device state.
//
// [RFC6241 7.7]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.7
type Get struct {
	Filter       Filter
	WithDefaults WithDefaultsMode
}

// MarshalXML implements xml.Marshaler.
func (rpc Get) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName      xml.Name             `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 get"`
		Filter       Filter               `xml:"filter,omitempty"`
		WithDefaults *withDefaultsElement `xml:",omitempty"`
	}{
		Filter: rpc.Filter,
	}

	if rpc.WithDefaults != "" {
		req.WithDefaults = &withDefaultsElement{Mode: rpc.WithDefaults}
	}

	return e.Encode(&req)
}

// Exec issues the operation on session and returns the content of the
// reply's <data> element.
func (rpc Get) Exec(ctx context.Context, session *netconf.Session) ([]byte, error) {
	return data(ctx, session, rpc, "get")
}

// data issues op, which is called name, and returns the content of the
// <data> element of its reply.  Nothing to report comes as an empty <data>;
// no <data> at all is a reply to something else.
func data(ctx context.Context, session *netconf.Session, op any, name string) ([]byte, error) {
	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return nil, operr.Named(name, err)
	}
	el, err := reply.Element("data")
	if err != nil {
		return nil, operr.Named(name, err)
	}
	return el.Inner, nil
}
