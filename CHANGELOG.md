# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

First release line of the fork of `nemith.io/netconf` at commit `e745332`.

### Changed

- Module path is `github.com/lasseh/netconf`.
- `NewSession(transport, ...)` is `Open(ctx, transport, ...)`. The context
  bounds and cancels the hello exchange, so one deadline covers connecting
  and opening a session; without a deadline 30 seconds apply.
  `WithHelloTimeout` and `ErrHandshakeTimeout` are gone.
- Replies are buffered. `Session.Do` is gone: `Session.Exec` and
  `Session.ExecReply` issue an operation and check its reply for RPC errors.
  `Session.DoStream` is `Session.Stream`, for replies too large to buffer.
  All three take the operation itself; pass a message built with `NewRPC`
  to put attributes on `<rpc>`. `Session.Prepare` is gone.
- The session sends every operation with a message-id of its own. One set
  in `RPC.MessageID` was sent as it was, and an operation that reused one,
  or met the next number of the session, failed while the first waited for
  its reply, or got that reply when the first had been given up on.
- Operations started after `Session.Close` was called fail with `ErrClosed`.
- An operation on a dead session returns an error that wraps both
  `ErrClosed` and the cause; compare with `errors.Is`.
- An `<rpc-error>` anywhere in a reply fails the operation, not only one
  directly under `<rpc-reply>`, and so does one without a severity. It is
  found in any namespace: Junos writes some errors inside a result element
  whose namespace they inherit.
- `RPCError.Error` names the path and the bad element when the device gave
  them, and says "netconf warning" for a warning. The fields of a decoded
  `RPCError` no longer carry the white space devices pad them with.
- One option for notifications: `WithNotifHandler` takes a function. The
  `NotifHandler` interface, `NotifHandlerFunc` and `WithNotifHandlerFunc`
  are gone.
- Notifications waiting for the handler are bounded: 10,000 notifications
  or 64 MiB by default. A handler that falls further behind ends the
  session with `ErrNotificationOverflow`; `WithNotifQueue` changes the
  bounds or drops instead. The queue was unbounded.
- `transport.Transport` includes `Upgrade`; the optional `Upgrader` is gone.
  A transport that embeds a `Framer` already has it.
- `rpc.Commit` has a field for each of the two elements that carry the
  token of a confirmed commit. `Persist` is sent as `<persist>` and needs
  `Confirmed`. `PersistID` is always sent as `<persist-id>`: it was sent as
  `<persist>` when `Confirmed` was set, so `Commit{Confirmed: true,
  PersistID: x}` still compiles and now sends the other element. Code that
  starts a confirmed commit with a token sets `Persist`. A further
  confirmed commit that names the first one, which gives it more time,
  could not be sent before.
- `rpc.Commit.ConfirmTimeout` is a `time.Duration` (it was a number of
  seconds), `rpc.KillSession.SessionID` a `uint64` like
  `Session.SessionID`.
- Removed because nothing used them: `RPCReply.RPCErrors` and
  `RPCErrors.Filter` (RPC errors are returned as the error), `rpc.OkReply`,
  `rpc.GetReply`, `Hello`, `ExpandCapability`.
- `ErrClosed` reads "netconf: session closed".
- `WithOperationTimeout` bounds an operation whose context has no deadline.
  A context with a deadline is taken at its word: it used to be cut off at
  the bound of the session when its own deadline was later, so no single
  operation could be given more time than the others. A context with a
  late deadline now takes the bound off. So do the deadlines the library
  sets itself: `Session.Close` has its 30 seconds whatever the bound is,
  and `junos.Change.Apply` gives the candidate back within a minute and
  confirms a confirmed commit within the time to confirm.
- `rpc.CreateSubscription.Exec` fails with `ErrNoNotifHandler`, before
  anything is sent, on a session that was opened without
  `WithNotifHandler`. It used to succeed, and the session then ended with
  "ignored 100 messages in a row" once the device had sent that many
  notifications.
- Every error of an operation of `rpc` and `junos` begins with the name of
  the operation: `lock: netconf error: protocol lock-denied: ...`. The name
  is wrapped around the error, so `errors.Is` with a tag and `errors.As`
  with `netconf.RPCErrors` or `netconf.RPCError` work as before, and a type
  assertion such as `err.(netconf.RPCErrors)` no longer matches: use
  `errors.As`. `rpc.ExecOK` names every error it returns, not only a reply
  without `<ok/>`. The errors of `junos.Change.Apply` name the step, as
  before, and not the operation as well.
