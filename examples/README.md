# Runnable examples

Each example is a `main.go` with a README that gives the prerequisites,
the command, the output and the failures. Read this page to pick one and
to learn what the examples share.

Run the commands from the repository root with the Go version that
[go.mod](../go.mod) requires. An AI agent that adapts an example starts
with [Using netconf with an AI agent](../docs/ai-usage.md).

| Example | What it does | Effect on the device |
| --- | --- | --- |
| [read-config](read-config/README.md) | Reads the running configuration, optionally filtered | Read-only |
| [junos-interfaces](junos-interfaces/README.md) | Decodes the reply to `<get-interface-information>` with `junos.Decode` | Read-only |
| [stream-config](stream-config/README.md) | Streams a `<get-config>` reply and counts elements | Read-only |
| [notifications](notifications/README.md) | Logs notifications on a session of its own | Creates a subscription only |
| [junos-change](junos-change/README.md) | Previews a set-command change or makes a confirmed commit | Writes even in its default dry run. `-commit` changes the running configuration |
| [standard-edit](standard-edit/README.md) | Locks, edits, validates and commits the candidate | Changes the running configuration permanently, only with `-config` and `-commit` |

## Common setup

Prepare the device, the account and a trusted host key as described in
[Getting started](../docs/getting-started.md). The examples use the default
host key check and logins of `ncssh.DialHost`, described in
[Host keys](../docs/connecting.md#host-keys) and
[Authentication](../docs/connecting.md#authentication). They take no
password and cannot skip the host key check.

Every example takes these flags:

| Flag | Meaning |
| --- | --- |
| `-host` | Device host name, or a host alias from the SSH config. Required |
| `-user` | SSH user. Defaults to the `User` in the `-ssh-config` file, then the OS user |
| `-port` | NETCONF port, default 830 |
| `-ssh-config` | SSH config file for host aliases, identities, known hosts and jump hosts. None is read without it |
| `-help` | Every flag of that example |

`-ssh-config` does not expand a leading `~`, so pass
`"$HOME/.ssh/config"`. `ncssh` ignores the `Port` of the device there, so
set the NETCONF port with `-port`. See
[The SSH config](../docs/connecting.md#the-ssh-config).

```bash
go run ./examples/read-config -host router1.example.net -user automation
go run ./examples/junos-interfaces -host router1 -ssh-config "$HOME/.ssh/config"
```

### The runner

The runner, [`examples/internal/exampleutil`](internal/exampleutil/exampleutil.go),
opens and closes the session for each example. An application cannot
import it, so copy what you need. Its bounds:

- Connecting and the hello exchange share 30 seconds.
- An operation without its own deadline times out after 30 seconds,
  through `netconf.WithOperationTimeout`.
- An SSH keepalive goes out every 15 seconds. Three unanswered in a row
  end the session with `ncssh.ErrKeepalive`.
- Closing the session gets a fresh 5 seconds after the example returns.

Ctrl-C or SIGTERM cancels the context of the example. The runner checks
`-host` before it connects, and each example checks its own flags, such as
`-config`, after the session is open.

When setup, the example or the close fails, the program logs the error
and exits with status 1. The close can fail after the work succeeded, so
read the error before you run a program again.

The output can contain password hashes and keys, so keep it out of shared
logs. The programs never retry or reconnect. Read
[Production use](../docs/production-use.md) before you build a
long-running program from one.

## Offline checks and live runs

`make build` compiles the examples, and `make test` runs their offline
tests with the race detector. Neither runs a `main` function or contacts a
device.

`go run` connects to the device named by `-host`. Try an example that
writes configuration on a lab device first, and review its payload file.
