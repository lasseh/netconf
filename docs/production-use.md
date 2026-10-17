# Production use

Read this page before you run the library in a long-lived program, such as
a device worker or a daemon. A `netconf.Session` is one session with one
device. Your program owns the rest: the device inventory, reconnects,
uncertain writes, notification recovery and logging.

- [Own the session lifetime](#own-the-session-lifetime)
- [Choose timeout bounds](#choose-timeout-bounds)
- [Reconnect without replaying writes](#reconnect-without-replaying-writes)
- [Notifications and reconnects](#notifications-and-reconnects)
- [Bound concurrency and memory](#bound-concurrency-and-memory)
- [Record useful diagnostics](#record-useful-diagnostics)

## Own the session lifetime

Give each device worker a context that your program cancels on shutdown,
and derive a child context for the dial, for `netconf.Open` and for each
operation. The context of `netconf.Open` bounds only the hello exchange.

Dial with `ncssh.WithKeepalive`, which is off by default. With it, a device
that stops answering SSH ends the session.

Watch the worker context and `session.Done()` together. On shutdown, stop
handing out new work, then close the session under a fresh context with a
short deadline:

```go
select {
case <-ctx.Done():
	// shutting down
case <-session.Done():
	log.Printf("session ended: %v", session.Err()) // reconnect
	return
}

closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := session.Close(closeCtx); err != nil {
	log.Printf("close: %v", err) // the session is over all the same
}
```

`session.Err()` says why the session ended, see
[Session lifetime](sessions.md#session-lifetime). `Close` does not wait for
a running notification handler, so wait for it yourself. Never call
`Close` from a `netconf.Trace` function, because it would wait for itself.

## Choose timeout bounds

| Bound | Default | What it detects | What it does not do |
| --- | --- | --- | --- |
| Context of `ncssh.DialHost` | None | Slow TCP setup, SSH login, jump hosts, the subsystem request | Bound anything after the dial |
| Context of `netconf.Open` | 30 s (`netconf.DefaultHelloTimeout`) when it has no deadline | A hello exchange that does not finish | Bound the session after `Open` returns |
| Operation context, or `netconf.WithOperationTimeout` for a context without a deadline | None | An operation that takes too long to send or answer | Stop an operation that the device has received |
| `ncssh.WithKeepalive(interval, maxMissed)`, `DialHost` only | Off | A device that stops answering SSH keepalives. The session ends with `ncssh.ErrKeepalive` | Notice a device that answers SSH but never finishes an operation |
| `netconf.WithMessageTimeout` | Off | Silence inside a buffered message that has begun. The session ends with `netconf.ErrMessageStalled` | Bound the wait before a message begins, catch a device that sends only framing, or bound a streamed reply |
| A read deadline of your own on a streamed reply | None | A streamed reply that takes too long. Abandoning the reply ends the session | Leave the other operations on that session running |
| Context of `Session.Close` | 30 s (`netconf.DefaultCloseTimeout`) when it has no deadline | A device that does not answer `<close-session>` | Wait for a notification handler that is still running |

A context with any deadline overrides `netconf.WithOperationTimeout`, so
give each operation its own short context, not the context of a whole
job. Set `netconf.WithMessageTimeout` above the slowest
operation on the session, usually a commit or a commit check. See
[Timeouts](operations.md#timeouts) and
[Stalled messages](operations.md#stalled-messages).

## Reconnect without replaying writes

When a session ends while your program still needs it:

1. Record `session.Err()` and which writes were in flight.
2. Stop using the old session. A session cannot be reopened, so dial and
   call `netconf.Open` again.
3. Retry with exponential backoff and jitter under the worker context, so
   shutdown ends the retries. Cap the attempts or the total time.
4. Do not retry a failure that waiting does not fix: `ncssh.ErrAuth`, a
   `*knownhosts.KeyError` or `*knownhosts.RevokedError`,
   `ncssh.ErrSubsystemRefused`, or a `*netconf.HelloError` with RPC errors
   from the device. See
   [When connecting fails](connecting.md#when-connecting-fails).
5. Read the device state before you repeat a write whose outcome is
   unknown.

A read can be sent again. A write cannot, because a failed exchange does
not show that the write failed. Give each write an id of your own, and
escalate when the device state does not show whether it took effect. See
[Recover from an uncertain write](standard-netconf.md#recover-from-an-uncertain-write)
and [After `ErrCommitUnknown`](junos.md#after-errcommitunknown).

## Notifications and reconnects

A new session has no subscription. Open it with `netconf.WithNotifHandler`,
then send `rpc.CreateSubscription` again. Read the capabilities again from
the new session.

Give the subscription a session of its own. It then does not depend on
`:interleave:1.0`, and slow operations elsewhere do not delay its
notifications. Without `notification interleave`, Junos 26.2 does not
answer `<close-session>` on a session with a subscription, so give its
`Session.Close` a short deadline. The
[notification example](../examples/notifications/README.md) uses a
separate session and exits when the session ends.

A full notification queue ends the session with
`netconf.ErrNotificationOverflow`, which means notifications were lost.
Move slow work out of the handler to a bounded queue of your own. With
`netconf.NotifQueue.Drop` set, the session drops what does not fit and
stays up. Neither the default nor `Drop` gives durable or exactly-once
delivery, so read the current device state after a gap. Replay with
`rpc.CreateSubscription.StartTime` needs RFC 5277 replay on the device and
the stream. Handle duplicates and gaps yourself. vJunos-router 26.2 sends
every system-log notification twice. See
[Notifications](sessions.md#notifications) for the queue bounds.

## Bound concurrency and memory

A session is safe for concurrent use, but nothing limits how many buffered
replies your program holds at once. `netconf.WithMaxMessageSize`, 256 MiB
by default, bounds one message, and `netconf.WithNotifQueue` bounds the
queue of one session. Neither bounds the memory of the program, so limit
how many sessions and operations run at once. See
[Untrusted devices](sessions.md#untrusted-devices).

Run one configuration workflow at a time per session, see
[Change safety](junos.md#change-safety). Separate sessions still compete
for the lock on a datastore, see
[Edit the candidate safely](standard-netconf.md#edit-the-candidate-safely)
and [Lock and candidate conflicts](junos.md#lock-and-candidate-conflicts).

Over chunked framing to a Junos device the session sends one request at a
time, so more goroutines do not speed it up. See
[Requests from several goroutines](sessions.md#requests-from-several-goroutines).

Give streamed reads a separate session and a read deadline of your own,
because the only way to abandon a streamed reply is to end its session.
See [Large replies](operations.md#large-replies).

## Record useful diagnostics

A `netconf.Trace`, passed with `netconf.WithTrace`, receives the
session-id, message-id, operation name, duration, reply size and error of
every operation. Add the device identity to each record, and classify
`OperationResult.Err` with `errors.Is`. Keep metrics for reconnect attempts
and for dropped notifications, which `Trace.Notification` reports with
`dropped` set. See [Tracing](sessions.md#tracing).

These outputs can carry configuration and secrets, so keep them out of
shared logs by default:

- `OperationInfo.Request`, which for a load holds the configuration with
  its password hashes and keys
- `Reply.Raw`, configuration diffs such as `junos.ChangeResult.Diff`, and
  notification payloads
- `SyntaxError.Around`, the bytes around a parse error
- `transport.Framer.DebugCapture`, which copies whole messages
- the session's log, which can quote a device warning, see
  [Options of `netconf.Open`](sessions.md#options-of-netconfopen)
- the device's error text, which can quote configuration statements

Never turn on `ncssh.WithInsecureIgnoreHostKey` to make unattended retries
succeed. A `*knownhosts.KeyError` or `*knownhosts.RevokedError` is a
problem to investigate.