- `CapabilitySet.Has` ignores capability parameters unless they are given.
- `rpc.EditConfig` and `rpc.SubtreeFilter` marshal a struct payload as a
  child element (previously its fields replaced the wrapper element);
  `rpc.EditConfig` without a config is an error.
- `rpc.CopyConfig` and `rpc.Validate` take typed `Source`/`Target` values.
- `testutil` moved to `internal/`; `inttest` is now `integration` with the
  `integration` build tag.

### Added

- `ncssh.DialHost` with key file, agent, password, ssh config, known_hosts
  and keepalive options; `Transport.Close` is idempotent.
- Jump hosts: `ncssh.WithProxyJump`, and `ProxyJump` from the ssh config.
  A jump host is logged in to the way `ssh` does it, with its own user,
  port and keys. It is never offered the device's password or key file and
  its host key is always verified.
- `ncssh.WithSSHConfig` honours `UserKnownHostsFile`: the known hosts of a
  host, device or jump host, are looked for where the ssh config says,
  unless `WithKnownHosts` is given.
- `Session.Done`, `Session.Err`, `Session.ExecReply` and `Reply`.
- `WithOperationTimeout`: a bound for every operation on a session.
- `Reply.Element` and `Reply.Elements` find a result element by name at any
  depth, so the same code reads a Junos reply with and without its
  routing-engine wrapper. A missing element is `ErrElementNotFound`.
- `RPCError.Detail` returns any element of an rpc-error: the RFC 6241
  error-info elements and those a device adds, such as Junos'
  `source-daemon`.
- `errors.Is(err, netconf.ErrLockDenied)` and the other tags work.
- `Session.ExecReply` returns the reply together with its RPC errors, so
  the warnings and data of a failed operation can still be read.
- `Notification.Content`: the element of a notification that says what
  happened, with its name and a `Decode` for a struct of one's own.
  `Notification` only had the time of the event. `Decode` reads the
  element as the device sent it, whatever is done with `Inner` afterwards.
- `WithMaxMessageSize`, default 256 MiB, and `ErrMessageTooLarge`.
- `ErrNoNotifHandler` and `Session.HasNotifHandler`.
- Chunked framing writes chunks of at most 4096 bytes, and where the end
  of the chunks would fall across a multiple of 4096 bytes of the message
  it puts the last byte of the message in a chunk of its own. A message
  used to go out as one chunk for each write. Junos 26.2 ends the session
  over a chunk of 9800 bytes, and does not find an end of the chunks that
  one of its reads cuts in two.
- Over chunked framing to a device that advertises the Junos capability, a
  session writes a request when the answer to the one before it has
  arrived whole. Requests from several goroutines otherwise go out as they
  come, and Junos 26.2 ended the session in some tries when one was
  written right behind another and the two together put the end of the
  chunks of the second across byte 8192. A request whose context ends
  while it waits for its turn is not sent.
- `junos.CommitConfiguration` and `junos.Change` refuse a comment of more
  than 512 bytes before anything is sent. Junos 26.2 refuses the commit
  for it, which a `Change` found out after its load.
- `junos.ErrPermissionDenied` and `junos.ErrUnsupported`, targets for
  `errors.Is` on the error of any operation: a request, a command or a
  part of the configuration that the login class of the user does not
  permit, and a request the platform does not have. Junos has no error tag
  for either. `RPCError.Is` asks a target that has a method
  `MatchRPCError(RPCError) bool`, and `RPCError.Request` is the name of the
  request an error answers.
- Package `transport/command`: NETCONF over the standard input and output
  of a command, for the `ssh` of the system and what it can do that the
  ssh client of `ncssh` cannot. `Close` kills the process, and a command
  that exits is in the error with its exit status and its last words on
  standard error, also when it exits inside a message that the session
  only reads to get past it. A child that the process left behind does not
  hide the exit: the status is waited for as long as `cmd.WaitDelay` gives
  a child that holds the standard error, and with the standard output held
  the output is over once nothing has come for a second after the process
  was waited for.
- `ncssh.WithCommand`: start NETCONF with a command, sent as an exec
  request, in place of the netconf subsystem. For a Junos device without
  a NETCONF service, which refuses the subsystem on port 22 and takes
  `xml-mode netconf need-trailer`.
