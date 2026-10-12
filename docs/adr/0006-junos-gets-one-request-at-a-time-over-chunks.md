# Over chunked framing a session sends Junos one request at a time

A session is safe for concurrent use: operations from several goroutines
are written one behind the other as they come, and the device has several
requests at once. Over chunked framing Junos 26.2 does not take that. When
a request was written right behind another, and the two together put the
end of the chunks of the second across byte 8192, vJunos-router ended the
session: in 3 of 40 tries from a transport that held the two back and
wrote them together, and in 3 of 80 from two goroutines. The writer of
package `transport` keeps the end of the chunks of a message clear of a
multiple of 4096 bytes counted from the beginning of that message, which
was enough for every request that went out by itself.

On a session with chunked framing to a device that advertises the Junos
capability, a request is written when the answer to the one before it has
arrived whole. The session takes a turn for a request before it writes it,
and the receive loop gives the turn back when a message that is no
notification has been read to its end.

The turn ends with the whole answer and not with its beginning. Junos
sends the start tag of a reply when it has the start tag of the request: a
request written in pieces had the start tag of its reply back 9 ms after
its own start tag, with the operation still to be written. A second
request let go at that moment could arrive behind the rest of a large
first one. The whole answer is also what had run against the device. The
requests of the integration suite each go out after the reply before
them, and those of the sizes that matter were all answered in 13 runs.

A writer that counts across messages was not built. It would have to keep
the end of the chunks clear of a place for every request that is not
answered yet, and none of that has run against a device.

Only Junos is sent one request at a time. RFC 6241 lets a client send
requests before the earlier ones are completed (section 4.5), netopeer2
takes them, and a session to it writes them as they come.

## Consequences

- A request whose context ends while it waits for its turn is not sent.
  On a session without turns it is sent, and carried out with nobody
  waiting for the reply.
- A request whose caller gave up keeps its turn until its answer has
  arrived. One that the device never answers holds up all behind it,
  `<close-session>` included, and `Close` then ends the session when its
  context is over. Junos carries requests out one after the other, so the
  ones behind waited for it before as well, in the input of the device.
- A request that waits leaves later than it did: when the answer before it
  has arrived, where it used to be in the input of the device by then.
- A streamed reply has arrived whole when it is closed.
- Every message but a notification ends the turn, one that nobody can read
  included: a session that waited for a better one would send nothing
  more. A message that the device sends unasked lets the next request go
  early.
- A `Trace` is told of an operation before it waits, so the wait is part
  of its duration.
- A session opened with `WithCapability(CapNetConf10)` speaks
  end-of-message framing to such a device and has no turns.
- It is not known whether Junos has taken the end of the chunks of a
  request when its reply has arrived whole. In the same probe the reply
  was whole before the end of the message was written.
- The session that ended was seen on vJunos-router 26.2 only. The turn is
  for every device with the Junos capability, vJunos-switch and
  vJunosEvolved too, where two requests at once were not tried.
