# Sessions

This page covers a `netconf.Session` from `netconf.Open` to its end. Read
it when you write a program that keeps a session open or shares one
between goroutines.

- [Session lifetime](#session-lifetime)
- [Options of `netconf.Open`](#options-of-netconfopen)
- [Requests from several goroutines](#requests-from-several-goroutines)
- [Tracing](#tracing)
- [Malformed XML from the device](#malformed-xml-from-the-device)
- [Notifications](#notifications)
- [Untrusted devices](#untrusted-devices)

## Session lifetime

`Session.Close` sends `<close-session>`, waits for the reply and closes the
transport. The session is over when `Close` returns, even with an error.
`Close` is safe to call more than once and on a session that has already
ended. Only the first call can return an error. With a streamed reply
still open, `Close` does not send `<close-session>`, because it could not
read the reply. `Close` cancels a running notification handler's context
without waiting for the handler.

A session also ends without `Close`. `Session.Done` returns a channel that
is closed at session end, and `Session.Err` reports the cause. `Err` is nil
while the session is alive and when `Close` ended it. Wait on `Done` and on
your own context in one `select`:

```go
select {
case <-ctx.Done():
	// shutting down: ctx is over, so close with a fresh context
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.Close(closeCtx); err != nil {
		log.Printf("close: %v", err)
	}
case <-session.Done():
	log.Printf("session ended: %v", session.Err()) // nil after Close
}
```

These causes end a session without `Close`:

| Cause | `Session.Err` |
| --- | --- |
| The device closes the transport, or a read fails | The transport's error |
| A write fails partway through | `failed to write message`, wrapping the write error |
| `ncssh.WithKeepalive` gives up on the device | `ncssh.ErrKeepalive` |
| A message exceeds `WithMaxMessageSize` | `netconf.ErrMessageTooLarge` |
| A message stalls longer than `WithMessageTimeout` | `netconf.ErrMessageStalled` |
| The notification queue is full | `netconf.ErrNotificationOverflow` |
| [100 skipped messages](#untrusted-devices) in a row | `ignored 100 messages in a row`, with no sentinel |

An operation on a session that ended fails with an error that matches
`netconf.ErrClosed` and wraps the cause, if any. See
[Error targets](operations.md#error-targets).

In a long-running program, dial with `ncssh.WithKeepalive`, an option of
`ncssh.DialHost` only, so that `Done` closes when the device stops
answering SSH keepalives. To reconnect, see
[Reconnect without replaying writes](production-use.md#reconnect-without-replaying-writes).

## Options of `netconf.Open`

| Option | Effect |
| --- | --- |
| `WithOperationTimeout(d)` | Bounds every operation whose context has no deadline, default none. See [Timeouts](operations.md#timeouts) |
| `WithMaxMessageSize(n)` | Largest buffered message, the hello included, default 256 MiB (`DefaultMaxMessageSize`). Zero or less removes the limit. Streamed replies are exempt |
| `WithMessageTimeout(d)` | Ends the session with `ErrMessageStalled` when a message that has begun goes this long without a byte, default none. See [Stalled messages](operations.md#stalled-messages) |
| `WithNotifHandler(fn)` | The function that receives notifications, see [Notifications](#notifications) |
| `WithNotifQueue(q)` | The bounds of the notification queue, and whether overflow ends the session or drops the notification |
| `WithCapability(caps...)` | The capabilities the client advertises, in place of `DefaultCapabilities`, base:1.0 and base:1.1. Without `CapNetConf10` or `CapNetConf11`, `Open` fails before it sends anything |
| `WithReplyRepair(fn)` | A function that sees every buffered reply and notification first, default none. See [Malformed XML from the device](#malformed-xml-from-the-device) |
| `WithTrace(trace)` | Functions called for each operation and notification. See [Tracing](#tracing) |
| `WithLogger(logger)` | The session's `*slog.Logger`, default `slog.Default()`. A nil logger turns logging off |

The session logs at warn level:

- warnings in the hello, and warnings that `Session.Exec` cannot return
- each skipped message
- notifications dropped under `NotifQueue.Drop`, less and less often
  after the first drop
- a session end without `Close`
- a transport that fails to close after `Open` fails

At debug level it logs a late reply to an operation whose caller gave up.
A record quotes the first warning, or the message-id or element name of a
skipped message, truncated to 128 bytes. A device warning can quote
configuration, so pass `WithLogger(nil)` or a logger whose `slog.Handler`
redacts device text. The transports and the `rpc` and `junos` packages log
nothing of their own.

## Requests from several goroutines

A `netconf.Session` is safe for concurrent use. Concurrent operations are
written one behind the other, and each reply reaches its operation by
message-id. RFC 6241, section 4.5, requires the device to process requests
and reply in the order they arrive.

Concurrency safety covers single operations, not a configuration workflow.
Run one `junos.Change.Apply` at a time per session, see
[Change safety](junos.md#change-safety).

Over chunked framing to a Junos device, the session writes one request at a
time. That applies when both sides advertise `base:1.1` and the device
advertises the Junos capability, `http://xml.juniper.net/netconf/junos/1.0`.
Each operation holds a turn from before its request is written until the
next message that is not a notification has arrived whole, normally its
reply. vJunos-router 26.2 ended the session when a
request was written right behind another, see
[ADR 0006](adr/0006-junos-gets-one-request-at-a-time-over-chunks.md).
Junos 26.2 advertises `base:1.1` only with both `rfc-compliant` and
`version-1.1` set under `system services netconf`, see
[Configuration for chunked framing](junos-verified.md#configuration-for-chunked-framing).

On a session with turns:

- An operation whose context ends while it waits for its turn is not sent.
- An operation whose caller gave up keeps its turn, so a request the
  device never answers holds up every request behind it,
  `<close-session>` included. `Close` then fails when its context ends,
  and the session is over anyway.
- A streamed reply keeps the turn until the caller closes its message.

A session opened with `netconf.WithCapability(netconf.CapNetConf10)` uses
end-of-message framing and has no turns. See
[Untrusted devices](#untrusted-devices) for the risk.

## Tracing

`netconf.WithTrace` gives a session a `*netconf.Trace`, functions the
session calls for each operation and each notification, in the manner of
`net/http/httptrace`:

```go
trace := &netconf.Trace{
	OperationDone: func(op netconf.OperationInfo, result netconf.OperationResult) {
		slog.Info("netconf operation",
			"session", op.SessionID,
			"operation", op.Name,
			"duration", result.Duration,
			"reply_bytes", result.ReplySize,
			"error", result.Err)
	},
}
session, err := netconf.Open(ctx, tr, netconf.WithTrace(trace))
```

An operation is reported once its request is encoded. One whose context
ended before that, or whose request fails to encode, is not.
`Trace.OperationStart` is called when an operation is about to be sent,
and `Trace.OperationDone` once for each `OperationStart`, when the reply
has arrived and been checked for RPC errors or the wait for it ended. Both
get a `netconf.OperationInfo` with the session-id, message-id,
operation name, request as sent and start time. `OperationDone` also gets a
`netconf.OperationResult`:

- `Duration` runs from the start to the reply or the end of the wait.
  It ends where a streamed reply begins, and includes any wait for a turn.
- `ReplySize` is the reply's size in bytes, 0 when no reply arrived and for
  a streamed reply.
- `Err` is the error the wait ended with, the reply's RPC errors, or the
  `SyntaxError` from the scan for them, see
  [Malformed XML from the device](#malformed-xml-from-the-device). It does
  not include an error found later, such as a missing `<ok/>`. A streamed
  reply is not checked.

`Trace.Notification` is called for every notification on its way to the
handler, with its size in bytes and a `dropped` argument, true when the
queue had no room or the session is closing.

An operation's name is its element name without a prefix, and `Close`
reports `close-session`. A `junos.Change` committed under the lock reports
`lock`, `load-configuration`, `get-configuration` for the compare,
`commit-configuration` and `unlock`. With `Private`, `open-configuration`
and `close-configuration` replace `lock` and `unlock`. `Confirm` can add a
second `commit-configuration`, and `DryRun` can replace the commit with
`get-commit-information` and a `commit-configuration` check. A rollback
before `unlock` shows as a second `load-configuration`.

The session calls the trace functions synchronously. `Notification` runs
on the goroutine that reads from the device and holds up every reply
behind it. Return quickly, and make the functions safe for concurrent
use. A trace function must not call `Close` on the session, because the
call can deadlock, so leave that to another goroutine. It must not panic
either, because the panic can leave the transport open or an unread reply
that every later operation waits behind.

`OperationInfo.Request` holds whatever the operation carries, such as a
loaded configuration with its password hashes and keys. Do not change it.
Log a redacted copy.

## Malformed XML from the device

XML allows no control characters other than tab, line feed and carriage
return. A device that passes on text from elsewhere, such as an LLDP
neighbor's description, can put one in a reply. Every way of reading that
reply then fails with a `*netconf.SyntaxError` that says where:

```go
reply := &netconf.Reply{Raw: []byte("<rpc-reply><description>Vendor OS 1.2\x01build 7</description></rpc-reply>")}

_, err := reply.Element("description")

var syntaxErr *netconf.SyntaxError
if errors.As(err, &syntaxErr) {
	fmt.Printf("byte %d of the reply: %q\n", syntaxErr.Offset, syntaxErr.Around)
}
fmt.Println(err)
```

```text
byte 37 of the reply: "reply><description>Vendor OS 1.2\x01build 7</description></rpc-repl"
failed to parse <description>: XML syntax error on line 1: illegal character code U+0001 at byte 37
```

`SyntaxError.Around` holds up to 32 bytes on either side of the offset.
The error text leaves those bytes out, because they can hold a password
hash. It keeps the character or name the parser reports, such as
`U+0001`.

`Session.ExecReply` scans a reply for RPC errors only when the reply
contains the text `rpc-error` or `<xnm:error`. Otherwise it returns a nil
error even for a reply that is not XML, as does `Session.Exec` with a nil
`reply` argument, and the `SyntaxError` comes from `Reply.Element`,
`Reply.Elements` or `Reply.Decode`. When the scan fails, `ExecReply`
returns the reply with the `SyntaxError`, which also holds the RPC errors
found before the bad byte, so `errors.Is(err, netconf.ErrLockDenied)`
still finds a refused lock.

To read such replies, open the session with a repair:

```go
session, err := netconf.Open(ctx, tr, netconf.WithReplyRepair(netconf.StripInvalidChars))
```

`netconf.StripInvalidChars` takes out the characters XML does not allow,
U+FFFE and U+FFFF included, bytes that are not UTF-8, and references to a
disallowed character, such as `&#27;`, outside a CDATA section.
`netconf.WithReplyRepair` takes any `func([]byte) []byte`, which sees each
buffered reply and notification as the device sent it and may change it in
place. On a device with the Junos capability, the
[Junos text normalization](junos.md#non-ascii-text) runs after the repair.
Neither runs on the hello or on a streamed reply.

The session skips a message whose start tag it cannot read, before any
repair, because it needs that tag to tell a reply from a notification. An
operation waiting for that reply waits until its context ends.

Junos 26.2 sends a control character in a statement of its own
configuration as `&#167;`, the section sign, so reading that configuration
needs no repair. See
[Control characters in statements](junos-verified.md#control-characters-in-statements).

## Notifications

Give `netconf.Open` a notification handler with `netconf.WithNotifHandler`,
then subscribe, for example with `rpc.CreateSubscription`:

```go
session, err := netconf.Open(ctx, tr,
	netconf.WithNotifHandler(func(ctx context.Context, msg *netconf.Message) {
		var notif netconf.Notification
		if err := msg.Decode(&notif); err != nil {
			log.Printf("decode notification: %v", err)
			return
		}
		// the content is one element, named for what happened
		log.Printf("%s at %s", notif.Content.XMLName.Local, notif.EventTime)

		if notif.Content.XMLName.Local == "netconf-config-change" {
			var change struct {
				User string `xml:"changed-by>username"`
			}
			if err := notif.Content.Decode(&change); err == nil {
				log.Printf("changed by %s", change.User)
			}
		}
	}),
)
// ...
err = rpc.CreateSubscription{}.Exec(ctx, session)
```

A session cannot get a handler after `Open`. Without one,
`rpc.CreateSubscription` sends nothing and fails with
`netconf.ErrNoNotifHandler`, because the session would skip every
notification and end after 100 in a row. A subscription sent as literal
XML is not checked.

The session calls the handler on its own goroutine, one notification at a
time, in arrival order. The handler may issue operations on the same
session. Its context is canceled at session end.

Notifications that arrive while the handler is busy wait in a queue, by
default up to 10,000 notifications or 64 MiB,
`netconf.DefaultNotifQueueMessages` and `netconf.DefaultNotifQueueBytes`.
An empty queue takes one notification above the byte budget. A
notification that arrives at a full queue ends the session with
`netconf.ErrNotificationOverflow`. `netconf.WithNotifQueue` changes the
bounds. With `NotifQueue.Drop` set, the session drops what does not fit,
logs the loss and stays open.

The `:interleave` capability says whether a device accepts other operations
on a session with a subscription. Check it with
`session.ServerCaps().Has(":interleave:1.0")`, and without it give the
subscription a session of its own. On Junos, do not rely on it. Junos 26.2
and Junos OS Evolved 26.2 advertise it whenever notifications are
configured, but without `notification interleave` they reply to nothing on
a session with a subscription. See
[Operations on a subscription session](junos-verified.md#operations-on-a-subscription-session).

vJunos-router 26.2 sends every system log event twice, as `<syslog>` and as
`<state-system-syslog>`, see
[Captured replies](junos-verified.md#captured-replies).

For a complete program, see the
[notification example](../examples/notifications/README.md). For
reconnects and lost notifications, see
[Notifications and reconnects](production-use.md#notifications-and-reconnects).

## Untrusted devices

A session bounds what a device can make it buffer or wait for, per
session, not for the whole program.

- **Message size.** A buffered message over `netconf.WithMaxMessageSize`
  ends the session with `netconf.ErrMessageTooLarge`,
  and a hello that large fails `Open`. Nothing limits the size or duration
  of a reply read with `Session.Stream`.
- **Notification queue.** A full queue ends the session with
  `netconf.ErrNotificationOverflow`, unless `netconf.NotifQueue.Drop` is
  set. See [Notifications](#notifications).
- **Skipped messages.** A session ends after 100 skipped messages in a row.
  It skips messages with no XML element or of an unknown kind, replies to
  no operation, and notifications on a session without a handler. A late
  reply to an operation whose caller gave up, or a notification dropped
  under `netconf.NotifQueue.Drop`, resets the count.
- **Stalled messages.** Without `netconf.WithMessageTimeout`, off by
  default, a message that begins and then stops holds the session forever.
  See [Stalled messages](operations.md#stalled-messages).
- **End-of-message framing.** Under base:1.0, a message ends at the first
  `]]>]]>`, even inside the data. If a device may return data you do not
  control, require chunked framing with
  `netconf.WithCapability(netconf.CapNetConf11)`. `Open` then fails against
  a device that speaks only base:1.0.
- **CLI commands.** A `junos.Command` is one line of CLI. Building it from
  untrusted input lets that input choose the command.
- **Diagnostics with secrets.** `tr.DebugCapture(in, out)`, called before
  `Open`, copies every framed message, secrets included. Every transport
  here embeds it from `transport.Framer`. `netconf.OperationInfo.Request`
  and `netconf.SyntaxError.Around` hold message bytes as well.

Report a vulnerability as [SECURITY.md](../.github/SECURITY.md) describes.
