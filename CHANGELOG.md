# Changelog

This file lists the notable changes to `github.com/lasseh/netconf`. It
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Unreleased

### Fixed

- A session closes the reader of a streamed reply once. It closed it
  twice, which ended the session on a transport that refuses a second
  `Close` of a message reader.

## v0.1.0 - 2026-10-19

These changes make up the first release of the fork of `nemith.io/netconf`
at commit `e745332`. Each entry describes how the fork differs from
`nemith.io/netconf` at that commit.

### Changed

- Module path is `github.com/lasseh/netconf`.
- Requires Go 1.26 or newer. `nemith.io/netconf` at `e745332` required
  Go 1.25.
- `Open(ctx, transport, ...)` replaces `NewSession`. Its context bounds
  and cancels the hello exchange, so one deadline covers connecting and
  opening a session. Without a deadline the exchange is bounded by
  `DefaultHelloTimeout`, 30 seconds.
- `Open` fails when the capabilities given to `WithCapability` include no
  base capability, and when the device shares no base capability with the
  client.
- Replies are buffered. `Session.Exec` and `Session.ExecReply` read the
  whole reply into memory and check it for RPC errors. A reply larger than
  `WithMaxMessageSize`, 256 MiB by default, ends the session.
  `Session.Stream` returns a reply as a stream, for replies too large to
  buffer. All three take the operation, either a value that marshals to
  its XML or the literal XML as a string or `[]byte`, and wrap it in
  `<rpc>` themselves. To put attributes on `<rpc>`, pass a message built
  with `NewRPC`.
- The session assigns the message-id of every operation itself.
  `nemith.io/netconf` sent a message-id set in `RPC.MessageID` unchanged.
  An operation that reused a message-id, or that collided with the next
  one the session assigned, failed while the first operation waited for
  its reply, or received that reply after the first operation was
  abandoned.
- Operations started after `Session.Close` was called fail with
  `ErrClosed`.
- An operation on a session that has ended returns an error that wraps
  both `ErrClosed` and the cause. Compare with `errors.Is`. When a failed
  write ended the session, the cause is that write, not the read that
  failed once the transport was closed.
- `ErrClosed` reads "netconf: session closed".
- `Session.Close` applies `DefaultCloseTimeout`, 30 seconds, when its
  context has no deadline. `nemith.io/netconf` waited for the reply to
  `<close-session>` for as long as the context allowed.
- An `<rpc-error>` anywhere in a buffered reply fails the operation, not
  only one directly under `<rpc-reply>`, and so does one without a
  severity. It is found in any namespace, because Junos writes some errors
  inside a result element whose namespace they inherit.
- `RPCError.Error` names the path and the bad element when the device
  gives them, and says "netconf warning" for a warning. Decoding an
  `RPCError` trims the white space that devices pad its fields with.
- `NotifHandler` is the function type `func(context.Context, *Message)`,
  and `WithNotifHandler` takes one.
- Notifications are buffered and handed to the handler one at a time, in
  the order received. `nemith.io/netconf` read no further message until
  the handler had returned or closed the notification. A handler no longer
  holds up replies, and it may issue operations on the session without
  closing the message first. By default the queue in front of a busy
  handler holds at most 10,000 notifications and 64 MiB, the values of
  `DefaultNotifQueueMessages` and `DefaultNotifQueueBytes`. A handler that
  falls further behind ends the session with `ErrNotificationOverflow`.
  `WithNotifQueue` takes a `NotifQueue` that changes the bounds or, with
  `Drop`, drops a notification that does not fit instead of ending the
  session. Notifications still waiting when the session ends are dropped.
- `rpc.CreateSubscription.Exec` fails with `ErrNoNotifHandler`, before
  anything is sent, on a session that was opened without
  `WithNotifHandler`. `nemith.io/netconf` sent the subscription, then
  logged and dropped every notification. `Session.HasNotifHandler` reports
  whether a session has a handler.
- A session skips a message it has no use for: one without an XML element,
  one whose element is neither `<rpc-reply>` nor `<notification>`, a reply
  that no operation waits for, and a notification on a session without a
  handler. After 100 such messages in a row the session ends. A late reply
  to an abandoned operation does not count. `nemith.io/netconf` ended the
  session over the first two kinds and skipped the others without a limit.
