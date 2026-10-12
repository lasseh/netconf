package rpc

import (
	"context"
	"encoding/xml"
	"errors"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
)

func TestCreateSubscriptionMarshalXML(t *testing.T) {
	tests := []struct {
		name    string
		sub     *CreateSubscription
		wantXML string
	}{
		{
			name:    "minimalSubscription",
			sub:     &CreateSubscription{},
			wantXML: `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"></create-subscription>`,
		},
		{
			name: "withStream",
			sub: &CreateSubscription{
				Stream: "NETCONF",
			},
			wantXML: `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><stream>NETCONF</stream></create-subscription>`,
		},
		{
			name: "withFilter",
			sub: &CreateSubscription{
				Filter: SubtreeFilter("<interfaces/>"),
			},
			wantXML: `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><filter type="subtree"><interfaces/></filter></create-subscription>`,
		},
		{
			name: "withStartTime",
			sub: func() *CreateSubscription {
				t := time.Date(2024, 1, 5, 12, 34, 56, 0, time.UTC)
				return &CreateSubscription{
					StartTime: &t,
				}
			}(),
			wantXML: `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><startTime>2024-01-05T12:34:56Z</startTime></create-subscription>`,
		},
		{
			name: "withStartAndStopTime",
			sub: func() *CreateSubscription {
				start := time.Date(2024, 1, 5, 12, 0, 0, 0, time.UTC)
				stop := time.Date(2024, 1, 5, 13, 0, 0, 0, time.UTC)
				return &CreateSubscription{
					StartTime: &start,
					StopTime:  &stop,
				}
			}(),
			wantXML: `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><startTime>2024-01-05T12:00:00Z</startTime><stopTime>2024-01-05T13:00:00Z</stopTime></create-subscription>`,
		},
		{
			name: "fullSubscription",
			sub: func() *CreateSubscription {
				start := time.Date(2024, 1, 5, 12, 0, 0, 0, time.UTC)
				return &CreateSubscription{
					Stream:    "NETCONF",
					Filter:    SubtreeFilter("<config/>"),
					StartTime: &start,
				}
			}(),
			wantXML: `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><stream>NETCONF</stream><filter type="subtree"><config/></filter><startTime>2024-01-05T12:00:00Z</startTime></create-subscription>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := xml.Marshal(tt.sub)
			be.NilErr(t, err)
			be.Equal(t, tt.wantXML, string(data))
		})
	}
}

func TestCreateSubscriptionExec(t *testing.T) {
	tests := []struct {
		name        string
		op          CreateSubscription
		serverReply string
		shouldErr   bool
	}{
		{
			name:        "okReply",
			op:          CreateSubscription{},
			serverReply: `<ok/>`,
		},
		{
			name: "stopTimeWithoutStartTime",
			op: func() CreateSubscription {
				stopTime := time.Date(2024, 1, 5, 13, 0, 0, 0, time.UTC)
				return CreateSubscription{
					StopTime: &stopTime,
				}
			}(),
			shouldErr: true,
		},
		{
			name: "stopTimeBeforeStartTime",
			op: func() CreateSubscription {
				startTime := time.Date(2024, 1, 5, 14, 0, 0, 0, time.UTC)
				stopTime := time.Date(2024, 1, 5, 13, 0, 0, 0, time.UTC)
				return CreateSubscription{
					StartTime: &startTime,
					StopTime:  &stopTime,
				}
			}(),
			shouldErr: true,
		},
		{
			name:        "errorReply",
			op:          CreateSubscription{},
			serverReply: `<rpc-error><error-type>application</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>Subscription failed</error-message></rpc-error>`,
			shouldErr:   true,
		},
		{
			name:        "rpcErrorReply",
			op:          CreateSubscription{},
			serverReply: `<rpc-reply><rpc-error><error-type>application</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>Subscription failed</error-message></rpc-error></rpc-reply>`,
			shouldErr:   true,
		},
		{
			name:        "notOkReply",
			op:          CreateSubscription{},
			serverReply: `<data></data>`,
			shouldErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := mockSession(t, tc.serverReply)

			err := tc.op.Exec(context.Background(), session)
			if tc.shouldErr {
				be.Nonzero(t, err)
			} else {
				be.NilErr(t, err)
			}
		})
	}
}

// A subscription on a session that has nowhere to deliver notifications is
// refused before the device hears of it.  Each notification would count as a
// message nobody asked for, and the session would end after a hundred of
// them: within seconds on Junos, which sends hundreds for one commit.
func TestCreateSubscription_NeedsAHandler(t *testing.T) {
	const hello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>42</session-id></hello>`
	device := func(t *testing.T) *netconftest.Transport {
		return netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
			return []string{netconftest.OKReply(netconftest.ExtractMessageID(req))}
		}))
	}

	t.Run("without a handler", func(t *testing.T) {
		tr := device(t)
		sess, err := netconf.Open(t.Context(), tr)
		be.NilErr(t, err)

		err = CreateSubscription{}.Exec(t.Context(), sess)
		be.True(t, errors.Is(err, netconf.ErrNoNotifHandler))
		be.In(t, "create-subscription: ", err.Error())
		be.In(t, "WithNotifHandler", err.Error())

		// nothing but the hello went out, and the session is good for more
		be.Equal(t, 1, len(tr.Outputs()))
		be.NilErr(t, sess.Exec(t.Context(), "<get/>", nil))
	})

	t.Run("with a handler", func(t *testing.T) {
		tr := device(t)
		sess, err := netconf.Open(t.Context(), tr, netconf.WithNotifHandler(func(context.Context, *netconf.Message) {}))
		be.NilErr(t, err)

		be.NilErr(t, CreateSubscription{}.Exec(t.Context(), sess))
		be.Equal(t, 2, len(tr.Outputs()))
		be.In(t, "<create-subscription", tr.Outputs()[1])
	})
}
