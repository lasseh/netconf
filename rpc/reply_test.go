package rpc

import (
	"context"
	"encoding/xml"
	"errors"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
)

// Nothing to report comes as an empty <data>.  A reply without <data>
// answers something else and must not read as an empty result.
func TestDataElement(t *testing.T) {
	tests := []struct {
		name    string
		reply   string
		want    string
		missing bool
	}{
		{name: "present", reply: `<data><x>1</x></data>`, want: `<x>1</x>`},
		{name: "empty, self closing", reply: `<data/>`},
		{name: "empty, with end tag", reply: `<data></data>`},
		{name: "absent", reply: `<ok/>`, missing: true},
		{name: "nothing at all", reply: ``, missing: true},
	}

	ops := map[string]func(context.Context, *netconf.Session) ([]byte, error){
		"get":        Get{}.Exec,
		"get-config": GetConfig{Source: Running}.Exec,
	}

	for _, tc := range tests {
		for name, exec := range ops {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				got, err := exec(t.Context(), mockSession(t, tc.reply))
				if tc.missing {
					be.True(t, errors.Is(err, netconf.ErrElementNotFound))
					be.In(t, name+":", err.Error())
					return
				}
				be.NilErr(t, err)
				be.Equal(t, tc.want, string(got))
			})
		}
	}
}

// A reply obtained with Stream has not been looked at for errors, so
// decoding one that carries an error instead of <data> has to report the
// device's error, not the missing element.
func TestGetConfigReply_DecodeUncheckedReply(t *testing.T) {
	const rpcError = `<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>syntax error</error-message></rpc-error>`

	t.Run("error", func(t *testing.T) {
		msg, err := mockSession(t, rpcError).Stream(t.Context(), netconf.NewRPC(GetConfig{Source: Running}))
		be.NilErr(t, err)

		var reply GetConfigReply
		err = msg.Decode(&reply)

		var rpcErrs netconf.RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 1, len(rpcErrs))
		be.Equal(t, "syntax error", rpcErrs[0].Message)
	})

	t.Run("neither", func(t *testing.T) {
		msg, err := mockSession(t, `<ok/>`).Stream(t.Context(), netconf.NewRPC(GetConfig{Source: Running}))
		be.NilErr(t, err)

		var reply GetConfigReply
		be.True(t, errors.Is(msg.Decode(&reply), netconf.ErrElementNotFound))
	})

	t.Run("data", func(t *testing.T) {
		msg, err := mockSession(t, `<data><x/></data>`).Stream(t.Context(), netconf.NewRPC(GetConfig{Source: Running}))
		be.NilErr(t, err)

		var reply GetConfigReply
		be.NilErr(t, msg.Decode(&reply))
		be.Equal(t, `<x/>`, string(reply.Config))
	})
}

// Lock ids start at 1, so a reply without one granted nothing.
func TestPartialLock_ExecWithoutLockID(t *testing.T) {
	_, err := PartialLock{Select: []string{"/x"}}.Exec(t.Context(), mockSession(t, `<ok/>`))
	be.True(t, errors.Is(err, netconf.ErrElementNotFound))
	be.In(t, "<lock-id>", err.Error())
}

// Config is the inline source of copy-config and validate.  Given to
// edit-config it is the configuration itself, not a value to wrap again.
func TestEditConfig_ConfigValue(t *testing.T) {
	out, err := xml.Marshal(EditConfig{Target: Candidate, Config: Config{Payload: `<x/>`}})
	be.NilErr(t, err)
	be.Equal(t, `<edit-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target><config><x/></config></edit-config>`, string(out))

	_, err = xml.Marshal(EditConfig{Target: Candidate, Config: Config{}})
	be.Nonzero(t, err)
}

// The operation validates itself when it is encoded, so it does on every way
// of sending it, not only through its own Exec.
func TestCreateSubscription_ValidatedWhenEncoded(t *testing.T) {
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	before := start.Add(-time.Hour)

	_, err := xml.Marshal(CreateSubscription{StopTime: &start})
	be.Nonzero(t, err)

	_, err = xml.Marshal(CreateSubscription{StartTime: &start, StopTime: &before})
	be.Nonzero(t, err)

	sess := mockSession(t, `<ok/>`)
	_, err = sess.ExecReply(t.Context(), CreateSubscription{StopTime: &start})
	be.Nonzero(t, err)
}

// <ok/> counts wherever the device puts it: directly in the reply by the
// RFC, inside the result element for a Junos <load-configuration>.
func TestExecOK(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		ok    bool
	}{
		{name: "top level", reply: `<ok/>`, ok: true},
		{name: "after results", reply: `<commit-results><routing-engine><name>re0</name><commit-success/></routing-engine></commit-results><ok/>`, ok: true},
		{name: "nested", reply: `<load-configuration-results><ok/></load-configuration-results>`, ok: true},
		{name: "none", reply: `<load-configuration-results></load-configuration-results>`},
		{name: "empty reply", reply: ``},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ExecOK(t.Context(), mockSession(t, tc.reply), "<load-configuration/>", "load-configuration")
			if tc.ok {
				be.NilErr(t, err)
				return
			}
			be.True(t, errors.Is(err, netconf.ErrElementNotFound))
			be.In(t, "load-configuration:", err.Error())
		})
	}
}
