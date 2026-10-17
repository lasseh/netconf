# Integration tests

The tests in this directory run the library end to end against a NETCONF
device over SSH. Read this page before you point the suite at your own
device or run it against netopeer2 in Docker.

By default the tests only read, apart from commit checks, which commit
nothing. Junos 26.2 on vJunos-router takes a commit check from any session
as the confirmation of a pending confirmed commit, see
[Change safety](../docs/junos.md#change-safety), so the tests send none
while one is pending. Tests that load or commit run only when
`NETCONF_DUT_CHANGE_DRYRUN` or `NETCONF_DUT_CHANGE_COMMIT` is set.

The `integration` build tag keeps the tests out of `make test`, and every
test skips unless `NETCONF_DUT_SSHHOST` is set.

```bash
NETCONF_DUT_SSHHOST=switch1.example.net \
NETCONF_DUT_SSHUSER=automation \
NETCONF_DUT_FLAVOR=junos \
make integration-test
```

To run one test, pass `-run` with a pattern through `TESTFLAGS`. Make
expands the value, so write `$$` where the pattern needs a `$`.

```bash
NETCONF_DUT_SSHHOST=switch1.example.net NETCONF_DUT_FLAVOR=junos \
make integration-test TESTFLAGS=-run=TestJunosCommitCheck
```

- [Variables](#variables)
- [Running over system OpenSSH](#running-over-system-openssh)
- [Tracing](#tracing)
- [What runs](#what-runs)
- [Containers](#containers)

## Variables

| Variable | Effect |
| --- | --- |
| `NETCONF_DUT_SSHHOST` | Host to test against. Required. |
| `NETCONF_DUT_SSHPORT` | Port. Default 830. |
| `NETCONF_DUT_SSHUSER` | Username. Defaults to the `User` in the SSH config that `NETCONF_DUT_SSHCONFIG` names, then the OS user. |
| `NETCONF_DUT_SSHPASS` | Password. |
| `NETCONF_DUT_SSHKEYFILE` | Private key file. |
| `NETCONF_DUT_SSHKEYPASS` | Passphrase of the private key. |
| `NETCONF_DUT_SSHCOMMAND` | Start NETCONF with this command through `ncssh.WithCommand` instead of the `netconf` subsystem. On Junos use `xml-mode netconf need-trailer` with `NETCONF_DUT_SSHPORT=22`. With `need-trailer` in the command the sessions offer only `base:1.0`. |
| `NETCONF_DUT_SSHCONFIG` | Resolve the host through this SSH config file. An empty value selects `~/.ssh/config`. |
| `NETCONF_DUT_COMMAND` | Run every session over this command, see [Running over system OpenSSH](#running-over-system-openssh). |
| `NETCONF_DUT_FLAVOR` | `junos` or `netopeer2` adds the tests of that flavor, see [What runs](#what-runs). Any other value, such as `eos` or `confd` in the compose files, runs only the tests for every device. |
| `NETCONF_DUT_CHANGE_DRYRUN` | Set to anything to also run the tests that load configuration and commit nothing. |
| `NETCONF_DUT_CHANGE_COMMIT` | Set to anything to also run the tests that commit. For a lab device only. |
| `NETCONF_DUT_TRACE` | Set to anything to log every message, see [Tracing](#tracing). |
| `NETCONF_DUT_YANG` | Directory where `TestJunosSchema` keeps the two YANG modules it reads off a Junos device, for `make junos-schema JUNOS_YANG_MORE=<dir>`. |

`NETCONF_DUT_CHANGE_COMMIT` does not turn on `NETCONF_DUT_CHANGE_DRYRUN`,
so set both to run the whole suite.

Without a password or key file, `ncssh.DialHost` logs in with the
ssh-agent and the default identity files, see
[Authentication](../docs/connecting.md#authentication). The suite dials
with `ncssh.WithInsecureIgnoreHostKey`, so it does not verify the device's
host key. A jump host that the SSH config names still needs its host key
in `known_hosts`.

## Running over system OpenSSH

With `NETCONF_DUT_COMMAND` set, every session runs over that command with
the `command` transport instead of `ncssh.DialHost`. The suite splits the
command at white space and starts it without a shell, so quotes do not
group words. Put options whose values contain a space in the SSH config.
The command gets no terminal, so the login must succeed without a prompt.

The `NETCONF_DUT_SSH*` variables then have no effect, with two exceptions.
`NETCONF_DUT_SSHHOST` still has to be set, and `need-trailer` in
`NETCONF_DUT_SSHCOMMAND` still limits the sessions to `base:1.0`. The
system `ssh` checks the host key according to its own configuration.

```bash
NETCONF_DUT_SSHHOST=switch1.example.net \
NETCONF_DUT_COMMAND="ssh -s -p 830 switch1.example.net netconf" \
NETCONF_DUT_FLAVOR=junos \
make integration-test
```

`TestKeepalive` skips here, because it needs the keepalive of
`ncssh.DialHost`.

## Tracing

With `NETCONF_DUT_TRACE` set the suite logs every message it sends and
receives. Several tests read the whole running configuration, so the log
holds it, secrets included. Keep such a log out of shared places and out
of issues.

## What runs

Each gate adds tests to those that run without it. A test whose flavor,
gate or capability is missing skips and says which. Some tests run
differently over chunked framing, which Junos 26.2 uses only with
`rfc-compliant` and `version-1.1` set under `system services netconf`.

| Gate | What runs | Effect on the device | Commits added |
| --- | --- | --- | --- |
| None, only `NETCONF_DUT_SSHHOST` | [Tests for every device](#every-device): the hello exchange, reads, framing, keepalive, notifications | Reads | None |
| `NETCONF_DUT_FLAVOR=junos` | [Junos tests that read](#junos-flavor) | Reads, and commit checks while no confirmed commit is pending | None |
| `NETCONF_DUT_FLAVOR=netopeer2` | [The subtree-filter test](#netopeer2-flavor) | Reads | None |
| `NETCONF_DUT_CHANGE_DRYRUN` | [Tests that load and commit nothing](#dry-run-gate) | On Junos, loads into private copies of the candidate and closes them. `TestEditConfigCandidate` edits the shared candidate under its lock and discards the edit. | None |
| `NETCONF_DUT_CHANGE_COMMIT` | [Tests that commit](#commit-gate) | Commits the probe group and takes it out. Loads into the shared candidate and puts it back. Commits to the ephemeral database. Can write and delete a file in `/var/tmp`, and copy the running configuration over startup. | About two dozen on Junos, two more where notifications are configured |

### The probe group and the commit history

Apart from `TestCopyConfigStartup`, which copies the running configuration
over startup, nothing the tests write changes how the device runs. The
tests commit a probe group. On Junos it is the configuration group
`netconf-lib-probe`, which nothing applies. On netopeer2 it is an
access-control group of that name with a user that does not exist.

A test that commits fails without touching an existing probe group,
because the test did not create it. A run that was cut short leaves the
group behind. On Junos, `delete groups netconf-lib-probe` takes it out.

Each run of the commit gate adds the commits in the table above to the
commit history. Junos 26.2 keeps the 50 newest, see
[Commit history](../docs/junos.md#commit-history). Run the commit gate on
a lab device only.

`make integration-test` gives `go test` a 30-minute timeout instead of its
default 10, because the tests that commit wait on the device for minutes
at a time.

### Every device

- `TestSSHDial` runs the hello exchange and logs the device's
  capabilities.
- `TestSSHGetConfig`, `TestSSHGetConfigStream` and
  `TestConcurrentOperations` read the running configuration buffered,
  streamed, and eight at once on one session.
- `TestBadGetConfig` checks that the session still works after an
  operation the device refuses.
- `TestKeepalive` checks that a keepalive every 200 ms leaves a healthy
  session alone.
- `TestRequestSizes` sends requests that end at each byte position around
  the 4096-, 8192- and 65536-byte boundaries, 46 over end-of-message
  framing and 51 over chunked framing.
- `TestRequestsAtOnce` runs over chunked framing only. It sends 24 pairs of
  requests at once, sized like the pairs that ended a session on
  vJunos-router 26.2. With the `junos` flavor it checks that only one
  request is outstanding at a time. Any other device has to answer both.
- `TestMessageTimeout` fails on a silence of more than a minute inside a
  reply. An idle session with a 3-second message timeout has to stay open
  for 7 seconds.
- `TestNotifications` runs on a device that advertises `:notification:1.0`.
  One session subscribes while a second one logs in, and a notification
  with an event time and content has to arrive within a minute.

### Junos flavor

With `NETCONF_DUT_FLAVOR=junos`:

- `TestJunosRawRPC` sends literal XML with `Session.Exec` and
  `Session.ExecReply`, and an unknown operation that has to fail with
  `netconf.RPCError`.
- `TestJunosGetConfiguration` reads with `junos.GetConfiguration` in XML,
  text, set, inherited text and JSON. Over end-of-message framing it
  compares the minified formats with the indented ones. Over chunked
  framing it checks that `GetConfiguration` refuses
  `junos.FormatXMLMinified`, and logs whether the device finishes such a
  reply.
- `TestJunosElementAttributes` reads the commit time from the attributes of
  `<configuration>`, and decodes an element taken out of a reply with its
  namespaces.
- `TestJunosCompareRollback` runs `junos.CompareRollback` against rollback
  1.
- `TestJunosCommand` runs `junos.Command` with text, JSON and XML output,
  and checks that a failed command in text does not read as one that
  printed nothing. The one exception is the
  [empty reply](../docs/junos-verified.md#empty-replies-to-failed-operations-in-text-format)
  that Junos 23.4R2-S2.1 on vJunos-router sends in `rfc-compliant` mode
  for an interface that does not exist.
- `TestJunosSystemInformation` runs `junos.IsJunos` and
  `junos.GetSystemInformation`.
- `TestJunosSchema` reads `junos-conf-system` and `junos-rpc-system` with
  `<get-schema>` on a device that advertises `ietf-netconf-monitoring`.
- `TestJunosCommitInformation` checks the order, the numbering and the
  newest commit time that `junos.GetCommitInformation` returns.
- `TestJunosDatabaseStatus` lists the sessions that edit the configuration
  with `junos.GetDatabaseStatusInformation`.
- `TestJunosErrorDetail` checks that operations for things that do not
  exist fail in XML and in JSON. In XML the error is a `netconf.RPCError`
  with a message, whether Junos sends `<rpc-error>` or `<xnm:error>`.
- `TestJunosCommitCheck` sends a commit check, and `<validate>` of the
  candidate, which the suite treats as a commit check on Junos.
- `TestJunosCommitCommentLimit` checks the 512-byte limit on commit
  comments with commit checks at 512 and 513 bytes, and at 256 and 257
  two-byte letters.
- `TestGetConfigSubtreeFilter` checks that a subtree filter returns that
  subtree, not the whole configuration.
- `TestMessageTimeout` also logs the longest silence inside replies in XML,
  text, set and JSON, and inside a commit check.

### netopeer2 flavor

With `NETCONF_DUT_FLAVOR=netopeer2`, `TestGetConfigSubtreeFilter` reads a
subtree of the netopeer2 server configuration, and the standard write
tests write the probe group. The flavor opens no gate by itself. The
compose file sets `NETCONF_DUT_CHANGE_COMMIT`, because its netopeer2
server exists to be written to.

### Dry-run gate

With `NETCONF_DUT_CHANGE_DRYRUN` and the `junos` flavor:

- `TestJunosDatabaseStatus` opens a private copy and finds itself among the
  sessions that edit the configuration.
- `TestJunosChangeDryRun` runs a `junos.Change` dry run of the probe group,
  then loads its diff as a patch in a second dry run that has to give the
  same diff. It skips while a confirmed commit is pending.
- `TestJunosTextRoundTrip` loads and reads back non-ASCII text in every
  format, the characters `encoding/json` escapes, a character reference
  that Junos 26.2 stores as one byte, and the syslog severity `error`,
  which has the name and namespace of the Junos error element.
- `TestJunosLoadPatchURLRescue` loads a patch, and the rescue configuration
  where the device has one. Where it has none, the test checks the refusal.
- `TestJunosJSONIsJSON` reads command output, and configuration that holds
  backslashes and a quote, as JSON that has to be valid.

`TestEditConfigCandidate` runs with `NETCONF_DUT_CHANGE_DRYRUN` or
`NETCONF_DUT_CHANGE_COMMIT`, the `junos` or `netopeer2` flavor, and a
device that advertises `:candidate`. It locks the shared candidate, edits
it with `<edit-config>`, validates it where the device advertises
`:validate`, and discards the edit. On Junos it skips while a confirmed
commit is pending, because the suite treats `<validate>` there as a commit
check.

### Commit gate

With `NETCONF_DUT_CHANGE_COMMIT` and the `junos` flavor:

- `TestJunosChangeCommit` applies a `junos.Change` whose comment holds `<`,
  `&`, quotes and non-ASCII letters, and checks that the commit history
  shows the comment as sent. Applying it again has to commit nothing.
- `TestJunosChangeCommitFull` commits with `Full`, like the CLI's
  `commit full`.
- `TestJunosChangeConfirmed` makes a confirmed commit that `Verify` checks
  and `Apply` confirms.
- `TestJunosChangeNotConfirmed` makes a confirmed commit that `Verify`
  turns down, and waits up to three minutes for Junos to take it back.
- `TestJunosCommitUnknown` ends the context 150 ms after it sends a commit,
  once plain and once confirmed. `Apply` has to return
  `junos.ErrCommitUnknown`, and a second session finds the commit with
  `junos.FindCommit` and confirms the confirmed one with a commit check.
  The test skips when the device answers within 150 ms.
- `TestJunosChangePrivate` applies a `junos.Change` in a private copy.
- `TestJunosChangePrivateConflict` has two private copies change the same
  statement. The second has to commit nothing and fail with an error other
  than `junos.ErrCommitUnknown`.
- `TestJunosChangeCandidateTaken` checks that `Apply` fails with
  `netconf.ErrLockDenied` and the holder's session-id while another session
  holds the lock, and with `junos.ErrCandidateModified` while another
  session has uncommitted changes. Nothing is committed.
- `TestJunosChangeLockWait` has another session hold the candidate lock for
  10 seconds. A dry-run change fails at once without `LockWait`, and gives
  up and names the holder with a `LockWait` of 2 seconds. With a minute it
  gets the lock, loads into the shared candidate and puts it back. Nothing
  is committed.
- `TestJunosStandardConfirmedCommit` makes a confirmed commit with
  `rpc.Commit` and ends its session, and another session confirms it with
  a commit check. Meanwhile a `junos.Change` dry run on the other session
  has to fail with `junos.ErrCommitPending` and send no commit check.
  Without `:confirmed-commit:1.1`, `<cancel-commit>` has to fail with
  `junos.ErrUnsupported`, from `rpc.CancelCommit` and as
  `<cancel-commit/>`.
- `TestJunosEphemeral` commits a prefix list named `netconf-lib-probe`,
  which nothing uses, to the default instance of the ephemeral database,
  reads it back and takes it out. That adds no entry to the commit
  history.
- `TestJunosLoadPatchURLRescue`, with `NETCONF_DUT_CHANGE_DRYRUN` also set,
  writes a configuration file to `/var/tmp`, loads it into a private copy
  and deletes it.
- `TestNotifications`, on a device with notifications, commits the probe
  group once more and reads that commit's comment from a notification.
- `TestMessageTimeout` times a commit of the probe group and the commit
  that takes it out.

With `NETCONF_DUT_CHANGE_COMMIT` and the `junos` or `netopeer2` flavor:

- `TestEditConfigCandidate` also commits the edit and takes it out again.
  With `:confirmed-commit:1.1` it makes a confirmed commit and cancels it.
- `TestCommitPersist` runs on a device that advertises
  `:confirmed-commit:1.1`. A second session extends a confirmed commit
  that has a persist token, confirms it, and cancels another one. It takes
  at least 15 seconds, because it waits out the first commit's original
  timeout.
- `TestEditConfigRunning` edits the running configuration on a device that
  advertises `:writable-running`. With `:validate:1.1` it also sends a
  test-only edit that has to load nothing.
- `TestCopyConfigStartup` copies the running configuration to startup on a
  device that advertises `:startup` and `:writable-running`.

## Containers

`make integration-netopeer2` builds a
[netopeer2](https://github.com/CESNET/netopeer2) server and runs the suite
against it twice, once with `ncssh.DialHost` and a password and once over
system OpenSSH with a key made when the image is built. CI runs this
target. The compose file sets `NETCONF_DUT_TRACE`, so the CI log holds
every message of both runs.

The image is the newest Alpine with libyang, libnetconf2, sysrepo and
netopeer2 built from the latest release on their `master` branches.
Nothing is pinned, so every build needs Docker Hub and GitHub. Access
control is off in the image so that the tests can write.

The other compose files under `integration/compose/` need vendor images
that are not publicly available. CI does not run them, and
[What is verified against Junos](../docs/junos-verified.md) records no
result from them. Set the image variable that the compose file reads, and
run compose directly:

```bash
CSRX_IMAGE=csrx:20.3R1.8 docker compose -f integration/compose/csrx.yml up --build --abort-on-container-exit --exit-code-from runner
CEOS_IMAGE=ceos64-lab:4.28.3M docker compose -f integration/compose/ceos.yml up --build --abort-on-container-exit --exit-code-from runner
CONFD_IMAGE=confd-basic:7.8 docker compose -f integration/compose/confd.yml up --build --abort-on-container-exit --exit-code-from runner
```

- For Juniper cSRX, download the image from the Juniper support site, then
  run `docker image load -i <file>`.
- For Arista cEOS-lab, download the image from arista.com, then run
  `docker import <file> ceos64-lab:<version>`.
- For ConfD basic, download `confd-basic-7.8.3.linux.x86_64.zip` from Cisco
  DevNet into `integration/docker/`, then run
  `docker build -f integration/docker/Dockerfile.confd integration/docker -t confd-basic:7.8`.
  For another release, change the file names in `Dockerfile.confd`.
