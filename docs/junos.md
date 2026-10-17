# Junos

Package `junos` covers reading Junos configuration and operational data,
changing configuration with `junos.Change`, and finding out what became of
a commit whose reply was lost. Read this page before you write code for a Junos
device. [Standard NETCONF](standard-netconf.md) covers package `rpc`, and
[What is verified against Junos](junos-verified.md) the device evidence.

- [Reading configuration and operational data](#reading-configuration-and-operational-data)
- [Non-ASCII text](#non-ascii-text)
- [Changing configuration](#changing-configuration)
- [Change safety](#change-safety)
- [After `ErrCommitUnknown`](#after-errcommitunknown)
- [Lock and candidate conflicts](#lock-and-candidate-conflicts)
- [Lower-level configuration operations](#lower-level-configuration-operations)
- [The ephemeral database](#the-ephemeral-database)

## Reading configuration and operational data

`junos.IsJunos(session)` reports whether the device advertised
`junos.Capability`, which every Junos device does. Package `junos` has
these read operations:

```go
info, err := junos.GetSystemInformation{}.Exec(ctx, session) // host name, model, OS name, version, serial number

text, err := junos.GetConfiguration{Format: junos.FormatText}.Exec(ctx, session)
small, err := junos.GetConfiguration{Format: junos.FormatJSONMinified}.Exec(ctx, session) // or FormatXMLMinified
diff, err := junos.CompareRollback{Rollback: 1}.Exec(ctx, session)

out, err := junos.Command("show bgp summary").Exec(ctx, session)  // as the CLI prints it
doc, err := junos.Command("show bgp summary").JSON(ctx, session)  // as JSON
reply, err := junos.Command("show bgp summary").XML(ctx, session) // as XML, for junos.Decode
```

`errors.Is` matches a denied permission against
`junos.ErrPermissionDenied` and an unsupported operation against
`junos.ErrUnsupported`. See [Junos errors](operations.md#junos-errors).

### Configuration and diffs

`junos.GetConfiguration` reads the active configuration. It sends
`database="committed"` unless `Candidate` is set, because without that
attribute Junos reads the candidate.

| Field | Effect |
| --- | --- |
| `Format` | `junos.FormatXML` (default), `junos.FormatText`, `junos.FormatSet`, `junos.FormatJSON`, `junos.FormatXMLMinified` or `junos.FormatJSONMinified` |
| `Candidate` | Read the candidate, uncommitted changes included. After `junos.OpenConfiguration`, read what the session opened |
| `Inherit` | Apply configuration groups and interface ranges, as the device does |
| `Filter` | Part of the hierarchy, as a `<configuration>` element in a `string`, a `[]byte` or a value that marshals to it. Nil returns everything |

```go
ntp, err := junos.GetConfiguration{
	Format: junos.FormatSet,
	Filter: "<configuration><system><ntp/></system></configuration>",
}.Exec(ctx, session)
```

`Exec` returns the `<configuration>` element for the XML formats, the JSON
document for JSON, and plain text otherwise.

`FormatXMLMinified` and `FormatJSONMinified` return the configuration
without indentation. A Junos release that does not know them replies with
indented XML and no error, and `Exec` then fails with
`the device answered in another format than ...`. Junos 26.2 never
finishes either reply over chunked framing, so `Exec` refuses both on such
a session. Do not send them there with `Session.ExecReply` either,
because the session stalls. See
[Formats without indentation over chunked framing](junos-verified.md#formats-without-indentation-over-chunked-framing).

`junos.CompareRollback` returns the diff between the candidate and an
earlier commit, in the patch format of `show | compare rollback N`. The
zero value compares with the active configuration, which shows what a
commit would change. An empty patch means the two are equal.

### CLI commands

`junos.Command` sends one operational-mode CLI line, such as
`show version`, and returns its output as text, JSON or XML. Do not build
the line from untrusted input. Junos does not apply pipes such as
`| match` to the line and returns the whole output, so filter in Go, or
send the XML operation with its own arguments.

### Commit history

`junos.GetCommitInformation` returns the commit history, newest first:

```go
commits, err := junos.GetCommitInformation{}.Exec(ctx, session)
if err != nil {
	return err
}
for _, commit := range commits {
	fmt.Printf("%d %s %s by %s via %s\n", commit.Sequence, commit.Time.Format(time.RFC3339), commit.Comment, commit.User, commit.Client)
}
```

Junos 26.2 keeps the 50 newest commits. `Sequence` is the number that
`junos.Rollback` and `junos.CompareRollback` take, 0 for the newest.
`Time` comes from the seconds that Junos sends, not from its local-time
text, and is the zero time when Junos sends none. `Note` holds the remark
that Junos adds, such as `commit confirmed, rollback in 5mins`, and keeps
it after the confirmation. `Pending` tells whether a confirmed commit still
waits for its confirmation.

The newest entry shows whether anything was committed since you last
looked, but a commit to the ephemeral database leaves no entry, and two
commits within one second have the same time. With a history for each
routing engine, `GetCommitInformation` returns the first.

### Other operations as literal XML

Send any other operation as literal XML with `Session.ExecReply`, and read
the result with `junos.Decode`:

```go
reply, err := session.ExecReply(ctx, "<get-interface-information><terse/></get-interface-information>")
if err != nil {
	return err
}
var interfaces []struct {
	Name string `xml:"name"`
	Oper string `xml:"oper-status"`
}
if err := junos.Decode(reply, "physical-interface", &interfaces); err != nil {
	return err
}
```

`junos.Decode` finds the element at any depth, also inside a
`<multi-routing-engine-item>`, and trims the white space that Junos pads
values with. Into a slice other than `[]byte` it decodes every match, into
anything else the first, and it fails with `netconf.ErrElementNotFound`
when there is none. See the
[junos-interfaces example](../examples/junos-interfaces/README.md).

For text or JSON, add `format="text"` or `format="json"` to the
operation's element and read the reply with `junos.Text` or `junos.JSON`:

```go
reply, err := session.ExecReply(ctx, `<get-interface-information format="text"><terse/></get-interface-information>`)
if err != nil {
	return err
}
out, err := junos.Text(reply)
if err != nil {
	return err // the error Junos wrote into the reply
}
fmt.Println(out)
```

Junos writes most failures of such an operation into the body of the
reply, so `ExecReply` returns no error. `Text`, `JSON`,
`junos.Command.Exec` and `junos.Command.JSON` return the error they find
there, `JSON` together with the document. On Junos 23.4R2-S2.1 and 26.2, some
failures leave nothing to find, so ask in XML when you need to detect
them. See
[Junos limits the library does not work around](junos-verified.md#junos-limits-the-library-does-not-work-around).

## Non-ASCII text

Junos 26.2 keeps text as bytes and writes each byte above 127 as its own
character reference. `ø`, the bytes C3 B8 in UTF-8, arrives as
`&#195;&#184;`, which an XML parser reads as `Ã¸`. For a device that
advertises `junos.Capability`, Junos text normalization joins such
references into the character in every reply and notification that the
session buffers, `reply.Raw` included. A reply read with `Session.Stream`
is not normalized.

A reference that is not part of a UTF-8 character stays, so Latin-1 text
such as `&#248;` still reads as `ø`. Two to four Latin-1 characters in a
row whose bytes form one UTF-8 character, such as `É»`, are joined too.
[ADR 0004](adr/0004-junos-text-is-put-back-together-in-the-session.md)
records why.

## Changing configuration

`junos.Change` is a whole configuration change in one value. `Apply`
reserves the candidate, under the lock or as a private copy, loads,
compares, commits, and releases the candidate however the change ends.
`bgpIsUp` stands for a health check of your own:

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
	return fmt.Errorf("in effect until Junos goes back, check that it does: %w", err)
case errors.Is(err, junos.ErrCommitUnknown):
	return fmt.Errorf("commit outcome unknown, ask the device and do not apply again: %w", err)
case err != nil && result.Committed:
	return fmt.Errorf("committed, but the candidate was not released: %w", err)
case err != nil:
	return err // nothing was committed; the error says which step failed
}
fmt.Println(result.Diff) // empty when the load changed nothing; then nothing was committed
```

With `junos.LoadPatch`, an empty `Diff` can mean that Junos 26.2 read no
patch, see
[Lower-level configuration operations](#lower-level-configuration-operations).

| Field | Effect |
| --- | --- |
| `Format`, `Action`, `Config` | What to load and how, as for `junos.LoadConfiguration`, including a patch with `junos.LoadPatch` |
| `Private` | Work in a private copy of the candidate, like `configure private`, instead of under the lock |
| `LockWait` | Wait up to this long for a lock that another session holds |
| `DryRun` | Stop before the commit. `Review` runs, `Verify` does not. Not read-only, see [Change safety](#change-safety) |
| `Review` | Your function, given a non-empty diff before the commit. An error from it ends the change |
| `Confirm` | Commit as a confirmed commit, and confirm after `Verify`. Rounded up to whole minutes, at most 65535 minutes |
| `Verify` | Your function, run while the confirmed commit is in effect. An error or the end of its context leaves the commit unconfirmed |
| `Comment` | Recorded with the commit. Junos 26.2 refuses a comment over 512 bytes in UTF-8 |
| `Synchronize` | Commit on both routing engines. [Not verified](junos-verified.md#not-verified-on-a-device) on a device with two routing engines |
| `Full` | Have every process read the whole configuration again, like `commit full` |

`Apply` refuses a `Change` before it sends anything when `Config` is
missing, `Comment` or `Confirm` is over its limit, or the format or action
is unknown or does not fit, such as an `Action` for set commands,
`junos.LoadReplace` for JSON, or a patch in a format other than text.

`Apply` and `junos.LoadConfiguration` write each JSON escape that has a
literal form, such as the `\u0026` that `encoding/json` writes for `&`,
as that literal. See
[Line breaks and tabs in JSON strings](junos-verified.md#line-breaks-and-tabs-in-json-strings).

## Change safety

- Run one `junos.Change.Apply` at a time per session, and send no other
  configuration writes on that session while it runs. Junos reserves the
  candidate for the session, not for the call.
- Do not combine `Private` with `Confirm`, or set `Verify` without
  `Confirm`. `Apply` refuses both before it reserves the candidate.
- Let `Verify` read through the session, but never commit or run a commit
  check from it, on any session, because Junos 26.2 treats either one as
  the confirmation. Make `Verify` return when its context ends, which
  happens when the time to confirm is up, and check the service the change
  affects, not only reachability.
- Set `Confirm` long enough for the commit, `Verify` and the confirmation.
  `Apply` counts from when it sends the commit, which can take a minute on
  a large configuration, and gives up before Junos does, failing with
  `junos.ErrNotConfirmed`.
- A dry run is not read-only. It loads, and with a non-empty diff it sends
  a commit check, which Junos 26.2 treats as the confirmation of a pending
  confirmed commit from any session. So `Apply` reads the commit history
  first, and while a confirmed commit is pending it fails with
  `junos.ErrCommitPending`, which names who made it, and sends no check.
  The result still has the diff. A confirmed commit made between that read
  and the check is confirmed all the same, so coordinate changes on the
  device.
- After `junos.ErrNotConfirmed`, the change stays in effect until Junos
  goes back, which can be well after the time to confirm is up, so check
  the device first. After `junos.ErrCommitUnknown`, see
  [After `ErrCommitUnknown`](#after-errcommitunknown) before you apply
  again.

The [change example](../examples/junos-change/README.md) defaults to a
dry run in a private copy.

### What `Apply` does on the way out

- Under the lock, when a load may have left statements behind,
  `junos.Change.Apply` loads the active configuration back with
  `junos.Rollback(0)` before it unlocks, so the next commit by somebody
  else carries none of the change. A private copy is closed and dropped.
- Releasing the candidate has its own one-minute timeout, so it runs even
  after the context of `Apply` has ended. When the release fails, the
  error says so, and the candidate stays reserved until the session ends.
  Close the session and check the device before you retry.
  `result.Committed` can be true then, and `errors.Is` still finds
  `junos.ErrNotConfirmed` or `junos.ErrCommitUnknown` in the error.
- When the context ends before the commit is sent, `Apply` sends no
  commit, releases the candidate and returns the context's error.
- A commit or a confirmation fails with `ErrCommitUnknown` when its reply
  is missing, incomplete, or has neither `<ok/>` nor a refusal. Junos may
  have carried it out.
- A commit that Junos refuses is a plain error, and nothing was committed.
  A reply with warnings and a `<load-error-count>` in place of `<ok/>` is
  a refusal too. Its error matches `netconf.ErrElementNotFound` and holds
  the warnings as `netconf.RPCErrors`.

## After `ErrCommitUnknown`

After `junos.ErrCommitUnknown`, look for the commit in the commit
history. Put a unique token in each commit's comment and find it with
`junos.FindCommit`, on a new session if needed. `FindCommit` matches the
token only with no letter, digit, `-` or `_` next to it, so `change-1` is
not found in `change-12`.

Here `other` is a session opened after the failure, and `healthy` stands
for the checks of `Verify`. The recovery gets a new context, with room
for a [slow commit check](junos-verified.md#slow-commit-checks).

```go
token := "change-7f3a" // an id of your own, unique to this change
_, err := junos.Change{
	Format:  junos.FormatSet,
	Config:  "set system ntp server 192.0.2.1",
	Comment: "add ntp server, " + token,
	Confirm: 5 * time.Minute,
}.Apply(ctx, session)
if !errors.Is(err, junos.ErrCommitUnknown) {
	return err
}

rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
defer cancel()
commits, err := junos.GetCommitInformation{}.Exec(rctx, other)
if err != nil {
	return err
}
commit, found := junos.FindCommit(commits, token)
switch {
case !found:
	// nothing is known: the commit may still be running, or may have
	// dropped out of the history; ask again
case commit.Pending:
	// in effect, and Junos goes back unless it is confirmed in time
	if err := healthy(rctx); err != nil {
		return err // leave it unconfirmed, and Junos goes back by itself
	}
	// on Junos 26.2 a commit check from any session confirms it
	_, err := junos.CommitConfiguration{Check: true}.Exec(rctx, other)
	return err
case commit.Sequence == 0:
	// carried out, and what the device runs
default:
	// carried out, and committed over since
}
```

On Junos 26.2, a commit from another session confirms too, but commits
the whole shared candidate with it, uncommitted changes of other users
included.

A found entry that is not pending is not always in effect. Only
`Sequence` 0 is what the device runs. An entry further down can sit under
a commit that changed nothing, because Junos 26.2 records even
[a commit with nothing to commit](junos-verified.md#commits-with-nothing-to-commit).

When `commit.Note` shows a confirmed commit, the entry on top of it is
the commit that confirmed it or Junos going back. Junos 26.2 records going
back as a commit by the user `root` through the client `other`. A
confirmation by a commit check adds no entry, so the confirmed commit
stays the newest.

## Lock and candidate conflicts

`junos.Change.Apply` fails when another session holds the lock on the
candidate, or when the candidate has uncommitted changes. The error says
which:

```go
var rpcErr netconf.RPCError
switch {
case errors.Is(err, netconf.ErrLockDenied) && errors.As(err, &rpcErr):
	holder, _ := rpcErr.Detail("session-id")
	log.Printf("session %s has the lock on the candidate", holder)
case errors.Is(err, junos.ErrCandidateModified):
	log.Print("the candidate holds changes that nobody has committed")
}
```

With `LockWait` set, `Apply` asks for the lock again every second. When
`LockWait` or the context runs out, it returns the last refusal, which
names the holder, with the reason it stopped. Nothing is loaded before the
lock, so asking again is safe. Junos 26.2 releases the lock of a session
that ends and drops what that session loaded.

`Apply` does not wait for uncommitted changes in the candidate, which may
never go away. It fails at once with `junos.ErrCandidateModified`, and
somebody has to commit or drop them. `ErrCandidateModified` also matches
that refusal from `rpc.Lock` or `junos.OpenConfiguration`. Junos has no
error tag for it, so the library matches the message, as Junos 26.2 words
it.

With `Private`, `Apply` takes no lock, and `LockWait` does nothing. Junos
opens a private copy while another session holds the lock, so the load,
the diff and `Review` run before Junos refuses the commit with
`netconf.ErrLockDenied`. While the candidate holds uncommitted changes,
Junos refuses the private copy with `ErrCandidateModified`. Junos also
refuses the commit when somebody else has committed another value for a
statement that the copy changes. Junos 26.2 then commits nothing and
replies with a warning for each such statement,
`statement does not match patch`, and their count, which `Apply` returns
as a refusal.

### Who is editing the configuration

`junos.GetDatabaseStatusInformation` lists who has the configuration open
for editing, like `status` in configuration mode:

```go
editing, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, session)
if err != nil {
	return err
}
for _, edit := range editing {
	log.Printf("%s on %s since %s, at %s", edit.User, edit.Terminal, edit.Start.Format(time.RFC3339), edit.EditPath)
}
```

The list has the CLI users in configuration mode, with `Private` or
`Exclusive` set for `configure private` or `configure exclusive`, and the
NETCONF sessions that hold the lock or a private copy, with the
session-id in `PID`. It leaves out a NETCONF session that loaded into the
candidate without committing, and a user who left changes behind and
logged out, so it cannot show who caused `junos.ErrCandidateModified`.
Use `junos.CompareRollback{}` to see whether the candidate is clean. Seen
on vJunos-router and vJunos-switch with Junos 26.2 and on Junos OS
Evolved 26.2.

## Lower-level configuration operations

`junos.Change` is built from these operations. Call them yourself for a
sequence that `Change` does not cover:

| Operation | Effect |
| --- | --- |
| `junos.LoadConfiguration` | Loads into the candidate without committing, in any format, as a patch, or from a file on the device |
| `junos.CompareRollback` | Returns the diff, see [Configuration and diffs](#configuration-and-diffs) |
| `junos.CommitConfiguration` | Commits the candidate, or with `Check` only runs a commit check |
| `junos.Rollback` | Loads an earlier commit into the candidate, `Rollback(0)` the active configuration |
| `junos.LoadRescue` | Loads the rescue configuration into the candidate |
| `junos.OpenConfiguration`, `junos.CloseConfiguration` | Open and close a private copy of the candidate or the ephemeral database for the session |
| `rpc.Lock`, `rpc.Unlock` | Lock and unlock the candidate. Package `junos` has no `<lock-configuration/>` |

A load that fails can still have changed the candidate, because Junos
keeps the set commands that worked. Put the candidate back with
`Rollback(0)`, or close the private copy.

`Rollback` takes 0 to 49, and other numbers fail before anything is sent.
A number Junos has no configuration for fails with
`Cannot open configuration file`, and Junos 26.2 then fails the next load
of set commands on that session with a syntax error. `Rollback(0)` or one
more load ends that.

To load a patch, a file on the device or the rescue configuration:

```go
// A patch, as CompareRollback returns it: the changes in the candidate,
// taken out and loaded again.
patch, err := junos.CompareRollback{}.Exec(ctx, session)
if err != nil {
	return err
}
if _, err := junos.Rollback(0).Exec(ctx, session); err != nil {
	return err
}
_, err = junos.LoadConfiguration{Action: junos.LoadPatch, Config: patch}.Exec(ctx, session)
if err != nil {
	return err
}

// A file that Junos reads, by its path on the device.
_, err = junos.LoadConfiguration{Format: junos.FormatText, URL: "/var/tmp/golden.conf"}.Exec(ctx, session)
if err != nil {
	return err
}

// The rescue configuration.
_, err = junos.LoadRescue{}.Exec(ctx, session)
if err != nil {
	return err
}
```

`junos.ChangeResult.Diff` is a patch as well. Ftp and http locations for
`URL`, which Juniper documents, are
[not verified on a device](junos-verified.md#not-verified-on-a-device).

On Junos 26.2:

- A file it cannot open, or a rescue configuration that was never saved,
  fails with `Cannot open configuration file`.
- A patch that does not fit the candidate, such as one that removes a
  missing statement, gets warnings and no `<ok/>`. Junos loads none of
  it, and `LoadConfiguration` fails with the warnings in its error.
- Input that is not a patch at all is "noise" to Junos. The load goes
  through, changes nothing, and has a warning.

## The ephemeral database

`junos.OpenConfiguration{Ephemeral: true}` opens the default instance of
the ephemeral database for the session. Until `junos.CloseConfiguration`
or the session end, `junos.LoadConfiguration`,
`junos.CommitConfiguration`, and `junos.GetConfiguration` with
`Candidate: true` act on it:

```go
openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
err := junos.OpenConfiguration{Ephemeral: true}.Exec(openCtx, session)
if err != nil {
	return err // after the deadline, also close the session, see below
}
defer junos.CloseConfiguration{}.Exec(ctx, session) // drops what was not committed

_, err = junos.LoadConfiguration{
	Format: junos.FormatSet,
	Config: "set policy-options prefix-list blocked 192.0.2.0/24",
}.Exec(ctx, session)
if err != nil {
	return err
}
_, err = junos.CommitConfiguration{}.Exec(ctx, session)
if err != nil {
	return err
}
set, err := junos.GetConfiguration{Format: junos.FormatSet, Candidate: true}.Exec(ctx, session) // what is in it
if err != nil {
	return err
}
fmt.Printf("%s\n", set)
```

What is committed to the ephemeral database is not part of the
configuration, so `GetConfiguration` without `Candidate` does not show it.

Give `OpenConfiguration` a context with a deadline, and close the session
when it runs out. Junos gives an instance to one session at a time, so
`OpenConfiguration` waits while another session has it open. Junos may
still open it after the deadline, and later loads and commits on the
session would then go to the ephemeral database. No recorded device run
covers this wait.

`EphemeralInstance` opens a named instance instead. Named instances are
[not verified on a device](junos-verified.md#not-verified-on-a-device).

The ephemeral database takes no configuration groups, no confirmed commit
and no diff, and Junos 26.2 and Junos OS Evolved 26.2 refuse all three, so
`junos.Change` does not work with it.
