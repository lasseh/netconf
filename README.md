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

A NETCONF client library for Go, made for work against Juniper devices:
RFC 6241 sessions over SSH and TLS, the standard operations, notifications,
and the Junos-native operations with a whole configuration change in one
call.

- **A context on everything.** Connecting, the hello exchange and every
  operation take a `context.Context`. An operation that times out leaves
  the session usable.
- **One session for many goroutines.** A session is safe for concurrent use
  and matches each reply to its operation.
- **Errors a program can act on.** An `<rpc-error>` anywhere in a reply is a
  Go error that says where the device found the problem, also when Junos
  nests it in a result element or writes its own `<xnm:error>` in its
  place. `errors.Is(err, netconf.ErrLockDenied)` works.
- **Junos.** The configuration as XML, text, set commands or JSON, CLI
  commands, replies that read the same with and without a routing-engine
  wrapper, and `junos.Change`: lock, load, diff, commit, confirm and unlock
  in one call that gives the candidate back on every way out.
- **Connects the way `ssh` does.** Known hosts, ssh-agent, key files and
  jump hosts, the ssh config when asked for, and a keepalive that turns a
  dead peer into an ended session.
- **Bounded against a misbehaving device.** Message size, the notification
  queue and unsolicited messages have limits, and the code that reads device
  input is fuzzed.

## Install

```bash
go get github.com/lasseh/netconf
```

Requires Go 1.26 or newer, the oldest release `golang.org/x/crypto`
supports.

