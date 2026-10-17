# Standard NETCONF

Read this page to use the standard operations of package `rpc`. For Junos
configuration changes, use
[`junos.Change`](junos.md#changing-configuration) instead.

- [Check capabilities](#check-capabilities)
- [Read and filter configuration](#read-and-filter-configuration)
- [Choose a datastore](#choose-a-datastore)
- [Edit the candidate safely](#edit-the-candidate-safely)
- [Confirmed commits](#confirmed-commits)
- [Recover from an uncertain write](#recover-from-an-uncertain-write)

## Check capabilities

The operations of package `rpc` never check the device's capabilities.
Check them yourself before you send:

```go
if !session.ServerCaps().Has(":candidate:1.0") {
	return errors.New("device has no candidate datastore")
}
```

| Operation or option | Required capability |
| --- | --- |
| `rpc.EditConfig` on the candidate, `rpc.Commit`, `rpc.DiscardChanges` | `:candidate:1.0` |
| `rpc.EditConfig` on running | `:writable-running:1.0` |
| `rpc.Startup` as a source or target | `:startup:1.0` |
| `rpc.Validate` | `:validate:1.0` or `:validate:1.1` |
| `rpc.EditConfig{TestOption: rpc.TestOnly}` | `:validate:1.1`, which Junos 26.2 does not advertise |
| `rpc.EditConfig{TestOption: rpc.TestThenSet}` or `rpc.SetOnly` | `:validate:1.0` or `:validate:1.1` by RFC 6241 section 8.6.1. Sections 7.2 and 8.6.5.1 name only 1.1 |
| `rpc.EditConfig{ErrorOption: rpc.RollbackOnError}` | `:rollback-on-error:1.0` |
| `rpc.XPathFilter` | `:xpath:1.0` |
| `rpc.URL` as a source or target | `:url:1.0` with the URL's scheme in `scheme` |
| `rpc.Commit{Confirmed: true}` | `:confirmed-commit:1.0` or `:confirmed-commit:1.1` |
| `rpc.Commit.Persist`, `rpc.Commit.PersistID`, `rpc.CancelCommit` | `:confirmed-commit:1.1` |
| `rpc.PartialLock`, `rpc.PartialUnlock` | `:partial-lock:1.0` (RFC 5717, running only) |
| `WithDefaults` on `rpc.GetConfig`, `rpc.Get` or `rpc.CopyConfig` | `:with-defaults:1.0`, with the modes in `basic-mode` and `also-supported` |
| `rpc.CreateSubscription` | `:notification:1.0` |
| Other operations on a session with a subscription | `:interleave:1.0`. Junos 26.2 also needs `notification interleave`, see [Notifications](sessions.md#notifications) |

`CapabilitySet.Has` takes a short name under
`urn:ietf:params:netconf:capability:`, such as `:candidate:1.0`, and
`netconf.CapNetConf10` or `netconf.CapNetConf11` for the base versions.
Each version is a capability of its own, so check both versions of
`:validate` when either will do.

Given a name without parameters, `Has` matches any parameters. Given
parameters, it needs the exact advertised string, so
`Has(":url:1.0?scheme=http")` is false against `scheme=http,ftp,file`.
Read parameters from `All` instead:

```go
const urlCap = "urn:ietf:params:netconf:capability:url:1.0?"

for c := range session.ServerCaps().All() {
	if params, ok := strings.CutPrefix(c, urlCap); ok {
		fmt.Println(params) // scheme=http,ftp,file
	}
}
```

## Read and filter configuration

`rpc.GetConfig` reads configuration, and `rpc.Get` adds state data. Their
`Exec` methods return the content of `<data>`. A subtree filter names the
elements to return, in the namespace of the device's data model:

```go
data, err := rpc.GetConfig{
	Source: rpc.Running,
	Filter: rpc.SubtreeFilter(`<interfaces xmlns="urn:ietf:params:xml:ns:yang:ietf-interfaces"/>`),
}.Exec(ctx, session)
```

On Junos, name elements under `<configuration>` with no namespace, such
as `<configuration><system><services/></system></configuration>`. Junos
26.2 and Junos OS Evolved 26.2 returned that subtree, with and without
`rfc-compliant`.

A filter that matches nothing, such as one in a namespace the device does
not implement, can return empty data instead of an error.

`rpc.XPathFilter(path, namespaces)` builds an XPath filter. Junos 26.2
does not advertise `:xpath:1.0`. Declare every prefix the path uses in
`namespaces`:

```go
filter := rpc.XPathFilter("/if:interfaces/if:interface[if:name='eth0']",
	map[string]string{"if": "urn:ietf:params:xml:ns:yang:ietf-interfaces"})
```

For Junos operational data, see
[Junos](junos.md#reading-configuration-and-operational-data).

## Choose a datastore

- `rpc.Running` is the configuration in effect. An edit there takes effect
  at once.
- `rpc.Candidate` holds changes until `rpc.Commit` applies them or
  `rpc.DiscardChanges` drops them. Assume other sessions can change it.
  Junos also offers a private copy, `junos.Change.Private`.
- `rpc.Startup` is the configuration the device loads at boot. A commit
  does not save running to startup.

`rpc.CopyConfig` replaces the whole target with its source, so treat it as
a write, even from running to startup.

## Edit the candidate safely

The [standard-edit example](../examples/standard-edit/README.md) runs this
sequence.

1. Check for `:candidate:1.0`, and for `:validate:1.0` or `:validate:1.1`.
2. Lock the candidate with `rpc.Lock{Target: rpc.Candidate}`. When the lock
   times out, you cannot tell whether you hold it, so close the session.
3. Check the candidate for changes made before yours, and never discard or
   commit them. Junos 26.2 refuses to lock such a candidate with
   `junos.ErrCandidateModified`, but a device may grant the lock anyway.
4. Edit with a payload in the device's data model. A failed edit can leave
   its accepted statements in the candidate, unless the device advertises
   `:rollback-on-error:1.0` and the edit sets `rpc.RollbackOnError`.
5. Validate with `rpc.Validate{Source: rpc.Candidate}`, then commit with
   `rpc.Commit`. A plain commit is permanent, unlike a
   [confirmed commit](#confirmed-commits).
6. After a failed edit or validation, or a commit refused with an RPC
   error, drop your edits with `rpc.DiscardChanges`. It drops everything
   in the candidate, which step 3 made safe. Run cleanup under a fresh
   context with its own deadline.
7. If the commit outcome is unknown or the discard failed, send nothing
   more and close the session to release the lock. Otherwise unlock the
   candidate. Report cleanup failures, and close the session on every exit.

Run one such sequence at a time per session, because two would share its
lock and candidate. Standard NETCONF has no diff operation, so compare
the candidate with running in code that knows the device's data model.

On Junos, use [`junos.Change`](junos.md#changing-configuration), which
handles the diff, the cleanup and an unknown commit outcome. No recorded
run has sent `<validate>` there during a pending confirmed commit, so
treat `rpc.Validate` as a [commit check](#confirmed-commits) and first
check with `junos.GetCommitInformation` that no commit is `Pending`.

## Confirmed commits

`rpc.Commit{Confirmed: true, ConfirmTimeout: 5*time.Minute}` makes a
confirmed commit, which the device takes back unless a later commit
confirms it in time. A `ConfirmTimeout` of zero leaves the time to the
device, 600 seconds by RFC 6241.

Without `rpc.Commit.Persist`, RFC 6241 section 8.4.1 requires the
confirming commit to come from the same session, and the device takes the
commit back if that session ends first. With `:confirmed-commit:1.1`, set
`Persist` to a token of your own and keep it in your change record. The
commit then outlives its session, and any session can act on it with
`PersistID`:

- Confirm it: `rpc.Commit{PersistID: token}`.
- Give it more time: `rpc.Commit{Confirmed: true, PersistID: token}`.
- Cancel it: `rpc.CancelCommit{PersistID: token}`.

`:confirmed-commit:1.0` defines neither the token nor `<cancel-commit>`.
Seen on two devices:

- Junos 26.2 advertises `:confirmed-commit:1.0` and has no
  `<cancel-commit>`. On vJunos-router 26.2, a confirmed commit made with
  `rpc.Commit` outlived its session, and a commit or a commit check from
  any session confirms it. See
  [No `<cancel-commit>`](junos-verified.md#no-cancel-commit) and
  [Commit checks confirm a pending commit](junos-verified.md#commit-checks-confirm-a-pending-commit).
- netopeer2 2.8.7 refuses `PersistID` next to `Confirmed`. To give a
  persistent commit more time there, send `Confirmed` with the same token
  in `Persist`. See
  [Standard operations and reported quirks](junos-verified.md#standard-operations-and-reported-quirks).

## Recover from an uncertain write

A timeout ends the wait, not the operation, so a write that the device has
received may still take effect. `rpc.Commit` never returns
`junos.ErrCommitUnknown`. Its outcome is unknown when `errors.As` finds no
`netconf.RPCErrors` in the error, or finds a `*netconf.SyntaxError`. That
covers an ended context or session, a reply without `<ok/>` and an
unreadable reply:

```go
err := rpc.Commit{}.Exec(ctx, session)

var rpcErrs netconf.RPCErrors
var syntaxErr *netconf.SyntaxError
switch {
case err == nil:
	// committed
case !errors.As(err, &rpcErrs) || errors.As(err, &syntaxErr):
	// outcome unknown: send nothing more on this session
default:
	// the device refused the commit
}
```

After an unknown outcome, do not discard, edit or commit again. Close the
session. From a new one under a fresh context, read the running
configuration and any commit records the device keeps. Standard NETCONF
cannot report what became of a commit, but a `Persist` token lets the new
session confirm or cancel a pending confirmed commit.

On Junos, commit with `junos.CommitConfiguration{Comment: token}` instead
of `rpc.Commit`, and find the token as
[After `ErrCommitUnknown`](junos.md#after-errcommitunknown) shows.