- `transport.Transport` has an `Upgrade` method. `nemith.io/netconf`
  called `Upgrade` only on a transport that had one, so a transport of
  your own needs it now. A transport that embeds a `Framer` has it
  already. A transport that does not frame messages on a byte stream
  implements it as a method that does nothing.
- Chunked framing writes chunks of at most 4096 bytes. When the
  end-of-chunks marker would straddle a 4096-byte boundary of the message,
  the writer sends the last byte of the message in a chunk of its own.
  `nemith.io/netconf` wrote one chunk for each write. Junos 26.2 ends the
  session when it receives a chunk of 9800 bytes, and misses an
  end-of-chunks marker that one of its reads splits in two. The chunks
  go to the transport in writes of 32768 bytes, a multiple of 4096, not
  one write per chunk.
- Over chunked framing to a device that advertises the Junos capability,
  the session writes a request only after the reply to the previous one
  has arrived whole. On every other session, requests from several
  goroutines go out as they come. Junos 26.2 ended the session in some
  runs when one request was written right behind another and the two
  together put the end-of-chunks marker of the second across byte 8192. An
  operation whose context ends while it waits for its turn is not sent.
- `rpc.Commit` has a field for each of the two elements that carry the
  token of a confirmed commit. `Persist` is sent as `<persist>` and needs
  `Confirmed`. `PersistID` is always sent as `<persist-id>`.
  `nemith.io/netconf` sent it as `<persist>` when `Confirmed` was set, so
  `Commit{Confirmed: true, PersistID: x}` still compiles but sends
  `<persist-id>` in place of `<persist>`. Code that starts a confirmed
  commit with a token sets `Persist`. A follow-up confirmed commit can
  name the pending one with `PersistID` to extend its timeout.
  `nemith.io/netconf` could not send such a commit. `ConfirmTimeout` or
  `Persist` without `Confirmed` fails to encode.
- `rpc.Commit.ConfirmTimeout` is a `time.Duration`, rounded up to whole
  seconds. It was a number of seconds. `rpc.KillSession.SessionID` is a
  `uint64`, like `Session.SessionID`.
- Every error that an operation in package `rpc` or `junos` returns begins
  with the name of the operation, as in
  `lock: netconf error: protocol lock-denied: ...`. The name is wrapped
  around the error, so `errors.As` with `netconf.RPCErrors` or
  `netconf.RPCError` and `errors.Is` with a tag find what is underneath. A
  type assertion such as `err.(netconf.RPCErrors)` no longer matches. Use
  `errors.As`.
- Given a capability without parameters, `CapabilitySet.Has` matches
  whatever parameters the device advertised with it. Given one with
  parameters, it requires an exact match. Given a capability without a
  version, such as `:validate`, it matches any version the device
  advertised.
- `rpc.EditConfig` and `rpc.SubtreeFilter` marshal a struct payload as a
  child element. Its fields used to replace the wrapper element.
  `rpc.EditConfig` without a config fails to encode.
- `rpc.CopyConfig` and `rpc.Validate` take the interfaces `rpc.Source` and
  `rpc.Target` in place of `any`. A source is a `Datastore`, a `URL` or an
  inline `rpc.Config`, and a target is a `Datastore` or a `URL`.
- The integration tests are in `integration/`, behind the `integration`
  build tag. They were in `inttest/`, behind `inttest`.

### Removed

- `NewSession`, `WithHelloTimeout` and `ErrHandshakeTimeout`. Use `Open`,
  whose context bounds the hello exchange.
- `Session.Do(ctx, *RPC)`. `Session.Stream(ctx, op)` returns the reply as
  a stream. For a buffered reply use `Session.ExecReply`, or
  `Session.Exec` to decode it.
- `Session.Prepare`. The session assigns every message-id itself.
- `NotifHandlerFunc`, its `HandleNotification` method and
  `WithNotifHandlerFunc`. Pass the function to `WithNotifHandler`.
- `Hello`. `Session.SessionID` and `Session.ServerCaps` hold what the
  device's hello said, and a `HelloError` holds it when the hello exchange
  fails.
- `ExpandCapability`. `CapabilitySet.Has` takes a capability in short
  form, such as `:candidate:1.0`.
- `RPCReply.RPCErrors` and `RPCErrors.Filter`. `Session.Exec` and
  `Session.ExecReply` return the RPC errors of a reply as the error, and
  `ExecReply` returns the warnings in `Reply.Warnings`.
