# Operations

Operations are values with an `Exec` method:

```go
err := rpc.Lock{Target: rpc.Candidate}.Exec(ctx, session)
err = rpc.EditConfig{Target: rpc.Candidate, Config: `<configuration>...</configuration>`}.Exec(ctx, session)
err = rpc.Commit{}.Exec(ctx, session)
```

Any RPC can also be sent as literal XML. `Exec` decodes the reply into a
struct, `ExecReply` returns it raw:

```go
var info struct {
	Model string `xml:"system-information>hardware-model"`
}
err := session.Exec(ctx, "<get-system-information/>", &info)

reply, err := session.ExecReply(ctx, "<get-interface-information><terse/></get-interface-information>")
// reply.Raw is the whole <rpc-reply>, reply.Warnings the warning-severity rpc-errors
```

To put attributes on the `<rpc>` element, pass a message built with
`netconf.NewRPC(op, netconf.WithAttributes(...))` in place of the operation.

## Errors

An `<rpc-error>` anywhere in the reply fails the operation. The error says
which operation it was and where the device found the problem:

```
commit-configuration: netconf error: application invalid-value: [edit system ntp] 'server 192.0.2.1': server address is a local address
```

The operations of `rpc` and `junos` put their name in front of every error
they return, also when the session or the context ended. `Session.Exec` and
`Session.ExecReply` are given the operation as it is and name nothing.
Underneath is a `netconf.RPCErrors`, the errors of the device. Get it out
with `errors.As`, or ask for a tag with `errors.Is`; a type assertion on the
error does not find it:

```go
reply, err := session.ExecReply(ctx, op)

var rpcErrs netconf.RPCErrors
if errors.As(err, &rpcErrs) {
	for _, e := range rpcErrs {
		daemon, _ := e.Detail("source-daemon") // Junos: who complained
		log.Printf("%s %s (%s)", e.Path, e.Message, daemon)
	}
}
if errors.Is(err, netconf.ErrLockDenied) {
	// someone else holds the lock
}
```

`RPCError.Detail` reads any element of the error: what RFC 6241 puts in
`<error-info>` (`bad-element`, `session-id` of a lock holder, ...) and what
a device adds. Warnings never fail an operation; they are in
`reply.Warnings`. A failed operation still returns its reply, so warnings
and data that came with the errors are not lost.

Junos answers some requests with an error element of its own,
`<xnm:error>`, where an `<rpc-error>` belongs: the routing process does,
and Junos OS Evolved does for an interface that is not there, inside the
result. It is returned the same way. Such an error has a message and
neither type nor tag, so `errors.Is` finds no tag in it, and
`Detail("source-daemon")` names the process. With `rfc-compliant` set the
routing process is answered for with an `<rpc-error>`; Junos OS Evolved
writes its own element inside a result all the same.

Junos has no tag for two failures a program wants to tell from the rest.
Every one of them is `operation-failed`, and the message says which.
Package `junos` has a target for each, for `errors.Is` on the error of any
operation:

```go
_, err := session.ExecReply(ctx, "<get-chassis-cluster-status/>")
switch {
case errors.Is(err, junos.ErrPermissionDenied):
	// the login class of the user does not permit it
case errors.Is(err, junos.ErrUnsupported):
	// this platform or release has no such request
}
```

`junos.ErrPermissionDenied` is the message "permission denied". Junos
answers with it for a request, for a word of a command and for a part of
the configuration that the login class does not permit, and names that as
the bad element. It does not send `access-denied`, so
`netconf.ErrAccessDenied` does not match. `junos.ErrUnsupported` is a
"syntax error" whose bad element is the request itself, which is what
Junos answers for a request it has not got, an SRX request on a router.
A load answers a statement it does not know with the same error and the
statement as the bad element, which does not match, and a request that
Junos knows and has no use for on the platform is refused in words of its
own: "command is not valid on the vmx". Both were seen on Junos 26.2
(vJunos-router, vJunos-switch) and Junos OS Evolved 26.2, and on Evolved
with `rfc-compliant` set, where the errors are the same.

## Finding the result in a reply

Devices wrap results. Junos puts the output of each routing engine or
virtual chassis member in a `<multi-routing-engine-item>`, and does so for
some commands and platforms only, so a struct that matches the whole reply
on one device does not on the next. `Reply.Element` finds an element by
name wherever it sits, and fails when it is not there:

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
err = el.Decode(&uptime)
```

`Reply.Elements` returns every match, one per routing engine in this case.
An `Element` has the element with its tags (`Raw`), its content (`Inner`),
its text (`Text`) and the attributes of its start tag (`Attr`). Junos puts
facts in attributes: on `<configuration>` when it was committed and by whom,
on every time the seconds behind it.

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

The name of an attribute written with a prefix has the namespace of the
prefix as its `Space`, wherever in the reply the prefix was declared: Junos
declares `junos` on the `<rpc-reply>`. `Element.Decode` has the namespaces
of the elements around it in force too, so a struct can ask for an element
or an attribute by its namespace in a piece cut out of a reply. The
envelope is an element like the others: `reply.Element("rpc-reply")` has
the attributes of the `<rpc-reply>`.

## Timeouts

Everything that waits takes a context. The one given to `DialHost` bounds
connecting, the one given to `Open` the hello exchange, and the one given to
an operation the wait for its reply. `Open` and `Close` allow 30 seconds
when their context has no deadline. `netconf.WithOperationTimeout(d)` does
the same for the operations of a session: it bounds each one whose context
has no deadline. Without it such an operation waits for as long as the
device stays connected.

A context with a deadline is taken at its word, before the bound of the
session or after it. That is how one operation gets more time than the
others:

```go
session, err := netconf.Open(ctx, tr, netconf.WithOperationTimeout(30*time.Second))
// ...
commitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
defer cancel()
result, err := junos.Change{Format: junos.FormatSet, Config: config}.Apply(commitCtx, session)
```

It also means that a context with a late deadline, one that belongs to a
whole request, takes the bound of the session off the operations it is
given to.

A timed out operation leaves the session usable. Cancelling stops the wait
for the reply, not the operation: an rpc that was already sent may still be
carried out by the device.

A device that stops in the middle of a message is another matter. The
session then waits on a read that no context of an operation interrupts:
operations time out one after the other, `Done` is never closed, and an SSH
keepalive goes on being answered. `netconf.WithMessageTimeout(d)` ends the
session when a message has begun and no further byte of it arrives for `d`,
and `session.Err()` then matches `netconf.ErrMessageStalled`:

```go
session, err := netconf.Open(ctx, tr, netconf.WithMessageTimeout(10*time.Minute))
```

It is off by default, and ten minutes is not a typing error. Junos sends the
start tag of a reply at once and the rest when it has done what it was
asked, so the silence inside a reply is the time the request takes. In a
lab, on Junos 26.2 (vJunos-router, vJunos-switch) and Junos OS Evolved 26.2
(vJunosEvolved) with configurations of 11 to 71 kB, that was up to 6
seconds for the first `<get-configuration>` of a session, about a second
for a commit check and up to 5 seconds for a commit of one statement. Once
the body of a reply had begun, no silence was longer than half a second. A
commit of a large configuration on hardware takes minutes, and a bound
below your slowest commit ends the session in the middle of it. The time
starts again with every byte, so a large reply that keeps coming is never
cut short, and a reply read with `Stream` is not timed.

## Large replies

`Exec` and `ExecReply` buffer the reply. `Stream` hands it over as a stream
instead; the returned message must be closed (or decoded) before the
session can process anything else, and nobody has looked for an rpc-error
in it.
