# Contributing

This page says where to report a problem, which checks a change has to
pass, and the conventions for tests, documentation and commits.

## Issues

Report bugs, feature requests and questions in the
[issues](https://github.com/lasseh/netconf/issues).
[Collecting a useful report](../docs/troubleshooting.md#collecting-a-useful-report)
lists what a bug report needs besides `go version`.

Issues are public, and device output holds whole configurations. Before you
paste any, remove password hashes, keys, SNMP communities, serial numbers
and addresses. Report a security problem in private, see
[SECURITY.md](SECURITY.md).

GitHub issues are the public tracker. The local `.scratch/` files that
[the agent issue-tracker guide](../docs/agents/issue-tracker.md) describes
are planning notes for agents. They do not replace public bug reports or
feature requests.

## Workflow

Every check runs through Make, and `make help` lists the targets:

| Target | What it does |
| --- | --- |
| `make build` | Compiles every package and vets it with the `integration` tag |
| `make build-linux` | Cross-compiles for Linux on amd64 and 32-bit 386 |
| `make test` | Runs the unit tests with the race detector. `TESTFLAGS` adds flags, such as `TESTFLAGS=-count=1` to skip cached results |
| `make lint` | Runs golangci-lint |
| `make docs-check` | Checks local inline links, image sources and ATX heading anchors in Markdown, not external URLs or Markdown extensions |
| `make check` | Runs `build`, `test`, `lint` and `docs-check` |
| `make tidy` | Fails when `go.mod` or `go.sum` is not tidy |
| `make vulncheck` | Scans for known vulnerabilities with govulncheck |
| `make actionlint` | Lints the GitHub Actions workflows |
| `make fuzz` | Runs every fuzz target for `FUZZTIME` each, 10s by default |
| `make coverage` | Writes unit-test coverage to `coverage.out` and `coverage.html` |
| `make fix` | Runs `go fix ./...` |
| `make clean` | Removes the coverage files |
| `make integration-test` | Runs the integration suite against the device in `NETCONF_DUT_*`, see [integration/README.md](../integration/README.md) |
| `make integration-netopeer2` | Runs the integration suite against netopeer2 in Docker |
| `make junos-schema` | Compares what package `junos` reads with the YANG modules of every Junos release, see [Tests](#tests) |

A change is ready when `make check`, `make tidy` and `make build-linux` are
clean.

The targets need the Go version that `go.mod` requires. CI uses the latest
stable release. `make lint` needs [golangci-lint](https://golangci-lint.run)
v2, `make vulncheck` needs
[govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck),
`make actionlint` needs [actionlint](https://github.com/rhysd/actionlint),
and `make integration-netopeer2` needs Docker with the Compose plugin.
`make junos-schema` needs git and access to github.com. `make test` uses
[gotestsum](https://github.com/gotestyourself/gotestsum) when it is
installed.

## Pull requests

- One logical change per pull request.
- On every pull request, every push to `main` and once a week, CI runs the
  targets `build`, `build-linux`, `test`, `tidy`, `docs-check`, `lint`,
  `vulncheck`, `actionlint` and `integration-netopeer2`.
- `make junos-schema` runs once a week, and on a pull request that changes
  `internal/cmd/junosschema`, the Makefile or its workflow.
- `make fuzz` runs every night for five minutes per target, starting from
  the inputs earlier runs found, and keeps a failing input in the artifact
  `fuzz-failures`. A pull request that changes the Makefile or the fuzz
  workflow runs it for ten seconds per target.
- A change that a user can notice gets an entry under Unreleased in
  [CHANGELOG.md](../CHANGELOG.md). The API is pre-1.0, so an exported name
  can change, and the entry under Changed then says how to migrate.

## Tests

- Test behavior through the public interface. Session tests that need a
  device use `internal/netconftest`. `netconftest.Transport` scripts whole
  messages. `netconftest.Wire` scripts raw bytes over the real framer, for
  when framing or the position of a byte matters.
- Every transport implementation must pass `transport/transporttest`.
- Where parsing can run offline, add an `Example` function with an
  `// Output:` comment, which `make test` checks.
- Pin a device quirk with bytes captured from the device. Name the device
  and its software version in the test, and clean the capture as
  [Issues](#issues) describes.
- Code that reads what a device sent, such as framing, the receive path and
  reply parsing, has a fuzz target that states what must hold for any
  input. `make test` runs the seeds. Add a seed for every bug found in that
  code.
- Where a test needs a real device, add it to the integration suite, see
  [integration/README.md](../integration/README.md).
- An element that a decoder in package `junos` starts to read also goes
  into `replies` in `internal/cmd/junosschema`, so that `make junos-schema`
  checks it against every Junos release, see
  [From one Junos release to the next](../docs/junos-verified.md#from-one-junos-release-to-the-next).

## Conventions

- Use the vocabulary in [CONTEXT.md](../CONTEXT.md).
- [Architecture](../docs/architecture.md) maps the request and reply paths.
  Read the ADRs in [docs/adr](../docs/adr) before you change the transport
  contract or reply delivery, and add one for a decision that is hard to
  reverse or surprising.
- Put a runnable use case in `examples/<use-case>/main.go`, with a short
  `README.md` that gives prerequisites, commands, expected results and
  failure handling. Link to the source instead of copying a whole program
  into Markdown. Unit tests must not run a live-device example. Its parsing
  and error-handling helpers get offline tests.
- Guides and doc comments describe current behavior. Design history goes
  in ADRs, and release changes go in the changelog. Link a detailed
  investigation instead of repeating it in a new changelog entry.
- Commit messages follow `<type>: <description>`, imperative, lowercase and
  without a period, with an optional scope as in
  `test(integration): ...`. The type is one of `feat`, `fix`, `refactor`,
  `test`, `docs`, `chore`, `ci` or `build`.