- `rpc.OkReply`. `rpc.ExecOK` issues an operation and checks the reply for
  `<ok/>`.
- `rpc.GetReply`. `rpc.Get.Exec` returns the content of `<data>`, and
  `Reply.Element("data")` finds it in a reply from `ExecReply`.
- `rpc.PartialLockReply.RunningErrors`. The RPC errors of a partial lock
  are the error of `rpc.PartialLock.Exec`.
- `ErrAccesDenied`. The tag is spelled `ErrAccessDenied`.
- Package `testutil`. Its scripted devices are in `internal/netconftest`,
  which other modules cannot import. Package `transport/transporttest` has
  the exported test helpers, for transports.

### Added

- `ncssh.DialHost` connects much as the OpenSSH client does, with options
  for key files, the ssh-agent, a password, an SSH config file,
  `known_hosts` files and an SSH keepalive. Key files, signers and the
  keys of the agent are offered in one go. The context bounds the whole
  dial, including the wait for an ssh-agent that does not answer.
  `ncssh.DefaultPort` is 830. `ncssh.Transport.Close` is safe to call more
  than once.
- Jump hosts, with `ncssh.WithProxyJump` and with `ProxyJump` from the SSH
  config. `DialHost` logs in to a jump host the way `ssh` does, with the
  jump host's own user, port and keys. A jump host is never offered the
  device's password, key files or signers, and its host key is always
  verified.
- `ncssh.WithSSHConfig` reads `HostName`, `User`, `IdentityFile`,
  `UserKnownHostsFile` and `ProxyJump` for the device, through the new
  dependency `github.com/kevinburke/ssh_config`. For a jump host it reads
  `HostName`, `User`, `Port`, `IdentityFile` and `UserKnownHostsFile`, and
  refuses the first jump host when its block names a `ProxyJump` or
  `ProxyCommand` of its own. Unless `WithKnownHosts` is given, the known
  hosts of the device and of each jump host are read from the files that
  `UserKnownHostsFile` names.
- `Session.Done`, `Session.Err`, `Session.ExecReply` and `Reply`.
  `Session.ExecReply` returns the buffered `Reply` together with its RPC
  errors, so the warnings and data of a failed operation can still be
  read.
- `WithOperationTimeout` bounds each operation whose context has no
  deadline. A context with a deadline keeps it, whether that is earlier or
  later than the bound. The deadlines the library sets itself also
  override the bound. `Session.Close` keeps its 30 seconds, and
  `junos.Change.Apply` releases the candidate within a minute and
  confirms a confirmed commit within its `Confirm` time.
- `Reply.Element` and `Reply.Elements` find a result element by name at
  any depth, so the same code reads a Junos reply with and without its
  routing-engine wrapper. `Reply.Element` fails with `ErrElementNotFound`
  when the reply has no such element. `Reply.Elements` then returns none
  and no error. `Element.Decode` applies the namespaces that the element's
  ancestors declared in the reply, and an untagged `XMLName xml.Name`
  field gets the namespace the element had there.
- `Element.Attr` and `NotificationContent.Attr` hold the attributes of the
  start tag, with the namespace a prefix stands for, such as the
  `junos:commit-seconds` of a `<configuration>` and the `junos:seconds` of
  a time. `reply.Element("rpc-reply").Attr` holds the attributes of the
  envelope.
- `RPCError.Detail` returns any element of an `<rpc-error>`: the RFC 6241
  error-info elements and those a device adds, such as the
  `source-daemon` of Junos.
- `ErrTag` is a target for `errors.Is`, as in
  `errors.Is(err, netconf.ErrLockDenied)`.
- `RPCError.Is` asks a target that has a method
  `MatchRPCError(RPCError) bool` whether the error matches it.
  `RPCError.Request` names the operation an error answers.
- `Notification.Content`, a `NotificationContent`, is the element of a
  notification that says what happened, with its name and a `Decode` for a
  struct of your own. `Notification` had only the time of the event.
  For a `Notification` that `Message.Decode` filled, `Decode` reads the
  element as the device sent it, with the namespaces the `<notification>`
  declares, even if the caller changes `Inner` afterwards. For a
  `Notification` decoded with `encoding/xml` alone, `Decode` rebuilds the
  element from `XMLName`, `Inner` and the namespace declarations in
  `Attr`. Other attributes are not part of it.
