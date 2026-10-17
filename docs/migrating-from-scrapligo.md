# Migrating from scrapligo

Read this page when you port code from scrapligo's NETCONF driver,
`github.com/scrapli/scrapligo/driver/netconf`. Each section maps scrapligo
v1.3.3 calls to this library and lists the differences.

scrapligo's driver package is also named `netconf`, so import one under
an alias. In the tables, `options` and `opoptions` are scrapligo's
`driver/options` and `driver/opoptions`.

## Connecting

| scrapligo | lasseh/netconf |
| --- | --- |
| `netconf.NewDriver(host, opts...)` + `driver.Open()` | `ncssh.DialHost(ctx, host, opts...)` + `netconf.Open(ctx, tr)` |
| `options.WithPort(n)` | `ncssh.WithPort(n)` |
| `options.WithAuthUsername(u)` | `ncssh.WithUser(u)` |
| `options.WithAuthPassword(p)` | `ncssh.WithPassword(p)` |
| `options.WithAuthPrivateKey(file, passphrase)` | `ncssh.WithPrivateKeyFile(file, passphrase)` |
| `options.WithAuthNoStrictKey()` | `ncssh.WithInsecureIgnoreHostKey()` |
| `options.WithSSHKnownHostsFile(file)` | `ncssh.WithKnownHosts(file)` |
| `options.WithSSHKnownHostsFileSystem()` | No option. `DialHost` reads `~/.ssh/known_hosts` by default |
| `options.WithSSHConfigFile(path)` | `ncssh.WithSSHConfig(path)` |
| `options.WithSSHConfigFileSystem()` | `ncssh.WithSSHConfig("")` for `~/.ssh/config`, never `/etc/ssh/ssh_config` |
| `options.WithTransportType("standard")` or `"system"`, the default | `ncssh.DialHost`, or `nccmd.Start` to keep OpenSSH |
| `options.WithStandardTransportExtraCiphers(c)`, `options.WithStandardTransportExtraKexs(k)` | `ncssh.WithClientConfig(fn)`, where `fn` sets `Ciphers` and `KeyExchanges` |
| `options.WithTimeoutSocket(d)` | A deadline on the context of `ncssh.DialHost` |
| `options.WithTimeoutOps(d)` | `netconf.WithOperationTimeout(d)`, or a deadline on one operation's context |
| `options.WithNetconfPreferredVersion("1.0")` or `"1.1"` | `netconf.WithCapability(netconf.CapNetConf10)` or `CapNetConf11` |
| `driver.Close()` | `session.Close(ctx)` |
| `driver.SessionID()` | `session.SessionID()` |
| `driver.ServerHasCapability(c)` | `session.ServerCaps().Has(c)` |
| `driver.ServerCapabilities()` | `slices.Collect(session.ServerCaps().All())`, in no fixed order |

### Defaults that differ

- **Port.** scrapligo connects to port 22, `ncssh.DialHost` to 830, so
  code that relied on 22 needs `ncssh.WithPort(22)`.
