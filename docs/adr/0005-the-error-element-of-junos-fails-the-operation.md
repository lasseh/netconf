# The error element of Junos fails the operation

Junos has an error element of its own, `<xnm:error>`, from the protocol it
had before NETCONF, and answers some operations with it where an
`<rpc-error>` belongs. The routing process does so under `<rpc-reply>`, on
Junos 26.2 and on Junos OS Evolved 26.2. Junos OS Evolved 26.2 does so
inside the result for an interface that does not exist:
`<interface-information><xnm:error>`. The reply parser collected
`<rpc-error>` only, see [ADR 0003](0003-rpc-errors-at-any-depth.md), so
such an operation read as one that had succeeded and found nothing.

The parser collects the element like an `<rpc-error>`, at any depth, and
the operation fails. The element becomes a `netconf.RPCError` with severity
`error` and the element's message. Junos gives the element neither
type nor tag, and the parser invents none, so `errors.Is` finds no tag in
it.

The parser recognizes the element by how it is written, with the prefix
`xnm`, and not by its namespace. ADR 0003 left the element out because
the namespace cannot identify it. The Junos configuration is in that
namespace too and has `<error/>` and `<warning/>` statements, the syslog
severities, so a rule by namespace would fail every read of a
configuration that sets the syslog severity `error`. A configuration
statement is written without a prefix. Checking which namespace the
prefix stands for does not work either, because Junos OS Evolved 23.2R2
does not declare the prefix, as reported in PyEZ issue 1397.

## Consequences

- An operation that returned no data and no error before this decision
  returns an error. One of these errors has no message. The routing
  process gives none for the routes received from a BGP peer it does not
  know, where the CLI prints nothing.
- An element that another vocabulary writes as `xnm:error` would be taken
  for the Junos error. We know of none.
- The parser ignores `<xnm:warning>`. None was seen in a NETCONF session.
  Junos sent every warning we provoked as an `<rpc-error>`.
- A reply read with `Session.Stream` is scanned neither for the element
  nor for `<rpc-error>`.
- `rfc-compliant` mode does not make the element go away. Junos OS Evolved
  26.2 then sends the errors of the routing process as `<rpc-error>`, and
  still writes its own element inside a result.
- In JSON the same errors are members of the document, which the session
  does not read. `junos.JSON` looks for them there.