- `WithMaxMessageSize` sets the largest message a session accepts, by
  default `DefaultMaxMessageSize`, which is 256 MiB. A larger message ends
  the session with `ErrMessageTooLarge`. The limit covers the hello,
  replies, notifications and the messages the session skips. A reply read
  with `Session.Stream` is not limited.
- `rpc.ExecOK` issues an operation and fails unless the reply holds
  `<ok/>` at any depth. It is for vendor operations that answer that way.
  Every error it returns begins with the name it is given.
- `junos.CommitConfiguration` and `junos.Change` refuse a comment of more
  than 512 bytes before anything is sent. Junos 26.2 refuses the commit
  for it.
- `junos.ErrPermissionDenied` and `junos.ErrUnsupported` are targets for
  `errors.Is` on the error of any operation. `ErrPermissionDenied` matches
  an operation, a command or a part of the configuration that the user's
  login class does not permit. `ErrUnsupported` matches an operation that
  the device's platform or Junos release does not know. Junos has no error
  tag for either.
- Package `transport/command` runs NETCONF over the standard input and
  output of a command. Use it to run system OpenSSH for what `ncssh`
  cannot do. `Close` kills the process. When the command exits, reads and
  writes fail with an error that holds its exit status and the end of what
  it wrote to standard error, even when it exits in the middle of a
  message the session is skipping. A child process that the command left
  behind delays that error but does not hide it. See `nccmd.Start`.
- `ncssh.WithCommand` starts NETCONF with a command, sent as an exec
  request, in place of the `netconf` subsystem. It is for a Junos device
  without a NETCONF service, which refuses the subsystem on port 22 and
  accepts `xml-mode netconf need-trailer`.
- `ncssh.WithDialer` takes a function that makes the first connection of
  `DialHost`: one that a device opened toward the program, one from a
  given source address, or one through a proxy. Host key check, login,
  keepalive and close work as on a connection that `DialHost` made, and
  the device's `known_hosts` entry is looked up under the host name given
  to `DialHost`. A function that returns neither a connection nor an error
  fails the dial, and a connection that comes with an error is closed.
- `ncssh.WithSigner` adds public key authentication with keys that are not
  files, such as one from a vault or a certificate signed for the session.
  The signers are offered first, together with any `WithPrivateKeyFile`
  keys and, with `WithAgent`, the agent's keys. They are never offered to
  a jump host. Called with no signer, `WithSigner` still counts as the
  chosen login. `DialHost` tries no other key in its place, and a dial
  with no other login fails before it connects.
- `ncssh.WithHostKey` takes host keys, and the device must present one of
  them. It is for a program that has no `known_hosts` file and knows the
  keys from its inventory. Called with no key, `WithHostKey` fails the
  dial, whatever the other options say about the host key check.
- `ncssh.Dial` and `DialHost` fail with `ncssh.ErrAuth` when the device or
  a jump host refuses the login. The error names the user and the host,
  says what `DialHost` offered, and wraps the SSH client's error. They
  fail with `ncssh.ErrSubsystemRefused` when the device let the user in
  and refused the `netconf` subsystem.
- `Open` fails with `ErrHello` and a `HelloError` when the hello exchange
  fails. The error holds the capabilities, the RPC errors and the plain
  text the device sent where its hello belongs. When the connection ends
  before a hello, the error names the usual causes. When the context ends
  the exchange, the error still holds what the device sent until then. It
  also holds what the device sent when the client could not write its
  hello because the device had refused and closed the connection. Such a
  device is given a second to say why.
- `WithTrace` takes a `Trace` of functions that the session calls when an
  operation starts, when it ends, and for every notification. An
  `OperationInfo` and an `OperationResult` carry the name of the
  operation, the `<rpc>` message, the duration, the size of the reply and
  the error. The functions are for timing, counting and logging, including
  the operations inside `junos.Change.Apply`. A function must not call
  `Close` on the session and must not panic.
- `WithMessageTimeout` ends the session when the device starts a message
  and then sends nothing more of it for the given time. `Session.Err` then
  returns an error that matches `ErrMessageStalled`. Without the option
  such a session waits forever, and `Done` is never closed. It is off by
  default, because Junos begins a reply at once and finishes it when the
  work is done, so the time has to be longer than the slowest operation.