- **Timeouts.** scrapligo gives up on connecting after 30 seconds and on an
  operation after 60. `ncssh.DialHost` and operations have none, see
  [Timeouts](operations.md#timeouts).
- **Keepalive.** scrapligo's `system` transport passes its socket timeout,
  30 seconds by default, to OpenSSH as `ServerAliveInterval`.
  `ncssh.DialHost` sends none without `ncssh.WithKeepalive`.
  `WithKeepalive(30*time.Second, 3)` matches the `system` default, as
  OpenSSH gives up after three missed probes by default.
- **Host keys.** scrapligo's `standard` transport refuses to connect when
  the check is on and no `WithSSHKnownHostsFile` is given.
  `ncssh.DialHost` reads `~/.ssh/known_hosts`, see
  [Host keys](connecting.md#host-keys).
- **Logins.** Without a password or key, `ncssh.DialHost` tries the
  ssh-agent and the default identity files, like OpenSSH under `system`.
  The `standard` transport tries neither, see
  [Authentication](connecting.md#authentication).
- **Paths.** scrapligo expands a leading `~/` in `WithSSHConfigFile` and
  `WithSSHKnownHostsFile`. The `ncssh` options do not, so use
  `os.UserHomeDir`, or `WithSSHConfig("")` for `~/.ssh/config`.

### The SSH config and jump hosts

With `WithSSHConfigFile`, the `system` transport runs OpenSSH with
`-F <file>`, where every directive works except those scrapligo sets on the
command line, such as `Port`. Without it, scrapligo passes `-F /dev/null`.
The `standard` transport reads no SSH config.

`ncssh.WithSSHConfig` reads a few directives, refuses `ProxyCommand`, and
fails every dial on a `Match` that starts with a criterion other than
`host` or `all`. See [The SSH config](connecting.md#the-ssh-config).

Under the `system` transport, OpenSSH logs in to a jump host with the SSH
config and no scrapligo option. `ncssh` reads the jump host's block too,
but refuses an unknown jump host key whatever `StrictHostKeyChecking` says,
and a `ProxyJump` in the first jump host's block. See
[Jump hosts](connecting.md#jump-hosts).

### The system transport

scrapligo's default `system` transport runs the `ssh` program.
`ncssh.DialHost` replaces it. For `ProxyCommand`, jump hosts chained in the
SSH config, connection sharing, Kerberos or a hardware token that no agent
offers, run `ssh` through `nccmd.Start`:

```go
tr, err := nccmd.Start(exec.Command("ssh", "-s", "-p", "830", "router1.example.net", "netconf"))
if err != nil {
	return err
}
session, err := netconf.Open(ctx, tr)
```

Arguments of `WithSystemTransportOpenArgs` go on that command line.
`nccmd` gives `ssh` no terminal for a password, so use `ncssh.DialHost` for
password logins. See [System OpenSSH](connecting.md#system-openssh).

## Operations

scrapligo returns a `*response.NetconfResponse` and leaves the caller to
check `Failed`. Here every operation returns an `error`. Each `rpc` value
below runs with `.Exec(ctx, session)`.

| scrapligo | lasseh/netconf |
| --- | --- |
| `driver.RPC(opoptions.WithFilter(xml))`, then `r.Result` | `session.ExecReply(ctx, xml)`, then `reply.Raw` |
| `driver.RPC(...)`, then `xml.Unmarshal([]byte(r.Result), &v)` | `session.Exec(ctx, xml, &v)`, where `v` matches the whole `<rpc-reply>` |
| `r.ErrorMessages` | `errors.As(err, &rpcErrs)` with `var rpcErrs netconf.RPCErrors` |
| `r.WarningErrorMessages` | `reply.Warnings`, from `session.ExecReply` |
| `driver.Get(f)` | `rpc.Get{Filter: rpc.SubtreeFilter(f)}` |
| `driver.GetConfig("running", opoptions.WithFilter(f))` | `rpc.GetConfig{Source: rpc.Running, Filter: rpc.SubtreeFilter(f)}` |
| `opoptions.WithFilterType("xpath")` with `opoptions.WithFilter(x)` | `Filter: rpc.XPathFilter(x, namespaces)` |
| `opoptions.WithDefaultType("report-all")` | `WithDefaults: rpc.DefaultsReportAll` in `rpc.GetConfig` |
| `driver.EditConfig("candidate", cfg)` | `rpc.EditConfig{Target: rpc.Candidate, Config: cfg}`, with `cfg` stripped of its `<config>` element, or the device gets `<config><config>` |
| `driver.CopyConfig("running", "startup")` | `rpc.CopyConfig{Source: rpc.Running, Target: rpc.Startup}` |
| `driver.DeleteConfig("startup")` | `rpc.DeleteConfig{Target: rpc.Startup}` |
| `driver.Lock("candidate")` / `Unlock` | `rpc.Lock{Target: rpc.Candidate}` / `rpc.Unlock{...}` |
| `driver.Validate("candidate")` | `rpc.Validate{Source: rpc.Candidate}` |
| `driver.Commit()` | `rpc.Commit{}` |
| `driver.Commit(opoptions.WithCommitConfirmed(), opoptions.WithCommitConfirmTimeout(600))` | `rpc.Commit{Confirmed: true, ConfirmTimeout: 600 * time.Second}` |
| `opoptions.WithCommitConfirmedPersist(p)`, `opoptions.WithCommitConfirmedPersistID(id)` | `Persist: p`, `PersistID: id` in `rpc.Commit` |
| `driver.Discard()` | `rpc.DiscardChanges{}` |

`rpc.Commit.ConfirmTimeout` is a `time.Duration`, not whole seconds as in
scrapligo. `ConfirmTimeout: 600` compiles, means 600 nanoseconds and goes
out as `<confirm-timeout>1</confirm-timeout>`, rounded up to the next
second.

Like `Result`, `reply.Raw` is the complete `<rpc-reply>`, so structs
decoded from `Result` keep working. `Raw` keeps the white space around the
document and a leading XML declaration, which scrapligo trims. From a Junos
device, `Raw` has Junos text normalization applied, see
[Non-ASCII text](junos.md#non-ascii-text).

scrapligo sets `Failed` on any `<rpc-error>`, warnings included, and
ignores the Junos `<xnm:error>`. Here a warning never fails an operation
and an `<xnm:error>` does, see
[Errors](operations.md#errors). To stop on warnings, check
`reply.Warnings`, see [Where warnings go](operations.md#where-warnings-go).

The `rpc` operations that return no data fail in `Exec` when the reply has
no `<ok/>`. scrapligo and `session.ExecReply` do not check for it.
`rpc.Get.Exec` and `rpc.GetConfig.Exec` return the content of `<data>`,
not the whole reply.

## Junos

| scrapligo | lasseh/netconf |
| --- | --- |
| `driver.RPC(opoptions.WithFilter("<get-configuration format=\"text\"/>"))`, then unwrapping `<configuration-text>` | `junos.GetConfiguration{Format: junos.FormatText}.Exec(ctx, session)` |
| `driver.RPC(opoptions.WithFilter("<get-configuration compare=\"rollback\" rollback=\"1\" format=\"text\"/>"))` | `junos.CompareRollback{Rollback: 1}.Exec(ctx, session)` |
| `driver.RPC(opoptions.WithFilter("<command>show version</command>"))` | `junos.Command("show version").XML(ctx, session)` for the same XML reply, `.Exec` for text, `.JSON` for JSON |

`junos.GetConfiguration` reads the active configuration unless `Candidate`
is set. The scrapligo `<get-configuration>` above names no database, so
Junos reads the candidate, see
[Configuration and diffs](junos.md#configuration-and-diffs).

`junos.Change` replaces lock, edit-config, commit and unlock with one call,
see [Changing configuration](junos.md#changing-configuration) and
[Change safety](junos.md#change-safety).

## Notifications

scrapligo stores notifications until `driver.GetSubscriptionMessages(id)`
collects them. Here the session pushes each one to the handler given to
`netconf.Open` with `netconf.WithNotifHandler`. `rpc.CreateSubscription`
subscribes to an RFC 5277 stream. For scrapligo's
`EstablishPeriodicSubscription`, send the YANG-push
`<establish-subscription>` as XML with `session.ExecReply`, on a session
opened with `netconf.WithNotifHandler`. See
[Notifications](sessions.md#notifications).

## Concurrency and lifetime

- A `netconf.Session` is safe for concurrent use, so drop the mutex around
  each call. That covers single operations, not sequences, see
  [Requests from several goroutines](sessions.md#requests-from-several-goroutines).
- An operation whose context ends leaves the session open, and the device
  may still carry out the request, see
  [Canceled operations](operations.md#canceled-operations).

## Parsing a result

A struct named for the payload element, such as
`<system-uptime-information>`, cannot be decoded from a whole
`<rpc-reply>`. Take the element out first:

```go
reply, err := session.ExecReply(ctx, "<get-system-uptime-information/>")
if err != nil {
	return err
}
el, err := reply.Element("system-uptime-information")
if err != nil {
	return err
}
var uptime SystemUptimeInformation // your struct, XMLName system-uptime-information
if err := el.Decode(&uptime); err != nil {
	return err
}
```

`Reply.Element` finds the element at any depth, see
[Finding the result in a reply](operations.md#finding-the-result-in-a-reply).

## A minimal wrapper

A typical scrapligo helper pair, ported:

```go
func Open(ctx context.Context, host, user, keyFile string) (*netconf.Session, error) {
	tr, err := ncssh.DialHost(ctx, host,
		ncssh.WithUser(user),
		ncssh.WithPrivateKeyFile(keyFile, ""),
		ncssh.WithKeepalive(15*time.Second, 3),
	)
	if err != nil {
		return nil, fmt.Errorf("netconf: connecting to %s: %w", host, err)
	}
	return netconf.Open(ctx, tr, netconf.WithOperationTimeout(time.Minute))
}

func InterfaceInfo(ctx context.Context, s *netconf.Session) ([]byte, error) {
	reply, err := s.ExecReply(ctx, "<get-interface-information><extensive/></get-interface-information>")
	if err != nil {
		return nil, err
	}
	return reply.Raw, nil
}
```
