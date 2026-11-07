# Close-is-cancel is the transport's only cancellation mechanism

The transport interface, `transport.Transport`, has no context parameters
and no deadline methods, by design. Closing the transport is the
documented way to interrupt a read or a write in progress, and the
conformance suite tests it. We considered an interface with contexts or
deadlines, such as `MsgReader(ctx)` or `SetDeadline`. But SSH channels in
`x/crypto/ssh`, the primary adapter, have no deadline support at all, so
such an interface would either be impossible to implement or fall back to
closing on cancel without saying so. The contract names the mechanism
that actually works. Every interruption of I/O in the session, whether the
hello timeout, `Session.Close` or dead-device handling, reduces to a
transport close, and the conformance suite in `transporttest` pins the
invariant "Close must unblock in-flight I/O" for every adapter.

## Consequences

- Interrupting I/O always ends the whole session. You cannot abort the
  read of one operation without closing the transport. A timeout on one
  operation abandons its reply instead of interrupting the read, and the
  session drops the reply when it arrives.
- Any future adapter, such as call-home or in-memory, must satisfy the
  same invariant to pass the conformance suite.

## Update, 2026-10-17

The decision holds. The TLS transport and the command transport pass the
same conformance suite. Dead-device handling is `ncssh.WithKeepalive`, and
`netconf.WithMessageTimeout` ends a session the same way when a message
stalls, by closing the transport.

## Update, 2026-10-31

The decision holds, and closing what is under a transport turned out not
to be enough. A device can stop the read loop of `x/crypto/ssh` or get a
key exchange stuck, and then nothing reads the socket that closing it
could wake. It can also make `crypto/tls` hold its write lock through a
socket write from inside a read. So a transport has to release its IO
whatever the library under it is doing: the SSH transport works through
pipes it can cut, and the TLS transport closes the socket under a timer.

The suite pinned the invariant for a device that goes silent only. For a
device that does more, `transporttest.RunStuck` takes what the adapter
needs to get stuck, and `Close` has to return and release a read. The
session also no longer rests on `Close` returning: `Session.Close` gives a
transport a moment behind its context, and ends the session without it.