- A reply, an element or a notification that is not XML fails with a
  `SyntaxError`, which holds the byte offset where it stops being XML. The
  bytes around that offset are in its `Around` field and are left out of
  its error text, which ends up in logs. The error text keeps the
  character or name that the parser reports there. The parser's
  `*xml.SyntaxError`, which has only a line number, is underneath. When a
  reply that is not XML has the text of an `<rpc-error>` or `<xnm:error>`
  in it, `Session.ExecReply` returns the reply together with the
  `SyntaxError`, and the RPC errors that come before the place where the
  reply stops being XML are in that error. For any other such reply
  `ExecReply` succeeds, and `Reply.Element`, `Reply.Elements` or
  `Reply.Decode` fails with the `SyntaxError`.
- `WithReplyRepair` runs a function on every buffered reply and
  notification before the session reads it. `StripInvalidChars` is such a
  function. It removes control characters, bytes that are not UTF-8, and
  references to characters that XML does not allow, such as `&#27;`. Use
  it for a device that puts such characters into replies. Without it,
  every way of decoding such a reply fails.
- `ncssh.WithClientConfig` adjusts the `ssh.ClientConfig` that `DialHost`
  built. Password authentication also answers a keyboard-interactive
  exchange that asks for the password only.
- Once its keepalive has closed a transport, reads and writes on it fail
  with `ncssh.ErrKeepalive`. `Session.Err` then says that the keepalive
  ended the session and how many probes went unanswered, even when the
  device went silent in the middle of a message the session was skipping.
- Package `junos`:
  - Reading. `GetConfiguration` returns XML, text, set commands or JSON,
    with inherited groups applied on request. It also returns XML and JSON
    without indentation, and refuses those over chunked framing, where
    Junos 26.2 does not finish such a reply. `CompareRollback` returns a
    diff, `GetSystemInformation` what kind of device it is, and `Command`
    the output of a CLI command as text, JSON or XML.
    `GetCommitInformation` returns the commit history as values: sequence
    number, user, client, time, comment, what Junos notes on a confirmed
    commit, and whether one still waits for its confirmation. `FindCommit`
    finds a commit in that history by a token that appears in its comment
    as a separate word, which is how to find out what became of a commit
    after `ErrCommitUnknown`. `GetDatabaseStatusInformation` lists who has
    the configuration open for editing: users of the CLI in configuration
    mode and NETCONF sessions with the lock or a private copy.
  - Changing. `LoadConfiguration` loads set commands, text, XML or JSON,
    with merge, override, replace or update. Before it sends a JSON
    document, it replaces each escape that Junos does not read and that
    JSON can also write as a plain character, such as the `\u0026` of
    `encoding/json`, with that character. Escapes of control characters,
    such as `\n`, stay as they are, and Junos stores them with the
    backslash.
    It also loads a patch, the output of a compare, with `LoadPatch`, and
    a file that Junos reads itself, by path or URL, with `URL`.
    `LoadRescue` loads the rescue configuration. `CommitConfiguration`
    checks or commits, with a comment, a confirmation time as a
    `time.Duration`, `Synchronize`, `ForceSynchronize` and `Full`.
    `Rollback` loads an earlier configuration. `OpenConfiguration` and
    `CloseConfiguration` open and close a private copy or an ephemeral
    database. Loads, commits and rollbacks fail unless Junos answers
    `<ok/>`, and the error holds what Junos said. That includes a commit
    from a private copy that conflicts with the active configuration,
    because a commit from elsewhere set a different value for a statement
    the copy changes. Junos answers such a commit with a warning and
    commits nothing.
  - A whole change in one call. `Change.Apply` takes the lock or a private
    copy, loads, compares and commits. It releases the candidate however
    the change ends. Under the lock, when a load may have left statements
    in the candidate, it rolls the candidate back to the active
    configuration and then unlocks.
    It closes a private copy. A change can stop at a commit check with
    `DryRun`, show the diff to a `Review` function, and make the commit a
    confirmed commit with `Confirm`, running a `Verify` function before it
    confirms. A load that changed nothing commits nothing. With `LockWait`
    a change retries a lock that another session holds, and stops asking
    when that time is up. The errors of `Apply` name the step, not the
    operation.
  - The errors of a change. `ErrNotConfirmed` and `ErrCommitUnknown` say
    where a change that did not finish has left the device.
    `ErrCandidateModified` says that the candidate was not available
    because it holds changes that nobody has committed. `errors.Is` also
    matches it against the same refusal in an error from `rpc.Lock` or
    `OpenConfiguration`, and from no other operation. `ErrCommitPending`
    says that a dry run sent no commit check, because Junos 26.2 would take
    it for the confirmation of a confirmed commit that is pending.
  - Replies. `Decode` finds a result with or without a routing-engine
    wrapper and trims the padding off every string value. `Text` and
    `JSON` read the output of operations sent with `format="text"` and
    `format="json"`, and return an error for a failure that Junos writes
    into that output. `IsJunos` reports whether a session is to a Junos
    device, and `Capability` is the capability every Junos device
    advertises.
