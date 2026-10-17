# Getting started

Read this guide before you connect to a device for the first time. It
prepares the device and your machine for NETCONF and reads the running
configuration over SSH, which does not change the device.

- [Prepare the device](#prepare-the-device)
- [Prepare Go and SSH](#prepare-go-and-ssh)
- [Run the first read](#run-the-first-read)
- [Understand the session lifetime](#understand-the-session-lifetime)
- [Next guides and examples](#next-guides-and-examples)

## Prepare the device

The device needs a NETCONF service you can reach, an account that may read
the configuration, and a host key you trust. NETCONF over SSH uses port 830
by default. A working SSH login on port 22 does not show that NETCONF is
enabled.

On Junos, one statement enables the service:

```text
set system services netconf ssh
```

Commit it yourself, because no example program does. Open the NETCONF port
in every firewall on the path. Give the account a login class that permits
only what the program needs. On Junos, an operation that the login class
does not permit fails with `junos.ErrPermissionDenied`.

For a jump host or a Junos device without the NETCONF service, see
[Jump hosts](connecting.md#jump-hosts) and
[A device without a NETCONF service](connecting.md#a-device-without-a-netconf-service).

## Prepare Go and SSH

Add the library to your module as [Install](../README.md#install) shows.

Package `github.com/lasseh/netconf/transport/ssh` is imported as `ncssh`.
`ncssh.DialHost` checks the device's host key against `~/.ssh/known_hosts`
by default.
Without a password, key file or signer, it logs in with the keys in
ssh-agent and your default identity files.

Before you add a host key to `known_hosts`, compare it with a source you
trust, such as your inventory. `ssh-keyscan` returns the key of whatever
host answers at the address, so it does not authenticate the device. A
program without `known_hosts`, such as one in a container, can pass the
trusted key to `ncssh.WithHostKey` instead. Do not turn off the host key
check to get past a failure. [Host keys](connecting.md#host-keys),
[Authentication](connecting.md#authentication) and
[The SSH config](connecting.md#the-ssh-config) cover the other options.

## Run the first read

[Quick start](../README.md#quick-start) shows a complete program that reads
the running configuration. The read-config example does the same from the
command line. By default, the example programs log in with the keys in
ssh-agent and your default identity files and check the host key against
`known_hosts`. They take no password and no host key from an inventory.
[Common setup](../examples/README.md#common-setup) lists their flags.

From the root of a clone of `github.com/lasseh/netconf`:

```bash
go run ./examples/read-config -host router1.example.net -user automation
```

Empty output with exit status 0 is a successful read. The output can hold
password hashes and keys, so keep it out of shared logs.

A nonzero exit status means a step failed. Closing can fail after a
complete read, so read the logged error before you trust the output or run
the program again. See
[Output and failures](../examples/read-config/README.md#output-and-failures)
and [Troubleshooting](troubleshooting.md).

## Understand the session lifetime

`ncssh.DialHost`, `netconf.Open` and each operation take a context that
bounds only that step, so canceling it after the step returns does not end
the session. `netconf.Session.Close` ends the session, so give it a fresh
context with a short deadline, not one that expired during an operation.
[Timeouts](operations.md#timeouts) lists the defaults and what a timeout
leaves running on the device.

## Next guides and examples

- [Examples](../examples/README.md) lists the runnable programs and what
  each one does to the device.
- [Operations](operations.md#choosing-an-api) helps you choose between
  typed operations, buffered replies and streamed replies.
- [Sessions](sessions.md) covers how a session ends, sharing one between
  goroutines, and notifications.
- [Junos](junos.md) covers Junos reads and configuration changes with
  `junos.Change`.
- [Standard NETCONF](standard-netconf.md) covers capabilities, filters and
  candidate edits on any NETCONF device.
- [Production use](production-use.md) covers long-running programs and
  reconnects.
