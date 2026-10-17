# Operations

Read this page before you write code that sends operations and handles
their replies.

- [Choosing an API](#choosing-an-api)
- [Errors](#errors)
- [Finding the result in a reply](#finding-the-result-in-a-reply)
- [Timeouts](#timeouts)
- [Large replies](#large-replies)

## Choosing an API

Every operation in packages `rpc` and `junos` is a value with an `Exec`
method:

```go
cfg, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
if err != nil {
	return err
}
fmt.Printf("%s\n", cfg)
```

Send any other operation as literal XML, or as a value that marshals to
it. `Session.Exec` decodes the reply into a struct that matches the whole
`<rpc-reply>`:

```go
var info struct {
	Model string `xml:"system-information>hardware-model"`
}
if err := session.Exec(ctx, "<get-system-information/>", &info); err != nil {
	return err
}
fmt.Println(info.Model)
```

| Need | API |
| --- | --- |
| A standard or Junos operation | The operation's `Exec` |
| Decode the whole `<rpc-reply>` into a struct | `Session.Exec` |
| Read `Reply.Raw`, warnings, attributes or result elements | `Session.ExecReply` |
| Decode a Junos result at any depth and trim its line breaks | `junos.Decode` on a reply |
| Process a reply as it arrives | `Session.Stream` |

To put attributes on the `<rpc>` element, pass
`netconf.NewRPC(op, netconf.WithAttributes(...))` in place of the operation.

[Standard NETCONF](standard-netconf.md) covers filters, capability checks
and a candidate edit.

## Errors

An `<rpc-error>` anywhere in a buffered reply fails the operation unless
its severity is `warning`. One without a severity fails it too.

The typed operations of `rpc` and `junos` put their name in front of every
error they return, context and session ends included:

```text
commit-configuration: netconf error: application invalid-value: [edit system ntp] 'server 192.0.2.1': server address is a local address
```

`junos.Change.Apply` names the failed step instead, as in
`junos: commit: netconf error: ...`. `Session.Exec` and
`Session.ExecReply` add no name.

The error carries the device's RPC errors as a `netconf.RPCErrors`. The
typed operations wrap it, and `Session.ExecReply` joins it with a
`*netconf.SyntaxError` when the reply stops being XML partway. Read it with
`errors.As` and match a tag with `errors.Is`. A type assertion sees only
the outer error.

```go
reply, err := session.ExecReply(ctx, op) // op: any operation

var rpcErrs netconf.RPCErrors
if errors.As(err, &rpcErrs) {
	for _, e := range rpcErrs {
		daemon, _ := e.Detail("source-daemon") // the Junos process that reported it
		log.Printf("%s %s (%s)", e.Path, e.Message, daemon)
	}
	log.Printf("%d warnings came with the errors", len(reply.Warnings))
}
if errors.Is(err, netconf.ErrLockDenied) {
	// another session holds the lock
}
```

`RPCError.Detail` reads any element inside the error, such as the
`bad-element` or `session-id` that RFC 6241 puts in `<error-info>`, or one
a device adds.

### The reply of a failed operation

`Session.ExecReply` and `junos.Command.XML` return the reply with a
`netconf.RPCErrors` or `*netconf.SyntaxError`, so the data and warnings
that came with the errors stay readable. With any other error, such as a
timeout or a session end, the reply is nil.

- `junos.LoadConfiguration`, `CommitConfiguration`, `Rollback` and
  `LoadRescue` return the reply whenever the device answered, even when it
  has no `<ok/>`.
- `junos.Command.JSON` and `junos.GetConfiguration` with `FormatJSON` or
  `FormatJSONMinified` return the JSON document with the error Junos wrote
  into it.
- `Session.Exec` returns no reply. The other operations that return data,
  such as `rpc.GetConfig` and the `junos` reads, return none when they
  fail.

### Where warnings go

Warnings never fail an operation.

| API | Warnings |
| --- | --- |
| `Session.ExecReply`, `junos.LoadConfiguration`, `CommitConfiguration`, `Rollback`, `LoadRescue`, `Command.XML` | `Reply.Warnings` of the reply |
| `junos.Change.Apply` | `ChangeResult.Warnings`, for the load, the check and the commits |
| `junos.Command.JSON`, `junos.GetConfiguration` with `FormatJSON` or `FormatJSONMinified` | In the JSON document |
| `Session.Exec`, `rpc.PartialLock` and the `rpc` operations answered with `<ok/>` | Logged at warn level, see `netconf.WithLogger` |
| `rpc.Get`, `rpc.GetConfig` and every other `junos` operation | Dropped |

To see a typed operation's warnings, give it to `Session.ExecReply`.

### Junos errors

Junos 26.2 and Junos OS Evolved 26.2 answer some operations with an error
element of their own, `<xnm:error>`, in place of an `<rpc-error>`:

- The routing process writes it under `<rpc-reply>` on both. With
  `rfc-compliant` set, Junos OS Evolved 26.2 sends these errors as
  `<rpc-error>`.
- For an interface that does not exist, Junos OS Evolved 26.2 writes it
  inside the result, with `rfc-compliant` set too. Junos 26.2 sends an
  `<rpc-error>` after an empty result.

The session returns an `<xnm:error>` as a `netconf.RPCError` without type
or tag, so `errors.Is` finds no tag in it. `Detail("source-daemon")`
names the process, and so does the error text when the message is empty,
as in `netconf error: reported by routing`.

On Junos, two failures that a program often needs to tell apart carry only
the generic tag `operation-failed`. Package `junos` has an `errors.Is`
target for each:

```go
_, err := session.ExecReply(ctx, "<get-chassis-cluster-status/>")
switch {
case errors.Is(err, junos.ErrPermissionDenied):
	// the login class of the user does not permit the operation
case errors.Is(err, junos.ErrUnsupported):
	// this platform or release has no such operation
}
```

`junos.ErrPermissionDenied` matches the message "permission denied", which
Junos sends for an operation, a CLI keyword or a part of the configuration
that the login class does not permit, with the refused item as the bad
element. `netconf.ErrAccessDenied` does not match, because Junos does not
send `access-denied`.

`junos.ErrUnsupported` matches a "syntax error" whose bad element is the
operation itself, which is how Junos answers an operation it does not
know, such as an SRX operation sent to a router. It does not match:

- a configuration statement that Junos does not know, whose bad element
  is the statement
- an unknown `junos.Command` line
- an operation that Junos knows but cannot run on the platform, refused
  with a message of its own: "command is not valid on the vmx" on
  vJunos-router 26.2, "the virtual-chassis-control subsystem is not
  running" on vJunos-switch 26.2, "the l2-learning subsystem is not
  running" on Junos OS Evolved 26.2
- an error read from a streamed reply or built by hand, which lacks the
  operation name that `Session.Exec` and `Session.ExecReply` record in
  `RPCError.Request`

Junos 26.2 and Junos OS Evolved 26.2 sent these refusals, see
[Captured replies](junos-verified.md#captured-replies).

Junos writes many failures of a `format="text"` or `format="json"`
operation into the reply body, where `junos.Text` and `junos.JSON` find
them, see
[Other operations as literal XML](junos.md#other-operations-as-literal-xml).
Junos 26.2 and Junos OS Evolved 26.2 still send an `<rpc-error>` for a
`junos.Command.Exec` line that the login class refuses or Junos does not
know.

### Error targets

Match each target below in any operation's error with `errors.Is`, or
with `errors.As` for the types `netconf.RPCErrors`, `netconf.RPCError`,
`*netconf.SyntaxError` and `*netconf.HelloError`.

| Target | Where it comes from |
| --- | --- |
| `netconf.RPCErrors`, `netconf.RPCError` | The RPC errors of the reply |
| `netconf.ErrLockDenied` and the other `netconf.ErrTag` values | The `<error-tag>` of an RPC error |
| `junos.ErrPermissionDenied`, `junos.ErrUnsupported` | The message of a Junos RPC error |
| `junos.ErrCandidateModified` | A refused lock or private copy, see [Lock and candidate conflicts](junos.md#lock-and-candidate-conflicts) |
| `netconf.ErrElementNotFound` | `Reply.Element`, `junos.Decode`, or a typed operation, when the reply lacks the element, the `<ok/>` or the result |
| `*netconf.SyntaxError` | A reply or element that is not XML, see [Malformed XML from the device](sessions.md#malformed-xml-from-the-device) |
| `context.DeadlineExceeded`, `context.Canceled` | The context of the operation ended first |
| `netconf.ErrClosed` | The session is over. It wraps the cause from `Session.Err`, if any |
| `netconf.ErrMessageTooLarge`, `netconf.ErrMessageStalled`, `netconf.ErrNotificationOverflow`, `ncssh.ErrKeepalive` | Why the session ended, also in `Session.Err()`, see [Session lifetime](sessions.md#session-lifetime) |
| `netconf.ErrNoNotifHandler` | `rpc.CreateSubscription` on a session opened without `netconf.WithNotifHandler`, see [Notifications](sessions.md#notifications) |
| `netconf.ErrHello`, `*netconf.HelloError` | `netconf.Open`, see [When connecting fails](connecting.md#when-connecting-fails) |
| `junos.ErrNotConfirmed`, `junos.ErrCommitUnknown`, `junos.ErrCommitPending` | `junos.Change.Apply`, see [Changing configuration](junos.md#changing-configuration) |

A session that ended on its own keeps its cause in `Session.Err` after a
later `Close`.

## Finding the result in a reply

Junos wraps the output of each routing engine or virtual chassis member in
a `<multi-routing-engine-item>` for some operations on some platforms.
Junos 25.4 on an EX4100 wraps `<get-system-uptime-information>` this way,
and another platform does not, so a struct that matches the whole reply
breaks on the next device. `Reply.Element` finds an element by name at
any depth:

```go
reply, err := session.ExecReply(ctx, "<get-system-uptime-information/>")
if err != nil {
	return err
}
el, err := reply.Element("system-uptime-information")
if err != nil {
	return err // errors.Is(err, netconf.ErrElementNotFound)
}
var uptime struct {
	Booted string `xml:"system-booted-time>date-time"`
}
if err := el.Decode(&uptime); err != nil {
	return err
}
fmt.Println("booted", uptime.Booted)
```

The name matches in any namespace. To match one namespace only, put it in
front with a space, as in an `encoding/xml` struct tag:
`reply.Elements("urn:example:interfaces interfaces")`.

`Reply.Elements` returns every match in document order, from every
routing engine in a wrapped reply, but not a match nested inside another.
When nothing matches, it returns an empty slice and no error.
`junos.Decode` does the same search, see
[Other operations as literal XML](junos.md#other-operations-as-literal-xml).

A `netconf.Element` holds the element with its tags in `Raw`, its content
in `Inner`, its own text in `Text` and its start tag's attributes in
`Attr`. Junos puts the commit time and committer of `<configuration>` in
`junos:commit-seconds` and `junos:commit-user`, and the Unix time of a time
element such as `<date-time>` in `junos:seconds`:

```go
reply, err := session.ExecReply(ctx, junos.GetConfiguration{})
if err != nil {
	return err
}
config, err := reply.Element("configuration")
if err != nil {
	return err
}
for _, attr := range config.Attr {
	// written junos:commit-seconds, with the seconds since 1970
	if attr.Name.Local == "commit-seconds" {
		seconds, _ := strconv.ParseInt(attr.Value, 10, 64)
		fmt.Println("committed", time.Unix(seconds, 0))
	}
}
```

`Name.Space` of a prefixed attribute holds the prefix's namespace,
wherever the reply declares it. Junos declares `junos` on the
`<rpc-reply>`. `Element.Decode` also applies the namespaces declared on
enclosing elements, so struct tags can match by namespace.
`reply.Element("rpc-reply")` returns the envelope and its `Attr`.

## Timeouts

`ncssh.DialHost`, `nctls.Dial`, `netconf.Open`, every operation and
`Session.Close` take a context that bounds only that step, see
[Choose timeout bounds](production-use.md#choose-timeout-bounds).
`nccmd.Start` and reading or closing a streamed reply take none.

`netconf.Open` and `Session.Close` apply `netconf.DefaultHelloTimeout` and
`netconf.DefaultCloseTimeout`, 30 seconds each, when their context has no
deadline. `netconf.WithOperationTimeout(d)` bounds each operation whose
context has no deadline to `d`. Without it, such an operation waits as
long as the device stays connected.

A context with a deadline overrides `d`, longer or shorter. This gives one
operation more time than the rest:

```go
session, err := netconf.Open(ctx, tr, netconf.WithOperationTimeout(30*time.Second))
if err != nil {
	return err
}

// The change gets five minutes, not the 30 seconds of the session.
commitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
defer cancel()
_, err = junos.Change{Format: junos.FormatSet, Config: config}.Apply(commitCtx, session)
return err
```

The deadline of a whole job overrides `d` too, so derive a shorter context
for each operation from the job's context.

Give commits and commit checks, `junos.Change.Apply` and its dry run
included, a deadline longer than `d`. A commit can take a minute on a
large configuration, and a failed commit check took more than 30 seconds
on an EX4100 with Junos 25.4.

### Canceled operations

An operation whose context ends leaves the session open. Canceling stops
the wait for the reply, not the operation, so the device may still carry
out an operation already sent. The session drops its late reply. An
operation whose context ended before it starts sends nothing.

On a Junos session over chunked framing, a canceled operation keeps its
turn until its reply arrives, and that wait counts toward the timeout of
every later operation, `<close-session>` included. See
[Requests from several goroutines](sessions.md#requests-from-several-goroutines).

### Stalled messages

A message stalls when the device stops sending in the middle of it. No
operation context interrupts the session's read, so operations time out
one after another, `Session.Done` stays open and SSH keepalives still get
answers.

`netconf.WithMessageTimeout(d)` ends the session when a message has begun
and no further byte of it arrives for `d`. `session.Err()` then matches
`netconf.ErrMessageStalled`, and every waiting operation fails with
`netconf.ErrClosed`. A message begins with its first byte that is neither
framing nor white space, so a chunk header alone does not begin one.

```go
session, err := netconf.Open(ctx, tr, netconf.WithMessageTimeout(10*time.Minute))
if err != nil {
	return err
}
```

The option is off by default. The timer restarts with every byte and does
not run between messages or on a reply read with `Session.Stream`.

Set `d` longer than the slowest operation on the session, usually a commit
or a commit check. Junos 26.2 on vJunos-router sends the start tag of a
reply at once and the rest when the work is done, so a shorter `d` ends
the session mid-commit. Ten minutes is longer than the commit times
under [Timeouts](#timeouts).

## Large replies

`Session.Exec` and `Session.ExecReply` buffer the whole reply.
`Session.Stream` streams it, for code that processes the data as it
arrives. Decoding a streamed reply into one value, such as
`rpc.GetConfigReply`, still holds all of it in memory.

A streamed reply leaves these to you:

- Close the message, or decode it with `Message.Decode`, which closes it.
  Until then the session processes no other reply or notification.
- Bound the read yourself. The context of `Session.Stream` bounds only
  sending the operation and waiting for the reply to begin.
  `netconf.WithMaxMessageSize` and `netconf.WithMessageTimeout` do not
  apply.
- Find errors yourself. The session does not look for `<rpc-error>` or
  `<xnm:error>`, apply Junos text normalization or run the reply repair.
- Close the session to give up on a reply partway, which ends its other
  operations too. `Message.Close` reads the rest first and does not
  interrupt a device that stopped sending.

The [streaming example](../examples/stream-config/README.md) counts XML
elements with a byte limit and a read deadline of its own.
