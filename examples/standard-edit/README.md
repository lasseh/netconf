# Edit and commit a standard candidate

[Source](main.go) and [offline tests](main_test.go)

The standard-edit example loads configuration XML into the candidate,
validates it and makes a plain commit. The change is permanent, with no
confirmed commit and no check afterward. Run it against a lab device
first. On Junos, use the
[junos-change example](../junos-change/README.md).

The account needs permission to lock, unlock, read, edit, validate,
discard and commit. Write `change.xml` in the device's own data model,
without an `<rpc>`, `<edit-config>` or `<config>` wrapper. Follow the
[common setup](../README.md#common-setup), review the file, then run:

```bash
go run ./examples/standard-edit -host router1.example.net -user automation -config change.xml -commit
```

Without both `-config` and `-commit`, the program writes nothing and
fails. On success it prints nothing.

## What the program does

The program runs the sequence in
[Edit the candidate safely](../../docs/standard-netconf.md#edit-the-candidate-safely),
with five minutes in all. It stops before the edit, and discards
nothing, when the device lacks `:candidate:1.0` or both validate
capabilities, when the lock fails, or when the candidate differs from
running.

The comparison is byte for byte, after trimming white space at both ends.
A difference in formatting, attributes or element order stops the program
on a candidate that holds no changes. Adapt it to your device's data
model.

After a failed edit, validation or refused commit, the program discards
the changes and unlocks, with one minute for that cleanup. It does not
unlock after a failed discard. When the unlock fails after a successful
commit, the error says the commit succeeded. Do not apply the file
again.

`editCandidate` in [main.go](main.go) assumes no other code uses its
session. The [offline tests](main_test.go) script a device and check what
the program sends.

## Unknown commit outcome

When the error from `rpc.Commit` carries no RPC error, or wraps a
`netconf.SyntaxError`, the program treats the outcome as unknown and
sends nothing more. Read the running configuration from a new session before
you run it again. See
[Recover from an uncertain write](../../docs/standard-netconf.md#recover-from-an-uncertain-write).

## Running standard-edit against Junos

The program sends `<validate>`. Junos 26.2 takes a commit check from any
session as the confirmation of a pending confirmed commit, and no recorded
run has sent `<validate>` while one was pending. On Junos, first check
with `junos.GetCommitInformation` that no commit is pending. See
[Commit checks confirm a pending commit](../../docs/junos-verified.md#commit-checks-confirm-a-pending-commit).
