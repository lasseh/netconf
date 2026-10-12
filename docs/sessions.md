# Sessions

A session is safe for concurrent use. `Close` sends `<close-session>` and
closes the transport. A session also ends when its connection dies:

```go
select {
case <-session.Done():
	log.Printf("session ended: %v", session.Err()) // nil after Close
}
```

Operations on an ended session fail with an error matching
`netconf.ErrClosed`. Combine `Done` with `ncssh.WithKeepalive` to detect dead
peers in long-running programs and reconnect. A session that the keepalive
ended says so: `errors.Is(session.Err(), ncssh.ErrKeepalive)`.

`netconf.Open` takes these options:

| Option | Effect |
| --- | --- |
| `WithOperationTimeout(d)` | A bound for every operation whose context has no deadline, default none |
| `WithMaxMessageSize(n)` | The largest message the session accepts, default 256 MiB |
| `WithMessageTimeout(d)` | How long a message that has begun may go without a byte before the session ends, default for ever; see [Timeouts](operations.md#timeouts) |
| `WithNotifHandler(fn)` | The function that receives notifications |
| `WithNotifQueue(q)` | How many notifications may wait for the handler, and what happens to one that does not fit |
| `WithCapability(caps...)` | The capabilities the client advertises, default base:1.0 and base:1.1 |
| `WithReplyRepair(fn)` | A function that sees every buffered reply and notification before the session reads it, default none; see [Messages that are not XML](#messages-that-are-not-xml) |
| `WithTrace(trace)` | Functions that are told of every operation as it starts and ends, and of every notification; see [Tracing](#tracing) |
| `WithLogger(logger)` | The `slog.Logger` of the session, default `slog.Default()`; nil turns logging off |

The session logs little: at warn level a session that ended without `Close`,
a message it skipped, and warnings in a reply that `Exec` had no way to
return.

## Requests from several goroutines

Operations issued at the same time are written one behind the other as they
come, and each reply goes to its operation by its message-id. The device
then has several requests at once. It carries them out one after the other
and answers in the order they arrived, which RFC 6241 asks of it in section
4.5.

Over chunked framing to a Junos device the session writes a request only
when the answer to the one before it has arrived whole. Junos 26.2 ended
sessions over a request that was written right behind another, see chunked
framing in [What is verified against Junos](junos-verified.md). The session
does this by itself where
both sides advertise `base:1.1` and the device advertises the Junos
capability. Three things are different on such a session:

- A request whose context ends while it waits for its turn is not sent.
  On another session it is sent, and carried out with nobody waiting for
  the reply.
- A request whose caller gave up keeps its turn until its answer has
  arrived. A request that the device does not answer holds up all that
  come after it, `<close-session>` included: `Close` then returns an error
  when its context ends, and the session is over all the same.
- A reply read with `Stream` has arrived whole when its message is closed.

A session opened with `netconf.WithCapability(netconf.CapNetConf10)` speaks
end-of-message framing to such a device, and writes requests as they come.

## Tracing

The typed operations and `junos.Change.Apply` issue their operations inside
the library, where no caller can put a timer. `netconf.WithTrace` takes
functions that the session calls for every operation, in the manner of
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

`OperationStart` is called when an operation is about to be sent and
`OperationDone` when its reply has arrived or the wait for it has ended,
once each, with the session-id, the message-id, the name of the operation
and the request as it is sent. The name is that of the element: a
`junos.Change` shows as `lock`, `load-configuration`, `get-configuration`
(the compare), `commit-configuration` and `unlock`. `Notification` is called
for every notification on its way to the handler, and says whether the queue
had room for it.

The functions run on the path of what they report and have to return
quickly. They must not call `Close` on the session, which would wait for
itself, and must not panic: to end a session over what a function saw, tell
another goroutine. The request holds whatever the operation carries, the configuration
of a load with its secrets included: log it with care. For a reply read with
`Stream` the duration is the time to the beginning of the reply. Where a
request waits for its turn, see
[Requests from several goroutines](#requests-from-several-goroutines),
`OperationStart` is called before the wait and the wait is part of the
duration.

## Messages that are not XML

XML allows no control characters but tab, line feed and carriage return. A
device that passes on text it does not own, the description an LLDP
neighbour sends for one, can put such a character into a reply, and then
nothing reads that reply: every way of decoding it fails. The error says
where:

```go
reply := &netconf.Reply{Raw: []byte("<rpc-reply><description>Vendor OS 1.2\x01build 7</description></rpc-reply>")}

_, err := reply.Element("description")

var syntaxErr *netconf.SyntaxError
if errors.As(err, &syntaxErr) {
	fmt.Printf("byte %d of the reply: %q\n", syntaxErr.Offset, syntaxErr.Around)
}
fmt.Println(err)
```

```
byte 37 of the reply: "reply><description>Vendor OS 1.2\x01build 7</description></rpc-repl"
failed to parse <description>: XML syntax error on line 1: illegal character code U+0001 at byte 37
```

The text of the error has the place and not the bytes around it: an error
goes where it is logged, and next to a control character in a
configuration there may be a password hash. `Around` has them for a
program that wants to show them. What the parser itself names is still in
the text: the character it does not allow, or the name of an entity or an
element it cannot read.

A reply that the session itself cannot go through for errors comes back
from `ExecReply` together with that error, so `reply.Raw` has what the
device sent. The RPC errors in front of where it stops being XML are in
the error as well: `errors.Is(err, netconf.ErrLockDenied)` still finds a
lock that was refused.

To read such replies, open the session with a repair:

```go
session, err := netconf.Open(ctx, tr, netconf.WithReplyRepair(netconf.StripInvalidChars))
```

`StripInvalidChars` takes out the characters XML does not allow, bytes that
are not UTF-8 and references to such characters (`&#27;`). `WithReplyRepair`
takes any function from a message to a message; it sees each reply and
notification as the device sent it, before the session reads it. Without
the option no message is changed.

Junos 26.2 needs none of this for what it holds itself. Loaded with a
control character in a statement or in the comment of a commit, it sends a
reference to another character back: `&#167;`, the section sign, in a
configuration in every format, and `&#xe01b;` for U+001B, a character from
the private use area, in the output of a command, in a diff and in the
commit history. That was looked at on vJunos-router, vJunos-switch and
vJunosEvolved. Text that Junos passes on from a neighbour was not.

## Notifications

```go
session, err := netconf.Open(ctx, tr,
	netconf.WithNotifHandler(func(ctx context.Context, msg *netconf.Message) {
		var notif netconf.Notification
		if err := msg.Decode(&notif); err != nil {
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

The handler has to be there before the subscription, and `Open` is the only
place to give it. On a session without one `rpc.CreateSubscription` sends
nothing and fails with `netconf.ErrNoNotifHandler`: the session would skip
every notification, and end after a hundred of them in a row.

The handler is called for one notification at a time, in the order they
arrive, and may issue operations on the same session. Notifications that
arrive while it is busy wait in a queue of at most 10,000 notifications or
64 MiB. When that is full the session ends with
`netconf.ErrNotificationOverflow`, so a receiver that lost events knows it;
`netconf.WithNotifQueue` changes the bounds, or drops what does not fit and
keeps the session.

Whether the device accepts other operations on a session with a
subscription is its `:interleave` capability
(`session.ServerCaps().Has(":interleave:1.0")`); without it, give the
subscription a session of its own.

Junos has notifications with `set system services netconf notification`,
and what it sends is not the three events its documentation names. Junos
26.2 sends its system log: every event twice, as a `<syslog>` and as a
`<state-system-syslog>` element, hundreds of them for one commit, and
changes of state such as `<oc-nd6-change>` in between. Junos OS Evolved
26.2 sends changes of state, `<oc-system-state-change>` and the like, and
nothing about a login or a commit. Both advertise `:interleave` whatever is
configured, and answer on a session that has a subscription only with
`notification interleave` set. Without it they answer nothing there,
`<close-session>` included: an operation waits for its context, and so
does `Close`.

## Untrusted devices

- Messages are limited to 256 MiB (`netconf.WithMaxMessageSize`); a larger
  one ends the session with `ErrMessageTooLarge`. Only replies requested
  with `Stream` are exempt.
- A session ends after 100 messages in a row that nobody asked for.
- Base:1.0 framing ends a message at the first `]]>]]>`, wherever it comes
  from. If a device may return data you do not control, require chunked
  framing with `netconf.WithCapability(netconf.CapNetConf11)`; the hello
  exchange then fails against devices that only speak base:1.0.
- `Framer.DebugCapture` copies whole messages, including any secrets in
  them.

How to report a vulnerability is in [SECURITY.md](../.github/SECURITY.md).
