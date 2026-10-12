# Integration tests

The tests in this directory run the library end to end against a NETCONF
device over SSH. As they come they read: nothing they do changes the
configuration of the device. The closest they come is a commit check, which
has the device validate its candidate as it is. Junos takes a commit check
for the confirmation of a confirmed commit, from any session, so the tests
that send one look at the commit history first and send none while a
confirmed commit is pending. Tests that load or commit
only run when asked for, with `NETCONF_DUT_CHANGE_DRYRUN` and
`NETCONF_DUT_CHANGE_COMMIT` below.

They are kept out of `make test` by the `integration` build tag, and skip
unless `NETCONF_DUT_SSHHOST` is set.

```bash
NETCONF_DUT_SSHHOST=switch1.example.net \
NETCONF_DUT_SSHUSER=admin \
NETCONF_DUT_FLAVOR=junos \
make integration-test
```

| Variable | Use |
| --- | --- |
| `NETCONF_DUT_SSHHOST` | Host to test against. Required. |
| `NETCONF_DUT_SSHPORT` | Port, default 830 |
| `NETCONF_DUT_SSHUSER` | Username, default from the ssh config, then the OS user |
| `NETCONF_DUT_SSHPASS` | Password authentication |
| `NETCONF_DUT_SSHKEYFILE` | Private key file |
| `NETCONF_DUT_SSHKEYPASS` | Passphrase of the private key |
| `NETCONF_DUT_SSHCOMMAND` | Start NETCONF with this command in place of the netconf subsystem, on Junos `xml-mode netconf need-trailer` with port 22. With `need-trailer` in it the sessions offer `base:1.0` only, as Junos ends its messages with the trailer then whatever its hello says. |
| `NETCONF_DUT_SSHCONFIG` | Resolve the host through this ssh config; set it to an empty value for `~/.ssh/config` |
| `NETCONF_DUT_COMMAND` | Run the suite over this command and the `command` transport in place of the ssh client of the library, see below |
| `NETCONF_DUT_FLAVOR` | `junos` enables the Junos tests |
| `NETCONF_DUT_CHANGE_DRYRUN` | Set to anything to also run the tests that load into a private copy of the candidate and commit nothing, see below |
| `NETCONF_DUT_CHANGE_COMMIT` | Set to anything to also run the tests that commit, see below. For a lab device. |
| `NETCONF_DUT_TRACE` | Set to anything to log every message sent and received, see below |
| `NETCONF_DUT_YANG` | Keep the two YANG modules that are read off a Junos device in this directory, for `make junos-schema` |

Without a password or key file the ssh-agent and the usual identity files
are used. The host key of the device is not verified. A jump host that the
ssh config names for it is verified and has to be among the known hosts.

## Over the ssh of the system

With `NETCONF_DUT_COMMAND` set, every session of the suite runs over that
command, started without a shell, and the `command` transport. The other
`NETCONF_DUT_SSH*` variables then have no say but `NETCONF_DUT_SSHHOST`,
which still has to be set. The login has to work without typing:

```bash
NETCONF_DUT_SSHHOST=switch1.example.net \
NETCONF_DUT_COMMAND="ssh -s -p 830 switch1.example.net netconf" \
NETCONF_DUT_FLAVOR=junos \
make integration-test
```

The tests that ask for something of the ssh client of the library, the
keepalive, are skipped.

## Tracing

With `NETCONF_DUT_TRACE` set the suite logs every message it sends and
receives. Several tests fetch the whole running configuration, so the log
of a run against a real device contains it, password hashes and other
secrets included. Keep such a log out of shared places, and do not paste it
into an issue.

## What runs

Against every device: connecting and the hello exchange, with the
capabilities of the device in the log, `<get-config>` of
the running configuration (buffered, streamed, and eight at a time on one
session), an operation the device refuses, a keepalive, and 46 requests
whose sizes put the end of the message at every place around a 4096-byte
boundary. Against a
device that advertises notifications, a session subscribes while another
one logs in, and has to be sent something with a time and a content. The
test says whether the device answers operations on the session with the
subscription, and does not require it.

A device that says `base:1.1` in its hello is spoken to in chunks, which
Junos does with `rfc-compliant` and `version-1.1` set under `system
services netconf`. Two tests do less then. The one that reads the
configuration without indentation holds `GetConfiguration` to refusing
that format there, and asks the device for a single statement that way on
a session of its own with a message timeout, to log whether it finishes
the reply, which Junos 26.2 does not. The one that reads JSON back leaves
the format without indentation out. One test runs only then: it makes
pairs of requests at the same time, of the sizes that ended a session on
Junos 26.2 when the two were written one right behind the other, and holds
a session to a Junos device to writing each request when the one before it
has been answered.