- `ncssh.WithDialer`: a function that makes the first connection of
  `DialHost`, for a connection a device opened towards the program, a
  source address or a proxy. Host key check, login, keepalive and close
  are as on a connection `DialHost` made. A function that returns neither
  a connection nor an error fails the dial, and a connection that came
  with an error is closed.
- `ncssh.WithSigner`: public key authentication with keys that are no
  files, one from a vault or a certificate signed for the session. They
  are offered to the device together with key files and the agent, and
  never to a jump host. Given no signer it still names the login, and
  nothing is tried in its place.
- `ncssh.WithHostKey`: the host keys a device has to present one of, for a
  program that has no known_hosts file and knows the keys from its
  inventory. It had to switch host key checking off. Given no key it
  fails the dial, whatever else was said about the check.
- `ncssh.ErrAuth`: a dial that a device or a jump host refused the login of
  fails with it, with the user, the host and what was offered, where it
  had the text of the ssh client only. `ncssh.ErrSubsystemRefused`: the
  device let the user in and refused the netconf subsystem.
- `ErrHello` and `HelloError`: a hello exchange that fails says so in a way
  a program can ask for, with the capabilities, the RPC errors and the
  plain text the device sent where its hello belongs. A connection that
  ends before a hello says what that usually means. An exchange that its
  context ended has what the device sent until then, and so has one in
  which the hello of the client could not be written, to a device that
  had refused and hung up. Such a device is given a second to say why.
- `WithTrace` with `Trace`, `OperationInfo` and `OperationResult`:
  functions that the session calls when an operation starts and when it
  ends, with its name, the request, the duration, the size of the reply
  and the error, and for every notification. For timing, counting and
  logging, also of the operations inside `junos.Change.Apply`. A function
  must not call `Close` on the session and must not panic.
- `WithMessageTimeout` ends a session whose device began a message and
  then sent nothing more of it for the given time, with
  `ErrMessageStalled` in `Session.Err`. Without it such a session waits for
  ever, with `Done` never closed. Off by default: Junos begins a reply at
  once and finishes it when the work is done, so the time has to be longer
  than the slowest operation.
- `SyntaxError`: a reply, an element or a notification that is not XML
  fails with where it is not, as a byte offset. The bytes around that
  place are in its `Around` field and not in its text, which goes where an
  error is logged; what the parser names of it, a character or a name, is
  still in the text. The `*xml.SyntaxError` of the parser, which has a line
  number only, is underneath. `Session.ExecReply` returns the reply together with
  such an error, where it returned none, and the RPC errors that stand in
  front of that place are in the error, where they were lost.
- `WithReplyRepair` runs a function on every buffered reply and
  notification before the session reads it, and `StripInvalidChars` is one
  that takes out what XML does not allow: control characters, bytes that
  are not UTF-8 and references to either. For devices that put such a
  character into a reply, which fails every way of decoding it.
- `Element.Attr` and `NotificationContent.Attr`: the attributes of the
  start tag, with the namespace a prefix stands for, such as the
  `junos:commit-seconds` of a `<configuration>` and the `junos:seconds` of
  a time. `reply.Element("rpc-reply").Attr` is the attributes of the
  envelope.
- `ncssh.WithClientConfig`; password authentication also answers a
  keyboard-interactive exchange that asks for the password only.
- `ncssh.ErrKeepalive`: a transport that its keepalive closed fails reads
  and writes with it, so the session says in `Session.Err` that the
  keepalive ended it and how many probes went unanswered, also when the
  device went silent inside a message that the session only reads to get
  past it. It used to end with "unexpected EOF".