## Quick start

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// As with ssh: the host key is checked against ~/.ssh/known_hosts, and
	// the keys of the ssh-agent and the usual identity files are offered.
	tr, err := ncssh.DialHost(ctx, "router.example.com", ncssh.WithUser("admin"))
	if err != nil {
		log.Fatal(err)
	}

	session, err := netconf.Open(ctx, tr)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close(context.Background())

	cfg, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", cfg)
}
```

## A configuration change on Junos

A configuration change is one value. `Apply` locks the candidate, loads the
configuration, looks at what that changes, commits, and gives the candidate
back whatever happened on the way:

```go
result, err := junos.Change{
	Format:  junos.FormatSet,
	Config:  "set system ntp server 192.0.2.1",
	Comment: "add ntp server",
	// Junos goes back by itself unless the commit is confirmed in time
	Confirm: 5 * time.Minute,
	// runs with the new configuration in effect, before the confirmation
	Verify: func(ctx context.Context) error { return bgpIsUp(ctx, session) },
}.Apply(ctx, session)
switch {
case errors.Is(err, junos.ErrNotConfirmed):
	// in effect now, and Junos is taking it back
case errors.Is(err, junos.ErrCommitUnknown):
	// the answer to a commit never came: ask the device
case err != nil && result.Committed:
	// committed, and the candidate was not given back: the error says why
case err != nil:
	return err // nothing was committed; names the statement Junos refused
}
fmt.Println(result.Diff) // empty: the load changed nothing, nothing was committed
```

[Changing configuration](docs/junos.md#changing-configuration) has the
other fields of a `Change`, a dry run and a private copy among them, and
what to do after `ErrCommitUnknown`.

## Documentation

| Guide | What it covers |
| --- | --- |
| [Connecting](docs/connecting.md) | The options of `ncssh.DialHost`, host keys, jump hosts, the `ssh` of the system, a device without a NETCONF service, telling failures apart, TLS, writing a transport |
| [Operations](docs/operations.md) | Typed operations and literal XML, RPC errors, finding the result in a reply, timeouts, large replies |
| [Junos](docs/junos.md) | Reading the configuration, CLI commands, the commit history, `junos.Change` and the operations under it, the ephemeral database, text that is not ASCII |
| [What is verified against Junos](docs/junos-verified.md) | What captured replies pin, what the integration suite ran against on which platform, what Junos does that the library does not make up for, and what the YANG modules say from one Junos release to the next |
| [Sessions](docs/sessions.md) | The options of `netconf.Open`, requests from several goroutines, tracing, notifications, messages that are not XML, the limits against an untrusted device |
| [Migrating from scrapligo](docs/migrating-from-scrapligo.md) | The scrapligo NETCONF driver mapped to this library |
| [Integration tests](integration/README.md) | Running the integration suite against netopeer2 or a device of yours |

The API reference, with examples, is on
[pkg.go.dev](https://pkg.go.dev/github.com/lasseh/netconf).

## Packages

| Package | Purpose |
| --- | --- |
| `netconf` | `Session`: hello exchange, operations, replies, RPC errors, notifications |
| `netconf/rpc` | The standard operations: get, get-config, edit-config, copy-config, delete-config, lock, unlock, commit, cancel-commit, discard-changes, validate, kill-session, partial-lock, create-subscription; subtree and XPath filters; with-defaults |
| `netconf/junos` | Junos: a whole change with `Change`, get, load and commit configuration, compare rollback, the commit history, who is editing, CLI commands, system information, reply decoding |
| `netconf/transport/ssh` | NETCONF over SSH (RFC 6242), imported as `ncssh` |
| `netconf/transport/tls` | NETCONF over TLS (RFC 7589), imported as `nctls` |
| `netconf/transport/command` | NETCONF over a command, the `ssh` of the system for one, imported as `nccmd` |
| `netconf/transport` | The `Transport` interface and the RFC 6242 framing, for writing other transports |
| `netconf/transport/transporttest` | The conformance suite a transport has to pass |

## Status

The API is pre-1.0. Exported names can change between releases, and
[CHANGELOG.md](CHANGELOG.md) lists what did.

- Unit tests run with the race detector. Framing, the receive path and reply
  parsing have fuzz targets, and every transport passes one conformance
  suite.
- CI runs the integration suite end to end against
  [netopeer2](https://github.com/CESNET/netopeer2).
- Junos behaviour is pinned by replies captured from Junos 25.4 on an
  EX4100, Junos 26.2 on a vJunos-router and a vJunos-switch, and Junos OS
  Evolved 26.2 on a vJunosEvolved, replayed in the unit tests. The
  integration suite passes against the last three and against Junos 23.4
  on a vJunos-router, commits included.
  [What is verified against Junos](docs/junos-verified.md) says what that
  covers, and which failed command Junos 23.4 does not report.

## RFC support

| RFC | Status |
| --- | --- |
| [RFC 6241](https://www.rfc-editor.org/rfc/rfc6241.html) NETCONF | supported |
| [RFC 6242](https://www.rfc-editor.org/rfc/rfc6242.html) NETCONF over SSH | supported |
| [RFC 7589](https://www.rfc-editor.org/rfc/rfc7589.html) NETCONF over TLS | supported |
| [RFC 5277](https://www.rfc-editor.org/rfc/rfc5277.html) Event notifications | supported |
| [RFC 5717](https://www.rfc-editor.org/rfc/rfc5717.html) Partial lock | supported |
| [RFC 6243](https://www.rfc-editor.org/rfc/rfc6243.html) With-defaults | supported |
| [RFC 8071](https://www.rfc-editor.org/rfc/rfc8071.html) Call home | not implemented |

## Development

```bash
make help                   # every target
make check                  # build, unit tests, and lint
make test TESTFLAGS=-count=1 # rerun tests without cached results
make fuzz                   # every fuzz target, 10 seconds each
make integration-netopeer2  # the integration suite against netopeer2 in Docker
make integration-test       # the same suite against a device of yours
```

- [CONTRIBUTING.md](.github/CONTRIBUTING.md): how to work on the library and what a
  pull request needs
- [SECURITY.md](.github/SECURITY.md): how to report a vulnerability
- [CONTEXT.md](CONTEXT.md) and [docs/adr](docs/adr): the vocabulary of the
  project and its recorded design decisions

## Attribution and license

This project is derived from [nemith/netconf](https://github.com/nemith/netconf)
by Brandon Bennett, itself derived from Juniper Networks' go-netconf.
BSD 2-Clause licensed, see [LICENSE](LICENSE).
