# Troubleshooting

This page maps an error or symptom to a check, a next action and the
section that explains it. Match errors
with `errors.Is` and `errors.As`, not by their text.

- [Connecting and opening](#connecting-and-opening)
- [Operations and writes](#operations-and-writes)
- [Replies and notifications](#replies-and-notifications)
- [Collecting a useful report](#collecting-a-useful-report)

## Connecting and opening

| Symptom | Check | Next action |
| --- | --- | --- |
| `ncssh.DialHost` fails with `context.DeadlineExceeded` or a network error | Is the NETCONF port open? On Junos, is `system services netconf ssh` set? | Open the port or enable the service. See [When connecting fails](connecting.md#when-connecting-fails). |
| `ncssh.ErrAuth` | Which host refused? The error names it. | Check the account and its keys or password on that host, which can be a jump host. See [Authentication](connecting.md#authentication). |
| `*knownhosts.KeyError` with an empty `Want` | Is the host in the `known_hosts` file that `ncssh` reads? | Verify the key against a source you trust, then add it. See [Host keys](connecting.md#host-keys). |
| `*knownhosts.KeyError` with keys in `Want` | Did the host key change, or did the program reach another device? | Stop and verify the device's identity. Never replace the trusted key automatically. See [Host keys](connecting.md#host-keys). |
| `*ssh.AlgorithmNegotiationError` | Does the device lack the key type given to `ncssh.WithHostKey`, or offer only old algorithms? | Pass a key type the device has. Update an old device, or change the algorithms with `ncssh.WithClientConfig` for that device only. See [When connecting fails](connecting.md#when-connecting-fails). |
| `ncssh.ErrSubsystemRefused` | Is NETCONF enabled on this port? | Enable it, or set the port with `ncssh.WithPort`. See [A device without a NETCONF service](connecting.md#a-device-without-a-netconf-service). |
| `transport.ErrMalformedChunk` after a dial with `ncssh.WithCommand` | Does the device advertise `base:1.1` and still send `]]>]]>`? | Open the session with `netconf.WithCapability(netconf.CapNetConf10)`. See [Trailers](junos-verified.md#trailers-on-a-session-started-with-a-command). |
| A host alias does not resolve, or its `ProxyJump` is not followed | Was `ncssh.WithSSHConfig` passed? | Pass it. For `ProxyCommand` and other unsupported directives, see [System OpenSSH](connecting.md#system-openssh). |
| `netconf.ErrHello` or `*netconf.HelloError` | What do `Err`, `Capabilities`, `RPCErrors` and `Text` hold? | Fix the service, the login class or the capabilities. Without a deadline, `netconf.Open` gives up after 30 seconds. See [When connecting fails](connecting.md#when-connecting-fails). |

## Operations and writes

| Symptom | Check | Next action |
| --- | --- | --- |
| An operation times out on a live session | Does its context have its own deadline? | The device may still carry it out. Read the device state before you repeat a write. See [Canceled operations](operations.md#canceled-operations). |
| `netconf.WithOperationTimeout` seems to have no effect | Does the context carry a deadline? Any deadline overrides it, even a later one. | Give each operation its own `context.WithTimeout`. See [Timeouts](operations.md#timeouts). |
| Repeated timeouts on a live session | Did a buffered reply stop partway? | `netconf.WithMessageTimeout` ends such a session. Set it longer than the slowest operation, usually a commit. If no reply began, close the session. See [Stalled messages](operations.md#stalled-messages). |
| Operations fail with `netconf.ErrClosed` | What does `Session.Err()` return? [Session lifetime](sessions.md#session-lifetime) lists the causes. | [Reconnect without replaying writes](production-use.md#reconnect-without-replaying-writes). For `netconf.ErrMessageTooLarge`, raise `netconf.WithMaxMessageSize` or use `Session.Stream`. |
| `netconf.ErrLockDenied` | Which session holds the lock? `netconf.RPCError.Detail("session-id")` names it. | Wait only where you expect another holder, with `junos.Change.LockWait`. See [Lock conflicts](junos.md#lock-and-candidate-conflicts). |
| `junos.ErrCandidateModified` | What does `junos.CompareRollback{}` show? | Ask whoever made the changes to commit or drop them. Never discard changes your program did not make. See [Lock conflicts](junos.md#lock-and-candidate-conflicts). |
| `junos.ErrCommitPending` from a dry run | Who made the pending confirmed commit? The error names the user. | Coordinate with that user. On Junos 26.2, a commit check would confirm their commit. See [Change safety](junos.md#change-safety). |
| `junos.ErrNotConfirmed` | Did `Verify` fail, did the time run out, or did Junos refuse the confirmation? | The change stays until Junos goes back, which can be well after the time is up. Check the device. See [Change safety](junos.md#change-safety). |
| `junos.ErrCommitUnknown` | Did Junos carry out the commit or its confirmation? | Do not apply the change again until you know the outcome. Look for your comment token with `junos.FindCommit`. See [After `ErrCommitUnknown`](junos.md#after-errcommitunknown). |
| `Apply` returns an error with `junos.ChangeResult.Committed` true | Was the candidate released? | The commit went through, so do not apply it again. If the candidate was not released, close the session. See [On the way out](junos.md#what-apply-does-on-the-way-out). |
| `junos.ErrPermissionDenied` | Does the login class permit the operation? `netconf.RPCError.Detail("bad-element")` names what was refused. | Change the login class, or narrow the operation. See [Junos errors](operations.md#junos-errors). |
| `junos.ErrUnsupported` | Does this platform have the operation? | Use an operation the platform supports. See [Junos errors](operations.md#junos-errors). |

## Replies and notifications

| Symptom | Check | Next action |
| --- | --- | --- |
| `netconf.ErrElementNotFound` | What does a sanitized `Reply.Raw` hold? | Match the element's local name. See [Finding the result](operations.md#finding-the-result-in-a-reply). |
| `*netconf.SyntaxError` | What sits at `Offset`? `Around` holds the bytes there, which can include secrets. | If losing invalid characters is acceptable, open the session with `netconf.WithReplyRepair(netconf.StripInvalidChars)`. See [Malformed XML](sessions.md#malformed-xml-from-the-device). |
| Text such as `Ã¸` where `ø` belongs | Was the reply streamed? Does the device advertise `junos.Capability`? | Read the reply buffered, or join the character references yourself. See [Non-ASCII text](junos.md#non-ascii-text). |
| Nothing else runs on the session after `Session.Stream` | Is a streamed reply still open? | Read and close the reply, or close the session to give up. See [Large replies](operations.md#large-replies). |
| `junos.GetConfiguration` refuses `junos.FormatJSONMinified` or `junos.FormatXMLMinified` | Do both sides advertise `base:1.1`? | Use an indented format, or open the session with `netconf.WithCapability(netconf.CapNetConf10)`. See [Formats without indentation](junos-verified.md#formats-without-indentation-over-chunked-framing). |
| `netconf.ErrNoNotifHandler` | Was the session opened with `netconf.WithNotifHandler`? | Open the session with a handler, then subscribe. See [Notifications](sessions.md#notifications). |
| `netconf.ErrNotificationOverflow` | Is the handler too slow for the bursts? | Move slow work out of the handler, or raise the bounds with `netconf.WithNotifQueue`. Notifications were lost, so read the current device state. See [Notifications and reconnects](production-use.md#notifications-and-reconnects). |
| Operations on a subscription session get no reply | On Junos, is `notification interleave` configured? | Give the subscription its own session. Junos 26.2 advertises `:interleave` either way. See [Subscription sessions](junos-verified.md#operations-on-a-subscription-session). |

## Collecting a useful report

A bug report needs:

- the library version, from `go list -m github.com/lasseh/netconf`
- the device model and exact software version
- the transport package
- the capabilities from `Session.ServerCaps()` and `Session.ClientCaps()`
- the sanitized request and reply
- the operation's deadline and its error
- whether the device state changed, and whether a new session behaves the
  same

Before you share a reply, a trace, a diff or `netconf.SyntaxError.Around`,
remove password hashes, keys, SNMP communities, serial numbers and
addresses. Do not log `netconf.OperationInfo.Request` or turn on
`transport.Framer.DebugCapture` on every session to catch one failure. See
[Record useful diagnostics](production-use.md#record-useful-diagnostics).

Report a bug as [Contributing](../.github/CONTRIBUTING.md#issues)
describes, and a vulnerability in private as the
[Security policy](../.github/SECURITY.md#reporting-a-vulnerability)
describes.
