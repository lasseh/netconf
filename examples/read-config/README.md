# Read running configuration

[Source](main.go)

The read-config example reads the running configuration with
`rpc.GetConfig` and prints it. It changes nothing and works on any
NETCONF device, with an account that may run `<get-config>`.

Follow the [common setup](../README.md#common-setup), then run:

```bash
go run ./examples/read-config -host router1.example.net -user automation
```

The read has the runner's 30-second timeout, which no flag changes. The
[stream-config example](../stream-config/README.md) handles slower reads.

## Filter a subtree

`-filter` names a file with a subtree filter in the device's data
model. See
[Read and filter configuration](../../docs/standard-netconf.md#read-and-filter-configuration).

```bash
go run ./examples/read-config -host router1.example.net -filter interfaces.xml
```

## Output and failures

The output is the content of `<data>`, which can hold several top-level
elements and namespace prefixes declared outside it. Do not treat it as
one XML document or a loadable file. It can contain password hashes and
keys, so write it to a new file under `umask 077` in a subshell. On
Junos, [Junos text normalization](../../docs/junos.md#non-ascii-text)
applies.

Unless only the close failed, treat the output of a failed run as
incomplete.

A device with NETCONF access control, RFC 8341, silently leaves out what
the account may not read. Exit status 0 does not prove the output is
complete.