- Package `transport/transporttest` has a conformance suite, `Run` with a
  `Peer`, that holds a `transport.Transport` to the contract the session
  depends on. `StreamPeer` and `Relay` script a device on a byte stream,
  including one that goes silent. The `Framer` and the SSH, TLS and
  command transports pass the suite.

### Fixed

- `Open` failed with "expected element type <hello> but have <rpc-reply>"
  for a Junos user whose login class permits nothing. For such a user
  Junos puts plain text and two replies with errors in front of its hello.
  `Open` reads past them and fails with a `HelloError` that holds those
  errors and the text. Such a user still gets no session.
- Non-ASCII text from a Junos device came out garbled. Junos sends every
  byte above 127 as a character reference of its own, which an XML parser
  reads as one character per byte, so `ø` came out as `Ã¸` in a
  configuration, in the output of a command and in an error. The session
  joins each run of such references that forms one UTF-8 character into
  that character, in the replies and notifications it buffers from a
  device that advertises the Junos capability, so `Reply.Raw` is not byte
  for byte what such a device sent. `Session.Stream` returns the reply as
  sent.
- An operation that Junos answered with its own error element looked like
  a success that found nothing. Junos writes `<xnm:error>` where an
  `<rpc-error>` belongs for errors of the routing process, and Junos OS
  Evolved writes it inside the result for an interface that does not
  exist. The element fails the operation like an `<rpc-error>` and is
  returned as an `RPCError` with its message. `RPCError.Error` names the
  process that reported an error without a message.
- `ErrTypeApp` is "application", the value RFC 6241 defines. It was
  "app", so it did not match the `Type` of an application error from a
  device that follows the RFC.
- An operation waited forever when the connection dropped while its reply
  was being read.
- The session stopped reading messages when a caller abandoned a reply,
  and an operation that raced `Close` could panic.
- `Session.Close` logged spurious warnings against Junos.
- An operation that failed to encode still sent an empty or partial
  message to the device.
- `rpc.KillSession` encoded `<kill-session>` wrong, and `rpc.PartialLock`
  did not escape its XPath expressions.
- A truncated chunked message was accepted as complete, and a read after
  the end of a chunked message consumed the next one.
- Closing a session while a reply from `Session.Do` was still open left
  the session unusable.
- `nctls.Dial` verifies the certificate against the dialed host instead of
  requiring `ServerName` or `InsecureSkipVerify`.
- A message that end-of-message framing cannot carry, because it contains
  `]]>]]>` or ends in a way that would complete the delimiter early, is
  refused with `transport.ErrEndOfMsgInPayload` before any of it is sent.
  `nemith.io/netconf` sent it anyway, and the device read an empty or
  truncated message.
- `<rpc>` attribute names are validated. They were written verbatim.
- An `<rpc-reply>` without a message-id was dropped. The session matches
  it to the only operation that was sent and still waits for its reply,
  unless a late reply to an abandoned operation may still arrive.
- A failed write ends the session, and the operation fails with
  `ErrClosed` like every other operation on the session, instead of a bare
  transport error.
- `rpc.Get` and `rpc.GetConfig` fail on a reply without `<data>` instead
  of returning an empty result. So does decoding a reply into
  `rpc.GetConfigReply`, which returns the device's RPC errors when the
  reply has them.
