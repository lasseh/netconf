# An rpc-error anywhere in a reply fails the operation

RFC 6241 puts `<rpc-error>` directly under `<rpc-reply>`, and the session
used to look only there. Junos also nests it inside result wrappers such
as `<load-configuration-results>` and `<commit-results>`, so a failed load
or commit read as a success for anyone who sent those operations as
literal XML. The reply parser therefore collects every `<rpc-error>`
element at any depth, and fails the operation unless all of them have
warning severity. We preferred this over a list of known wrapper elements,
because such a list is vendor-specific and silently wrong the day a device
adds a wrapper.

The parser matches the element by name, in any namespace. It first matched
only the NETCONF base namespace, which missed a second Junos habit. An
error written inside a result element that declares its own namespace
inherits that namespace. As reported in PyEZ issue 716, Junos 16.1R4
answers a failed `<ping>` with
`<ping-results xmlns="…/junos-probe-tests"><rpc-error>…`. Other clients
read that reply as a success.

## Consequences

- Retrieved data that contains an element named `rpc-error`, in any
  namespace, would be misread as a failure. No data model we know does
  this, and failing a read is a cheaper mistake than passing a failed
  change.
- A reply that contains the name of an error element is tokenized once to
  find the errors, even when the caller wants only the raw bytes. A reply
  without one is handed on unread, so the session does not notice when
  such a reply is not XML. An operation that needs an acknowledgment
  reads the reply itself. `junos.LoadConfiguration` and
  `junos.CommitConfiguration` read it to its end for the `<ok/>`.
- An `<rpc-error>` without `<error-severity>`, as non-conforming devices
  send it, counts as an error, not a warning.
- `Session.ExecReply` returns the reply of a failed operation along with
  the error, because the errors are part of a reply that may hold data and
  warnings as well.

## Update, 2026-10-17

[ADR 0005](0005-the-error-element-of-junos-fails-the-operation.md)
extends this decision. The parser also collects the error element of
Junos, `<xnm:error>`, and recognizes it by its `xnm` prefix, not by its
namespace. In `rfc-compliant` mode Junos 26.2 nests the error the same
way, as `<nc:rpc-error>` inside `<load-configuration-results>`.
`TestRFCCompliant` in `junos/change_test.go` replays such a reply captured
from Junos 26.2 on vJunos-router.
