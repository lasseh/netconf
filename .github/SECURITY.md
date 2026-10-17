# Security policy

This page says how to report a vulnerability, which versions get fixes,
what the library protects against and what it leaves to the caller.

## Reporting a vulnerability

Report a vulnerability in private, not as an issue, because issues are
public. On GitHub, open the Security tab of the repository and choose
"Report a vulnerability", or go to
<https://github.com/lasseh/netconf/security/advisories/new>.

A report is easiest to act on when it says:

- the version or commit
- what an attacker needs: a device under their control, a position on the
  network between the program and the device, or something else
- how to reproduce it: a test, or the bytes a device has to send

Remove password hashes, keys, SNMP communities, serial numbers and
addresses from what you send, even in a private report. A device reply or
a wire capture holds whole configurations.

Each report opens a private draft security advisory on GitHub. The
advisory is published with the release that holds the fix.

## Supported versions

The library is pre-1.0. Fixes land on `main` and go out with the next
release. Earlier releases are not patched.

## What the library protects against

- **Talking to the wrong device.** `ncssh.DialHost` checks the device's
  host key against the known hosts or the keys from `ncssh.WithHostKey`,
  and never adds a key by itself. `nctls.Dial` verifies the server
  certificate against the host it dialed. Only the caller turns these
  checks off, with `ncssh.WithInsecureIgnoreHostKey`, a `HostKeyCallback`
  set through `ncssh.WithClientConfig`, or `tls.Config.InsecureSkipVerify`.
  `ncssh.Dial` and `ncssh.NewTransport` check what the caller's
  `ssh.ClientConfig` checks, and the system `ssh` behind `nccmd` follows
  its own configuration. See [Host keys](../docs/connecting.md#host-keys).
- **Credentials reaching a jump host.** A jump host is never offered the
  password, key files or signers given for the device. Its host key has to
  be among the known hosts, whatever the options and the SSH config say.
  See [Jump hosts](../docs/connecting.md#jump-hosts).
- **Unbounded device input.** By default a buffered message is limited to
  256 MiB, set by `netconf.WithMaxMessageSize`, and the notification queue
  to 10,000 notifications and 64 MiB, set by `netconf.WithNotifQueue`. An
  empty queue accepts one notification over the byte budget, up to the
  message size limit. A session ends after 100 messages in a row that it skips:
  malformed, unknown or unsolicited. These limits hold per session, not
  for the whole program.
- **Malformed input from a device.** The framing, the receive path and
  reply parsing have fuzz targets. Their seeds run with the unit tests, and
  CI fuzzes each target for five minutes every night.

## Known limits

These limits follow from the protocol or are left to the caller.
[Untrusted devices](../docs/sessions.md#untrusted-devices) describes each.

- End-of-message framing, base:1.0, ends a message at the first
  `]]>]]>`, even inside data. To read data you do not control, require
  chunked framing with
  `netconf.WithCapability(netconf.CapNetConf11)`. The hello exchange then
  fails against a device that speaks only base:1.0.
- A message that the device begins and never finishes holds the session,
  and every operation on it waits for its own context, unless
  `netconf.WithMessageTimeout` is set.
- A reply requested with `Session.Stream` is not limited in size or time.
- `transport.Framer.DebugCapture` copies whole messages, secrets included.
  `netconf.OperationInfo.Request` in a trace and `netconf.SyntaxError.Around`
  hold message bytes too.
- `junos.Command` is one line of CLI. Building it from untrusted input lets
  that input choose the command.
