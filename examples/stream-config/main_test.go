package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
)

func TestCountElements(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		count   int
		wantErr bool
	}{
		{"counts within data", `<rpc-reply><data><interfaces><interface/><interface/></interfaces></data></rpc-reply>`, 2, false},
		{"empty data", `<rpc-reply><data/></rpc-reply>`, 0, false},
		{"ignores outside data", `<rpc-reply><interface/><data/></rpc-reply>`, 0, false},
		{"prefixed names", `<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0"><nc:data><interface xmlns="urn:example"/></nc:data></nc:rpc-reply>`, 1, false},
		{"warning", `<rpc-reply><rpc-error><error-severity>warning</error-severity></rpc-error><data><interface/></data></rpc-reply>`, 1, false},
		{"rpc error", `<rpc-reply><rpc-error><error-tag>access-denied</error-tag><error-severity>error</error-severity></rpc-error></rpc-reply>`, 0, true},
		{"nested rpc error", `<rpc-reply><data><rpc-error><error-tag>operation-failed</error-tag></rpc-error></data></rpc-reply>`, 0, true},
		{"missing data", `<rpc-reply><ok/></rpc-reply>`, 0, true},
		{"truncated", `<rpc-reply><data><interface/>`, 0, true},
		{"wrong root", `<data><interface/></data>`, 0, true},
		{"two documents", `<rpc-reply><data/></rpc-reply><rpc-reply><data/></rpc-reply>`, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			count, err := countElements(strings.NewReader(tt.raw), "interface")
			if (err != nil) != tt.wantErr || count != tt.count {
				t.Fatalf("got count=%d err=%v, want count=%d error=%t", count, err, tt.count, tt.wantErr)
			}
		})
	}
}

func TestCountElementsRPCErrorTag(t *testing.T) {
	_, err := countElements(strings.NewReader(`<rpc-reply><rpc-error><error-tag>access-denied</error-tag></rpc-error></rpc-reply>`), "interface")
	if !errors.Is(err, netconf.ErrAccessDenied) {
		t.Fatalf("got %v, want ErrAccessDenied", err)
	}
}

func TestStreamConfigReadBounds(t *testing.T) {
	for _, tt := range []struct {
		name      string
		body      string
		maxBytes  int64
		stall     bool
		wantErr   bool
		wantCount int
	}{
		{"complete reply", `<data><interface/><interface/></data></rpc-reply>`, 1024, false, false, 2},
		{"reply over byte limit", `<data><interface/><interface/></data></rpc-reply>`, 100, false, true, 0},
		{"malformed reply", `<data><interface></data></rpc-reply>`, 1024, false, true, 0},
		{"stalled body", `<data>`, 1024, true, true, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire, device := netconftest.NewWire(t)
			deviceDone := make(chan error, 1)
			go func() {
				if _, err := device.Recv(); err != nil {
					deviceDone <- err
					return
				}
				if err := device.Send(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>1</session-id></hello>`); err != nil {
					deviceDone <- err
					return
				}
				req, err := device.Recv()
				if err != nil {
					deviceDone <- err
					return
				}
				reply := fmt.Sprintf(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id=%q>%s`, netconftest.ExtractMessageID(req), tt.body)
				if tt.stall {
					deviceDone <- device.WriteRaw(reply) // deliberately no message delimiter or remaining body
				} else {
					deviceDone <- device.Send(reply)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			session, err := netconf.Open(ctx, wire)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				closeCtx, stop := context.WithCancel(context.Background())
				stop()
				// The scripted peer does not answer close-session. An expired
				// context still closes the transport without waiting for it.
				_ = session.Close(closeCtx)
			}()
			count, err := streamConfig(ctx, session, "interface", 100*time.Millisecond, tt.maxBytes)
			if (err != nil) != tt.wantErr || count != tt.wantCount {
				t.Fatalf("count=%d err=%v, want count=%d error=%t", count, err, tt.wantCount, tt.wantErr)
			}
			if tt.stall && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("got %v, want read deadline exceeded", err)
			}
			if tt.wantErr {
				select {
				case <-session.Done():
				default:
					t.Error("failed stream left the session open")
				}
			}
			select {
			case sendErr := <-deviceDone:
				// The client may cut off a malformed or oversized reply while
				// the peer is still writing it; that is the expected behavior.
				if sendErr != nil && !tt.wantErr {
					t.Errorf("scripted device: %v", sendErr)
				}
			case <-ctx.Done():
				t.Fatal("scripted device did not finish")
			}
		})
	}
}
