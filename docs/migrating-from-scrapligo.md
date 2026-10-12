# Migrating from scrapligo

This maps the scrapligo NETCONF driver (`github.com/scrapli/scrapligo/driver/netconf`)
to this library.

## Connecting

| scrapligo | netconf |
| --- | --- |
| `netconf.NewDriver(host, opts...)` + `driver.Open()` | `ncssh.DialHost(ctx, host, opts...)` + `netconf.Open(ctx, tr)` |
| `options.WithPort(830)` | `ncssh.WithPort(830)` (830 is the default) |
| `options.WithAuthUsername(u)` | `ncssh.WithUser(u)` |
| `options.WithAuthPassword(p)` | `ncssh.WithPassword(p)` |
| `options.WithAuthPrivateKey(file, passphrase)` | `ncssh.WithPrivateKeyFile(file, passphrase)` |
| `options.WithAuthNoStrictKey()` | `ncssh.WithInsecureIgnoreHostKey()` |
| `options.WithSSHConfigFile(path)` | `ncssh.WithSSHConfig(path)` |
| `options.WithTransportType("standard")` | not needed, there is one SSH transport |
| `options.WithTimeoutSocket(d)` | a deadline on the context passed to `DialHost` and `Open` |
| `options.WithTimeoutOps(d)` | `netconf.WithOperationTimeout(d)`, or a deadline on the context of one operation |
| `driver.Close()` | `session.Close(ctx)` |
| `driver.SessionID()` | `session.SessionID()` |
| `driver.ServerCapabilities()` | `session.ServerCaps()` |

Differences to be aware of:

- Host keys are verified against `~/.ssh/known_hosts` by default. scrapligo
  code that passed `WithAuthNoStrictKey()` needs `WithInsecureIgnoreHostKey()`
  or, better, a known_hosts file.
- `WithSSHConfig` honours `HostName`, `User`, `IdentityFile`,
  `UserKnownHostsFile` and `ProxyJump`. It does not take `Port` from the ssh
  config (that is the login port, not the NETCONF port) and rejects hosts
  that need `ProxyCommand`.
- What that changes depends on the scrapligo transport you came from.
  Without `WithTransportType`, scrapligo uses its `system` transport, which
  runs the OpenSSH binary with `-F <file>`: every ssh config directive
  works there. Other directives than the ones above are ignored here.
  scrapligo's `standard` transport never reads the ssh config file, so code
  on that transport loses nothing.
- Under the `system` transport a jump host is logged in to by a second
  `ssh`, which reads the same config file and none of scrapligo's options.
  Part of that is the same here:
  - The same: a jump host takes `HostName`, `User`, `Port`, `IdentityFile`
    and `UserKnownHostsFile` from its own block in the ssh config, else the
    OS user, port 22, the ssh-agent, the default identity files and
    `~/.ssh/known_hosts`. Nothing said about the device applies to it:
    scrapligo's `WithAuthNoStrictKey` does not reach it, and neither does
    `WithInsecureIgnoreHostKey` here.
  - Different: `StrictHostKeyChecking` is not read. A jump host whose key
    is not among the known hosts is refused, also where the config says
    `no` or `accept-new` and `ssh` adds it on first contact. Log in to it
    with `ssh` once from the account that runs the program, or name a file
    with its key with `WithKnownHosts`.
  - Not read either: `HostKeyAlias`, `GlobalKnownHostsFile`, the
    system-wide `/etc/ssh/ssh_known_hosts` and `~/.ssh/known_hosts2` that
    `ssh` reads by default, `IdentitiesOnly`, and a `ProxyJump` in the block
    of a jump host itself: write the chain out, `ProxyJump first,second`.
- Without a password or key file option, the ssh-agent and the identity
  files (`IdentityFile` from the ssh config, else `~/.ssh/id_*`) are tried.
- Devices that need legacy SSH algorithms: set them with
  `ncssh.WithClientConfig`.

## Operations

scrapligo returns a `*response.NetconfResponse` and expects the caller to
check `Failed`. Here an `<rpc-error>` is the returned `error`.