- Package `junos`:
  - reading: `GetConfiguration` (xml, text, set, json, and xml and json
    without indentation, which it refuses over chunked framing, where
    Junos 26.2 does not finish such a reply; inherited groups),
    `CompareRollback`, `GetSystemInformation`, `Command` with text, JSON and
    XML output, and `GetCommitInformation`, the commit history as values:
    sequence number, user, client, time, comment, what Junos notes on a
    confirmed commit, and whether one still waits for its confirmation.
    `FindCommit` looks a commit up in it by a token that stands by itself
    in its comment, which is how to ask what became of a commit after
    `ErrCommitUnknown`.
    `GetDatabaseStatusInformation` lists who has the configuration open
    for editing: users of the CLI in configuration mode and NETCONF
    sessions with the lock or a private copy
  - changing: `LoadConfiguration` (set commands, text, XML, JSON; merge,
    override, replace, update; a JSON document goes out with the escapes
    Junos does not read, such as the `\u0026` of `encoding/json`, written
    out as the characters), `CommitConfiguration` (check, comment,
    confirmed with a `time.Duration`, synchronize, full), `Rollback`,
    `OpenConfiguration` and `CloseConfiguration` for a private copy or an
    ephemeral database. `LoadConfiguration` also loads a patch, the output
    of a compare (`LoadPatch`), and a file that Junos reads itself, by path
    or URL (`URL`); `LoadRescue` loads the rescue configuration
  - a whole change in one call: `Change.Apply` takes the lock or a private
    copy, loads, compares, commits and gives the candidate back on every
    way out, after putting it back to the active configuration if the
    change did not get that far. It can stop at a commit check (`DryRun`),
    ask a `Review` function with the diff, and make the commit a confirmed
    one with a `Verify` function before the confirmation.
    `ErrNotConfirmed` and `ErrCommitUnknown` say where a change that did
    not finish has left the device, `ErrCandidateModified` that the
    candidate was not to be had because it holds changes that nobody has
    committed, which also matches that refusal on the error of `rpc.Lock`
    and of `OpenConfiguration` and of no other request, `ErrCommitPending`
    that a dry run sent no
    commit check
    because Junos would take it for the confirmation of a confirmed commit
    that is pending. With `LockWait` a change waits for a lock that
    another session holds, where it gave up on the first refusal, and
    asks for none once the time is up.
  - replies: `Decode` finds a result with or without a routing-engine
    wrapper and trims the padding off every value, `Text` and `JSON` read
    the output of requests made with `format="text"` and `format="json"`,
    `IsJunos`
- `transporttest.StreamPeer` and `transporttest.Relay`, a ready-made peer
  for transports on a byte stream and a way to make it go silent. The
  conformance suite checks close against a silent peer and now runs against
  the ssh and tls transports too.

### Fixed

- The known hosts were asked with the address of the connection next to
  the name of the host, and a connection whose address is no host and
  port, a unix socket, failed the check over that. A host is looked up by
  the name it was dialed under.
- A Junos user whose login class permits nothing got "expected element
  type <hello> but have <rpc-reply>" from `Open`: Junos puts plain text
  and two replies with errors in front of its hello for such a user. `Open`
  reads past them and returns the errors in a `HelloError`.
- `Element.Decode` failed for a struct that asks for a namespace when the
  element had that namespace from an element around it, as its default
  namespace or through a prefix: the piece cut out of the reply no longer
  declared it. The namespaces declared around an element are in force when
  it is decoded. A field `XMLName xml.Name` without a tag now gets the
  namespace the element had in the reply, where it got none.
  `NotificationContent.Decode` does the same with what the
  `<notification>` declares, and has the attributes of the content, which
  it dropped, for a `Notification` that `Message.Decode` filled.
- Text that is not ASCII read from a Junos device. Junos sends every byte
  above 127 as a character reference of its own, which an XML parser reads
  as one character per byte: `ø` came out as `Ã¸`, in a configuration, in
  the output of a command and in an error. The session puts such references
  back together in the replies and notifications it buffers from a device
  that advertises the Junos capability, so `Reply.Raw` is no longer byte
  for byte what such a device sent. `Session.Stream` returns the reply as
  sent.
- An operation hanging forever when the connection dropped while its reply
  was read.
- Session wedge when a reply was abandoned, panic when an operation raced
  `Close`.
- Spurious warnings on every `Close` against Junos.
- An operation that failed to encode still sent a frame to the device.
- `<kill-session>` encoding; `partial-lock` XPath escaping.
- Truncated chunked messages accepted as complete; read after end of a
  chunked message consuming the next one.
- A session left unusable when closed under an unclosed streamed reply.
- `nctls.Dial` verifies the certificate against the dialed host instead of
  requiring `ServerName` or `InsecureSkipVerify`.
- A message that base:1.0 framing cannot carry (it contains `]]>]]>`, or
  ends in a way that would complete it early) is refused before any of it
  is sent; previously the device received an empty or truncated frame.
- Rpc attribute names are validated (they were written verbatim).
- Frames without an XML element are skipped instead of ending the session;
  an rpc-reply without message-id answers the only operation that was
  sent and has no reply yet.
