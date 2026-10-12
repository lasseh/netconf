# NETCONF Client

A Go client library for the NETCONF network-device management protocol
(RFC 6241 and friends). One context: everything here is about driving a
protocol session against a network device.

## Language

### Protocol

**Session**:
A live NETCONF conversation with one device, established by the hello
exchange and addressed by a server-assigned session-id. All operations and
notifications flow through a session.
_Avoid_: connection, client

**Hello exchange**:
The opening exchange in which client and server each advertise capabilities
and the server assigns the session-id. A session does not exist until it
completes.
_Avoid_: login, negotiation

**Capability**:
A URN advertised during the hello exchange declaring an optional protocol
feature (`:candidate`, `:xpath`, base version). Determines which operations
and framing a session may use.
_Avoid_: feature flag, extension

**Operation**:
A single protocol request (get-config, edit-config, kill-session, …) that
knows its own wire encoding. Operations execute against a session and are
correlated to their reply by message-id.
_Avoid_: command, method, call. A command is what a transport runs to
reach a device: the program of `transport/command`, the line `WithCommand`
sends in place of the subsystem request, and the CLI line of
`junos.Command`.

**Message**:
One framed unit received from the server — either a reply or a
notification.
_Avoid_: packet, frame (framing is the transport-level encoding)

**Reply**:
The rpc-reply message correlated to an operation by message-id. Delivered
buffered by default; see Streamed reply.
_Avoid_: response

**Session end**:
The single moment a session is over, whether by close or because its
transport died. Everything waiting on the session is released then, and the
cause is kept for whoever asks.
_Avoid_: disconnect, teardown

**Stalled message**:
A message whose beginning has arrived and whose rest does not come. The
beginning is the first byte of the message that is not white space, not
the framing in front of it. The session waits on it and nothing else is
read. Only a bound on the silence inside a message ends the session over
it.
_Avoid_: hang, frozen

**Streamed reply**:
A reply handed over as a live stream from the transport instead of
buffered. The consumer must close it before the session can process
anything else — the obligation is part of asking for one.

**Turn**:
What an operation takes before it is written and keeps until its reply has
arrived whole, on a session that has one operation on its way at a time:
over chunked framing to a Junos device. On every other session operations
are written as they come.
_Avoid_: lock (a datastore has one), queue

**RPC error**:
Structured error carried inside a reply (type, tag, severity, path), at any
depth. Warning-severity RPC errors are informational and never fail an
operation; everything else does. The error element of Junos, `<xnm:error>`,
is one too: it has a message and neither type nor tag.
_Avoid_: fault

**Notification**:
An unsolicited event message the server sends under an active subscription
(RFC 5277). Notifications are delivered to a handler one at a time, in the
order received. Those that arrive while the handler is busy wait in a
bounded queue; overflowing it ends the session unless told to drop.
_Avoid_: event, trap (SNMP vocabulary)

### Configuration

**Datastore**:
A named configuration store on the device: running, candidate, startup.
_Avoid_: database, config bank

**Source**:
Where a configuration operation reads from: a datastore, a URL, or an
inline Config.
_Avoid_: input, from

**Target**:
Where a configuration operation writes to: a datastore or a URL. An inline
Config is never a valid target.
_Avoid_: destination, output

**Config**:
An inline configuration payload supplied by the caller and used as a
source.
_Avoid_: blob, body

**Change**:
One alteration of a Junos device's configuration carried out as a whole:
the candidate is reserved (locked, or copied privately), configuration is
loaded into it, compared, committed, and the candidate given back whatever
happened on the way.
_Avoid_: transaction (nothing is undone by the library; an unconfirmed
commit is taken back by Junos), push, deploy

**Confirmed commit**:
A commit Junos takes back by itself unless a second commit, the
confirmation, reaches it in time. Protects against a change that cuts the
device off.
_Avoid_: commit-confirm, rollback timer (that is how it works, not what it
is)

**Filter**:
A constraint on what a retrieval operation returns — subtree or XPath.
XPath filtering requires the `:xpath` capability.
_Avoid_: query, selector

### Transport

**Transport**:
The message-oriented pipe a session runs over: the SSH subsystem, a command
run over SSH, TLS, or a process of the system that speaks NETCONF on its
standard input and output. Owns framing and message boundaries; the
session never sees raw bytes.
_Avoid_: socket, connection, channel (SSH vocabulary)

**Framing**:
The RFC 6242 encoding that marks message boundaries on the byte stream:
end-of-message framing or chunked framing.
_Avoid_: encoding, packetization

**Upgrade**:
The one-way switch from end-of-message to chunked framing, taken after a
hello exchange in which both peers advertise base:1.1.
_Avoid_: renegotiation

**Keepalive**:
A periodic probe at the transport level that turns a silently dead peer
into a closed transport, and thereby a session end.
_Avoid_: heartbeat, ping

**Jump host**:
An SSH server the transport logs in to only to be connected onward, to the
next jump host or to the device. It has credentials and a host key of its
own; nothing said about the device applies to it.
_Avoid_: bastion, proxy, hop (a hop is any host on the way, the device
included)

**Close-is-cancel**:
The transport contract that closing the transport is the only way to
interrupt in-flight reads and writes. Exists because some underlying
transports (SSH channels) have no deadline support; every cancellation
path in the library ultimately reduces to a close.

### Testing

**Wire**:
A test transport running the real framing over in-memory pipes, with the
device side scripted byte by byte. Used when a test depends on what a
device actually put on the wire, including bytes outside any message.

**Conformance suite**:
The executable contract that certifies a transport implementation against
the invariants the session depends on (single active reader/writer, EOF
latch, no cross-message bleed, close-is-cancel).

**Peer**:
The scripted device side of a transport under conformance test — it sends
and receives whole messages, never raw bytes. It can go silent, like a
device that lost power: the connection stays up and nothing moves on it.
_Avoid_: server, mock (the peer scripts behaviour, it doesn't assert)
