package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
)

func TestEditCandidate(t *testing.T) {
	const refused = `<rpc-error><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>refused</error-message></rpc-error>`
	for _, tt := range []struct {
		name       string
		failure    string
		body       string
		dirty      bool
		wantErr    bool
		operations []string
	}{
		{"commit", "", "", false, false, []string{"lock", "get-config", "get-config", "edit-config", "validate", "commit", "unlock"}},
		{"pre-existing changes", "", "", true, true, []string{"lock", "get-config", "get-config", "unlock"}},
		{"edit refused", "edit-config", refused, false, true, []string{"lock", "get-config", "get-config", "edit-config", "discard-changes", "unlock"}},
		{"validation refused", "validate", refused, false, true, []string{"lock", "get-config", "get-config", "edit-config", "validate", "discard-changes", "unlock"}},
		{"commit refused", "commit", refused, false, true, []string{"lock", "get-config", "get-config", "edit-config", "validate", "commit", "discard-changes", "unlock"}},
		{"damaged commit refusal", "commit", refused + "<broken>", false, true, []string{"lock", "get-config", "get-config", "edit-config", "validate", "commit"}},
		{"commit missing acknowledgement", "commit", "", false, true, []string{"lock", "get-config", "get-config", "edit-config", "validate", "commit"}},
		{"cleanup fails after commit", "unlock", refused, false, true, []string{"lock", "get-config", "get-config", "edit-config", "validate", "commit", "unlock"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hello := `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability><capability>urn:ietf:params:netconf:capability:candidate:1.0</capability><capability>urn:ietf:params:netconf:capability:validate:1.1</capability></capabilities><session-id>7</session-id></hello>`
			tr := netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
				operation := operationName(req)
				body := "<ok/>"
				if operation == "get-config" {
					body = "<data><system><hostname>old</hostname></system></data>"
					if tt.dirty && strings.Contains(req, "<candidate") {
						body = "<data><system><hostname>somebody-elses-edit</hostname></system></data>"
					}
				}
				if operation == tt.failure {
					body = tt.body
				}
				return []string{fmt.Sprintf(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id=%q>%s</rpc-reply>`, netconftest.ExtractMessageID(req), body)}
			}))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			session, err := netconf.Open(ctx, tr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := session.Close(ctx); err != nil {
					t.Errorf("close: %v", err)
				}
			}()
			err = editCandidate(ctx, session, []byte(`<system><hostname>new</hostname></system>`))
			if (err != nil) != tt.wantErr {
				t.Fatalf("got err=%v, want error=%t", err, tt.wantErr)
			}
			if tt.failure == "unlock" && !strings.Contains(err.Error(), "commit succeeded") {
				t.Fatalf("cleanup error hid successful commit: %v", err)
			}
			var operations []string
			for _, sent := range tr.Outputs() {
				if name := operationName(sent); name != "" {
					operations = append(operations, name)
				}
			}
			if !slices.Equal(operations, tt.operations) {
				t.Fatalf("operations=%v, want %v", operations, tt.operations)
			}
		})
	}
}

func operationName(req string) string {
	decoder := xml.NewDecoder(strings.NewReader(req))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		if start, ok := token.(xml.StartElement); ok {
			if start.Name.Local == "hello" {
				return ""
			}
			if start.Name.Local != "rpc" {
				return start.Name.Local
			}
		}
	}
}
