# An rpc-error anywhere in a reply fails the operation

RFC 6241 puts `<rpc-error>` directly under `<rpc-reply>`, and the session
used to look only there. Junos (without `rfc-compliant`) also nests it
inside result wrappers such as `<load-configuration-results>` and
`<commit-results>`, so a failed load or commit read as success for anyone
sending those RPCs as literal XML. The reply parser therefore collects
every `<rpc-error>` element at any depth, and fails the operation unless
all of them have warning severity. We preferred this over a list of known
wrapper elements because the list is vendor-specific and silently wrong the
day a device adds one.

The element is matched by name in any namespace. It was first matched in
the NETCONF base namespace only, which missed a second Junos habit: an
error written inside a result element that declares its own namespace
inherits that namespace (a failed `<ping>` on Junos 16.1R4 answers
`<ping-results xmlns="…/junos-probe-tests"><rpc-error>…`). Other clients
read that reply as a success.

## Consequences

- Retrieved data containing an element named `rpc-error`, in whatever
  namespace, would be misread as a failure. No data model we know does
  this, and failing a read is the cheaper mistake than passing a failed
  change.
- The whole reply is tokenised once to find errors, also when the caller
  only wants the raw bytes.
- An rpc-error without `<error-severity>` (non-conforming servers) counts
  as an error, not a warning.
- A failed operation still returns its reply next to the error
  (`Session.ExecReply`), because the errors are part of a reply that may
  hold data and warnings as well.
- Junos' own `<xnm:error>` is collected too. It is told by how it is
  written, not by its namespace, see ADR 0005.
