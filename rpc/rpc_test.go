package rpc

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
)

func mockSession(t *testing.T, rpcReplyInnerXML string) *netconf.Session {
	const hello = `
		<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
			<capabilities>
				<capability>urn:ietf:params:netconf:base:1.0</capability>
			</capabilities>
			<session-id>42</session-id>
		</hello>`
	tr := netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		return []string{fmt.Sprintf(`
			<rpc-reply message-id="%s" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
				%s
			</rpc-reply>`, msgID, rpcReplyInnerXML)}
	}))

	// Create Session
	// This will immediately consume the first message (Server Hello)
	// and write the Client Hello to tr.outputs[0].  It has a handler for
	// notifications, as a session that subscribes must.
	s, err := netconf.Open(context.Background(), tr, netconf.WithNotifHandler(func(context.Context, *netconf.Message) {}))
	be.NilErr(t, err)

	return s
}

func TestUnmarshalOk(t *testing.T) {
	tt := []struct {
		name  string
		input string
		want  bool
	}{
		{"selfclosing", "<foo>><ok/></foo>", true},
		{"missing", "<foo></foo>", false},
		{"closetag", "<foo><ok></ok></foo>", true},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			var v struct {
				XMLName xml.Name   `xml:"foo"`
				Ok      ExtantBool `xml:"ok"`
			}

			err := xml.Unmarshal([]byte(tc.input), &v)
			be.NilErr(t, err)
			be.Equal(t, tc.want, bool(v.Ok))
		})
	}
}

func TestGet_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       Get
		expected string
	}{
		{
			name:     "noFilter",
			op:       Get{},
			expected: `<get xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"></get>`,
		},
		{
			name: "withFilter",
			op: Get{
				Filter: SubtreeFilter(`<interfaces/>`),
			},
			expected: `<get xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><filter type="subtree"><interfaces/></filter></get>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := xml.Marshal(&tt.op)
			be.NilErr(t, err)
			be.Equal(t, tt.expected, string(out))
		})
	}
}

func TestGet_Exec(t *testing.T) {
	const rpcReplyData = `<data><interfaces><interface><name>eth0</name></interface></interfaces></data>`

	sess := mockSession(t, rpcReplyData)

	getOp := &Get{}
	data, err := getOp.Exec(context.Background(), sess)
	be.NilErr(t, err)

	expectedData := `<interfaces><interface><name>eth0</name></interface></interfaces>`
	be.Equal(t, expectedData, string(data))
}

// TestOperationEnvelope marshals every exported operation through the RPC
// envelope (netconf.NewRPC) and asserts the correct wire element appears.  This
// is the regression net for the EncodeElement trap: a pointer receiver (or an
// EncodeElement call honoring the caller's start element) leaks the struct
// field name <Operation> onto the wire instead of the real operation element.
func TestOperationEnvelope(t *testing.T) {
	tests := []struct {
		name string
		op   any
		want string
	}{
		{"getConfig", GetConfig{Source: Running}, "<get-config"},
		{"editConfig", EditConfig{Target: Running, Config: "<x/>"}, "<edit-config"},
		{"copyConfig", CopyConfig{Source: Startup, Target: Candidate}, "<copy-config"},
		{"deleteConfig", DeleteConfig{Target: Startup}, "<delete-config"},
		{"lock", Lock{Target: Running}, "<lock"},
		{"unlock", Unlock{Target: Running}, "<unlock"},
		{"partialLock", PartialLock{Select: []string{"/x"}}, "<partial-lock"},
		{"partialUnlock", PartialUnlock{LockID: 1}, "<partial-unlock"},
		{"validate", Validate{Source: Running}, "<validate"},
		{"commit", Commit{}, "<commit"},
		{"cancelCommit", CancelCommit{}, "<cancel-commit"},
		{"discardChanges", DiscardChanges{}, "<discard-changes"},
		{"get", Get{}, "<get "},
		{"getWithFilter", Get{Filter: SubtreeFilter("<x/>")}, "<get "},
		{"killSession", KillSession{SessionID: 5}, "<kill-session"},
		{"createSubscription", CreateSubscription{}, "<create-subscription"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := xml.Marshal(netconf.NewRPC(tt.op))
			be.NilErr(t, err)
			be.In(t, tt.want, string(out))
			if strings.Contains(string(out), "<Operation>") {
				t.Errorf("envelope leaked struct field name <Operation>: %s", out)
			}
		})
	}
}

func TestKillSession_MarshalXML(t *testing.T) {
	out, err := xml.Marshal(netconf.NewRPC(KillSession{SessionID: 5}))
	be.NilErr(t, err)
	be.In(t, `<kill-session xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><session-id>5</session-id></kill-session>`, string(out))
}
