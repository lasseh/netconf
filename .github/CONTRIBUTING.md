# Contributing

## Issues

Bugs, feature requests and questions go to the
[issues](https://github.com/lasseh/netconf/issues) of the repository. A bug
report is most useful with the device and its software version, the
operation as sent and the reply as received.

Issues are public and device output holds whole configurations: remove
password hashes, keys, community strings, serial numbers and addresses
before pasting any. A security problem is reported in private, see
[SECURITY.md](SECURITY.md).

## Workflow

Everything goes through Make:

```bash
make build   # compile, including the integration tests
make test    # unit tests with the race detector
make lint    # golangci-lint
make vulncheck
make fuzz    # every fuzz target for FUZZTIME (default 10s) each
```

A change is ready when `make build test lint` is clean. `make help` lists
the other targets.

The targets need the current Go release and
[golangci-lint](https://golangci-lint.run) v2, `make vulncheck` needs
[govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) and
`make integration-netopeer2` Docker. `make test` uses
[gotestsum](https://github.com/gotestyourself/gotestsum) when it is
installed.

## Pull requests

- One logical change per pull request.
- CI runs the lint, the unit tests, the vulnerability scan and the
  integration suite against netopeer2. `make junos-schema` runs once a
  week, and for a change to `internal/cmd/junosschema`, to the Makefile or
  to its workflow.
- What a user of the library can notice gets an entry under Unreleased in
  [CHANGELOG.md](../CHANGELOG.md). The API is pre-1.0, so an exported name may
  change; the entry under Changed says how to move over.

## Tests

- Test behaviour through the public interface. Session tests that need a
  device use `internal/netconftest`: `Transport` scripts whole messages,
  `Wire` scripts raw bytes over the real framer.
- A transport implementation must pass `transport/transporttest`.
- Device quirks are pinned with bytes captured from the device; say in the
  test which device and version they came from, and remove secrets, serial
  numbers and addresses from the capture.
- Code that reads what a device sent (framing, the receive path, reply
  parsing) has a fuzz target stating what must hold for any input. The
  seeds run in `make test`; add one for every bug found in that code.
- `make integration-test` runs the integration suite against a real device.
  It reads, and loads or commits only when asked to, see
  [integration/README.md](../integration/README.md).
- `make junos-schema` holds what package `junos` reads from a reply against
  the YANG modules of every Junos release. It needs git and github.com. An
  element that a decoder there starts to read goes into `replies` in
  `internal/cmd/junosschema` as well, see
  [From one Junos release to the next](../docs/junos-verified.md#from-one-junos-release-to-the-next).

## Conventions

- Use the vocabulary in [CONTEXT.md](../CONTEXT.md).
- Decisions that are hard to reverse or surprising get an ADR in
  [docs/adr](../docs/adr); read the existing ones before changing the
  transport contract or reply delivery.
- Commits: `<type>: <description>`, imperative, lowercase, no period, with
  type one of `feat|fix|refactor|test|docs|chore|ci|build`.
