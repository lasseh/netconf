# The error element of Junos fails the operation

Junos has an error element of its own, `<xnm:error>`, from the protocol it
had before NETCONF, and answers some requests with it where an
`<rpc-error>` belongs. The routing process does, on Junos 26.2 and on Junos
OS Evolved 26.2, under `<rpc-reply>`. Junos OS Evolved does for an
interface that is not there, inside the result:
`<interface-information><xnm:error>`. The reply parser collected
`<rpc-error>` only (ADR 0003), so such a request read as one that had
succeeded and found nothing.

The parser collects the element like an `<rpc-error>`, at any depth, and
the operation fails. It becomes an `RPCError` with the severity error and
the message of the element. Nothing is made up for it: Junos gives it
neither type nor tag, so `errors.Is` finds no tag in it.

The element is told by how it is written, with the prefix `xnm`, and not
by its namespace. ADR 0003 left it out because the namespace cannot tell
it: the Junos configuration is in that namespace and has `<error/>` and
`<warning/>` statements, the severities of syslog, so a rule by namespace
fails the reading of most configurations. A statement is written without a
prefix. The namespace cannot be asked for either: Junos OS Evolved 23.2
does not declare the prefix.

## Consequences

- A request that returned no data returns an error. One of them has no
  message: the routing process gives none for the routes received from a
  BGP peer it does not know, where the CLI prints nothing.
- An element that another vocabulary writes as `xnm:error` would be taken
  for one. We know of none.
- `<xnm:warning>` is left alone. None was seen in a NETCONF session: Junos
  sent every warning we provoked as an `<rpc-error>`.
- A reply read with `Session.Stream` is not gone through, as for an
  `<rpc-error>`.
- `rfc-compliant` mode does not make the element go away. Junos 26.2 then
  sends the errors of the routing process as `<rpc-error>`, and Junos OS
  Evolved 26.2 still writes its own element inside a result.
- In JSON the same errors are members of the document, which the session
  does not read. `junos.JSON` looks for them there.
