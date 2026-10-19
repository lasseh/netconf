<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.png">
    <img alt="netconf" src="docs/assets/logo-light.png" width="360">
  </picture>
</p>

# netconf

[![Go Reference](https://pkg.go.dev/badge/github.com/lasseh/netconf.svg)](https://pkg.go.dev/github.com/lasseh/netconf)
[![Validate](https://github.com/lasseh/netconf/actions/workflows/validate.yaml/badge.svg?branch=main&event=push)](https://github.com/lasseh/netconf/actions/workflows/validate.yaml)

A NETCONF client library for Go, built for Juniper devices. It runs RFC 6241
sessions over SSH and TLS with the standard operations and notifications,
and adds the Junos-native operations, including a whole configuration
change in one call.

- **Context deadlines.** Dialing over SSH or TLS, the hello exchange, every
  operation and `Session.Close` take a `context.Context`. A timed-out
  operation leaves the session open, and the device may still carry it out.
- **One session for many goroutines.** A session is safe for concurrent use
  and matches each reply to its operation by message-id. Over chunked
  framing, [Junos gets one request at a time](docs/sessions.md#requests-from-several-goroutines).
- **Errors a program can act on.** A non-warning `<rpc-error>` or `<xnm:error>`
  anywhere in a buffered reply fails the operation with `netconf.RPCErrors`,
  whose text includes any path the device sends. `errors.Is` matches error tags.
- **Junos.** `junos.GetConfiguration` reads the configuration as XML, text,
  set commands or JSON. `junos.Command` runs CLI commands, `junos.Decode`
  finds a result at any depth, and `junos.Change` makes a whole change.
- **Connects much like OpenSSH.** `ncssh.DialHost` checks `known_hosts`
  and logs in with the ssh-agent or key files. Options add jump hosts, part
  of the SSH config and a keepalive. For what it does not do, such as
  `ProxyCommand`, `nccmd.Start` runs the system `ssh`. See
  [Connecting](docs/connecting.md).
- **Bounded against a misbehaving device.** A session ends after 100
  [skipped messages](docs/sessions.md#untrusted-devices) in a row and, by
  default, on a buffered message over 256 MiB or a full notification queue.

## Install

```bash
go get github.com/lasseh/netconf
```

The module requires Go 1.26 or newer, the oldest release that its
`golang.org/x/crypto` dependency supports.

## Quick start

The program below reads the running configuration over SSH. The device
needs the NETCONF service on port 830, which
`set system services netconf ssh` enables on Junos. The device's host key
must be in `~/.ssh/known_hosts`.
[Getting started](docs/getting-started.md) covers both.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/rpc"
	ncssh "github.com/lasseh/netconf/transport/ssh"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The login offers the keys in ~/.ssh/id_ed25519, id_ecdsa and id_rsa,
	// and those of the ssh-agent when SSH_AUTH_SOCK is set.
	tr, err := ncssh.DialHost(ctx, "router1.example.net", ncssh.WithUser("automation"))
	if err != nil {
		return err
	}

	session, err := netconf.Open(ctx, tr)
	if err != nil {
		return err
	}
	defer session.Close(context.Background())

	cfg, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", cfg)
	return nil
}
```

## A configuration change on Junos

`junos.Change` describes a whole configuration change, and its `Apply`
method carries it out in one call. `Apply` locks the candidate, or opens a
private copy when `Private` is set. It then loads, compares and commits.
It releases the candidate however the change ends, or reports why it could
not. When the release fails, the candidate stays reserved until the
session is closed. With `Confirm` set, `Apply` makes a confirmed commit and confirms it.
If `Apply` does not confirm it, Junos goes back to the previous configuration
after the `Confirm` time runs out. Inside a function that returns an error,
with `ctx` and an open `session`:

```go
result, err := junos.Change{
	Format:  junos.FormatSet,
	Config:  "set system ntp server 192.0.2.1",
	Comment: "add ntp server",
	Confirm: 5 * time.Minute,
}.Apply(ctx, session)
if err != nil {
	return err
}
fmt.Println(result.Diff) // empty when the load changed nothing, and then nothing is committed
```

[Changing configuration](docs/junos.md#changing-configuration) shows how
to tell the errors of `Apply` apart and lists the other fields. Read
[Change safety](docs/junos.md#change-safety) before you run a change
against a device.

## Documentation

Read the first four guides in order. Then read Junos or Standard NETCONF
for your device, and Production use before a program runs unattended.

| Guide | Read it when |
| --- | --- |
| [Getting started](docs/getting-started.md) | You connect to a device for the first time |
| [Examples](examples/README.md) | You want a runnable program to start from, and need to know what it does to the device |
| [Operations](docs/operations.md) | You send operations and handle their replies, errors and timeouts |
| [Sessions](docs/sessions.md) | You open, share or close sessions, trace them, or receive notifications |
| [Junos](docs/junos.md) | The device runs Junos or Junos OS Evolved |
| [Standard NETCONF](docs/standard-netconf.md) | The device runs neither Junos nor Junos OS Evolved, or you use package `rpc` |
| [Connecting](docs/connecting.md) | The defaults of `ncssh.DialHost` do not reach the device, or you need TLS or system OpenSSH |
| [Production use](docs/production-use.md) | A program will run unattended or for a long time |
| [Troubleshooting](docs/troubleshooting.md) | Something fails and you need the next step |
| [What is verified against Junos](docs/junos-verified.md) | You need to know what was tested on which Junos release and platform, or which Junos limits the library does not work around |
| [Migrating from scrapligo](docs/migrating-from-scrapligo.md) | You port code from the scrapligo NETCONF driver |
| [Integration tests](integration/README.md) | You run the integration suite against netopeer2 or a device of yours |
| [Using netconf with an AI agent](docs/ai-usage.md) | An AI agent writes code against the library. Give the agent this page. It maps each task to its guide and lists the safety rules |

The API reference, with examples, is on
[pkg.go.dev](https://pkg.go.dev/github.com/lasseh/netconf).

## Packages

| Import path | Purpose |
| --- | --- |
| `github.com/lasseh/netconf` | `Session`: the hello exchange, operations, replies, RPC errors and notifications |
| `github.com/lasseh/netconf/rpc` | The standard operations: get, get-config, edit-config, copy-config, delete-config, lock, unlock, commit, cancel-commit, discard-changes, validate, kill-session, partial-lock, partial-unlock and create-subscription, with subtree and XPath filters and with-defaults |
| `github.com/lasseh/netconf/junos` | The Junos-native operations: a whole change with `Change`, `get-configuration`, `load-configuration`, `commit-configuration`, comparing with a rollback, rolling back, loading the rescue configuration, the commit history, who is editing, private copies, the ephemeral database, CLI commands, system information and reply decoding |
| `github.com/lasseh/netconf/transport/ssh` | NETCONF over SSH (RFC 6242), imported as `ncssh` |
| `github.com/lasseh/netconf/transport/tls` | NETCONF over TLS (RFC 7589), imported as `nctls` |
| `github.com/lasseh/netconf/transport/command` | NETCONF over the standard input and output of a process, usually system OpenSSH, imported as `nccmd` |
| `github.com/lasseh/netconf/transport` | The `Transport` interface and the RFC 6242 framing, for writing other transports |
| `github.com/lasseh/netconf/transport/transporttest` | The conformance suite every transport must pass |

## Status

The API is pre-1.0 and no version is tagged, so `go get` resolves a
pseudo-version of the latest commit on `main`. Exported names can change,
and [CHANGELOG.md](CHANGELOG.md) records each change. Until v1.0.0, any new
version can change them. The supported Go releases are the two newest,
which are the ones the Go project supports. CI tests with the release
that go.mod requires and with the newest.

- Unit tests run with the race detector. Framing, the receive path and
  reply parsing have fuzz targets. Every transport passes the conformance
  suite in `transport/transporttest`.
- The unit tests replay replies captured from Junos 25.4 on an EX4100,
  Junos 26.2 on a vJunos-router and a vJunos-switch, and Junos OS Evolved
  26.2 on a vJunosEvolved.
- In recorded lab runs over end-of-message framing, every integration test
  has passed, commits included, against Junos 26.2 on vJunos-router and
  vJunos-switch, Junos OS Evolved 26.2 on vJunosEvolved, and Junos
  23.4R2-S2.1 on vJunos-router. Over chunked framing to Junos, that holds
  only for vJunos-router 26.2 with an earlier chunk writer.
- CI runs the integration suite against
  [netopeer2](https://github.com/CESNET/netopeer2) only, not against Junos.

[What is verified against Junos](docs/junos-verified.md#platform-and-release-summary)
says what each run covered and lists the Junos limits the library does not
work around.

## RFC support

| RFC | Status |
| --- | --- |
| [RFC 6241](https://www.rfc-editor.org/rfc/rfc6241.html) NETCONF | supported |
| [RFC 6242](https://www.rfc-editor.org/rfc/rfc6242.html) NETCONF over SSH | supported |
| [RFC 7589](https://www.rfc-editor.org/rfc/rfc7589.html) NETCONF over TLS | supported |
| [RFC 5277](https://www.rfc-editor.org/rfc/rfc5277.html) Event notifications | supported |
| [RFC 5717](https://www.rfc-editor.org/rfc/rfc5717.html) Partial lock | supported |
| [RFC 6243](https://www.rfc-editor.org/rfc/rfc6243.html) With-defaults | supported |
| [RFC 8071](https://www.rfc-editor.org/rfc/rfc8071.html) Call home | not implemented. [`ncssh.WithDialer`](docs/connecting.md#custom-connections-with-withdialer) takes a connection that a device opened |

## Development

```bash
make help                   # list every target
make check                  # build, unit tests, lint and documentation checks
make integration-netopeer2  # the integration suite against netopeer2 in Docker
make integration-test       # the same suite against a device of yours
```

- [CONTRIBUTING.md](.github/CONTRIBUTING.md) covers the other targets, the
  tests and what a pull request needs.
- [SECURITY.md](.github/SECURITY.md) says how to report a vulnerability.
- [CONTEXT.md](CONTEXT.md) defines the project vocabulary, and
  [docs/adr](docs/adr) records the design decisions.
- [Architecture](docs/architecture.md) maps the packages, the request and
  reply paths, ownership and shutdown.

## Attribution and license

This project is derived from [nemith/netconf](https://github.com/nemith/netconf)
by Brandon Bennett, itself derived from Juniper Networks' go-netconf. If
you are moving from `nemith.io/netconf`, the Changed and Removed sections
of [CHANGELOG.md](CHANGELOG.md) list the API changes. The license is
BSD 2-Clause. See [LICENSE](LICENSE).
