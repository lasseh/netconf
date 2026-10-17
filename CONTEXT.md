# NETCONF client

This glossary defines the domain language of this Go NETCONF client
library, for contributors and AI agents naming a concept in code, docs or
an issue. The one context is a NETCONF session with a device.

## Language

### Protocol

**Device**:
The NETCONF server at the other end of a session, such as a Junos router
or netopeer2.
_Avoid_: server, except in an RFC quote or an API name.

**Session**:
A live NETCONF conversation with one device, established by the hello
exchange and identified by its session-id.
`netconf.Session`, opened with `netconf.Open`.
_Avoid_: connection, client

**Hello exchange**:
The opening exchange in which both sides advertise capabilities and the
device assigns the session-id. When it fails, `netconf.Open` returns an
error that matches `netconf.ErrHello`.
_Avoid_: login, negotiation

**Capability**:
A URN advertised in the hello exchange that declares an optional protocol
feature, such as `:candidate`. `netconf.CapabilitySet`, from
`Session.ServerCaps`. The session checks for `junos.Capability` to decide
on turns and Junos text normalization.
_Avoid_: feature flag, extension

**Operation**:
One protocol action the client asks of the device, sent with
`Session.ExecReply` or `Session.Exec` and matched to its reply by
message-id.
_Avoid_: method, call, RPC except in `netconf.RPC` and "RPC error", and
command except for the program `nccmd.Start` runs, the line
`ncssh.WithCommand` sends and the CLI line of `junos.Command`.

**Typed operation**:
A type in package `rpc` or `junos`, such as `rpc.GetConfig`, that
marshals to one operation and whose `Exec` method sends it and reads the
reply.

**Request**:
The `<rpc>` message an operation writes. Say request where the wire
matters, such as framing, turns or `OperationInfo.Request`, and operation
elsewhere.

**Message**:
One framed unit on the transport, in either direction: a hello, a request,
a reply or a notification.
_Avoid_: packet, frame

**Reply**:
The `<rpc-reply>` message matched to an operation by message-id. By default
the session buffers it as a `netconf.Reply`.
_Avoid_: response, and answer as a noun.

