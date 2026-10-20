# Using netconf with an AI agent

Read this page before you write a Go application that imports
`github.com/lasseh/netconf`. It maps each task to an API and the section
that explains it, and lists the rules for generated code. To change the
library itself, follow [Contributing](../.github/CONTRIBUTING.md) instead.

- [Check the version and the device](#check-the-version-and-the-device)
- [Get permission before you touch a device](#get-permission-before-you-touch-a-device)
- [Choose the API](#choose-the-api)
- [Rules for generated code](#rules-for-generated-code)
- [Rules for `junos.Change`](#rules-for-junoschange)
- [Reuse the examples](#reuse-the-examples)
- [Test offline](#test-offline)
- [Report what you verified](#report-what-you-verified)

## Check the version and the device

- The library is a Go NETCONF client, with standard operations in `rpc`
  and Junos operations in `junos`. It is not a CLI driver or an SSH shell
  library.
- The module path is `github.com/lasseh/netconf`, not the upstream module
  `nemith.io/netconf`.
- The `go` line in [go.mod](../go.mod) sets the minimum Go version.
- The API is pre-1.0, and the docs on `main` can describe APIs that the
  selected version lacks. Run `go list -m github.com/lasseh/netconf` in the
  application's module, then check each signature with `go doc` there or
  on [pkg.go.dev](https://pkg.go.dev/github.com/lasseh/netconf) at that
  version.
- Never invent an option or carry over an API from another NETCONF
  library. See [Migrating from scrapligo](migrating-from-scrapligo.md).
- Find out the platform, software version, transport, data model and
  operation first. A Junos payload does not work on other vendors'
  devices, and an advertised capability does not mean the account may use
  it.

## Get permission before you touch a device

Writing code is not running it. Do not contact a device, load
configuration, run a commit check or commit without permission for that
action. See [Getting started](getting-started.md) for device
prerequisites.

## Choose the API

| Task | Use | Read |
| --- | --- | --- |
| Connect over SSH | `ncssh.DialHost` from `transport/ssh`, then `netconf.Open` | [Options of `ncssh.DialHost`](connecting.md#options-of-ncsshdialhost) |
| Connect over TLS or system OpenSSH | `nctls.Dial` from `transport/tls`, or `nccmd.Start` from `transport/command` | [TLS and ready-made SSH clients](connecting.md#tls-and-ready-made-ssh-clients), [System OpenSSH](connecting.md#system-openssh) |
| Set session timeouts, limits or logging | Options of `netconf.Open` | [Options of `netconf.Open`](sessions.md#options-of-netconfopen) |
| Run a standard operation | A typed operation in `rpc` and its `Exec` method | [Standard NETCONF](standard-netconf.md) |
| Read Junos configuration, CLI output or commit history | `junos.GetConfiguration`, `junos.Command`, `junos.GetCommitInformation` | [Reading configuration and operational data](junos.md#reading-configuration-and-operational-data) |
| Change Junos configuration | `junos.Change.Apply` | [Changing configuration](junos.md#changing-configuration) |
| Run an operation not in `rpc` or `junos`, or read warnings and reply attributes | `Session.ExecReply` with the operation XML, without an `<rpc>` wrapper | [Choosing an API](operations.md#choosing-an-api), [Where warnings go](operations.md#where-warnings-go) |
| Decode a whole reply | `Session.Exec` with a struct for the `<rpc-reply>` envelope | [Choosing an API](operations.md#choosing-an-api) |
| Decode a result inside wrappers | `Reply.Element`, `Reply.Elements` or `junos.Decode` | [Finding the result in a reply](operations.md#finding-the-result-in-a-reply) |
| Process a reply too large to buffer | `Session.Stream`, with your own read bounds and parsing | [Large replies](operations.md#large-replies) |
| Receive notifications | `netconf.WithNotifHandler`, then `rpc.CreateSubscription` | [Notifications](sessions.md#notifications) |
| Run a long-lived program | `Session.Done`, `Session.Err`, `ncssh.WithKeepalive` | [Production use](production-use.md) |
| Diagnose a failure | `errors.Is` and `errors.As` | [Troubleshooting](troubleshooting.md) |

`rpc.GetConfig.Exec` returns the content of `<data>`, not the whole reply.

## Rules for generated code

1. Never use `ncssh.WithInsecureIgnoreHostKey` or
   `tls.Config.InsecureSkipVerify` to get a connection working. See
   [Host keys](connecting.md#host-keys).
2. Give `ncssh.DialHost`, `netconf.Open` and every operation a deadline.
   `netconf.WithOperationTimeout` covers operations without one. See
   [Timeouts](operations.md#timeouts).
3. Close the session on every return path with a fresh context that has a
   short deadline, and report a close error along with the original error. See
   [Own the session lifetime](production-use.md#own-the-session-lifetime).
4. Run one configuration workflow, such as `junos.Change.Apply` or a lock,
   edit and commit, at a time per session. See
   [Requests from several goroutines](sessions.md#requests-from-several-goroutines).
5. Check capabilities with `session.ServerCaps().Has`, because the
   operations in `rpc` do not. See
   [Check capabilities](standard-netconf.md#check-capabilities).
6. Match errors with `errors.Is` and `errors.As`, never by their text. A failed
   `Session.ExecReply` can return a nil reply. See
   [Errors](operations.md#errors).
7. Never repeat a write whose outcome is unknown, such as after a timeout.
   Read the device state first. See
   [Reconnect without replaying writes](production-use.md#reconnect-without-replaying-writes).
8. Keep credentials out of source code and command-line arguments, and
   device data out of logs. See
   [Record useful diagnostics](production-use.md#record-useful-diagnostics).
9. Never build a `junos.Command` from untrusted input. Junos does not apply
   pipes such as `| match` to it. See [CLI commands](junos.md#cli-commands).
10. Assume that notifications were lost after a queue overflow, a drop
    under `netconf.NotifQueue.Drop` or a reconnect. A new session has no subscription. See
    [Notifications and reconnects](production-use.md#notifications-and-reconnects).
11. Do not generalize one recorded device run to other releases or
    platforms. See [What is verified against Junos](junos-verified.md).

## Rules for `junos.Change`

Read [Change safety](junos.md#change-safety) before you use
`junos.Change`. In short:

- Do not combine `Private` with `Confirm`, or set `Verify` without
  `Confirm`.
- Never commit or run a commit check from `Verify`, on any session.
- Set `Confirm` long enough for the commit, `Verify` and the confirmation.
- A dry run is not read-only. It loads the change and can send a commit
  check, and on Junos 26.2 a commit check from any session confirms a
  pending confirmed commit.
- A nil error with `result.Committed` false is a success when `DryRun` is
  set or `result.Diff` is empty. See
  [Changing configuration](junos.md#changing-configuration).
- `result.Committed` can be true with an error that says the candidate
  was not released. See
  [What `Apply` does on the way out](junos.md#what-apply-does-on-the-way-out).
- After `junos.ErrNotConfirmed`, the change stays in effect until Junos
  goes back. Check the device first.
- Never apply a change again after `junos.ErrCommitUnknown`. Put a unique
  token in every commit comment and look for it with `junos.FindCommit`.
  See [After `ErrCommitUnknown`](junos.md#after-errcommitunknown).

## Reuse the examples

The [examples index](../examples/README.md) lists each program and its
effect on the device. Packages under an `internal` directory, such
as `examples/internal/exampleutil` and `internal/netconftest`, cannot be
imported from another module. Write what you need from them with the
public API.

## Test offline

Test a decoder on `&netconf.Reply{Raw: []byte(captured)}` with captured
XML, as
[junos-interfaces/main_test.go](../examples/junos-interfaces/main_test.go)
does.

To test session behavior, let `transporttest.NewStreamPeer` from
`transport/transporttest` play the device on one end of `net.Pipe()`. On
the other end, use a transport that embeds `*transport.Framer` and adds
`Close`:

```go
type pipeTransport struct {
	*transport.Framer
	conn net.Conn
}

func (t *pipeTransport) Close() error { return t.conn.Close() }
```

1. Queue the device's hello with `peer.Send`.
2. Call `netconf.Open` in a goroutine while `peer.Recv` reads the client's
   hello, because a write on `net.Pipe` waits for its reader.
3. The client's hello advertises `base:1.1`. When the scripted hello does
   too, call `peer.Upgrade()` after `peer.Recv` returns, or the peer never
   reads the next request, which uses chunked framing.
4. Give each scripted `<rpc-reply>` the NETCONF namespace,
   `urn:ietf:params:xml:ns:netconf:base:1.0`, and the `message-id` of the
   request it answers. The session skips a reply that lacks one of them,
   see [Untrusted devices](sessions.md#untrusted-devices), and the
   operation waits until its context ends.
5. Script the reply to `<close-session>`, or `Session.Close` waits until
   its context ends, or 30 seconds without a deadline.

## Report what you verified

Compile and test the generated code in the application's module. Run it
against a device only as a separate step, with permission for each run.
Unit tests do not prove device behavior, an applied change or that Junos
went back. Say when no device run took place, and report an uncertain
outcome as uncertain.
