package rpc

import (
	"context"
	"encoding/xml"

	"github.com/lasseh/netconf"
)

// KillSession represents the `<kill-session>` operation defined in
// [RFC6241 7.9] for terminating another NETCONF session.
//
// Junos 26.2 answers with <ok/>, and the session it ends sees its
// connection close without a message.  It refuses with "pid must be of mgd"
// the session-id of a session that has ended, and with invalid-value the
// session-id of the session that sends the request.
//
// [RFC6241 7.9]: https://www.rfc-editor.org/rfc/rfc6241.html#section-7.9
type KillSession struct {
	// SessionID is the session to end: another session's
	// netconf.Session.SessionID, or one found in the server's session list.
	SessionID uint64
}

// MarshalXML implements xml.Marshaler.
func (rpc KillSession) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	req := struct {
		XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 kill-session"`
		SessionID uint64   `xml:"session-id"`
	}{
		SessionID: rpc.SessionID,
	}
	return e.Encode(&req)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.
func (rpc KillSession) Exec(ctx context.Context, session *netconf.Session) error {
	return ExecOK(ctx, session, rpc, "kill-session")
}
