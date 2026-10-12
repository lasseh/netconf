package netconf

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

const (
	helloGood = `
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
	<capability>urn:ietf:params:netconf:base:1.0</capability>
	<capability>urn:ietf:params:netconf:base:1.1</capability>
  </capabilities>
  <session-id>42</session-id>
</hello>`

	helloBadXML = `
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities//>
</hello>`

	helloNoSessID = `
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
	<capability>urn:ietf:params:netconf:base:1.0</capability>
	<capability>urn:ietf:params:netconf:base:1.1</capability>
  </capabilities>
</hello>`

	helloNoCaps = `
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities></capabilities>
  <session-id>42</session-id>
</hello>`
)

// echoHandler says hello and answers every RPC with <ok/>.
var echoHandler = netconftest.Hello(helloGood, func(req string) []string {
	msgID := netconftest.ExtractMessageID(req)
	if msgID != "" {
		return []string{netconftest.OKReply(msgID)}
	}
	return nil
})

func TestHello(t *testing.T) {
	tt := []struct {
		name        string
		serverHello string
		shouldError bool
		wantID      uint64
	}{
		{"good", helloGood, false, 42},
		{"bad xml", helloBadXML, true, 0},
		{"no capabilities", helloNoCaps, true, 0},
		{"no session-id", helloNoSessID, true, 0},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			tr := netconftest.NewTransport(t, func(string) []string {
				return []string{tc.serverHello}
			})
			sess, err := Open(context.Background(), tr)
			be.Equal(t, 1, len(tr.Outputs()))
			if tc.shouldError {
				be.Nonzero(t, err)
				be.True(t, tr.Closed())
				return
			}
			be.NilErr(t, err)
			be.Equal(t, tc.wantID, sess.SessionID())
		})
	}
}

func TestWithCapability(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	customCaps := []string{CapNetConf11, "urn:custom:cap:1.0", "urn:custom:cap:2.0"}
	session, err := Open(context.Background(), tt, WithCapability(customCaps...))
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	// Verify custom capabilities replaced the defaults
	be.True(t, session.ClientCaps().Has("urn:custom:cap:1.0"))
	be.True(t, session.ClientCaps().Has("urn:custom:cap:2.0"))
	be.False(t, session.ClientCaps().Has(CapNetConf10))
}

func TestWithCapabilityNoBase(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	// Replacing capabilities without a base capability must fail fast.
	_, err := Open(context.Background(), tt, WithCapability("urn:custom:cap:1.0"))
	be.Nonzero(t, err)
}