With `NETCONF_DUT_FLAVOR=junos` also: literal RPCs, `junos.GetConfiguration`
in every format, `CompareRollback`, `Command` with text, JSON and XML
output, `GetSystemInformation`, a commit check with `<validate>`, and two
requests for what is not there, asked for in XML and in JSON: Junos answers
them with an error in one of its two error elements. From a device that
advertises `ietf-netconf-monitoring` the modules `junos-conf-system` and
`junos-rpc-system` are read with `<get-schema>`, four megabytes and half a
one, see
[From one Junos release to the next](../docs/junos-verified.md#from-one-junos-release-to-the-next). One more holds Junos
to what it did with a report from users of other libraries: commit checks
with a comment of 512 bytes and of one more.

With `NETCONF_DUT_CHANGE_DRYRUN` set and the `junos` flavor, more tests
load into a private copy of the candidate, which they close again. Nothing
is committed. Three of them are small: one opens a private copy and finds
itself in the list of who is editing, one loads a patch and the rescue
configuration, if the device has one, and one loads a syslog match with
backslashes and a text with a quote in it and reads them back as JSON,
which has to be valid. One runs a whole change as a dry run: it loads a
configuration group that nothing applies into the copy, compares and has
Junos check it, loads the diff of that as a patch in a second dry run, and
fails if the candidate differs afterwards. The other
loads a statement of that group with text that is not ASCII, in every
format, and with the characters `encoding/json` escapes, and reads each
back in every format. It also gives a syslog file of that group the
severity `error`, a statement with the name of the Junos error element,
and reads the group back.

With `NETCONF_DUT_CHANGE_COMMIT` set and the `junos` flavor, ten more tests
commit: `junos.Change` under the lock on the candidate and in a private
copy, a commit of everything (`Full`), a confirmed commit that is
confirmed, one that is not and that Junos takes back, a commit whose
answer never arrives and that is found in the commit history, two private
copies that change the same statement, changes that Junos refuses because
another session has the lock or uncommitted changes, and the standard
operations: a commit, and a confirmed commit whose session ends and that
another session confirms. While that commit waits, a dry run of a
`junos.Change` from the other session is held to sending no commit check
and to failing with `junos.ErrCommitPending`. What they commit is a
configuration group that nothing applies, and they take it out again, so
the device runs the same
as before. Its commit history does not stay the same: one run adds 23
commits, 25 where notifications are configured, and Junos keeps 50. They
are for a lab device. The whole suite then takes about ten minutes, which
is more than `go test` allows a run by default, so `make integration-test`
gives it 30.

The same variable runs what commits nothing and is still for a lab device
only. A file with a configuration group is written to `/var/tmp` on the
device, loaded by its path into a private copy and deleted again; that
part needs `NETCONF_DUT_CHANGE_DRYRUN` as well. And a change waits for the
lock on the candidate that another session holds, loads under it and puts
the candidate back.

The same variable runs two more things. A test commits to the default
instance of the ephemeral database: a prefix list that nothing uses, which
it reads back and takes out again. That is no commit to the configuration
and leaves the commit history alone. And the test of notifications, on a
device that has them, commits the configuration group once more to read
the comment of that commit from a notification.

The standard write operations run with the `junos` and the `netopeer2`
flavor, which each have something to write that changes nothing: on Junos
the configuration group that nothing applies, on netopeer2 a group of the
access control model with a user that does not exist. One test reads a
subtree with a filter and needs no variable. With
`NETCONF_DUT_CHANGE_DRYRUN` set, on a device that advertises `:candidate`,
a test locks the candidate, edits the configuration in with
`<edit-config>`, has the device validate it where it advertises
`:validate`, and discards it. With `NETCONF_DUT_CHANGE_COMMIT` set the
same test commits it and takes it out with a second commit, and where the
device advertises `:confirmed-commit:1.1` it makes a confirmed commit and
cancels it. Another test makes a confirmed commit with a token there,
gives it more time and confirms it from a second session, and has that
session cancel a second one; it takes 15 seconds, as it waits until the
time of the first commit is over. Two more tests need that variable: an edit of the running
configuration on a device that advertises `:writable-running`, and a copy
of the running configuration to startup on one that advertises `:startup`.
A test that misses a capability is skipped and says which. The flavor
opens none of this by itself. The netopeer2 of the compose file is there to
be written to, and the compose file sets `NETCONF_DUT_CHANGE_COMMIT` for it.

## Containers

`make integration-netopeer2` builds a [netopeer2](https://github.com/CESNET/netopeer2)
server and runs the suite against it, twice: with the ssh client of the
library and a password, and over the `ssh` of the system with a key that
is made when the image is built. This is what CI runs. The image is
the newest Alpine with the latest release of libyang, libnetconf2, sysrepo
and netopeer2, built from their `master` branches: no version is named, so
the build asks Docker Hub and GitHub every time and does not work without
them. Access control is off in the image, so that the tests may write.

The other compose files under `compose/` need vendor images that are not
publicly available. Provide the image name and run compose directly:

```bash
CSRX_IMAGE=csrx:20.3R1.8 docker compose -f integration/compose/csrx.yml up --build --abort-on-container-exit --exit-code-from runner
CEOS_IMAGE=ceos64-lab:4.28.3M docker compose -f integration/compose/ceos.yml up --build --abort-on-container-exit --exit-code-from runner
CONFD_IMAGE=confd-basic:7.8 docker compose -f integration/compose/confd.yml up --build --abort-on-container-exit --exit-code-from runner
```

- Juniper cSRX: download from the Juniper support site, `docker image load -i <file>`.
- Arista cEOS-lab: download from arista.com, `docker import <file> ceos64-lab:<version>`.
- ConfD basic: download from Cisco DevNet into `integration/docker/` and
  `docker build -f integration/docker/Dockerfile.confd integration/docker -t confd-basic:7.8`.
