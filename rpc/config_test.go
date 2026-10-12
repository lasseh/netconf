package rpc

import (
	"encoding/xml"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
)

func TestMarshalDatastore(t *testing.T) {
	tt := []struct {
		input     Datastore
		want      string
		shouldErr bool
	}{
		{Running, "<rpc><target><running/></target></rpc>", false},
		{Startup, "<rpc><target><startup/></target></rpc>", false},
		{Candidate, "<rpc><target><candidate/></target></rpc>", false},
		{Datastore("custom-store"), "<rpc><target><custom-store/></target></rpc>", false},
		{Datastore(""), "", true},
		{Datastore("<xml-elements>"), "", true},
		{Datastore("9store"), "", true},
		{Datastore("-x"), "", true},
		{Datastore("_x"), "<rpc><target><_x/></target></rpc>", false},
		{Datastore("x9"), "<rpc><target><x9/></target></rpc>", false},
	}

	for _, tc := range tt {
		t.Run(string(tc.input), func(t *testing.T) {
			v := struct {
				XMLName xml.Name  `xml:"rpc"`
				Target  Datastore `xml:"target"`
			}{Target: tc.input}

			got, err := xml.Marshal(&v)
			if !tc.shouldErr {
				be.NilErr(t, err)
			}
			be.Equal(t, tc.want, string(got))
		})
	}
}