func TestWithLogger(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	logs := &logRecorder{}
	_, err := Open(context.Background(), tt, WithLogger(slog.New(logs)))
	be.NilErr(t, err)

	// something the session warns about goes to the given logger
	tt.Push(`<unknown-message xmlns="urn:example"/>`)
	deadline := time.Now().Add(5 * time.Second)
	for len(logs.Records()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	be.Equal(t, "skipping unknown message", strings.Join(logs.Records(), "; "))
}

func TestWithLoggerNil(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	// A nil logger discards; the session must still work.
	session, err := Open(context.Background(), tt, WithLogger(nil))
	be.NilErr(t, err)
	tt.Push(`<unknown-message xmlns="urn:example"/>`)
	be.NilErr(t, session.Exec(context.Background(), "<get/>", nil))
}

// All options given together take effect.
func TestMultipleOptions(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	logs := &logRecorder{}
	// WithCapability replaces the defaults, so a base capability must be
	// included for the handshake to succeed.
	customCaps := []string{CapNetConf10, CapNetConf11, "urn:custom:cap:1.0"}
	calls := make(chan struct{}, 1)

	session, err := Open(context.Background(), tt,
		WithCapability(customCaps...),
		WithLogger(slog.New(logs)),
		WithNotifHandler(func(context.Context, *Message) { calls <- struct{}{} }),
	)
	be.NilErr(t, err)
	be.True(t, session.ClientCaps().Has("urn:custom:cap:1.0"))

	tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2024-01-05T12:34:56Z</eventTime></notification>`)
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("notification handler not called")
	}
	be.Equal(t, 0, len(logs.Records()))
}

func TestOpenSuccess(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()
	be.Nonzero(t, session)

	be.Equal(t, uint64(42), session.SessionID())
	be.True(t, session.ServerCaps().Has(CapNetConf10))
}

func TestOpenHelloFailure(t *testing.T) {
	tt := netconftest.NewTransport(t, func(string) []string {
		return []string{helloBadXML}
	})

	session, err := Open(context.Background(), tt)
	be.Nonzero(t, err)
	be.Zero(t, session)

	// Transport should be closed on error
	be.True(t, tt.Closed())
}

// A caller that builds the rpc message itself keeps it as it was: the
// session numbers a copy, and leaves a message-id the caller chose alone.
func TestExecReplyWithRPCMessage(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)
	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	numbered := &RPC{Operation: "<get/>"}
	reply, err := session.ExecReply(context.Background(), numbered)
	be.NilErr(t, err)
	be.In(t, `message-id="1"`, string(reply.Raw))
	be.Equal(t, "", numbered.MessageID)

	chosen := &RPC{Operation: "<get/>", MessageID: "custom-id"}
	reply, err = session.ExecReply(context.Background(), chosen)
	be.NilErr(t, err)
	be.In(t, `message-id="custom-id"`, string(reply.Raw))
	be.In(t, `message-id="custom-id"`, tt.Outputs()[2])
}

func TestOperationSuccess(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	rpc := NewRPC("test-op")
	ctx := context.Background()
	reply, err := session.ExecReply(ctx, rpc)

	be.NilErr(t, err)
	be.Nonzero(t, reply)
	be.In(t, `message-id="1"`, string(reply.Raw))
}

func TestOperationContextCanceled(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	rpc := NewRPC("test-op")
	reply, err := session.ExecReply(ctx, rpc)

	be.Nonzero(t, err)
	be.Zero(t, reply)
	be.Equal(t, context.Canceled, err)
}

func TestOperationSessionClosed(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	// Close the transport to simulate connection loss
	_ = tt.Close()

	// Give recvLoop time to detect closure
	time.Sleep(50 * time.Millisecond)

	rpc := NewRPC("test-op")
	ctx := context.Background()
	reply, err := session.ExecReply(ctx, rpc)

	be.Nonzero(t, err)
	be.Zero(t, reply)
}

func TestExecSuccess(t *testing.T) {
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		return []string{netconftest.Reply(msgID, `<data><result>success</result></data>`)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	type TestReply struct {
		XMLName xml.Name `xml:"rpc-reply"`
		Data    struct {
			Result string `xml:"result"`
		} `xml:"data"`
	}

	var reply TestReply
	err = session.Exec(context.Background(), "test-op", &reply)
	be.NilErr(t, err)
	be.Equal(t, "success", reply.Data.Result)
}

func TestExecWithRPCError(t *testing.T) {
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		return []string{netconftest.Reply(msgID, `<rpc-error>
			<error-type>application</error-type>
			<error-tag>operation-failed</error-tag>
			<error-severity>error</error-severity>
			<error-message>Test error</error-message>
		</rpc-error>`)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	err = session.Exec(context.Background(), "test-op", nil)
	be.Nonzero(t, err)

	var rpcErrors RPCErrors
	be.True(t, errors.As(err, &rpcErrors))
	be.Equal(t, 1, len(rpcErrors))
	be.Equal(t, "Test error", rpcErrors[0].Message)
}

func TestExecWithRPCWarning(t *testing.T) {
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		return []string{netconftest.Reply(msgID, `<rpc-error>
			<error-type>application</error-type>
			<error-tag>operation-failed</error-tag>
			<error-severity>warning</error-severity>
			<error-message>Test warning</error-message>
		</rpc-error>
		<ok/>`)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	err = session.Exec(context.Background(), "test-op", nil)
	be.NilErr(t, err)
}

func TestExecNilReply(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	// Should not panic with nil reply
	err = session.Exec(context.Background(), "test-op", nil)
	be.NilErr(t, err)
}

func TestExecContextCanceled(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = session.Exec(ctx, "test-op", nil)
	be.Nonzero(t, err)
	be.Equal(t, context.Canceled, err)
}

func TestRecvMsgUnknownMessageType(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	// Push unknown message type
	tt.Push(`<unknown-message xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"/>`)

	// Give recvLoop time to process
	time.Sleep(100 * time.Millisecond)

	// Unknown messages are skipped; the session must stay usable
	be.False(t, tt.Closed())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = session.Exec(ctx, "<get/>", nil)
	be.NilErr(t, err)
}

func TestCloseSuccess(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	err = session.Close(context.Background())
	be.NilErr(t, err)
	be.True(t, tt.Closed())
}

func TestCloseWithPendingRequests(t *testing.T) {
	// a device that says hello and answers no RPC
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, nil))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	// Start a request in background
	done := make(chan error, 1)
	go func() {
		rpc := NewRPC("test-op")
		_, err := session.ExecReply(context.Background(), rpc)
		done <- err
	}()

	// Give request time to register
	time.Sleep(50 * time.Millisecond)

	// Close without responding
	_ = tt.Close()

	// Request should receive ErrClosed
	select {
	case err := <-done:
		be.True(t, errors.Is(err, ErrClosed))
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for request to fail")
	}
}

