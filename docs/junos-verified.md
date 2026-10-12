# What is verified against Junos

- **Pinned by replies captured from Junos 25.4 (EX4100):** the reading
  operations, loading in every format, the commit check, the private copy,
  and the errors of a failed load, commit check and command.
- **Pinned by replies captured from Junos 26.2 (vJunos-router):** commits,
  plain, with a comment and confirmed; the confirmation, in time and too
  late; the lock on the candidate; a private copy next to a lock, next to
  uncommitted changes and next to another private copy; text that is not
  ASCII in a configuration in every format, in a diff, in the output of a
  command, in an error and in a notification; and a session in
  `rfc-compliant` mode.
- **Pinned by replies captured from Junos 26.2 (vJunos-switch) and Junos OS
  Evolved 26.2 (vJunosEvolved):** the error element of Junos, under
  `<rpc-reply>` and inside a result, with and without a message, and what
  is left of it in `rfc-compliant` mode; the errors Junos writes into a
  JSON document; and a configuration with the syslog severity `error`, a
  statement with the name and the namespace of that element.
- **End to end:** the integration suite passes against vJunos-router 26.2,
  vJunos-switch 26.2 and vJunosEvolved 26.2, each reached through an
  OpenSSH jump host, and against netopeer2. On the three Junos platforms
  that includes `junos.Change` committing under the lock and in a private
  copy, a confirmed commit that is confirmed, one that is not and that
  Junos takes back, changes that are refused because somebody else is in
  the way, text that is not ASCII loaded and read back in every format, a
  document from `encoding/json` that reads back as the value it was made
  from, and requests that fail, asked for in XML and in JSON. On the
  switch and on Evolved all of it passes with `rfc-compliant` set as well.
  With notifications configured, on all three, a subscription delivers
  them with their time and content, next to operations on the same session
  where the device answers those, and the comment of a commit arrives on
  the router and on the switch with its letters right. On all
  three the default instance of the ephemeral database takes a load and a
  commit and is read back, and refuses what it does not do. To run the
  suite against a device of yours, see
  [integration/README.md](../integration/README.md).