func TestGetConfig_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       GetConfig
		expected string
	}{
		{
			name: "basic",
			op: GetConfig{
				Source: Running,
			},
			expected: `<get-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><running/></source></get-config>`,
		},
		{
			name: "subtreeFilter",
			op: GetConfig{
				Source: Running,
				Filter: SubtreeFilter(`<users/>`),
			},
			expected: `<get-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><running/></source><filter type="subtree"><users/></filter></get-config>`,
		},
		{
			name: "xpathFilter",
			op: GetConfig{
				Source: Running,
				Filter: XPathFilter("/interfaces/interface/name", nil),
			},
			expected: `<get-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><running/></source><filter type="xpath" select="/interfaces/interface/name"></filter></get-config>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}
func TestGetConfig_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          GetConfig
		serverReply string
		shouldError bool
		expected    string
	}{
		{
			name: "good reply",
			op: GetConfig{
				Source: Datastore("my-datastore"),
			},
			serverReply: `<data><top xmlns="http://example.com/schema/1.2/config"><users><user><name>root</name></user></users></top></data>`,
			shouldError: false,
			expected:    `<top xmlns="http://example.com/schema/1.2/config"><users><user><name>root</name></user></users></top>`,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		got, err := tc.op.Exec(t.Context(), session)
		be.NilErr(t, err)
		expected := `<top xmlns="http://example.com/schema/1.2/config"><users><user><name>root</name></user></users></top>`
		be.Equal(t, expected, string(got))

	}

}

func TestEditConfig_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       EditConfig
		expected string
	}{
		{
			name: "stringConfig",
			op: EditConfig{
				Target: Running,
				Config: `<interface><name>eth0</name></interface>`,
			},
			// Expect: <config>...content...</config>
			expected: `<edit-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><running/></target><config><interface><name>eth0</name></interface></config></edit-config>`,
		},
		{
			name: "byteSliceConfig",
			op: EditConfig{
				Target: Running,
				Config: []byte(`<interface><name>eth0</name></interface>`),
			},
			// Expect: <config>...content...</config>
			expected: `<edit-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><running/></target><config><interface><name>eth0</name></interface></config></edit-config>`,
		},
		{
			name: "urlConfig",
			op: EditConfig{
				Target: Candidate,
				Config: URL("https://example.com/config.xml"),
			},
			// Expect: <url>...</url> NOT wrapped in <config>
			expected: `<edit-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target><url>https://example.com/config.xml</url></edit-config>`,
		},
		{
			name: "optionsSet",
			op: EditConfig{
				Target:           Running,
				DefaultOperation: ReplaceConfig,
				TestOption:       TestThenSet,
				ErrorOption:      RollbackOnError,
				Config:           "foo",
			},
			expected: `<edit-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><running/></target><default-operation>replace</default-operation><test-option>test-then-set</test-option><error-option>rollback-on-error</error-option><config>foo</config></edit-config>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestEditConfig_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          EditConfig
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: EditConfig{
				Target: Running,
				Config: `<interface><name>eth0</name></interface>`,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestCopyConfig_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       CopyConfig
		expected string
	}{
		{
			name: "basic",
			op: CopyConfig{
				Source: URL("ftp://example.com/config.xml"),
				Target: Running,
			},
			expected: `<copy-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><url>ftp://example.com/config.xml</url></source><target><running/></target></copy-config>`,
		},
		{
			name: "withDefault",
			op: CopyConfig{
				Source: Startup,
				Target: Candidate,
			},
			expected: `<copy-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><startup/></source><target><candidate/></target></copy-config>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestCopyConfig_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          CopyConfig
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: CopyConfig{
				Source: URL("ftp://example.com/config.xml"),
				Target: Running,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestDeleteConfig_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       DeleteConfig
		expected string
	}{
		{
			name: "basic",
			op: DeleteConfig{
				Target: Datastore("my-custom-datastore"),
			},
			expected: `<delete-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><my-custom-datastore/></target></delete-config>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestDeleteConfig_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          DeleteConfig
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: DeleteConfig{
				Target: Datastore("my-custom-datastore"),
			},
			serverReply: `<ok/>`,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestLock_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       Lock
		expected string
	}{
		{
			name: "basic",
			op: Lock{
				Target: Running,
			},
			expected: `<lock xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><running/></target></lock>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestLock_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          Lock
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: Lock{
				Target: Running,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestUnlock_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       Unlock
		expected string
	}{
		{
			name: "basic",
			op: Unlock{
				Target: Running,
			},
			expected: `<unlock xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><running/></target></unlock>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestUnlock_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          Unlock
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: Unlock{
				Target: Running,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestPartialLock_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       PartialLock
		expected string
	}{
		{
			name: "singleSelect",
			op: PartialLock{
				Select: []string{"/interfaces/interface[name='eth0']"},
			},
			expected: `<partial-lock xmlns="urn:ietf:params:xml:ns:netconf:partial-lock:1.0"><select>/interfaces/interface[name=&#39;eth0&#39;]</select></partial-lock>`,
		},
		{
			name: "multipleSelect",
			op: PartialLock{
				Select: []string{
					"/interfaces/interface[name='eth0']",
					"/interfaces/interface[name='eth1']",
				},
			},
			expected: `<partial-lock xmlns="urn:ietf:params:xml:ns:netconf:partial-lock:1.0"><select>/interfaces/interface[name=&#39;eth0&#39;]</select><select>/interfaces/interface[name=&#39;eth1&#39;]</select></partial-lock>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			be.NilErr(t, err)
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestPartialLock_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          PartialLock
		serverReply string
		shouldError bool
		lockID      uint32
	}{
		{
			name: "success",
			op: PartialLock{
				Select: []string{"/interfaces/interface[name='eth0']"},
			},
			serverReply: `<lock-id>1234</lock-id><locked-node>/interfaces/interface[name='eth0']</locked-node>`,
			shouldError: false,
			lockID:      1234,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := mockSession(t, tc.serverReply)
			reply, err := tc.op.Exec(t.Context(), session)
			if tc.shouldError {
				be.Nonzero(t, err)
			} else {
				be.NilErr(t, err)
				be.Equal(t, tc.lockID, reply.LockID)
			}
		})
	}
}

func TestPartialUnlock_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       PartialUnlock
		expected string
	}{
		{
			name: "basic",
			op: PartialUnlock{
				LockID: 1234,
			},
			expected: `<partial-unlock xmlns="urn:ietf:params:xml:ns:netconf:partial-lock:1.0"><lock-id>1234</lock-id></partial-unlock>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			be.NilErr(t, err)
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestPartialUnlock_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          PartialUnlock
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: PartialUnlock{
				LockID: 1234,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestValidate_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       Validate
		expected string
	}{
		{
			name: "basic",
			op: Validate{
				Source: Running,
			},
			expected: `<validate xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><running/></source></validate>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestValidate_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          Validate
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: Validate{
				Source: Running,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestCommit_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       Commit
		expected string
	}{
		{
			name:     "basic",
			op:       Commit{},
			expected: `<commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"></commit>`,
		},
		{
			name: "confirmed",
			op: Commit{
				Confirmed:      true,
				ConfirmTimeout: 300 * time.Second,
			},
			expected: `<commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><confirmed></confirmed><confirm-timeout>300</confirm-timeout></commit>`,
		},
		{
			// the first confirmed commit sets the token
			name: "confirmedPersist",
			op: Commit{
				Confirmed: true,
				Persist:   "foobar",
			},
			expected: `<commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><confirmed></confirmed><persist>foobar</persist></commit>`,
		},
		{
			// a further confirmed commit names it, and has more time
			name: "extendPersistID",
			op: Commit{
				Confirmed:      true,
				ConfirmTimeout: 300 * time.Second,
				PersistID:      "foobar",
			},
			expected: `<commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><confirmed></confirmed><confirm-timeout>300</confirm-timeout><persist-id>foobar</persist-id></commit>`,
		},
		{
			// and may set a new one
			name: "extendPersistIDNewToken",
			op: Commit{
				Confirmed: true,
				Persist:   "foobar2",
				PersistID: "foobar",
			},
			expected: `<commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><confirmed></confirmed><persist>foobar2</persist><persist-id>foobar</persist-id></commit>`,
		},
		{
			// the confirmation names it
			name: "confirmPersistID",
			op: Commit{
				PersistID: "foobar2",
			},
			expected: `<commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><persist-id>foobar2</persist-id></commit>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

// A token for a commit that is not a confirmed one is a mistake of the
// caller, and nothing is sent for it.
func TestCommit_PersistNeedsConfirmed(t *testing.T) {
	const hello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>42</session-id></hello>`

	var mu sync.Mutex
	var requests []string
	tr := netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return []string{`<rpc-reply message-id="` + netconftest.ExtractMessageID(req) + `" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/></rpc-reply>`}
	}))
	session, err := netconf.Open(t.Context(), tr)
	be.NilErr(t, err)

	_, err = xml.Marshal(Commit{Persist: "token"})
	be.Nonzero(t, err)

	err = Commit{Persist: "token"}.Exec(t.Context(), session)
	be.Nonzero(t, err)
	be.In(t, "commit: persist requires Confirmed to be set", err.Error())

	// the session is as good as before, and the device saw this one only
	be.NilErr(t, Commit{PersistID: "token"}.Exec(t.Context(), session))
	mu.Lock()
	defer mu.Unlock()
	be.Equal(t, 1, len(requests))
	be.In(t, "<persist-id>token</persist-id>", requests[0])
}

func TestCommit_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          Commit
		serverReply string
		shouldError bool
	}{
		{
			name: "okReply",
			op: Commit{
				Confirmed:      true,
				ConfirmTimeout: 200 * time.Second,
			},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestCancelCommit_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       CancelCommit
		expected string
	}{
		{
			name:     "basic",
			op:       CancelCommit{},
			expected: `<cancel-commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"></cancel-commit>`,
		},
		{
			name: "persistID",
			op: CancelCommit{
				PersistID: "persist-123",
			},
			expected: `<cancel-commit xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><persist-id>persist-123</persist-id></cancel-commit>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestCancelCommit_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          CancelCommit
		serverReply string
		shouldError bool
	}{
		{
			name:        "okReply",
			op:          CancelCommit{},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestDiscardChanges_MarshalXML(t *testing.T) {
	tests := []struct {
		name     string
		op       DiscardChanges
		expected string
	}{
		{
			name:     "basic",
			op:       DiscardChanges{},
			expected: `<discard-changes xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"></discard-changes>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := xml.Marshal(tt.op)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			be.Equal(t, tt.expected, string(got))
		})
	}
}

func TestDiscardChanges_Exec(t *testing.T) {
	tests := []struct {
		name        string
		op          DiscardChanges
		serverReply string
		shouldError bool
	}{
		{
			name:        "okReply",
			op:          DiscardChanges{},
			serverReply: `<ok/>`,
			shouldError: false,
		},
	}

	for _, tc := range tests {
		session := mockSession(t, tc.serverReply)
		err := tc.op.Exec(t.Context(), session)
		if tc.shouldError {
			be.Nonzero(t, err)
		} else {
			be.NilErr(t, err)
		}
	}
}

func TestPartialLock_MarshalXML_Escaping(t *testing.T) {
	const expr = "/if:interface[if:mtu < 1500 and x='a&b']"

	out, err := xml.Marshal(PartialLock{Select: []string{expr}})
	be.NilErr(t, err)

	// The XPath must be escaped as chardata, not injected as raw XML.
	be.In(t, "&lt;", string(out))
	be.In(t, "&amp;", string(out))

	// And it must round-trip back to the original expression.
	var parsed struct {
		Select []string `xml:"select"`
	}
	be.NilErr(t, xml.Unmarshal(out, &parsed))
	be.Equal(t, 1, len(parsed.Select))
	be.Equal(t, expr, parsed.Select[0])
}

func TestCopyConfig_MarshalXML_Errors(t *testing.T) {
	// Zero-value copy-config has no source/target and must error.
	_, err := xml.Marshal(CopyConfig{})
	be.Nonzero(t, err)
}

func TestCopyConfig_MarshalXML_InlineSource(t *testing.T) {
	out, err := xml.Marshal(CopyConfig{
		Source: Config{Payload: `<interface><name>eth0</name></interface>`},
		Target: Running,
	})
	be.NilErr(t, err)

	expected := `<copy-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><config><interface><name>eth0</name></interface></config></source><target><running/></target></copy-config>`
	be.Equal(t, expected, string(out))
}

func TestCopyConfig_MarshalXML_InlineSourceBytes(t *testing.T) {
	out, err := xml.Marshal(CopyConfig{
		Source: Config{Payload: []byte(`<interface><name>eth0</name></interface>`)},
		Target: Running,
	})
	be.NilErr(t, err)

	expected := `<copy-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><config><interface><name>eth0</name></interface></config></source><target><running/></target></copy-config>`
	be.Equal(t, expected, string(out))
}

func TestCopyConfig_MarshalXML_InlineSourceStruct(t *testing.T) {
	type iface struct {
		XMLName xml.Name `xml:"interface"`
		Name    string   `xml:"name"`
	}

	out, err := xml.Marshal(CopyConfig{
		Source: Config{Payload: iface{Name: "eth0"}},
		Target: Running,
	})
	be.NilErr(t, err)

	expected := `<copy-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><config><interface><name>eth0</name></interface></config></source><target><running/></target></copy-config>`
	be.Equal(t, expected, string(out))
}

func TestValidate_MarshalXML_Errors(t *testing.T) {
	// Zero-value validate has no source and must error.
	_, err := xml.Marshal(Validate{})
	be.Nonzero(t, err)
}

func TestValidate_MarshalXML_InlineSource(t *testing.T) {
	out, err := xml.Marshal(Validate{
		Source: Config{Payload: `<interface><name>eth0</name></interface>`},
	})
	be.NilErr(t, err)

	expected := `<validate xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><source><config><interface><name>eth0</name></interface></config></source></validate>`
	be.Equal(t, expected, string(out))
}

func TestCommit_MarshalXML_ConfirmTimeout(t *testing.T) {
	// ConfirmTimeout without Confirmed emits an invalid RPC and must error.
	_, err := xml.Marshal(Commit{ConfirmTimeout: 5 * time.Minute})
	be.Nonzero(t, err)

	// Confirmed + ConfirmTimeout is valid.
	out, err := xml.Marshal(Commit{Confirmed: true, ConfirmTimeout: 5 * time.Minute})
	be.NilErr(t, err)
	be.In(t, "<confirmed></confirmed>", string(out))
	be.In(t, "<confirm-timeout>300</confirm-timeout>", string(out))

	// The protocol counts whole seconds; a part of one is rounded up.
	out, err = xml.Marshal(Commit{Confirmed: true, ConfirmTimeout: 1500 * time.Millisecond})
	be.NilErr(t, err)
	be.In(t, "<confirm-timeout>2</confirm-timeout>", string(out))

	_, err = xml.Marshal(Commit{Confirmed: true, ConfirmTimeout: -time.Second})
	be.Nonzero(t, err)
}

func TestEditConfig_MarshalXML_StructConfig(t *testing.T) {
	type configuration struct {
		XMLName  xml.Name `xml:"configuration"`
		Hostname string   `xml:"system>host-name"`
	}

	out, err := xml.Marshal(EditConfig{
		Target: Candidate,
		Config: configuration{Hostname: "switch1"},
	})
	be.NilErr(t, err)

	expected := `<edit-config xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target><config><configuration><system><host-name>switch1</host-name></system></configuration></config></edit-config>`
	be.Equal(t, expected, string(out))
}

func TestEditConfig_MarshalXML_NilConfig(t *testing.T) {
	_, err := xml.Marshal(EditConfig{Target: Candidate})
	be.Nonzero(t, err)
}

// Junos (25.4, without rfc-compliant) answers <validate> with the commit
// check results next to <ok/>.
func TestValidate_JunosReply(t *testing.T) {
	session := mockSession(t, `<commit-results>
<routing-engine junos:style="normal" xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos">
<name>fpc0</name>
<commit-check-success/>
</routing-engine>
</commit-results>
<ok/>`)

	be.NilErr(t, Validate{Source: Candidate}.Exec(t.Context(), session))
}
