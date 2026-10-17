# Preview or apply a Junos change

[Source](main.go)

The junos-change example applies set commands with `junos.Change`. By
default it makes a dry run in a private copy. With `-commit` it makes a
confirmed commit. Both modes write to the device, so try them in a lab
first.

The account needs permission to load and check configuration, read the
commit history and, with `-commit`, commit and read system information.
Follow the [common setup](../README.md#common-setup), put the `set` and
`delete` commands in `change.set`, and review them. Before you adapt the
program, read [Change safety](../../docs/junos.md#change-safety).

## Preview without committing

```bash
go run ./examples/junos-change -host router1.example.net -user automation -config change.set
```

Without `-commit`, the program sets `Private` and `DryRun`. It prints the
diff to stdout and, if it is not empty and no confirmed commit is
pending, sends a commit check. It logs a token, device warnings and a
status line to stderr:

```text
2026/10/17 09:30:01 change token: example-K7QZ2M4XWJ5RBN3TDPLC6YVHSA
2026/10/17 09:30:04 committed=false dry_run=true changed=true
```

## Commit explicitly

```bash
go run ./examples/junos-change -host router1.example.net -config change.set -commit
```

With `-commit`, the program locks the shared candidate without waiting,
loads the change, prints the diff and makes a confirmed commit with five
minutes to confirm. `Verify` only reads system information to check that
the device still answers. `Apply`, which gets six minutes in all, then
confirms the commit. A successful run logs
`committed=true dry_run=false changed=true`. When the load changes nothing,
the program logs `committed=false dry_run=false changed=false`.

Replace `Verify` with checks of the affected service. `Review` only prints
the diff, so read a dry run's diff first.

## Errors and recovery

Keep the token, which is also in the commit comment. Not every failure
means nothing changed:

- `junos.ErrCommitPending` in a dry run means a confirmed commit is
  pending, so no check was sent. Try again later.
- `netconf.ErrLockDenied` with `-commit`, or `junos.ErrCandidateModified`,
  means another session holds the lock or the candidate holds
  [uncommitted changes](../../docs/junos.md#lock-and-candidate-conflicts).
  Nothing was loaded.
- `junos.ErrCommitUnknown` means the outcome is unknown. Do not run the
  program again. Find the token in the commit history, as
  [After `ErrCommitUnknown`](../../docs/junos.md#after-errcommitunknown)
  shows.
- `junos.ErrNotConfirmed` means the change stays in effect until Junos
  goes back. Check that it does.
- An error that begins with `committed, but cleanup failed` means the
  commit succeeded. Do not apply the change again.
- Any other error that begins with `change:` means nothing was committed.
