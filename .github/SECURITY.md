# Security policy

## Reporting a vulnerability

Report it in private. On GitHub, open the **Security** tab of the
repository and choose **Report a vulnerability**, or go straight to
<https://github.com/lasseh/netconf/security/advisories/new>. Do not open an
issue for it: issues are public.

A report is easiest to act on when it says:

- the version or commit
- what an attacker needs: a device under their control, a position on the
  network between the program and the device, or something else
- how to reproduce it: a test, or the bytes a device has to send

Remove password hashes, keys and other secrets from what you send, also in
a private report. A device reply or a wire capture holds whole
configurations.

Reports are handled in the advisory. A fix goes out as a new release
together with a GitHub security advisory.

## Supported versions

The library is pre-1.0. Fixes are made on `main` and go out with the next
release; earlier releases are not patched.

## What the library protects against

- **Talking to the wrong device.** `ncssh.DialHost` verifies the host key
  against the known hosts and never adds a key by itself. `nctls.Dial`
  verifies the certificate against the host it dialed. Turning either off
  (`WithInsecureIgnoreHostKey`, `InsecureSkipVerify`) is the caller's
  decision.
- **Credentials reaching a jump host.** A jump host is never offered the
  password or the key file given for the device, and its host key is
  verified whatever the options and the ssh config say.
- **A device that exhausts memory.** A message is limited to 256 MiB, the
  notifications waiting for the handler to 10,000 or 64 MiB, and a session
  ends after 100 messages in a row that nobody asked for.
- **Malformed input from a device.** The code that reads what a device sent
  (framing, the receive path, reply parsing) has fuzz targets that run with
  the unit tests.

## Known limits

These follow from the protocol or are for the caller to handle.
[Untrusted devices](../docs/sessions.md#untrusted-devices) describes them.

- base:1.0 framing ends a message at the first `]]>]]>`, wherever it comes
  from. A program that reads data it does not control from a device should
  require chunked framing.
- A reply requested with `Session.Stream` is not limited in size.
- `Framer.DebugCapture` copies whole messages, secrets included.
- `junos.Command` is one line of CLI. Building it from untrusted input lets
  that input choose the command.
