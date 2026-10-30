# Over chunked framing a session sends Junos one request at a time

A session is safe for concurrent use. Requests from several goroutines are
written one after another as they come, so the device can have several
requests outstanding at once. Over chunked framing vJunos-router 26.2 does
not handle that. When a request was
written right behind another, and the end-of-chunks marker of the second
fell across byte 8192 counted from the start of the first, vJunos-router
ended the session. That happened in 3 of 40 tries from a transport that
held the two requests back and wrote them together, and in 3 of 80 tries
from two goroutines. The writer in package `transport` keeps the
end-of-chunks marker of a message clear of every multiple of 4096 bytes
counted from the start of that message. That was enough for every request
written on its own.

On a session with chunked framing to a device that advertises the Junos
capability, a request is written only when the reply to the one before it
has arrived whole. The session takes a turn for a request before it
writes it, and the receive loop gives the turn back when it has finished
reading a message that is not a notification.

The turn ends with the whole reply, not with its beginning. Junos sends
the start tag of a reply as soon as it has the start tag of the request.
In a probe that wrote a request in pieces, the start tag of the reply
arrived 9 ms after the start tag of the request, while the rest of the
request was still unwritten. A second request released at that moment
could arrive behind the rest of a large first one. Waiting for the whole
reply is also what was tested against the device. The integration suite
sends each request after the reply to the one before it, and the requests
of the sizes that matter were answered in all 13 runs.

A writer that counts across messages was not built. It would have to keep
the end-of-chunks marker clear of a boundary for every request that has
not been answered yet, and no such writer has run against a device.

Only Junos is sent one request at a time. RFC 6241 section 4.5 lets a
client send requests before the earlier ones are completed, netopeer2
accepts them, and a session to it writes them as they come.

## Consequences

- A request whose context ends while it waits for its turn is not sent.
  On a session without turns the same request has been written by then,
  and the device carries it out with nobody waiting for the reply.
- A request whose caller gave up keeps its turn until its reply has
  arrived. A request that the device never answers holds up every request
  behind it, `<close-session>` included, and `Session.Close` then ends the
  session when the context passed to it is done, or after
  `netconf.DefaultCloseTimeout` when that context has no deadline. Junos
  carries out requests one after the other, so before this decision the
  requests behind it waited as well, queued in the device's input.
- A request that waits is written later than before. It goes out when the
  reply ahead of it has arrived, where before this decision it would
  already have been queued in the device's input by then.
- A streamed reply counts as having arrived whole when the caller closes
  it, or when the session closes one whose context ended before the caller
  took it, so its turn ends then.
- Every message but a notification ends the turn, including one that
  nobody can read. A session that waited for a better message would send
  nothing more. A message that the device sends unasked lets the next
  request go early.
- `Trace.OperationStart` is called before the operation waits for its
  turn, so the wait is part of its duration.
- A session opened with `netconf.WithCapability(netconf.CapNetConf10)`
  speaks end-of-message framing to such a device and has no turns.
- It is not known whether Junos has read the end-of-chunks marker of a
  request when its reply has arrived whole. In the probe that wrote a
  request in pieces, the reply was whole before the end of the request was
  written.
- The session end was seen on vJunos-router 26.2 only. The turn applies
  to every device with the Junos capability, vJunos-switch and
  vJunosEvolved included, where two requests at once were not tried.

## Update, 2026-10-30

The decision holds. A message that the device sends unasked still ends the
turn, and one request goes early. The session used to stay one reply behind
from there: the reply to the request that lost its turn ended the turn of
the request behind it, and so on for as long as requests kept waiting. The
reply table now records who has the turn. A reply to a request that lost
its turn ends nothing, so one early request is all such a message costs.
A message that arrives while no request is on its way ends no turn.
