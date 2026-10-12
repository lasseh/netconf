# Replies are buffered by default; streaming is an explicit opt-in

`Session.Do` fully buffers each reply before returning it; streaming a
reply directly from the transport requires calling `Session.DoStream`,
whose documentation carries the close obligation. This looks backwards
for a library whose README advertises stream-based transports, so the
history matters: when streamed delivery was the default, the receive
loop had to wait for the caller to close every reply, and a forgotten
`Close` — or a context cancellation racing the reply's arrival — wedged
the entire session permanently (every later RPC, all notifications, and
even `Session.Close`). Buffering by default makes the common path
un-wedgeable and keeps `Message.Close` optional there, while `DoStream`
preserves constant-memory consumption of multi-hundred-MB get-config
replies for callers who accept the obligation.

## Consequences

- The framing layer still streams (chunk-at-a-time, bounded memory); the
  buffering decision applies only to reply delivery at the session
  interface.
- The wedge-defence machinery (abandoned-reply reclaim) exists solely on
  the DoStream path; do not "simplify" it away — the race it defends
  against is real and was reproduced before it was added.

## Update, 2026-10-08

The decision stands; the names changed. `Session.Do` is gone: `Exec` and
`ExecReply` are the buffered path, and they check the reply for RPC errors,
which `Do` did not. `Session.DoStream` is `Session.Stream`. The reclaim of an
abandoned streamed reply is pinned by `TestAwaitStreamReclaimsAbandonedReply`
without timing, next to the race test.