- Notifications queued when the session closes are dropped, not delivered
  late.
- A failed write ends the session and reports `ErrClosed` like every other
  operation on it, instead of a bare transport error.
- The size limit also covers messages the session ignores and the hello;
  a session ends after 100 ignored messages in a row.
- `rpc.Get`, `rpc.GetConfig` and the `junos` operations fail on a reply
  without the element they expect instead of returning an empty result.
- Closing an ssh transport no longer waits for the device. `Close` blocked
  for ever when a device went silent during a large write, so neither a
  keepalive nor `Session.Close` could end the session. With
  `ncssh.NewTransport` a blocked read was never released against a silent
  device, which also defeated the hello timeout.
- `ncssh.Dial` no longer leaves a callback on its context when it fails.
- `ncssh.DialHost` said "no home directory" and "the current user is
  unknown" without what the system had to say about either. Both errors
  carry that now, and the current user is looked up only when no user is
  given.
- A dial that its context ended returned the error of the context and
  nothing else. It still matches that error, and now says which step it
  interrupted and for which host: connecting, the ssh handshake, the way on
  from a jump host, or starting the netconf subsystem.
- `Session.Err` and the error of operations on an ended session report the
  failed write that ended it, not the read that failed once the transport
  was closed in response.
- Decoding a streamed reply into `rpc.GetConfigReply` reports the device's
  rpc-error instead of a missing `<data>`.
- `rpc.PartialLock` fails on a reply without a lock id.
- `rpc.EditConfig` given an `rpc.Config` sent it wrapped twice.
- `rpc.CreateSubscription` checks its times when it is encoded, so also
  when it is sent with `Session.ExecReply`.
- Warnings in a successful reply were dropped by `Session.Exec` and with it
  by every typed operation. They are logged now; `Session.ExecReply`
  returns them.
- `Session.Close` returned nil when the device never acknowledged
  `<close-session>`. It still closes the session and now returns the
  context's error.
- `Message.Close` raced with a `Read` in another goroutine.
- `rpc.ExecOK`, and so every typed operation that expects `<ok/>`, accepts
  it at any depth. Junos nests it in `<load-configuration-results>`.
- `ncssh.DialHost` never offered the keys of the ssh-agent once a key file
  had loaded, so `WithPrivateKeyFile` with `WithAgent`, or an `IdentityFile`
  from the ssh config next to an agent, failed against a device that only
  knew the agent's key.
- `ncssh.DialHost` waited past its context for an ssh-agent that did not
  answer: one that never lists its keys, or lists a key and holds the
  signature back until somebody confirms the key or touches it. The end of
  the context ends the dial.
- Nothing read the standard error of the netconf subsystem. What a device
  wrote there stayed in the window of the channel, and a device that had
  filled it could send no more replies. It is read and dropped.
- A session did not end after a malformed chunk header when the device then
  went quiet: it waited for the rest of the message. Operations hung until
  their context expired and `Done` never fired.
- An operation whose context was already over could still be sent.
- Late replies to operations the caller had stopped waiting for counted as
  unsolicited messages, and a hundred in a row ended the session.
- A message refused by base:1.0 framing after its caller had left was
  counted as awaiting a reply, which made the session ignore a later reply
  without message-id.
- `junos.Command` returned an empty string for a command that failed: Junos
  reports the error of a text request as plain text in the reply, without
  an rpc-error. It is returned as an error now.
- A request that Junos answers with its own error element read as one that
  had succeeded and found nothing. Junos writes `<xnm:error>` where an
  `<rpc-error>` belongs for errors of the routing process, and Junos OS
  Evolved does for an interface that is not there, inside the result. The
  element fails the operation like an `<rpc-error>` and is returned as an
  `RPCError` with its message. `RPCError.Error` names the process that
  reported an error without a message.
- `junos.JSON`, and with it `Command.JSON` and `GetConfiguration` in JSON,
  returned the document of a request that failed as its result. Junos
  writes most errors into the document, and those are returned as an error
  now.
- `junos.CommitConfiguration`, `junos.LoadConfiguration` and
  `junos.Rollback` took every reply without an error for a success. Junos
  has replies that are neither: a commit from a private copy whose change
  no longer fits, because somebody else committed another value for the
  statement, is answered with a warning and commits nothing. `junos.Change`
  reported that as committed. The three now fail unless Junos answers
  `<ok/>`, and the error holds what Junos said.

