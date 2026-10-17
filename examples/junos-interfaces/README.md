# Read Junos interfaces

[Source](main.go) and [offline tests](main_test.go)

The junos-interfaces example sends
`<get-interface-information><terse/></get-interface-information>` as
literal XML with `Session.ExecReply`, decodes the physical interfaces with
`junos.Decode`, and prints the name and operational status of each. It
changes nothing. The account needs permission to read interface
information.

Follow the [common setup](../README.md#common-setup), then run:

```bash
go run ./examples/junos-interfaces -host router1.example.net -user automation
```

Each line holds the name and status of one interface, separated by a
tab:

```text
ge-0/0/0	up
ge-0/0/1	down
```

`junos.Decode` finds `<physical-interface>` at any depth, so the example
works with or without a `<multi-routing-engine-item>` wrapper per routing
engine. With that wrapper, the example lists the interfaces of all routing
engines together. It keeps duplicates and does not record which routing
engine reported each line. See
[Other operations as literal XML](../../docs/junos.md#other-operations-as-literal-xml)
and
[Finding the result in a reply](../../docs/operations.md#finding-the-result-in-a-reply).

An RPC error ends the program before it decodes anything. When the reply
has no `<physical-interface>` element, the program fails with
`netconf.ErrElementNotFound` instead of printing nothing. Either way it
exits with status 1.

The [offline tests](main_test.go) decode a reply with the wrapper, a reply
without it and a reply without the element. They need no device.
