# Close-is-cancel is the transport's only cancellation mechanism

The transport interface (`transport.Transport`) deliberately has no
context parameters or deadline methods: closing the transport is the
documented, conformance-tested way to interrupt in-flight reads and
writes. We considered a ctx-aware interface (`MsgReader(ctx)` /
`SetDeadline`), but SSH channels in `x/crypto/ssh` — the primary adapter —
have no deadline support at all, so such an interface would either be
unimplementable or silently degrade to close-on-cancel anyway. Naming the
real mechanism keeps the contract honest; every cancellation path in the
session (hello timeout, Close, dead-peer handling) reduces to a transport
close, and the transporttest conformance suite pins the "Close must
unblock in-flight IO" invariant for every adapter.

## Consequences

- Cancellation is always whole-session: you cannot abort one RPC's read
  without killing the transport. Per-RPC timeouts abandon the reply
  instead (the session drops it) rather than interrupting the read.
- Any future adapter (call-home, in-memory) must satisfy the same
  invariant to pass the conformance suite.