func TestConcurrentDo(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	const numRequests = 10

	var wg sync.WaitGroup
	wg.Add(numRequests)
	results := make([]error, numRequests)

	// Send concurrent requests
	for i := range numRequests {
		go func() {
			defer wg.Done()
			rpc := NewRPC("test-op")
			_, err := session.ExecReply(context.Background(), rpc)
			results[i] = err
		}()
	}

	wg.Wait()

	// All requests should succeed
	for _, err := range results {
		be.NilErr(t, err)
	}

	_ = tt.Close()
}

func TestConcurrentOperationsAndNotifications(t *testing.T) {
	received := make(chan string, 10)
	handler := func(_ context.Context, msg *Message) {
		defer func() {
			_ = msg.Close()
		}()
		received <- "notification"
	}

	rpcCount := 0
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		rpcCount++
		// Interleave notifications with RPC replies
		if rpcCount == 1 {
			return []string{
				`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2024-01-05T12:34:56Z</eventTime></notification>`,
				netconftest.OKReply(msgID),
				`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2024-01-05T12:34:57Z</eventTime></notification>`,
			}
		}
		return []string{netconftest.OKReply(msgID)}
	}))

	session, err := Open(context.Background(), tt, WithNotifHandler(handler))
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	// Send RPC
	rpc := NewRPC("test-op")
	_, err = session.ExecReply(context.Background(), rpc)
	be.NilErr(t, err)

	// Should have received both notifications
	select {
	case <-received:
		// Got first notification
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for first notification")
	}

	select {
	case <-received:
		// Got second notification
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for second notification")
	}
}

func TestSessionID(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	be.Equal(t, uint64(42), session.SessionID())
}

func TestClientCaps(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	caps := session.ClientCaps()
	be.Nonzero(t, caps)
	be.True(t, caps.Has(CapNetConf10))
}

func TestServerCaps(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	caps := session.ServerCaps()
	be.Nonzero(t, caps)
	be.True(t, caps.Has(CapNetConf10))
	be.True(t, caps.Has(CapNetConf11))
}

func TestNotificationMessageNotClosed(t *testing.T) {
	handlerCalled := make(chan bool, 1)
	handler := func(context.Context, *Message) {
		// Intentionally don't close the message
		handlerCalled <- true
	}

	tt := netconftest.NewTransport(t, echoHandler)

	_, err := Open(context.Background(), tt, WithNotifHandler(handler))
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	// Push a notification
	tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
		<eventTime>2024-01-05T12:34:56Z</eventTime>
	</notification>`)

	// Wait for handler to be called
	select {
	case <-handlerCalled:
		// Handler was called, message should be auto-closed
	case <-time.After(1 * time.Second):
		t.Fatal("handler not called")
	}

	// Give time for auto-close
	time.Sleep(50 * time.Millisecond)
}

func TestNotificationConcurrent(t *testing.T) {
	var count atomic.Int32
	handler := func(_ context.Context, msg *Message) {
		defer func() {
			_ = msg.Close()
		}()
		count.Add(1)
	}

	tt := netconftest.NewTransport(t, echoHandler)

	_, err := Open(context.Background(), tt, WithNotifHandler(handler))
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	// Push 5 notifications
	for range 5 {
		tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
			<eventTime>2024-01-05T12:34:56Z</eventTime>
		</notification>`)
	}

	// Wait for all notifications to be processed
	time.Sleep(200 * time.Millisecond)

	be.Equal(t, int32(5), count.Load())
}