- **Junos 23.4:** every test of the suite has passed against vJunos-router
  23.4R2-S2.1, reached the same way: with the tests that commit on a
  device as it comes, with them again on one with `rfc-compliant` and
  notifications set, and the test of notifications without
  `rfc-compliant` as well. That was in several runs and not in one. It
  advertises the fifteen capabilities of 26.2, so the same tests run and
  the same are skipped. Two tests met something 26.2 does not do, and
  take either since. For a diff in the ephemeral database, which 26.2
  refuses in so many words, 23.4 answers with a list of errors about
  statements of its configuration file. And in `rfc-compliant` mode a
  command that fails, asked for as text, is answered with nothing, which
  the test of it lets pass and the library cannot see, see below. Chunked
  framing is not to be had there: 23.4 refuses `version-1.1` with a
  syntax error, as its YANG modules say it would, see
  [From one Junos release to the next](#from-one-junos-release-to-the-next).
- **Chunked framing:** Junos speaks `base:1.1` when `rfc-compliant` and
  `version-1.1` are both set under `system services netconf`; with
  `rfc-compliant` alone it advertises `base:1.0` only. Set on
  vJunos-router 26.2, every test of the suite has passed over chunked
  framing, commits and notifications included, in several runs and not in
  one. On vJunos-switch 26.2 and vJunosEvolved 26.2 the tests that commit
  nothing passed, but for three that were written afterwards. A session
  captured from the router that way, its hello and its replies in chunks,
  is replayed in the unit tests. Junos makes a chunk of every line it
  writes, the start tag of the reply first and by itself, and none larger
  than 512 bytes was seen. What it reads is narrower than RFC 6242: it
  ends the session over a chunk of 9800 bytes, where it takes one of 9500,
  and it does not find the end of the chunks when one of its own reads
  ends inside it, which happened for requests of 4098 and of 8193 bytes on
  the wire. The writer keeps clear of both for every device: no chunk is
  larger than 4096 bytes, and where the end of the chunks would fall
  across a multiple of 4096 bytes of the message, the last byte of the
  message goes in a chunk of its own, which moves it. The runs above were
  made before that, with a writer that sent a message as one chunk. With
  the writer as it is, requests of every size around 4096, 8192 and 65536
  bytes are answered by the router, fifteen runs out of fifteen where
  three of twelve had failed, nine more tests of the suite passed there
  as dry runs, and the whole suite passes against netopeer2, which speaks
  chunks. Of those sizes only the ones around 4096 bytes put the end of
  the chunks at a boundary on the wire, where the headers of the chunks
  count too. The test asks for the sizes that put it at each of the three
  boundaries since, and the router answers them all, thirteen runs out of
  thirteen. With the rule of the writer taken out beyond the first
  boundary, six runs of seven ended the session, at requests whose end of
  chunks lies across byte 8192 or byte 65536. The writer counts from the
  beginning of a message, which does not cover a request that is written
  right behind another. With two written together, the router ended the
  session where the end of the chunks of the second began three bytes in
  front of byte 8192, counted from the beginning of the first: in 3 of 40
  tries from a transport that held the two back, and in 3 of 80 from two
  goroutines. Two bytes in front of it that was 1 of 40, and one byte in
  front of it none of 40. So a session sends Junos one request at a time
  over chunks: to a device that advertises the Junos capability a request
  is written when the answer to the one before it has arrived whole, see
  [Requests from several goroutines](sessions.md#requests-from-several-goroutines).
  The beginning of an answer would not do. Junos sends the start tag of a
  reply when it has the start tag of the request, and the end of the reply
  when it has the end tag, before the end of the message was written: seen
  on the router over both framings, with a request written in pieces. With
  one request at a time, 240 pairs of requests from two goroutines at that
  place were all answered, and `TestRequestsAtOnce` of the suite holds a
  session to it there. With it, 27 tests of the suite passed over chunks
  on the router: all but those that commit and the one of notifications,
  which are not configured there.
  Two things do not work over chunks: the formats without indentation, see
  below, and a session started with a command, see
  [A device without a NETCONF service](connecting.md#a-device-without-a-netconf-service).
- **The standard write operations:** against netopeer2 the suite edits
  the candidate under its lock with `rpc.EditConfig`, has it checked with
  `rpc.Validate` and drops it with `rpc.DiscardChanges`; commits an edit
  with `rpc.Commit` and takes it out again; makes a confirmed commit and
  ends it with `rpc.CancelCommit`; edits the running configuration; and
  copies it to startup with `rpc.CopyConfig`. A confirmed commit with a
  token (`Commit.Persist`) is given more time and then confirmed from a
  second session, and another one is cancelled from it. netopeer2 2.8.7
  takes the further confirmed commit with the token in `<persist>` again
  and refuses `<persist-id>` next to `<confirmed/>`, which is the element
  RFC 6241 has there. Against vJunos-router 26.2,
  vJunos-switch 26.2 and vJunosEvolved 26.2 the candidate was edited under
  its lock, checked and dropped, and on the router an edit was committed
  with `rpc.Commit` and taken out with a second. The rest did not run
  there: Junos 26.2 advertises neither `:writable-running` nor `:startup`,
  and `:confirmed-commit` as 1.0. A read with a subtree filter returns
  that subtree on all four.
- **Reports from users of other libraries, looked at on 26.2:** each is a
  test of the integration suite now. Not seen on vJunos-router,
  vJunos-switch or vJunosEvolved: a request whose end-of-message delimiter
  falls across a 4096-byte boundary is answered like any other, at every
  place around three such boundaries, so the writer pads nothing; and the
  JSON Junos writes is JSON, for a syslog match with backslashes, for
  quotes and backslashes in a text and for the output of commands, with
  nothing in front of the document. Not seen on vJunos-router: a confirmed
  commit made with the standard `rpc.Commit` keeps the time it was given,
  and the end of its session does not take it back. While it waits there,
  a dry run of a `junos.Change` from another session sends no commit
  check, fails with `junos.ErrCommitPending` and leaves the commit
  pending. Seen: a commit comment
  of more than 512 bytes is refused, and `<cancel-commit>` is no request
  of Junos 26.2, see below.
- **Not run against a device:** a named instance of the ephemeral database
  from this library, `Synchronize` on a device with two routing engines,
  and a request sent to a routing engine that is not the master, which
  Junos answers with a sentence in an `<output>` and no error: the lab has
  no device with two. Three reports of others could not be looked at
  either. Two are about SRX chassis clusters, of which the lab has none: a
  reply to a commit check with a `<routing-engine>` that is not closed,
  and a load under the lock that changes nothing. The third is an XPath
  filter that Junos is said to want with `source` in place of `select`:
  Junos 26.2 advertises no `:xpath`. Junos `outbound-ssh`, where the
  device opens the connection, was not run either: no lab node has a way
  back to the program. `ncssh.WithDialer` takes such a connection, see
  [A connection of your own](connecting.md#a-connection-of-your-own), and
  the unit tests hold it to that with an ssh server that dials in. Nothing
  in the library reads what a device sends before SSH begins. What Junos does to text that is not ASCII and to the
  escapes of a JSON string, and which requests it answers with its own
  error element, was looked at on 26.2 only.

## What Junos does that the library does not make up for

- A request made with `format="json"` that fails does not always come back
  with the error. Junos writes most errors into the document, and
  `junos.JSON` returns those as an error. A request for an interface that
  is not there is answered by Junos 26.2 with a result without data, and
  by 23.4 as well.
- In `rfc-compliant` mode Junos 23.4 answers a request for text that
  fails with a reply that has nothing in it. `Command.Exec` then returns
  no output and no error: seen for an interface that is not there, as a
  command and as `<get-interface-information>`, on vJunos-router
  23.4R2-S2.1. A command that prints nothing, `show route table inet.3`
  there, gets the same reply, so nothing in it tells the two apart.
  Without the mode the error is in the reply as text, and `Command.XML`
  gets it as an `<rpc-error>` in both modes. A command with a syntax error is an
  `<rpc-error>` in both modes too. On 26.2 the test for this has passed
  in that mode, on the router, the switch and Evolved.
- `| match` and other filters after a `Command` are not applied.
- A line break or a tab in a JSON string is not loaded as one. JSON can
  only write it as an escape, and Junos stores `\n` as a backslash and an
  `n`.
- A control character in a statement does not come back. Junos 26.2 takes
  it, as a byte and as a character reference, and answers with `&#167;` in
  its place when the configuration is read, so it cannot be told from a
  section sign afterwards. U+0000 ends the value.
- After an `<edit-config>` that Junos 26.2 refuses for a syntax error in
  its `<config>`, the next load of set commands on the session fails with
  "internal communications error (tag 'configuration-set') expecting
  configuration-text" and loads nothing. The load after that one works. A
  load in another format, an `<edit-config>` that Junos takes or a
  `<get-configuration>` in between clears it, and a commit check does not.
  Seen on vJunos-switch and vJunosEvolved, each time in a private copy.
  `junos.Change` sends no `<edit-config>`, so it meets this only on a
  session on which an `rpc.EditConfig` was refused before.
- Over chunked framing Junos 26.2 does not finish a reply in a format
  without indentation, `junos.FormatXMLMinified` and
  `junos.FormatJSONMinified`: it sends the start of the reply and stops,
  for a single statement as for the whole configuration, and the session
  waits. `WithMessageTimeout` ends such a session with `ErrMessageStalled`.
  Over end-of-message framing, on the same device in the same mode, both
  formats work. Seen on vJunos-router, vJunos-switch and vJunosEvolved.
  `GetConfiguration.Exec` refuses the two formats on a session with chunked
  framing and sends nothing, which keeps the session. It goes on refusing
  on a release that finishes these replies, and a request sent with
  `Session.ExecReply` meets the device as it is.
- Junos 26.2 has no `<cancel-commit>`. It advertises
  `:confirmed-commit:1.0`, which has none, and answers `rpc.CancelCommit`
  with a syntax error that `junos.ErrUnsupported` matches, written as a
  pair of tags or as an empty element. A commit or a commit check from
  any session confirms a confirmed commit, whichever operation made it.
- With `rfc-compliant` and `version-1.1` set, Junos 26.2 sends its hello
  only when it has the hello of the client, where RFC 6242 has both sides
  send at once. `Open` sends first, so it does not notice; a client that
  waits for the device waits for good.

## Warnings in `rfc-compliant` mode

Juniper's documentation says that in `rfc-compliant` mode a warning that
would come next to an `<ok/>` is not sent. Junos 26.2 and Junos OS Evolved
26.2 sent the three that were tried the same with and without the mode,
and they are in `reply.Warnings` either way: the warning of a
`<synchronize/>` on a device with one routing engine, next to the `<ok/>`
of a commit check; that of a private copy opened while somebody has the
lock; and that of a load which deletes what is not there.

## From one Junos release to the next

Juniper publishes the YANG modules of its releases at
[github.com/Juniper/yang](https://github.com/Juniper/yang), and a device
hands out those of the release it runs. `make junos-schema` compares two of
them with what the library depends on, for every release of the `junos`
family, of `junos-ex` and of Junos OS Evolved; `JUNOS_FAMILIES` names other
families. From `junos-conf-system` it lists the statements under `system
services netconf` and what each release adds to them or takes away. In
`junos-rpc-system` it looks for the elements that package `junos` reads
from the replies to `<get-system-information>` and
`<get-commit-information>`, and fails when a release does not declare one
of them. The workflow `junos-schema.yaml` runs it once a week.

The integration suite reads the same two modules off a device with
`<get-schema>` and keeps them in the directory that `NETCONF_DUT_YANG`
names. Given that directory, the comparison takes them for the release they
say they are of:

```bash
NETCONF_DUT_SSHHOST=switch1.example.net NETCONF_DUT_FLAVOR=junos \
NETCONF_DUT_YANG=/tmp/switch1 GOFLAGS='-run=^TestJunosSchema$' make integration-test
make junos-schema JUNOS_YANG_MORE=/tmp/switch1
```

What it says of the releases Juniper has published, 17.4R1 to 25.4R1 and
for Evolved 23.4R1 to 25.4R1, of 23.4R2-S2.1 as a vJunos-router hands it
out, and of 26.2 as vJunos-router, vJunos-switch and vJunosEvolved do:

- Every release that declares the two replies declares all the elements
  read from them. That is every release from 23.1R1 on; the ones before
  say `anyxml` for both replies and nothing of what is in them.
- `rfc-compliant` is a statement of every release. `version-1.1`, which
  chunked framing needs next to it, is one from 24.4R1 on, on Evolved as
  well. `notification` with `interleave` is one from 19.2R1 on, in
  `junos-ex` from 20.4R1, and `tls` from 20.2R1.
- 26.2 has the statements of 25.4R1 under `system services netconf`, no
  more and no fewer.
- vJunos-router and vJunos-switch 26.2 hand out the same two modules, byte
  for byte.
- 23.4R2-S2.1 on a device has the statements Juniper publishes for
  23.4R2, which are those of 26.2 without `version-1.1`, and declares all
  the elements read from the two replies.

A device was asked whether it takes what its module declares:
vJunos-router 23.4R2-S2.1, with statements loaded one at a time into a
private copy that was closed again. It refuses `version-1.1` with a syntax
error, which its module does not have. Of twelve statements the module
does have it takes eleven, `rfc-compliant`, `notification interleave` and
`flatten-commit-results` among them. It refuses `yang-modules
emit-family-ns-and-module-name` with a syntax error, which is the
statement its module has had since 23.4R1.

That is a part of what the library depends on. The other requests of
package `junos` are in none of the 166 modules of operational requests
that Juniper publishes for the `junos` family of 25.4R1:
`<get-configuration>`, `<load-configuration>`, `<commit-configuration>`,
`<open-configuration>`, `<close-configuration>`, `<command>` and
`<get-database-status-information>`. YANG has no
attributes, so the `seconds` of a `<date-time>` are not in it either. And a
statement that a release declares says nothing of what a device does with
it. For all of that there is the integration suite, run against the
release. Against 23.4R2-S2.1 it found the two things at the top of this
page, a diff in the ephemeral database and a command that fails in
`rfc-compliant` mode, neither of which a module shows. Its `TestSSHDial`
logs the capabilities a device advertises, which is the first thing to
compare. vJunos-router 23.4R2-S2.1 and vJunos-router, vJunos-switch and
vJunosEvolved 26.2 advertise the same fifteen: `base:1.0`,
`:candidate:1.0`, `:confirmed-commit:1.0`, `:validate:1.0` and `:url:1.0`
with the schemes http, ftp and file, each of the five as
`urn:ietf:params:netconf:` and as `urn:ietf:params:xml:ns:netconf:`,
`ietf-netconf-monitoring`, `ietf-yang-metadata`, and three of Juniper,
`http://xml.juniper.net/netconf/junos/1.0` among them.