### Tests

- Fuzz targets for both framings, the session receive path, the reply
  parser, the Junos reply extraction, the text of a Junos device and the
  JSON of a load; `make fuzz` runs them.
- The integration suite can load into a private copy of the candidate
  that it never commits (`NETCONF_DUT_CHANGE_DRYRUN`): `junos.Change` as a
  dry run, and text that is not ASCII read back in every format. Off by
  default, the suite stays read-only.
- The integration suite subscribes to notifications on a device that
  advertises them, and reads the comment of a commit from one on Junos.
- The integration suite loads, commits and reads back the default instance
  of the Junos ephemeral database, with the tests that commit.
- The netopeer2 image of the integration suite is the newest Alpine with
  the latest releases of libyang, libnetconf2, sysrepo and netopeer2. It was
  Alpine 3.16 with releases from 2022.
- The integration suite can commit on a lab device
  (`NETCONF_DUT_CHANGE_COMMIT`): `junos.Change` under the lock and in a
  private copy, confirmed and not confirmed, and refused because somebody
  else is in the way. Its traffic is logged only with `NETCONF_DUT_TRACE`.
- Chunked framing has run against Junos: 26.2 on vJunos-router with the
  whole integration suite, on vJunos-switch and vJunosEvolved with what
  commits nothing. That was with the writer that sent a message as one
  chunk; since the change to its chunks, the test of request sizes and
  nine others have run over chunks on the router, and the whole suite
  against netopeer2. Over chunks the test of request sizes asks for the
  sizes that put the end of the chunks at each boundary on the wire, and
  the router answers them all. A session captured from the
  router is replayed through the framer and through a session in the unit
  tests. On a session with
  chunked framing the suite holds `GetConfiguration` to refusing
  configuration without indentation, and asks the device for it with a
  message timeout, as Junos 26.2 does not finish that reply,
  and over `NETCONF_DUT_SSHCOMMAND` with `need-trailer` it offers
  `base:1.0` only. `make integration-test` gives the suite 30 minutes: with
  the tests that commit it takes longer than the 10 of `go test`.
- Over chunked framing the integration suite makes pairs of requests at
  the same time, of the sizes that ended a session on Junos 26.2 when the
  two were written one right behind the other, and holds a session to a
  Junos device to one request at a time.
- The integration suite holds Junos to what it did with five reports from
  users of other libraries: requests of sizes around a 4096-byte boundary,
  the standard confirmed commit and `<cancel-commit>`, JSON with
  backslashes and quotes in it, and the length of a commit comment.
- The integration suite runs the standard write operations: `<edit-config>`
  on the candidate under its lock with `<validate>` and
  `<discard-changes>`, `<commit>`, a confirmed commit ended with
  `<cancel-commit>`, `<edit-config>` on the running configuration,
  `<copy-config>` to startup, and a read with a subtree filter. Each test
  is skipped on a device that does not advertise what it needs. The
  netopeer2 image has access control off for them, and its compose file
  sets `NETCONF_DUT_CHANGE_COMMIT`: the flavor `netopeer2` opens no write
  gate by itself. Where
  `:confirmed-commit:1.1` is advertised, a confirmed commit with a token is
  given more time and confirmed from a second session, and another one is
  cancelled from it.
- `make junos-schema` compares the YANG modules that Juniper publishes for
  every Junos release with what the library depends on. It lists the
  statements under `system services netconf` release by release, and fails
  when a release does not declare an element that package `junos` reads
  from a reply. A workflow runs it once a week. The integration suite reads
  the same modules off a device, kept with `NETCONF_DUT_YANG`, and logs the
  capabilities a device advertises.
- The integration suite has run against Junos 23.4, a vJunos-router on
  23.4R2-S2.1: with the tests that commit, with notifications, and in
  `rfc-compliant` mode. Two of its tests take what that release answers
  as well: errors about its configuration file for a diff in the ephemeral
  database, and a reply with nothing in it for a command that fails, asked
  for as text in `rfc-compliant` mode.

### Documentation

- Every package has a package comment and every exported identifier is
  documented; the linter now enforces it. Corrected the RFC sections cited
  for `rpc.Commit` and `rpc.KillSession`.

### Performance

- End-of-message framed replies are read in bulk (about 60 times faster).
