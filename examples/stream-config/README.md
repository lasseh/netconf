# Process a large configuration incrementally

[Source](main.go) and [offline tests](main_test.go)

The stream-config example streams the running configuration with
`Session.Stream` and counts the elements of one local name inside
`<data>`, without holding the configuration in memory. It changes nothing.

Follow the [common setup](../README.md#common-setup), then run:

```bash
go run ./examples/stream-config -host router1.example.net -user automation -element interface
```

After the whole reply, the program prints the count, such as
`interface: 12`.

| Flag | Meaning |
| --- | --- |
| `-element` | Local name to count, default `interface` |
| `-max-bytes` | Largest reply in bytes, default 536870912, which is 512 MiB |
| `-read-timeout` | Time for the whole read, default `2m` |

Both bounds are choices of this example, not library defaults. The program
refuses a bound that is not positive and an empty `-element`.

## Which elements count

Every element under `<data>` with that local name counts, in any namespace
and at any depth. On Junos, `interface` also matches entries under
`protocols`, such as those of OSPF areas and LLDP. To count one list, match
a path and a namespace instead.

## Parsing and lifetime

The program prints no count after any of these:

- no `<data>` element
- malformed XML
- a root other than one `<rpc-reply>`
- a reply over the byte limit
- an RPC error other than a warning
- a timeout of the read

The parser does not recognize `<xnm:error>`, so on a device that reports
errors that way, a count does not prove that the read succeeded. The byte
limit bounds the input, not peak memory, because `encoding/xml` holds each
whole token in memory.

On a timeout, Ctrl-C or SIGTERM, the program closes the session through
`context.AfterFunc`, which interrupts a stalled reply. After any failure,
it closes the session before the streamed message, so it reads no more of
the reply. See [Large replies](../../docs/operations.md#large-replies).

The [offline tests](main_test.go) feed synthetic replies to the parser and
to the read, and need no device.