func TestHandshakeUpgrade(t *testing.T) {
	// helloGood advertises base:1.1 and the client supports it by default
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	be.True(t, tt.Upgraded())

	// the two sides still understand each other in chunked framing
	be.NilErr(t, session.Exec(context.Background(), "<get/>", nil))
	be.NilErr(t, session.Close(context.Background()))
}

// A device may say more right behind its hello, before the client has got
// round to switching to chunked framing.  That is chunked all the same: the
// device switches as it sends its hello, not when the client does.
func TestMessageRightBehindHello(t *testing.T) {
	const notification = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2024-01-05T12:34:56Z</eventTime></notification>`

	// the two used to race, and the client lost about one time in fifteen
	for range 200 {
		tt := netconftest.NewTransport(t, func(req string) []string {
			if strings.Contains(req, "<hello") {
				return []string{helloGood, notification}
			}
			return []string{netconftest.OKReply(netconftest.ExtractMessageID(req))}
		})

		got := make(chan struct{}, 1)
		session, err := Open(context.Background(), tt, WithNotifHandler(func(context.Context, *Message) { got <- struct{}{} }))
		be.NilErr(t, err)

		select {
		case <-got:
		case <-session.Done():
			t.Fatalf("the session ended: %v", session.Err())
		case <-time.After(5 * time.Second):
			t.Fatal("the notification did not arrive")
		}
		be.NilErr(t, session.Close(context.Background()))
	}
}

func TestHandshakeNoUpgradeWhenServerLacks11(t *testing.T) {
	tt := netconftest.NewTransport(t, func(string) []string {
		return []string{`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
			<capabilities>
				<capability>urn:ietf:params:netconf:base:1.0</capability>
			</capabilities>
			<session-id>42</session-id>
		</hello>`}
	})
	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()
	be.Nonzero(t, session)

	// Transport should NOT have been upgraded
	be.False(t, tt.Upgraded())
}

// errorTransport returns an error on Close().
type errorTransport struct {
	*netconftest.Transport
	closeErr error
}

func (e *errorTransport) Close() error {
	_ = e.Transport.Close()
	return e.closeErr
}

func TestCloseTransportError(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)
	customErr := errors.New("custom transport error")
	et := &errorTransport{Transport: tt, closeErr: customErr}

	session, err := Open(context.Background(), et)
	be.NilErr(t, err)

	err = session.Close(context.Background())
	be.Nonzero(t, err)
	be.True(t, errors.Is(err, customErr))
}

func TestNotificationHandler(t *testing.T) {
	received := make(chan string, 10)

	handler := func(_ context.Context, msg *Message) {
		defer func() { _ = msg.Close() }()

		var notif Notification
		if err := msg.Decode(&notif); err != nil {
			t.Logf("notification handler decode failure: %v", err)
			return
		}

		received <- notif.EventTime.Format(time.RFC3339)
	}

	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt, WithNotifHandler(handler))
	be.NilErr(t, err)
	be.Nonzero(t, session)

	// Push an unsolicited notification
	tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
  <eventTime>2024-01-05T12:34:56Z</eventTime>
</notification>`)

	// Wait for notification to be received
	select {
	case eventTime := <-received:
		be.Equal(t, "2024-01-05T12:34:56Z", eventTime)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for notification")
	}

	_ = tt.Close()
}

// The context handed to notification handlers ends with the session.
func TestNotificationHandlerContextCanceled(t *testing.T) {
	ctxCh := make(chan context.Context, 1)
	handler := func(ctx context.Context, _ *Message) {
		ctxCh <- ctx
	}

	tt := netconftest.NewTransport(t, echoHandler)
	session, err := Open(context.Background(), tt, WithNotifHandler(handler))
	be.NilErr(t, err)

	tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2024-01-05T12:34:56Z</eventTime></notification>`)
	ctx := <-ctxCh
	be.NilErr(t, ctx.Err())

	be.NilErr(t, session.Close(context.Background()))
	be.Nonzero(t, ctx.Err())
}

func TestNotificationDecoding(t *testing.T) {
	// Test that Notification struct properly decodes
	xmlData := `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
  <eventTime>2024-01-05T12:34:56Z</eventTime>
  <event xmlns="urn:example:event">
    <severity>critical</severity>
  </event>
</notification>`

	var notif Notification
	err := xml.Unmarshal([]byte(xmlData), &notif)
	be.NilErr(t, err)
	be.Equal(t, "2024-01-05T12:34:56Z", notif.EventTime.Format(time.RFC3339))
}

// Without a handler notifications are dropped and the session carries on.
// They can only come after a <create-subscription> sent as literal XML, and
// the log says which option is missing.
func TestNotificationWithoutHandler(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	logs := &logRecorder{}
	session, err := Open(context.Background(), tt, WithLogger(slog.New(logs)))
	be.NilErr(t, err)
	be.False(t, session.HasNotifHandler())

	tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2024-01-05T12:34:56Z</eventTime></notification>`)
	be.NilErr(t, session.Exec(context.Background(), "<get/>", nil))
	be.Equal(t, "skipping a notification: the session has no handler, see WithNotifHandler", strings.Join(logs.Records(), "; "))

	with, err := Open(context.Background(), netconftest.NewTransport(t, echoHandler),
		WithNotifHandler(func(context.Context, *Message) {}))
	be.NilErr(t, err)
	be.True(t, with.HasNotifHandler())
}