- `rpc.PartialLock` fails on a reply without a lock id.
- `rpc.CreateSubscription` checks `StartTime` and `StopTime` when it is
  encoded, so the check also runs when it is sent with `Session.Exec`. It
  checked them in its own `Exec` only.
- `Session.Exec` dropped the warnings of a successful reply. It logs them,
  and so do the operations of `rpc` built on it: those that expect `<ok/>`
  and `rpc.PartialLock`. `Session.ExecReply` returns them in
  `Reply.Warnings`.
- `Session.Close` returned nil when the device never acknowledged
  `<close-session>`. It still closes the session, and returns an error
  that holds the context's error.
- `Message.Close` raced with a `Read` in another goroutine.
- The operations of `rpc` that expect `<ok/>` accept it at any depth.
  Junos nests it in `<load-configuration-results>`.
- After a malformed chunk header the session waited for the rest of the
  message instead of ending. Against a device that then went quiet, every
  operation waited until its context expired.
- Closing an SSH transport does not wait for the device. `Close` blocked
  forever when a device went silent during a large write, so
  `Session.Close` could not end the session. With `ncssh.NewTransport` a
  read blocked on a silent device was never released, which also defeated
  the hello timeout.
- Nothing read the standard error of the `netconf` subsystem. What a
  device wrote there stayed in the SSH channel's window, and a device that
  had filled it could send no more replies. It is read and dropped.
- When its context ended a dial, `ncssh.Dial` returned the context's
  error and nothing else. The error still matches the context's error, and
  also names the host and the step it interrupted: connecting, the SSH
  handshake, or starting the `netconf` subsystem.

### Tests

- Fuzz targets cover both framings, the session's receive path, the
  hello, notifications, reply parsing and element lookup, Junos text,
  `StripInvalidChars`, and the JSON rewrite of a load. `make fuzz` runs
  them, and CI runs them every night.
- `make integration-test` runs the integration suite against a device over
  SSH, with a 30-minute timeout. By default the suite only reads.
  `NETCONF_DUT_CHANGE_DRYRUN` adds the tests that load configuration and
  never commit it, `NETCONF_DUT_CHANGE_COMMIT` adds the tests that commit,
  for a lab device only, and `NETCONF_DUT_TRACE` logs every message. The
  suite covers `junos.Change` as a dry run and with commits, the standard
  write operations, notifications, the Junos ephemeral database, and Junos
  behaviors that users of other libraries reported. See
  [What runs](integration/README.md#what-runs).
- `make integration-netopeer2` runs the suite against netopeer2 in a
  container built on the newest Alpine from the latest releases of
  libyang, libnetconf2, sysrepo and netopeer2, and CI runs it.
  `nemith.io/netconf` used Alpine 3.16 with releases from 2022.
- `make junos-schema` compares the YANG modules that Juniper publishes for
  every Junos release with what the library depends on. It lists the
  statements under `system services netconf` release by release, and
  fails when a release does not declare an element that package `junos`
  reads from a reply. A workflow runs it every week. The integration suite
  reads the same modules off a device into the directory that
  `NETCONF_DUT_YANG` names, and logs the capabilities a device advertises.
- `make docs-check` checks the local links, image sources and heading
  anchors of the Markdown files, and CI runs it.
- [What is verified against Junos](docs/junos-verified.md#recorded-integration-runs)
  records which Junos releases and platforms the suite ran against, with
  which framing and chunk writer, and the replies captured from devices
  that the unit tests replay.

### Documentation

- Guides in `docs/`, indexed in the [README](README.md#documentation):
  getting started, connecting, sessions, operations, standard NETCONF,
  Junos, what is verified against Junos, production use, troubleshooting,
  migrating from scrapligo, and AI usage for agents that write
  applications with the library. `docs/architecture.md` maps the code for
  contributors, and decision records are in `docs/adr/`.
- Six runnable programs under [examples/](examples/README.md), each with a
  README. `make build` compiles them. Their `main` functions contact a
  device and do not run in the unit tests.
- Package examples that run offline in `make test`, among them
  `Reply.Element` with a namespace from a wrapper, `RPCError.Detail` and
  `CapabilitySet.Has`.
- Every package has a package comment and every exported identifier is
  documented, which the linter enforces. The RFC sections cited for
  `rpc.Commit` and `rpc.KillSession` are corrected.

### Performance

- Replies over end-of-message framing are read in bulk, about 60 times
  faster.