| scrapligo | netconf |
| --- | --- |
| `driver.RPC(opoptions.WithFilter(xml))` then `r.Result` | `session.ExecReply(ctx, xml)` then `reply.Raw` |
| `r.Failed != nil` | `err != nil`; `errors.As` with a `netconf.RPCErrors` for the details |
| `driver.GetConfig("running")` | `rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)` |
| `driver.GetConfig(src, opoptions.WithFilter(f))` | `rpc.GetConfig{Source: src, Filter: rpc.SubtreeFilter(f)}.Exec(ctx, session)` |
| `driver.EditConfig("candidate", cfg)` | `rpc.EditConfig{Target: rpc.Candidate, Config: cfg}.Exec(ctx, session)` |
| `driver.Lock("candidate")` / `Unlock` | `rpc.Lock{Target: rpc.Candidate}.Exec(ctx, session)` / `rpc.Unlock{...}` |
| `driver.Validate("candidate")` | `rpc.Validate{Source: rpc.Candidate}.Exec(ctx, session)` |
| `driver.Commit()` | `rpc.Commit{}.Exec(ctx, session)` |
| `driver.Discard()` | `rpc.DiscardChanges{}.Exec(ctx, session)` |

`reply.Raw` is the complete `<rpc-reply>` document, like scrapligo's
`Result`, so structs that were unmarshalled from `Result` keep working.
`Raw` keeps the white space around the document and a leading XML
declaration, which scrapligo trims. From a Junos device it has text that is
not ASCII as characters, where the device sent a character reference for
every byte of one (`&#195;&#184;` for `ø`).

scrapligo sets `Failed` when the reply contains any `<rpc-error>`, warnings
included. Here warnings do not fail the operation; they are in
`reply.Warnings`. If your code relied on warnings aborting a change, check
`len(reply.Warnings)` yourself.

The typed operations that return no data (`rpc.EditConfig`, `rpc.Commit`,
`rpc.Lock`, ...) additionally require `<ok/>` in the reply when run with
their `Exec` method. scrapligo does not. Passing the operation to
`session.ExecReply` instead skips that check.
`session.Exec(ctx, xml, &v)` does the unmarshalling in the same call.

`rpc.GetConfig.Exec` returns the content of `<data>`, not the whole reply.

### Junos

| scrapligo | netconf |
| --- | --- |
| `RPC(WithFilter("<get-configuration format=\"text\"/>"))` + unwrapping `<configuration-text>` | `junos.GetConfiguration{Format: junos.FormatText}.Exec(ctx, session)` |
| `RPC(WithFilter("<get-configuration compare=\"rollback\" rollback=\"1\" format=\"text\"/>"))` | `junos.CompareRollback{Rollback: 1}.Exec(ctx, session)` |
| `RPC(WithFilter("<command>show version</command>"))` | `junos.Command("show version").XML(ctx, session)` for the same XML reply, `.Exec` for text, `.JSON` for JSON |

`junos.GetConfiguration` reads the active configuration unless `Candidate`
is set. A bare `<get-configuration/>` reads the candidate, with whatever is
uncommitted in it.

For changes there is `junos.Change`: lock, load (set commands, text, XML,
JSON), diff, commit (with a comment, confirmed) and unlock in one call that
gives the candidate back on every way out, see
[Changing configuration](junos.md#changing-configuration). Underneath are
`junos.LoadConfiguration` and `junos.CommitConfiguration`. A failed load or
commit returns an error that names the statement and its place in the
configuration.

## Concurrency and lifetime

- A session is safe for concurrent use; the mutex scrapligo wrappers put
  around the driver is not needed.
- Every operation takes a context. Cancelling it abandons that operation
  only; the session stays usable.
- `session.Done()` is closed when the session ends and `session.Err()` says
  why, so an `isOpen` flag is not needed. Add
  `ncssh.WithKeepalive(interval, maxMissed)` so a silently dead peer ends
  the session instead of hanging operations until their deadline.

## Parsing a result

A struct named for the payload element (`<system-information>`,
`<interface-information>`) cannot be unmarshalled from a whole
`<rpc-reply>`: `xml.Unmarshal` fails on the envelope. Take the element out
of the reply first, with `Reply.Element` or `junos.Decode`:

```go
reply, err := session.ExecReply(ctx, "<get-system-information/>")
if err != nil {
	return err
}
el, err := reply.Element("system-information")
if err != nil {
	return err
}
var info SystemInformation // XMLName is system-information
err = el.Decode(&info)
```

That also reads the reply of a device that wraps the element in a
routing-engine item.

## A minimal wrapper

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
