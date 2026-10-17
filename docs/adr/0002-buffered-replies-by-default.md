# Replies are buffered by default; streaming is an explicit opt-in

`Session.Do` fully buffers each reply before it returns it. Streaming a
reply directly from the transport takes a call to `Session.DoStream`,
whose documentation states the obligation to close the reply. This looks
backwards for a library whose README advertises stream-based transports,
so the history matters. When streamed delivery was the default, the
receive loop had to wait for the caller to close every reply. A forgotten
`Message.Close`, or a context cancellation racing the arrival of the reply,
blocked the entire session for good: every later operation, all
notifications, and even `Session.Close`. Buffering by default keeps the
common path from blocking the session and makes `Message.Close` optional
there. `DoStream` keeps memory use constant for get-config replies of
hundreds of megabytes, for callers who accept the obligation.

## Consequences

- The framing layer still streams, a chunk at a time with bounded memory.
  The buffering decision applies only to how the session interface
  delivers replies.
- The reclaim of an abandoned streamed reply exists only on the
  `Session.DoStream` path. Do not remove it to simplify the code. The race
  it defends against is real, and was reproduced before the reclaim was
  added.

## Update, 2026-10-08

The decision stands, and the names changed. `Session.Do` is gone.
`Session.Exec` and `Session.ExecReply` are the buffered path, and they
check the reply for RPC errors, which `Do` did not. `ExecReply` returns a
`*netconf.Reply`, which has nothing to close. `Session.DoStream` is
`Session.Stream`, and `netconf.Message` is what `Stream` returns and what
a notification handler gets. The reclaim of an abandoned streamed reply is
pinned without timing by `TestAwaitStreamReclaimsAbandonedReply` in
`session_wire_test.go`, and by the race test `TestStreamAbandonedReplyRace`
in `session_test.go`.