**Session end**:
The single moment a session is over. Everything waiting on the session is
released, `Session.Done` is closed, and `Session.Err` reports the cause,
or nil when `Session.Close` ended it. See
[Sessions](docs/sessions.md#session-lifetime).
_Avoid_: disconnect, teardown, session loss

**Stalled message**:
A message whose beginning has arrived and whose rest does not come. Only
`netconf.WithMessageTimeout` ends the session on it, with
`netconf.ErrMessageStalled`. See
[Operations](docs/operations.md#stalled-messages).
_Avoid_: hang, frozen, stall timeout

**Streamed reply**:
A reply that `Session.Stream` hands over unbuffered, as a
`netconf.Message`. The caller must close it before the session can
process anything else.

**Turn**:
The right to have the one outstanding request on a session over chunked
framing to a Junos device. An operation takes the turn before its request
is written and keeps it until the next message that is not a notification
has arrived whole, normally its reply. See
[Sessions](docs/sessions.md#requests-from-several-goroutines).
_Avoid_: lock, queue

**RPC error**:
A structured error, `netconf.RPCError`, in a reply at any depth or with
the hello in `HelloError.RPCErrors`. A warning goes to `Reply.Warnings`,
and every other RPC error fails the operation. The Junos `<xnm:error>`
counts as one, with a message and neither type nor tag.
_Avoid_: fault

**Notification**:
A message the device sends without a request, under an RFC 5277
subscription. The session hands notifications to the
`netconf.WithNotifHandler` handler one at a time, in order. See
[Sessions](docs/sessions.md#notifications).
_Avoid_: event, which is what a notification reports, and trap.

**Junos text normalization**:
What the session does to every reply and notification it buffers from a
Junos device. Junos 26.2 writes each byte above 127 as a character
reference, and the session joins those that form one UTF-8 character. A
streamed reply is not normalized. See [Junos](docs/junos.md#non-ascii-text).
_Avoid_: rewrite, put back together, and repair, which is
`netconf.WithReplyRepair`.

**`rfc-compliant` mode**:
The Junos statement `rfc-compliant` under `system services netconf`. With
it, Junos 26.2 prefixes every NETCONF element with `nc` and sends
configuration in the Junos YANG namespaces. See
[What is verified](docs/junos-verified.md#rfc-compliant-mode).

### Configuration

**Datastore**:
A named configuration store on the device: running, candidate or startup.
`rpc.Datastore`.
_Avoid_: database, config bank, except in Juniper names such as the
ephemeral database.

**Source**:
Where a configuration operation reads from: a datastore, a URL or an
inline Config. `rpc.Source`.
_Avoid_: input, from

**Target**:
Where a configuration operation writes to: a datastore or a URL, never an
inline Config. `rpc.Target`.
_Avoid_: destination, output

**Config**:
An inline configuration payload the caller supplies as a source.
`rpc.Config`.
_Avoid_: blob, body

**Change**:
One configuration update on a Junos device that `junos.Change.Apply`
carries out as a whole. `Apply` reserves the candidate, loads, compares
and commits, and always releases it. See
[Junos](docs/junos.md#changing-configuration).
_Avoid_: transaction, push, deploy. The library never undoes a commit.

**Private copy**:
A copy of the candidate that Junos gives one session, like "configure
private" in the CLI. A commit applies this session's changes only. A
private copy takes no confirmed commit. `junos.Change{Private: true}`, or
`junos.OpenConfiguration{Private: true}`.

**Ephemeral database**:
The Junos configuration database opened with
`junos.OpenConfiguration{Ephemeral: true}`. Its commits are not part of
the configuration and add no commit history entry. `junos.Change` does not
work with it. See [Junos](docs/junos.md#the-ephemeral-database).
_Avoid_: datastore, a NETCONF term.

**Commit check**:
An operation that has Junos validate the candidate and commit nothing,
`junos.CommitConfiguration{Check: true}`. On Junos 26.2, a commit check
from any session confirms a pending confirmed commit.
_Avoid_: validate, which is `rpc.Validate`.

**Dry run**:
A `junos.Change` with `DryRun` set. `Apply` loads and compares, can send
a commit check, and never commits. A dry run is not read-only. It takes
the lock or a private copy. See [Junos](docs/junos.md#change-safety).

**Confirmed commit**:
A commit the device takes back by itself unless it is confirmed in time.
`junos.Change.Confirm`, `junos.CommitConfiguration.Confirm`, or
`rpc.Commit` with `Confirmed`. On Junos 26.2, a commit or a commit check
from any session confirms it. See
[Standard NETCONF](docs/standard-netconf.md#confirmed-commits).
_Avoid_: commit-confirm, rollback timer.

**Filter**:
A constraint on what a retrieval operation returns, `rpc.SubtreeFilter`
or `rpc.XPathFilter`. RFC 6241 allows an XPath filter only on a device
that advertises `:xpath`. See
[Standard NETCONF](docs/standard-netconf.md#read-and-filter-configuration).
_Avoid_: query, selector

### Transport

**Transport**:
The message-oriented pipe a session runs over, `transport.Transport`,
which owns the framing and the message boundaries. The library has SSH,
TLS and command transports.
_Avoid_: socket, connection, channel

**SSH transport**:
The transport in `transport/ssh`, imported as `ncssh`, built on
`golang.org/x/crypto/ssh`. `ncssh.DialHost` connects the way the OpenSSH
client does. See [Connecting](docs/connecting.md#options-of-ncsshdialhost).
_Avoid_: native SSH transport, the library's ssh client

**System OpenSSH**:
The `ssh` program, run with `nccmd.Start` for what the SSH transport
lacks, such as ProxyCommand or Kerberos. See
[Connecting](docs/connecting.md#system-openssh).
_Avoid_: the ssh of the system, the OpenSSH binary

**SSH config**:
The OpenSSH client configuration file. `ncssh.DialHost` reads some of its
directives, only with `ncssh.WithSSHConfig`. See
[Connecting](docs/connecting.md#the-ssh-config).
_Avoid_: SSH configuration, ssh config in lowercase

**Host key**:
The public key with which a device or jump host proves its identity in the
SSH handshake. See [Connecting](docs/connecting.md#host-keys).
_Avoid_: host-key, server key, fingerprint. Write `known_hosts` in
backticks when you mean the file.

**Framing**:
The RFC 6242 encoding that marks message boundaries on a byte stream,
end-of-message or chunked. `transport.Framer` implements both.
_Avoid_: encoding, packetization

**Upgrade**:
The one-way switch from end-of-message to chunked framing after a hello
exchange in which both sides advertise `base:1.1`.
`transport.Transport.Upgrade`.
_Avoid_: renegotiation

**Keepalive**:
A periodic SSH keepalive request, `ncssh.WithKeepalive`, that turns a
silently dead device or connection into a session end with
`ncssh.ErrKeepalive`.
_Avoid_: heartbeat, ping

**Jump host**:
An SSH server the SSH transport logs in to only to connect onward, set
with `ncssh.WithProxyJump`. It has its own credentials and host key.
See [Connecting](docs/connecting.md#jump-hosts).
_Avoid_: bastion, proxy and hop as names for the concept. A hop is any host
on the way, the device included. An example host may be
`bastion.example.net`.

**Close-is-cancel**:
The transport contract that only closing the transport interrupts a read
or a write in progress, because SSH channels have no deadlines. Canceling
an operation's context only abandons the wait for its reply. See
[ADR 0001](docs/adr/0001-close-is-cancel-transport-contract.md).

### Testing

**Wire**:
A test transport, `netconftest.Wire`, that runs the real framing over
in-memory pipes with the device side scripted to the byte, including bytes
outside any message.

**Conformance suite**:
`transporttest.Run`, the executable contract of `transport.Transport` that
every transport implementation must pass. It checks the invariants a
session depends on, close-is-cancel among them.

**Peer**:
The scripted device side of a transport under conformance test,
`transporttest.Peer`. It sends and receives whole messages.
`transporttest.StreamPeer` can also stall like a device that lost power.
_Avoid_: server, mock
