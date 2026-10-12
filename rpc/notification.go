package rpc

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
)

// CreateSubscription represents the `<create-subscription>` operation defined
// in [RFC 5277 Section 2.1.1] for initiating a notification subscription.
//
// The subscription will cause the server to begin sending event notifications
// to the client as they occur. If StartTime is specified, the server will first
// replay historical events from that time (if supported).
//
// A server without the `:interleave` capability may refuse other operations
// on a session that has a subscription.  Check
// session.ServerCaps().Has(":interleave:1.0"), or give the subscription a
// session of its own.
//
// On Junos the capability does not tell: it is advertised as soon as
// notifications are configured.  Without the statement "system services
// netconf notification interleave" Junos answers nothing on a session that
// has a subscription, <close-session> included, so an operation there waits
// for its context and so does Session.Close.
//
// [RFC 5277 Section 2.1.1]: https://www.rfc-editor.org/rfc/rfc5277.html#section-2.1.1
type CreateSubscription struct {
	// Stream indicates which notification stream to subscribe to.
	// If empty, the default NETCONF stream is used.
	Stream string

	// Filter selects a subset of notifications.
	Filter Filter

	// StartTime triggers replay of notifications from the specified time.
	// Requires server support for notification replay.
	StartTime *time.Time

	// StopTime automatically terminates the subscription at the specified time.
	// Requires StartTime to be set.
	StopTime *time.Time
}

// MarshalXML implements xml.Marshaler.  It fails for a StopTime without a
// StartTime or before it.
func (rpc CreateSubscription) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if rpc.StopTime != nil {
		if rpc.StartTime == nil {
			return fmt.Errorf("create-subscription: StopTime specified without StartTime")
		}
		if rpc.StopTime.Before(*rpc.StartTime) {
			return fmt.Errorf("create-subscription: StopTime is before StartTime")
		}
	}

	createSub := struct {
		XMLName   xml.Name   `xml:"urn:ietf:params:xml:ns:netconf:notification:1.0 create-subscription"`
		Stream    string     `xml:"stream,omitempty"`
		Filter    Filter     `xml:"filter,omitempty"`
		StartTime *time.Time `xml:"startTime,omitempty"`
		StopTime  *time.Time `xml:"stopTime,omitempty"`
	}{
		Stream:    rpc.Stream,
		Filter:    rpc.Filter,
		StartTime: rpc.StartTime,
		StopTime:  rpc.StopTime,
	}

	return e.Encode(createSub)
}

// Exec issues the operation on session.  It fails unless the server answers
// with <ok/>.  Notifications are delivered to the handler the session was
// opened with, see netconf.WithNotifHandler.
//
// On a session that was opened without a handler nothing is sent and the
// error is netconf.ErrNoNotifHandler.  Such a session skips the
// notifications it is sent and ends after a hundred of them in a row, which
// Junos sends for a single commit.
func (rpc CreateSubscription) Exec(ctx context.Context, session *netconf.Session) error {
	const name = "create-subscription"
	if !session.HasNotifHandler() {
		return operr.Named(name, netconf.ErrNoNotifHandler)
	}
	return ExecOK(ctx, session, rpc, name)
}