func TestConcurrentOperationsAndClose(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	const numRequests = 5
	var wg sync.WaitGroup
	wg.Add(numRequests)

	// Start concurrent operations
	for range numRequests {
		go func() {
			defer wg.Done()
			rpc := NewRPC("test-op")
			_, _ = session.ExecReply(context.Background(), rpc)
		}()
	}

	// Close while operations are in flight
	time.Sleep(10 * time.Millisecond)
	err = session.Close(context.Background())
	be.NilErr(t, err)

	// Should not panic or deadlock
	wg.Wait()
}

func TestQueueDrainsBeforeClose(t *testing.T) {
	// Track how many messages were processed
	var processed atomic.Int32
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		if !strings.Contains(req, "close-session") {
			processed.Add(1)
		}
		msgID := netconftest.ExtractMessageID(req)
		return []string{netconftest.OKReply(msgID)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	const numRequests = 10
	var wg sync.WaitGroup
	wg.Add(numRequests)

	// Queue up multiple requests
	for range numRequests {
		go func() {
			defer wg.Done()
			rpc := NewRPC("test-op")
			_, _ = session.ExecReply(context.Background(), rpc)
		}()
	}

	// Wait for all to queue/complete
	wg.Wait()

	// Close and verify all messages were processed
	err = session.Close(context.Background())
	be.NilErr(t, err)

	// All requests should have been processed
	be.Equal(t, int32(numRequests), processed.Load())
}

func TestOperationWhileClosing(t *testing.T) {
	// Handler that delays to ensure we catch the closing state
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		time.Sleep(50 * time.Millisecond)
		msgID := netconftest.ExtractMessageID(req)
		return []string{netconftest.OKReply(msgID)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	// Start a request
	go func() {
		rpc := NewRPC("test-op")
		_, _ = session.ExecReply(context.Background(), rpc)
	}()

	// Give it time to start
	time.Sleep(10 * time.Millisecond)

	// Start closing
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- session.Close(context.Background())
	}()

	// Try to send another request while closing
	rpc := NewRPC("test-op-2")
	// Close refuses new operations, so this either got in first or fails
	// with ErrClosed.
	if _, err := session.ExecReply(context.Background(), rpc); err != nil {
		be.True(t, errors.Is(err, ErrClosed))
	}

	// Close should complete without hanging
	select {
	case err := <-closeDone:
		be.NilErr(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung")
	}
}

func TestQueueSendContextCanceled(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = session.Close(context.Background()) }()

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Should fail immediately with context.Canceled
	rpc := NewRPC("test-op")
	reply, err := session.ExecReply(ctx, rpc)
	be.Nonzero(t, err)
	be.Zero(t, reply)
	be.Equal(t, context.Canceled, err)
}

func TestOperationAfterClose(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	// Close the session
	err = session.Close(context.Background())
	be.NilErr(t, err)

	// Try to send a request after close
	rpc := NewRPC("test-op")
	reply, err := session.ExecReply(context.Background(), rpc)
	be.Nonzero(t, err)
	be.Zero(t, reply)
	be.Equal(t, ErrClosed, err)
}

// TestAbandonedReplyRace exercises the race where the operation's context fires just
// as the reply arrives.  The receive loop must reclaim the abandoned message
// instead of wedging forever.
func TestAbandonedReplyRace(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	for i := range 500 {
		timeout := time.Duration(i%50+1) * 10 * time.Microsecond
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, _ = session.ExecReply(ctx, "<get/>")
		cancel()
	}

	// The session must still be usable.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = session.ExecReply(ctx, "<get/>")
	be.NilErr(t, err)
}

// TestOperationCloseRace hammers operations from multiple goroutines while Close runs.  A
// send racing shutdown must return ErrClosed, never panic on a closed
// channel.
func TestOperationCloseRace(t *testing.T) {
	for range 100 {
		tt := netconftest.NewTransport(t, echoHandler)

		session, err := Open(context.Background(), tt)
		be.NilErr(t, err)

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				for {
					if _, err := session.ExecReply(ctx, "<get/>"); err != nil {
						return
					}
				}
			})
		}

		_ = session.Close(context.Background())
		wg.Wait()
		_ = tt.Close()
	}
}

