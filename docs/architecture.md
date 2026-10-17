# Architecture

This page maps the code for contributors. Read it before you change the
session, a transport or `junos.Change`. Terms are in
[CONTEXT.md](../CONTEXT.md) and decisions in the [ADRs](adr).

## Package boundaries

| Directory | Responsibility |
| --- | --- |
| Module root, `netconf` | The session, its replies, RPC errors and notifications |
| `rpc` | The standard operations and their replies |
| `junos` | Junos operations and results, and `junos.Change` |
| `transport` | The `transport.Transport` interface and RFC 6242 framing |
| `transport/ssh`, `transport/tls`, `transport/command` | Login or process start, and the connection or process they own, see [Connecting](connecting.md#tls-and-ready-made-ssh-clients) |
| `transport/transporttest` | The conformance suite every transport must pass |
| `internal/netconftest` | Scripted devices for unit tests |
| `internal/operr`, `internal/xmlutil` | Error prefixes with the operation's name, and shared XML helpers |
| `internal/tail` | The last 4 KiB of standard error, quoted when an `nccmd` process or `ncssh.WithCommand` command exits |
| `internal/cmd/doccheck`, `internal/cmd/junosschema` | The programs behind `make docs-check` and `make junos-schema` |
| `examples` | Runnable programs, with shared setup in `examples/internal/exampleutil` |
| `integration` | Device and netopeer2 tests, under the `integration` tag |

## Request path

```text
typed Exec, or Session.Exec, ExecReply or Stream
  -> Session.do: new message-id, encode <rpc>, register pending reply
  -> Session.send: take the turn (Junos over chunked framing only),
     wait for the writer
  -> transport message writer and framing -> device
```

[`session.go`](../session.go) owns this path. `Session.do` ignores
`RPC.MessageID`, so a late reply cannot match a new operation. It encodes
the whole `<rpc>` first, so a failed encode sends nothing.
[`pending.go`](../pending.go) counts the operations whose caller gave up,
so the receive loop drops their late replies. [`trace.go`](../trace.go)
calls the `netconf.WithTrace` functions.

`Session.send` writes on its own goroutine, because only closing the
transport interrupts a write. A caller whose context ends is released, and
the next writer waits for the write. A write that fails partway ends the
session, because the stream has lost its message boundary. The receive
loop gives the Junos turn back when the next message that is not a
notification has arrived whole, normally the reply, even when the caller
gave up, see
[ADR 0006](adr/0006-junos-gets-one-request-at-a-time-over-chunks.md).

## Receive path

```text
transport message reader -> receive loop reads the start tag
  <rpc-reply>: the waiting operation, by message-id
    buffered: read whole under the limits, repair, normalize Junos text
              -> parseReply on the caller's goroutine
    streamed: the live message to the caller
  <notification>: read whole, repair, normalize
              -> bounded queue -> handler on the dispatcher goroutine
```

The receive loop is in [`recv.go`](../recv.go), `parseReply` in
[`msg.go`](../msg.go), the limits in [`limit.go`](../limit.go) and the
queue in [`notify.go`](../notify.go). The repair is the function given to
`netconf.WithReplyRepair`, such as `netconf.StripInvalidChars` in
[`repair.go`](../repair.go). For a Junos device,
[`charref.go`](../charref.go) joins the per-byte character references of
Junos 26.2, so `Reply.Raw` can differ from the bytes sent, see
[ADR 0004](adr/0004-junos-text-is-put-back-together-in-the-session.md).

`parseReply` returns every `<rpc-error>` at any depth and the Junos
`<xnm:error>` as `netconf.RPCErrors`, except warnings, which go to
`Reply.Warnings`. See [ADR 0003](adr/0003-rpc-errors-at-any-depth.md) and
[ADR 0005](adr/0005-the-error-element-of-junos-fails-the-operation.md).
The loop skips a message it cannot place, see
[Untrusted devices](sessions.md#untrusted-devices).

A streamed reply skips the buffered steps, see
[Large replies](operations.md#large-replies). The loop waits until the
caller closes it. When the caller gives up before collecting the message,
`awaitStream` reads it to the end under the message timeout.
[ADR 0002](adr/0002-buffered-replies-by-default.md) says why streaming is
opt-in.

## Ownership and session end

`netconf.Open` owns its transport even when the hello exchange fails, and
closes it before a failed `Open` returns. Otherwise the session closes it,
jump host connections included, at session end. `Open` in
[`session.go`](../session.go) runs the hello exchange, and
[`hello.go`](../hello.go) parses the device's hello. The receive loop
starts only after the exchange succeeds.

Closing the transport is the only way to interrupt a read or write in
progress, see [ADR 0001](adr/0001-close-is-cancel-transport-contract.md).
An operation's context never closes it. These do:

- the `netconf.Open` context, ending before the hello exchange is done
- `Session.Close`
- a write that failed partway
- a message over `netconf.WithMaxMessageSize`, or one that stalls past
  `netconf.WithMessageTimeout`
- `ncssh.WithKeepalive`, when the device stops answering
- the end of the receive loop

When the receive loop ends the session, it records the first cause, stops
notification dispatch, closes the transport and then closes the
`Session.Done` channel, see [Session lifetime](sessions.md#session-lifetime).
`Session.Close` refuses new operations, stops notification dispatch, sends
`<close-session>` unless a streamed reply is open, closes the transport and
waits for the receive loop to end.

## Configuration workflows

[`junos/change.go`](../junos/change.go) implements `junos.Change.Apply`:

1. Check the fields.
2. Reserve the candidate with `rpc.Lock`, or a private copy with
   `junos.OpenConfiguration`.
3. Load the configuration and compare with `junos.CompareRollback`. An
   empty diff ends the change.
4. Call `Review`, if set.
5. Commit, or in a dry run send a commit check and stop.
6. With `Confirm`, call `Verify` if set, then confirm.
7. Release the candidate, however the change ends.

The release runs on `context.WithoutCancel(ctx)` under `releaseTimeout`.
See [Change safety](junos.md#change-safety) and
[What `Apply` does on the way out](junos.md#what-apply-does-on-the-way-out).

## Where to test a change

See the contribution guide's [Tests](../.github/CONTRIBUTING.md#tests).
