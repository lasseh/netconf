# What is verified against Junos

This page records which Junos platforms and releases the library was
checked against, and what Junos does that the library does not hide. Read
it before you rely on a behavior on a platform or release not listed
here.

- [Platform and release summary](#platform-and-release-summary)
- [Known issues at a glance](#known-issues-at-a-glance)
- [Junos limits the library does not work around](#junos-limits-the-library-does-not-work-around)
- [`rfc-compliant` mode](#rfc-compliant-mode)
- [Not verified on a device](#not-verified-on-a-device)
- [Captured replies](#captured-replies)
- [Recorded integration runs](#recorded-integration-runs)
- [Chunked-framing investigations](#chunked-framing-investigations)
- [Standard operations and reported quirks](#standard-operations-and-reported-quirks)
- [From one Junos release to the next](#from-one-junos-release-to-the-next)
- [Recording future verification](#recording-future-verification)

## Platform and release summary

| Platform | Software | Evidence |
| --- | --- | --- |
| EX4100 | Junos 25.4R1-S1.7 | Captured replies. No integration run is recorded. |
| vJunos-router | Junos 26.2R1.7 | Captured replies and integration runs. For chunked framing, see [Runs by chunk writer](#runs-by-chunk-writer). |
| vJunos-switch | Junos 26.2R1.7 | Captured errors and refusals. Integration runs, with and without `rfc-compliant`. |
| vJunosEvolved | Junos OS Evolved 26.2R1.7 | Captured errors and refusals. Integration runs, with and without `rfc-compliant`. Notification content differs from Junos. |
| vJunos-router | Junos 23.4R2-S2.1 | Every integration test passed. No chunked framing. |
| netopeer2 | 2.8.7 where recorded | The CI target for standard NETCONF, chunked framing, and the `ncssh` and `nccmd` transports. Not evidence for Junos. |

A captured reply pins how the library parses what the device sent. An
integration run shows that the library, at that run's commit, worked
against that device. A comparison of Juniper's YANG modules shows what a
release declares, not what a device does.

The 25.4 and 26.2 builds come from the captures, and 23.4R2-S2.1 from the
integration runs. Run records name only the release for 26.2, and do not
always name the run date or library commit. CI builds netopeer2 from its
`master` branch, so its version moves.

## Known issues at a glance

[Troubleshooting](troubleshooting.md) says what to do about a symptom.

| Symptom | Seen on | Library behavior |
| --- | --- | --- |
| [Junos ends the session on a large chunk or two requests written together](#chunked-framing-investigations) | Junos 26.2 on vJunos-router, chunked framing | The chunk writer and the turn avoid both. |
| [A reply without indentation never finishes](#formats-without-indentation-over-chunked-framing) | Junos 26.2 and Junos OS Evolved 26.2, chunked framing | `junos.GetConfiguration.Exec` refuses `junos.FormatXMLMinified` and `junos.FormatJSONMinified` there. |
| [A session started with a command gets trailers and a `base:1.1` hello](#trailers-on-a-session-started-with-a-command) | vJunos-router 26.2 with `rfc-compliant` and `version-1.1` | Open it with `netconf.WithCapability(netconf.CapNetConf10)`. |
| [A client that waits for the device's hello waits forever](#hello-order) | Junos 26.2 with `rfc-compliant` and `version-1.1` | `netconf.Open` sends its hello first. |
| [Junos answers no operations on a session with a subscription](#operations-on-a-subscription-session) | Junos 26.2 and Junos OS Evolved 26.2 without `notification interleave` | Nothing. Use another session. |
| [Some failed text operations return no error](#empty-replies-to-failed-operations-in-text-format) | Junos 23.4R2-S2.1, `rfc-compliant` | `junos.Command.XML` gets the error. `junos.Command.Exec` does not. |
| [A failed JSON operation returns no error](#failed-json-operations-without-an-error) | Junos 26.2 and 23.4R2-S2.1, for a missing interface | Nothing. Ask in XML. |
| [Pipe filters are ignored](#pipe-filters-in-junoscommand) | No release recorded | `junos.Command` returns the whole output. |
| [A line break in a JSON string is stored as a backslash and a letter](#line-breaks-and-tabs-in-json-strings) | Junos 26.2 | Nothing. |
| [A control character reads back as `&#167;`](#control-characters-in-statements) | Junos 26.2 | Nothing. The value cannot be recovered. |
| [A set load fails after a refused `<edit-config>`](#set-loads-after-a-refused-edit-config) | Junos 26.2 on vJunos-switch and Junos OS Evolved 26.2 | `junos.Change` sends no `<edit-config>`. |
| [`<cancel-commit>` is refused](#no-cancel-commit) | Junos 26.2 | `rpc.CancelCommit` fails with an error that matches `junos.ErrUnsupported`. |
| [A commit check confirms another session's pending commit](#commit-checks-confirm-a-pending-commit) | Junos 26.2 on vJunos-router | A `junos.Change` dry run sends no commit check and fails with `junos.ErrCommitPending`. |
| [A commit comment over 512 bytes is refused](#commit-comments-over-512-bytes) | Junos 26.2 | `junos.CommitConfiguration` and `junos.Change` refuse it before they send anything. |
| [An empty commit adds a history entry](#commits-with-nothing-to-commit) | Junos 26.2 | `junos.Change` commits nothing when its load changed nothing. |
| [A failed commit check can take over 30 seconds](#slow-commit-checks) | Junos 25.4 on an EX4100 | Nothing. Allow for it in the deadline. |

## Junos limits the library does not work around

### Formats without indentation over chunked framing

Over chunked framing, Junos 26.2 and Junos OS Evolved 26.2 send the start
of a reply in `junos.FormatXMLMinified` or `junos.FormatJSONMinified` and
stop, for one statement or the whole
configuration. `netconf.WithMessageTimeout` ends such a session with
`netconf.ErrMessageStalled`. Over end-of-message framing, on the same
device with `rfc-compliant` set, both formats work. Seen on vJunos-router,
vJunos-switch and vJunosEvolved.

`junos.GetConfiguration.Exec` refuses both formats on a session with
chunked framing and sends nothing, so the session survives. See
[Reading configuration and operational data](junos.md#reading-configuration-and-operational-data).

### Trailers on a session started with a command

On vJunos-router 26.2 with `rfc-compliant` and `version-1.1` set, a session
started with `ncssh.WithCommand("xml-mode netconf need-trailer")` gets the
`]]>]]>` trailer, but the hello advertises `base:1.1`. A session that
switches to chunked framing ends with `transport.ErrMalformedChunk`. With
`netconf.WithCapability(netconf.CapNetConf10)` the session stays in
end-of-message framing, and the read tests pass. See
[A device without a NETCONF service](connecting.md#a-device-without-a-netconf-service).

### Hello order

With `rfc-compliant` and `version-1.1` set, Junos 26.2 sends its hello only
after the client's. RFC 6242 has both sides send their hellos at once. A
client that waits for the device's hello first waits forever.
`netconf.Open` sends its hello first.

On a session started with `ncssh.WithCommand`, vJunos-router 26.2 with
the same statements sends its hello unasked. See
[Sessions started with a command](#sessions-started-with-a-command).

### Operations on a subscription session

Junos 26.2 and Junos OS Evolved 26.2 advertise `:interleave` whenever
notifications are configured. They answer operations on a session with a
subscription only when `system services netconf notification interleave` is
set. Without it they answer nothing on that session, not even
`<close-session>`, so an operation waits until its context ends, and so
does `netconf.Session.Close`. See [Notifications](sessions.md#notifications).

### Empty replies to failed operations in text format

In `rfc-compliant` mode, Junos 23.4R2-S2.1 on vJunos-router sends an empty
reply to some failed operations that ask for text. It was seen for an
interface that does not exist, asked for as a command and as
`<get-interface-information>`. `junos.Command.Exec` then returns no output
and no error. A command that prints nothing, `show route table inet.3` on
that device, gets the same reply.

Without `rfc-compliant`, 23.4R2-S2.1 puts the error in the reply as text.
In both modes, `junos.Command.XML` gets the error as an `<rpc-error>`, and
a command with a syntax error fails with one. In `rfc-compliant` mode,
Junos 26.2 and Junos OS Evolved 26.2 return the error of a failed command,
and `TestJunosCommand` passed on vJunos-router, vJunos-switch and
vJunosEvolved.

### Failed JSON operations without an error

Junos writes most errors of a `format="json"` operation into the JSON
document, and `junos.JSON` returns them as an error. Some it leaves out.
Asked about an interface that does not exist, Junos 26.2 and Junos
23.4R2-S2.1 send a document without data, the same as a result without
data. The 26.2 capture is `show interfaces` on vJunos-switch, and its
document is `{"interface-information" : [{}]}`. Junos OS Evolved 26.2
writes the error into the document.

### Pipe filters in `junos.Command`

Junos applies no `| match` or other filter to a `junos.Command`. It returns
all the output. No release is recorded.

### Line breaks and tabs in JSON strings

Junos 26.2 reads two escapes in a JSON string, `\"` and `\\`, and stores
every other escape as written. `junos.LoadConfiguration` writes every
escape that has a plain form in JSON as that character. A line break or a
tab has none, so Junos stores `\n` as a backslash and an `n`.

### Control characters in statements

Junos 26.2 accepts a control character in a statement, as a byte and as a
character reference, and answers with `&#167;` in its place when the
configuration is read. The value then cannot be told apart from one that
contains a section sign. U+0000 ends the value.

### Set loads after a refused `<edit-config>`

After Junos 26.2 or Junos OS Evolved 26.2 refuses an `<edit-config>` for a
syntax error in its `<config>`, the next load of set commands on the
session fails with "internal communications error (tag 'configuration-set')
expecting configuration-text" and loads nothing. The load after that works.
A load in another format, an accepted `<edit-config>` or a
`<get-configuration>` in between clears the condition. A commit check does
not. Seen on vJunos-switch and vJunosEvolved, each time in a private copy.
`junos.Change` sends no `<edit-config>`, so it hits this only after a
refused `rpc.EditConfig` on the session.

### No `<cancel-commit>`

Junos 26.2 advertises `:confirmed-commit:1.0`, which does not define
`<cancel-commit>`. Junos answers `<cancel-commit>`, written as a pair of
tags or as an empty element, with an RPC error whose message is "syntax
error". `rpc.CancelCommit` fails with an error that matches
`junos.ErrUnsupported`. Let a confirmed commit time out, or confirm it.

### Commit checks confirm a pending commit

On Junos 26.2 on vJunos-router, a commit or a commit check from any session
confirms a pending confirmed commit, whether the Junos
`<commit-configuration>` or the standard `<commit>` made it. A confirmation
by a commit check adds no entry to the commit history.

A `junos.Change` dry run reads the commit history first, and while a
confirmed commit is pending it sends no commit check and fails with
`junos.ErrCommitPending`. See [Change safety](junos.md#change-safety).

### Commit comments over 512 bytes

Junos 26.2 accepts a commit comment of up to 512 bytes and refuses a longer
one: "Length 513 is not within range (1..512)". It counts bytes, so a
two-byte character counts as two. A commit check applies the
same limit. `junos.CommitConfiguration` and `junos.Change` refuse a longer
comment before they send anything.

### Commits with nothing to commit

Junos 26.2 commits a candidate that has nothing to commit. It answers with
`<ok/>` and adds an entry to the commit history, which keeps 50 entries.
`junos.Change` commits nothing when its load changed nothing.

### Slow commit checks

A failed commit check took more than 30 seconds on an EX4100 with Junos
25.4. Give a commit check, and a `junos.Change` that runs one, a context
deadline that allows for that.

## `rfc-compliant` mode

`rfc-compliant`, committed under `system services netconf`, takes effect at
once, also for the session that committed it. Junos 26.2 then writes every
NETCONF element with the prefix `nc`, including `<nc:ok/>` and
`<nc:rpc-error>` inside its own result elements. The library parses
prefixed and unprefixed elements the same way. The configuration comes in
the Junos YANG namespaces, such as
`http://yang.juniper.net/junos/conf/root`, so a struct tag that names a
namespace for a configuration element matches in only one of the two modes.
Seen on vJunos-router.

Junos 26.2 and Junos OS Evolved 26.2 sent these warnings next to an `<ok/>`
with and without `rfc-compliant`, and `netconf.Session.ExecReply` returns
them in `netconf.Reply.Warnings` either way:

- the warning of a `<synchronize/>` on a device with one routing engine,
  next to the `<ok/>` of a commit check
- that of a private copy opened while another session holds the lock
- that of a load that deletes configuration that does not exist

## Not verified on a device

These have not run against a device through this library:

- loading and committing a named instance of the ephemeral database,
  `junos.OpenConfiguration` with `EphemeralInstance`
- `junos.Change.Synchronize` and `junos.CommitConfiguration.Synchronize`
  on a device with two routing engines
- an operation sent to a routing engine that is not the master
- `junos.LoadConfiguration` with a `URL` for an ftp or http location. The
  integration suite loads a file by its path on the device.
- Junos `outbound-ssh`, where the device opens the connection and
  `ncssh.WithDialer` accepts it. Unit tests cover `ncssh.WithDialer` with
  an SSH server that dials in. See
  [Custom connections with `WithDialer`](connecting.md#custom-connections-with-withdialer).

Three reports from users of other libraries could not be checked:

- on an SRX chassis cluster, a reply to a commit check with a
  `<routing-engine>` that is not closed
- on an SRX chassis cluster, a load under the lock that changes nothing
- an XPath filter, for which Junos reportedly expects `source` in place
  of `select`. Junos 26.2 advertises no `:xpath`.

The detailed probes of non-ASCII text, control characters, `\n` in JSON
strings, the `&#167;` substitution and the Junos error element
`<xnm:error>` ran on release 26.2 only.

## Captured replies

The unit tests replay these replies, captured from lab devices. Each test
names the device, the release and, when recorded, the exact build.

- **Junos 25.4R1-S1.7 on an EX4100.** The reading operations, loads in
  every format, the commit check, the private copy, and the errors of a
  failed load, commit check and command.
- **Junos 26.2R1.7 on a vJunos-router.**
  - the hello, and the first message to a user whose login class is
    `unauthorized`: two lines of text, two replies with a warning and
    an error, and a hello. `netconf.Open` fails on it with a
    `*netconf.HelloError`.
  - plain commits, commits with a comment and confirmed commits, with the
    confirmation in time and too late
  - the lock on the candidate, and a private copy opened while another
    session holds the lock, while the candidate has uncommitted changes,
    and while another private copy is open
  - the commit history while a confirmed commit was pending, after it was
    confirmed, and after Junos took it back
  - the attributes of `<configuration>` that say when and by whom it was
    committed
  - non-ASCII text in a configuration in every format, in a diff, in
    command output, in an error and in a notification
  - notifications with `system services netconf notification` set. The
    NETCONF stream is the system log. Junos sends every event twice, as
    `<syslog>` and as `<state-system-syslog>`, and one commit produces a
    hundred or more.
  - a session in `rfc-compliant` mode, and one with `rfc-compliant` and
    `version-1.1` set, whose hello is in end-of-message framing and whose
    replies are in chunks
- **Junos 26.2R1.7 on vJunos-switch and Junos OS Evolved 26.2R1.7 on
  vJunosEvolved.** The Junos error element `<xnm:error>` under
  `<rpc-reply>`, with and without a message, and the errors Junos writes
  into a JSON document.
- **Junos OS Evolved 26.2R1.7 on vJunosEvolved only.** `<xnm:error>` inside
  a result, in XML and in JSON, and what is left of the error element in
  `rfc-compliant` mode. A configuration with the syslog severity `error`,
  whose statement has the name and the namespace of the Junos error
  element.
- **Junos 26.2R1.7 and Junos OS Evolved 26.2R1.7 on several platforms.**
  - refusals to a user whose login class can read but not configure, of
    unknown operations, and of operations that do not apply to the
    platform, from vJunos-router, vJunos-switch and vJunosEvolved, and
    from vJunosEvolved with `rfc-compliant` set. `junos.ErrPermissionDenied`
    and `junos.ErrUnsupported` are tested against them. An operation that
    does not apply to the platform matches neither.
  - who is editing the configuration, as
    `junos.GetDatabaseStatusInformation` reads it, from all three
  - four load results in a private copy, from vJunos-router. The switch
    and Junos OS Evolved answered the same.
  - the refusals of the default instance of the ephemeral database, for a
    compare, a confirmed commit and a configuration group, from
    vJunos-router. Junos OS Evolved answered the same, word for word.

Some unit tests replay replies that are not captures, and each test says
so. From PyEZ issues come a failed commit from Junos 14.1X53, a failed ping
from Junos 16.1R4 with its error inside the result element and in its
namespace, and an error element whose `xnm` prefix is not declared, from
Junos OS Evolved 23.2R2 on an ACX7509. A reply with a control character is
written by hand, because Junos 26.2 sends `&#167;` in place of one.

## Recorded integration runs

Each Junos and Junos OS Evolved device was reached through an OpenSSH jump
host. Over end-of-message framing, every test passed, including commits and
notifications, on vJunos-router 26.2, and on vJunos-switch and
vJunosEvolved 26.2 with and without `rfc-compliant`. To run the suite, see
[integration/README.md](../integration/README.md).

### What the 26.2 runs covered

On vJunos-router, vJunos-switch and vJunosEvolved 26.2, the runs covered:

- `junos.Change` committing under the lock and in a private copy
- a confirmed commit that is confirmed, and one that the device takes
  back
- changes refused because another session holds the lock or has
  uncommitted changes in the candidate
- non-ASCII text, loaded and read back in every format
- a document from `encoding/json` that reads back as the value it was
  made from
- failed operations, asked for in XML and in JSON
- the default instance of the ephemeral database, which takes a load and
  a commit, is read back, and refuses the operations it does not support
- with notifications configured, a subscription that delivers them with
  their time and content, and operations on the same session where the
  device answers them
- a commit comment, which arrives unchanged in a notification on
  vJunos-router and vJunos-switch. Junos OS Evolved 26.2 sends no
  notification about a commit.

### Junos 23.4R2-S2.1 on vJunos-router

Every suite test passed against vJunos-router 23.4R2-S2.1, over several
runs:

- the tests that commit, on the device in its default configuration
- the same tests with `rfc-compliant` and notifications set
- the notification test without `rfc-compliant`

Two tests accept the behavior of either release. For
`junos.CompareRollback` in the ephemeral database, Junos 26.2 answers
"Unsupported operation config compare in ephemeral database", and
23.4R2-S2.1 returns errors that name `/config/juniper.conf`. For
`show interfaces` with an interface that does not exist, in
`rfc-compliant` mode, 23.4R2-S2.1 sends an
[empty reply](#empty-replies-to-failed-operations-in-text-format).

### Sessions started with a command

Both ways of starting NETCONF with a command ran against Junos 26.2, and
`ncssh.WithCommand` also against Junos OS Evolved 26.2.

- The suite uses `ncssh.WithCommand("xml-mode netconf need-trailer")` on
  port 22 when `NETCONF_DUT_SSHCOMMAND` is set. On vJunos-router with the
  NETCONF service removed, port 22 refuses the netconf subsystem, and the
  command starts a session. With the service configured, on vJunos-router,
  vJunos-switch and vJunosEvolved, port 22 accepts both the subsystem and
  the command, and port 830 the subsystem only. The command gets the same
  hello as port 830, with the same capabilities and framing, and the read
  tests passed on it.
- On vJunos-router with `rfc-compliant` and `version-1.1`, the read tests
  passed with `netconf.WithCapability(netconf.CapNetConf10)`. See
  [Trailers on a session started with a command](#trailers-on-a-session-started-with-a-command).
- The `nccmd` transport, which the suite uses when `NETCONF_DUT_COMMAND`
  is set, ran the suite against vJunos-switch 26.2 with `ssh -s` through
  an SSH config with a jump host.

### Advertised capabilities

`TestSSHDial` logs the capabilities a device advertises. With no options
set under `system services netconf`, Junos 23.4R2-S2.1 on vJunos-router,
Junos 26.2 on vJunos-router and vJunos-switch, and Junos OS Evolved 26.2 on
vJunosEvolved advertise the same fifteen, so the suite runs and skips the
same tests on each:

- `base:1.0`, `:candidate:1.0`, `:confirmed-commit:1.0`, `:validate:1.0`
  and `:url:1.0` with the schemes http, ftp and file, each of the five
  under `urn:ietf:params:netconf:` and under
  `urn:ietf:params:xml:ns:netconf:`
- `ietf-netconf-monitoring` and `ietf-yang-metadata`
- three Juniper capabilities, including
  `http://xml.juniper.net/netconf/junos/1.0`

With `rfc-compliant`, `version-1.1` and notifications set, vJunos-router
26.2R1.7 advertises 21 capabilities, including `base:1.1`,
`ietf-inet-types`, `:notification:1.0` and `:interleave:1.0`.

## Chunked-framing investigations

These runs and probes are the evidence behind the chunk writer of package
`transport` and the turn, under which a session sends Junos one request at
a time over chunked framing.

### Configuration for chunked framing

Junos 26.2 speaks `base:1.1` when `rfc-compliant` and `version-1.1` are
both set under `system services netconf`. With `rfc-compliant` alone it
advertises `base:1.0` only.

### What Junos writes and reads

Junos 26.2 on vJunos-router writes each line as its own chunk, the start
tag of the reply first and in a chunk by itself. No chunk larger than 512
bytes was seen. Junos accepts less than RFC 6242 allows:

- it ends the session on a chunk of 9800 bytes, and accepts one of 9500
- it misses the end-of-chunks marker when one of its own reads ends
  inside the marker, which happened for messages of 4098 and 8193
  bytes on the wire

Junos sends the start tag of a reply as soon as it has the start tag of the
request, and the end of the reply as soon as it has the end tag, before the
client has written the end of the message. Seen on vJunos-router over both
framings, with a request written in pieces.

The chunk writer of package `transport` keeps clear of both limits for
every device. It writes no chunk larger than 4096 bytes, and it keeps the
end-of-chunks marker clear of every multiple of 4096 bytes into the
message. It hands the transport 32768 bytes at a time, a multiple of 4096,
so every write also ends on a multiple of 4096 bytes of the message.

### Two requests written together

The chunk writer counts bytes from the start of each message, so its
boundary rule does not cover a message written directly after another. With
two requests written together, vJunos-router 26.2 ended the session when
the end-of-chunks marker of the second began three bytes before byte 8192,
counted from the start of the first. That happened in 3 of 40 tries from a
transport that held the two back and wrote them together, and in 3 of 80
from two goroutines. With the marker two bytes before byte 8192, the
session ended in 1 of 40 tries, and with it one byte before, in none of
40.

So a session with chunked framing to a device that advertises the Junos
capability writes one request at a time and waits for the whole reply
before the next.
[ADR 0006](adr/0006-junos-gets-one-request-at-a-time-over-chunks.md)
records the decision, and
[Requests from several goroutines](sessions.md#requests-from-several-goroutines)
says what it means for callers.

### Runs by chunk writer

| Code | Platform | What passed |
| --- | --- | --- |
| An earlier writer that sent each message as one chunk | vJunos-router 26.2 | Every test, including commits and notifications, over several runs rather than one. |
| The same writer | vJunos-switch 26.2, vJunosEvolved 26.2 | The tests that commit nothing. Three tests have not run there. |
| The current chunks, one write per chunk | vJunos-router 26.2 | Requests of every size around 4096, 8192 and 65536 bytes, in 15 runs of 15, where the earlier writer failed 3 runs of 12. `TestRequestSizes` at each of the three boundaries on the wire, in 13 runs of 13. Nine more suite tests, as dry runs. |
| The same, with the turn | vJunos-router 26.2 | 27 tests, all but the commit tests and the notification test, with commits not enabled and notifications not configured. 240 pairs of requests from two goroutines, all answered. |
| The current writer: the same chunks in writes of 32768 bytes, with the turn | vJunos-router 26.2 | On 2026-10-12, on code from before the review fixes of 2026-10-14: `TestRequestSizes` in 31 runs of 31, and 5496 pairs of requests from two goroutines answered. In one run of 24 pairs, neither request of one pair was answered within the five minutes of the test. The cause is not known: the path to the lab stalled at other times that day, and it did not happen again. |
| The current writer | netopeer2 | The whole suite, in CI on every push. |

Of the sizes around 4096, 8192 and 65536 bytes, only those around 4096 put
the end-of-chunks marker on a boundary on the wire, where the chunk headers
count too. With the writer's boundary rule disabled beyond the first
boundary, Junos ended the session in 6 of 7 runs, at messages whose marker
lay across byte 8192 or byte 65536. The 240 pairs, like those that
`TestRequestsAtOnce` sends, put the marker of the second request 1, 2 and 3
bytes before byte 8192.

## Standard operations and reported quirks

### Standard write operations

The integration suite runs the standard operations of package `rpc`
against netopeer2, where every operation in the table passed, and against
Junos 26.2 on vJunos-router and vJunos-switch, and Junos OS Evolved 26.2 on
vJunosEvolved.

| Operation | Junos 26.2 and Junos OS Evolved 26.2 |
| --- | --- |
| `rpc.Lock` on the candidate, `rpc.EditConfig`, `rpc.Validate` and `rpc.DiscardChanges` | Passed on all three |
| `rpc.Commit` of an edit, and a second commit that removes it | Passed on vJunos-router |
| A confirmed commit ended with `rpc.CancelCommit` | Not run, because all three advertise `:confirmed-commit:1.0` |
| A confirmed commit with a token in `rpc.Commit.Persist`, given more time and confirmed from a second session, and another one canceled from that second session | Not run, because it needs `:confirmed-commit:1.1` |
| `rpc.EditConfig` on the running configuration, and `rpc.CopyConfig` of it to startup | Not run, because none advertises `:writable-running` or `:startup` |
| `rpc.GetConfig` with a subtree filter, which returns that subtree | Passed on all three |

netopeer2 2.8.7 refuses `<persist-id>` next to `<confirmed/>`, although
RFC 6241 puts `<persist-id>` there. netopeer2 accepts a further confirmed
commit that repeats the first commit's token in `<persist>`.

### Reports from users of other libraries

Users of other NETCONF libraries reported these Junos behaviors.
`integration/reports_test.go` has a test for each.

| Report | Seen on release 26.2 | Test |
| --- | --- | --- |
| Junos does not read a request whose `]]>]]>` falls across a 4096-byte boundary, so a client has to pad it. | Over end-of-message framing, vJunos-router, vJunos-switch and vJunosEvolved answered such a request at every offset around three boundaries. The end-of-message writer pads nothing. | `TestRequestSizes` |
| A confirmed commit sent as the standard `<commit>` loses its confirm timeout, or is taken back when its session ends. | On vJunos-router it kept its timeout and outlived its session. While it was pending, a `junos.Change` dry run from another session sent no commit check, failed with `junos.ErrCommitPending` and left the commit pending. | `TestJunosStandardConfirmedCommit` |
| `<cancel-commit>` behaves differently written as a pair of tags and as an empty element. | Junos 26.2 has no `<cancel-commit>` in either spelling. See [No `<cancel-commit>`](#no-cancel-commit). | `TestJunosStandardConfirmedCommit` |
| Junos JSON output is not always valid JSON: a line break inside a string, an unescaped backslash in a regular expression, an empty object before the document. | vJunos-router, vJunos-switch and vJunosEvolved wrote valid JSON for a syslog match with backslashes, for a quote and a backslash in a text, and for the output of commands, with nothing before the document. | `TestJunosJSONIsJSON` |
| A commit comment longer than 512 characters fails the commit after the load has succeeded. | Junos 26.2 refuses the comment and counts bytes. See [Commit comments over 512 bytes](#commit-comments-over-512-bytes). | `TestJunosCommitCommentLimit` |

## From one Junos release to the next

Juniper publishes the YANG modules of its releases at
[github.com/Juniper/yang](https://github.com/Juniper/yang), and a device
serves those of the release it runs. `make junos-schema` checks two of them
for every release of the families in `JUNOS_FAMILIES`, `junos junos-ex` by
default, where `junos` also covers Junos OS Evolved. From
`junos-conf-system` it lists the statements under `system services netconf`
and what each release adds or removes. In `junos-rpc-system` it fails when
a release does not declare an element that package `junos` reads from the
replies to `<get-system-information>` and `<get-commit-information>`.

`TestJunosSchema` reads the same two modules off a device with
`<get-schema>` into the directory that `NETCONF_DUT_YANG` names, and the
comparison takes them as the release they declare:

```bash
NETCONF_DUT_SSHHOST=switch1.example.net NETCONF_DUT_FLAVOR=junos \
NETCONF_DUT_YANG=/tmp/switch1 make integration-test TESTFLAGS=-run=TestJunosSchema
make junos-schema JUNOS_YANG_MORE=/tmp/switch1
```

The comparison reports the following for the releases Juniper has
published, 17.4R1 to 25.4R1 of Junos and 23.4R1 to 25.4R1 of Junos OS
Evolved, for 23.4R2-S2.1 as vJunos-router serves it, and for 26.2 as
vJunos-router, vJunos-switch and vJunosEvolved serve it:

- Every release from 23.1R1 on declares the content of the two replies,
  including all the elements read from them. Earlier releases declare
  both replies as `anyxml`, without their content.
- Every release declares `rfc-compliant`. Every Junos and Junos OS Evolved
  release from 24.4R1 declares `version-1.1`, which chunked framing also
  needs. Every release from 19.2R1, and in `junos-ex` from 20.4R1,
  declares `notification` with `interleave`. Every release from 20.2R1
  declares `tls`.
- 26.2 has the statements of 25.4R1 under `system services netconf`, no
  more and no fewer.
- vJunos-router and vJunos-switch 26.2 serve the same two modules, byte
  for byte.
- 23.4R2-S2.1 on a device has the statements Juniper publishes for 23.4R2,
  the 26.2 statements without `version-1.1`.

vJunos-router 23.4R2-S2.1 was also checked against its own module, with
statements loaded one at a time into a private copy that was then closed.
It refuses `version-1.1`, which its module does not declare, with an RPC
error that reports a syntax error. Of the twelve statements in the module,
it accepts eleven, including `rfc-compliant`, `notification interleave` and
`flatten-commit-results`. It refuses
`yang-modules emit-family-ns-and-module-name` the same way, although its
module declares that statement from 23.4R1 on.

The comparison cannot check the other operations of package `junos`, which
are in none of the 166 RPC modules that Juniper publishes for the `junos`
family of 25.4R1: `<get-configuration>`, `<load-configuration>`,
`<commit-configuration>`, `<open-configuration>`, `<close-configuration>`,
`<command>` and `<get-database-status-information>`. YANG has no
attributes, so the `seconds` of a `<date-time>` are not in the modules
either. A declared statement says nothing about what a device does with it.
The integration suite covers that, and against 23.4R2-S2.1 it found
[two behaviors](#junos-234r2-s21-on-vjunos-router) that no module shows.

## Recording future verification

For a new run against a device, record:

- the date and the commit of the library
- the device model and the exact software build
- the transport, and whether a jump host was used, never its name or
  address
- the advertised capabilities, the framing and the configuration under
  `system services netconf`
- the write gates, the tests selected and the outcome
- whether it was one complete run or several partial runs

Link sanitized captures or logs. Never publish a raw configuration or a
secret, and remove what [Contributing](../.github/CONTRIBUTING.md#tests)
lists from a capture. Record a schema check apart from runtime tests and
captured replies. When the framing or session code changes, record which
runs used the new code instead of carrying over earlier results.