// TestNotifHandlerExec verifies a notification handler can issue RPCs on the
// same session without deadlocking the receive loop.
func TestNotifHandlerExec(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	done := make(chan error, 1)
	var session *Session

	handler := func(context.Context, *Message) {
		execCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- session.Exec(execCtx, "<get/>", nil)
	}

	session, err := Open(context.Background(), tt, WithNotifHandler(handler))
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	tt.Push(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime><event/></notification>`)

	select {
	case err := <-done:
		be.NilErr(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("notification handler deadlocked")
	}
}

// TestExecErrorNoSeverity ensures rpc-errors from non-conforming servers
// that omit <error-severity> are still surfaced as errors.
func TestExecErrorNoSeverity(t *testing.T) {
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		return []string{netconftest.Reply(msgID,
			`<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag></rpc-error>`)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = session.Exec(ctx, "<get/>", nil)
	be.Nonzero(t, err)

	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
}

// TestCloseReportsRPCError ensures a close-session failure is reported
// instead of silently discarded.
func TestCloseReportsRPCError(t *testing.T) {
	tt := netconftest.NewTransport(t, netconftest.Hello(helloGood, func(req string) []string {
		msgID := netconftest.ExtractMessageID(req)
		if strings.Contains(req, "<close-session") {
			return []string{netconftest.Reply(msgID,
				`<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity></rpc-error>`)}
		}
		return []string{netconftest.OKReply(msgID)}
	}))

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)

	err = session.Close(context.Background())
	be.Nonzero(t, err)

	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
}

// TestMessageReadAfterClose ensures reads on a closed message error instead
// of observing a later message's bytes.
func TestMessageReadAfterClose(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := session.Stream(ctx, "<get/>")
	be.NilErr(t, err)
	be.NilErr(t, msg.Close())

	_, err = msg.Read(make([]byte, 1))
	be.Nonzero(t, err)
	be.True(t, errors.Is(err, ErrClosed))
}

func TestStream(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msg, err := session.Stream(ctx, NewRPC("<get/>"))
	be.NilErr(t, err)

	body, err := io.ReadAll(msg)
	be.NilErr(t, err)
	be.In(t, "<ok/>", string(body))
	be.NilErr(t, msg.Close())

	// Session must be free for the next RPC after Close.
	err = session.Exec(ctx, "<get/>", nil)
	be.NilErr(t, err)
}

// TestStreamAbandonedReplyRace exercises the reclaim path: a Stream whose
// context fires as the reply arrives must not wedge the receive loop.
func TestStreamAbandonedReplyRace(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	for i := range 500 {
		timeout := time.Duration(i%50+1) * 10 * time.Microsecond
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		msg, err := session.Stream(ctx, NewRPC("<get/>"))
		if err == nil {
			_ = msg.Close()
		}
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = session.ExecReply(ctx, "<get/>")
	be.NilErr(t, err)
}

// TestRepliesAreIndependent verifies buffered replies don't gate the receive
// loop: two replies can be held unclosed simultaneously.
func TestRepliesAreIndependent(t *testing.T) {
	tt := netconftest.NewTransport(t, echoHandler)

	session, err := Open(context.Background(), tt)
	be.NilErr(t, err)
	defer func() { _ = tt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := session.ExecReply(ctx, "<get/>")
	be.NilErr(t, err)

	// Nothing has to be done with the first reply for the next one to come.
	second, err := session.ExecReply(ctx, "<get/>")
	be.NilErr(t, err)

	be.In(t, `message-id="1"`, string(first.Raw))
	be.In(t, `message-id="2"`, string(second.Raw))
}
