# Junos

Package `junos` has the Junos-native operations and knows the shapes Junos
gives its replies.

```go
info, err := junos.GetSystemInformation{}.Exec(ctx, session) // name, model, version, serial

text, err := junos.GetConfiguration{Format: junos.FormatText}.Exec(ctx, session)
small, err := junos.GetConfiguration{Format: junos.FormatJSONMinified}.Exec(ctx, session) // or FormatXMLMinified
diff, err := junos.CompareRollback{Rollback: 1}.Exec(ctx, session)

out, err := junos.Command("show bgp summary").Exec(ctx, session)  // as the CLI prints it
doc, err := junos.Command("show bgp summary").JSON(ctx, session) // as JSON
```

`FormatJSONMinified` and `FormatXMLMinified` are the configuration without
its indentation: on the lab devices a third of the bytes as JSON and half
as XML. Junos does not refuse a format it does not know. It answers in XML
with indentation and says nothing, so `GetConfiguration` fails when what
came is not what was asked for. Over chunked framing Junos 26.2 does not
finish a reply in either of the two, so `GetConfiguration` refuses both on
such a session before it sends anything, see
[what Junos does that the library does not make up for](junos-verified.md#what-junos-does-that-the-library-does-not-make-up-for).

The commit history is values, newest first:

```go
commits, err := junos.GetCommitInformation{}.Exec(ctx, session)
if err != nil {
	return err
}
for _, commit := range commits {
	fmt.Printf("%d %s %s by %s via %s\n", commit.Sequence, commit.Time.Format(time.RFC3339), commit.Comment, commit.User, commit.Client)
}
if len(commits) > 0 && commits[0].Pending {
	// a confirmed commit that still waits for its confirmation
}
```

`Sequence` is the number `Rollback` and `CompareRollback` take, and `Time`
comes from the seconds Junos gives and not from the text it prints in its
own time zone. The newest entry tells cheaply whether anything was
committed since a program last looked, with two holes: a commit to the
ephemeral database leaves no entry, and the time has a resolution of one
second. Junos keeps 50 entries.

Any operational request is literal XML. `junos.Decode` finds the result
whether or not Junos wrapped it in a routing-engine item, and strips the
line breaks Junos pads values with:

```go
reply, err := session.ExecReply(ctx, "<get-interface-information><terse/></get-interface-information>")
if err != nil {
	return err
}
var interfaces []struct {
	Name string `xml:"name"`
	Oper string `xml:"oper-status"`
}
err = junos.Decode(reply, "physical-interface", &interfaces)
```

## Text that is not ASCII

Junos keeps text as bytes and sends every byte above 127 as a character
reference of its own. `ø` arrives as `&#195;&#184;`, which an XML parser
reads as `Ã¸`: in a configuration in any format, in the output of a
command, in an error. The session puts such references back together in
every reply and notification it buffers from a device that advertises the
Junos capability, so `reply.Raw` and everything read from it have `ø`. A
reply read with `Stream` is as the device sent it.

A reference that is not part of a character in UTF-8 is left alone. Text
that Junos holds in Latin-1 arrives as `&#248;`, which reads as `ø`
already. Two characters of such a text that are UTF-8 as bytes, an `Ã`
followed by a `¸`, are taken for the one character.

## Changing configuration

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

An empty `Diff` without an error is a load that changed nothing. Mostly the
device had the configuration already. With `LoadPatch` it is also what a
patch that Junos made nothing of looks like, and `result.Warnings` has what
Junos said about it.

| Field | Effect |
| --- | --- |
| `Format`, `Action`, `Config` | What to load: set commands, text, XML or JSON; merge, override, replace or update |
| `Private` | Work in a private copy of the candidate, next to others, instead of under the lock |
| `LockWait` | Wait this long for a lock that another session holds, asking again every second, before giving up with `netconf.ErrLockDenied` |
| `DryRun` | Load, diff and commit check; commit nothing. Junos takes a commit check for the confirmation of a confirmed commit that is pending, whoever made it, so `Apply` reads the commit history first and sends no check while one is pending: the error is `ErrCommitPending`, and the result has the diff |
| `Review` | Sees the diff before anything is committed and can stop the change |
| `Confirm`, `Verify` | Confirmed commit, a check of your own while it is in effect, then the confirmation |
| `Comment`, `Synchronize` | Recorded with the commit, up to 512 bytes of it: Junos refuses the commit for more, so a longer one is refused before anything is loaded. Commit on both routing engines |
| `Full` | Have every process read the whole configuration again, like `commit full` |

What `Apply` takes care of:

- A load that Junos refuses in part leaves the accepted lines in the
  candidate. The candidate is put back (`rollback 0`) before it is unlocked,
  so nothing is left for the next commit of somebody else.
- The candidate is given back also when the context is over, with a minute
  of its own. If that fails the error says so.
- The confirmation is not sent once the time to confirm is up. The time
  counts from when the confirmed commit is sent, which is before Junos
  starts counting, so the library gives up before Junos does. `Confirm` has
  to cover the commit, `Verify` and the confirmation.
- A commit whose answer never arrives is `ErrCommitUnknown`, not a failure:
  Junos may have carried it out. [After `ErrCommitUnknown`](#after-errcommitunknown)
  says how to ask.
- A commit that Junos answers without `<ok/>` is an error. Junos has such
  answers without an error in them: a private copy whose change no longer
  fits, because somebody else committed another value for the statement,
  gets a warning with a count of such statements, and nothing is
  committed.
- A JSON document is loaded with its escapes written out. In a JSON string
  Junos reads `\"` and `\\` and stores every other escape as written, so
  `R\u0026D`, which is how `encoding/json` writes `R&D`, would be in the
  configuration with its backslash. `junos.LoadConfiguration` does the
  same.

### After `ErrCommitUnknown`

The device knows what became of the commit, and its commit history says.
Give the commit a token in its comment that no other commit has, and look
for it afterwards, on a new session if the old one is gone. The token is
found where it stands by itself, so `change-1` is not found in the comment
of `change-12`:

```go
token := "change-7f3a" // an id of your own
_, err := junos.Change{
	Format:  junos.FormatSet,
	Config:  "set system ntp server 192.0.2.1",
	Comment: "add ntp server, " + token,
	Confirm: 5 * time.Minute,
}.Apply(ctx, session)
if !errors.Is(err, junos.ErrCommitUnknown) {
	return err
}

commits, err := junos.GetCommitInformation{}.Exec(ctx, other)
if err != nil {
	return err
}
commit, found := junos.FindCommit(commits, token)
switch {
case !found:
	// nothing is known: the commit may still be running, ask again
case commit.Pending:
	// In effect, and Junos takes it back unless it is confirmed.  Any
	// session confirms it, with a commit check.
	_, err = junos.CommitConfiguration{Check: true}.Exec(ctx, other)
case commit.Sequence == 0:
	// carried out, and what the device runs
default:
	// carried out, and committed over since
}
```

A commit check confirms a confirmed commit and commits nothing. The empty
commit confirms too, and from a session without the lock it commits
whatever else is in the candidate with it, the unfinished changes of
somebody else included. `junos.Change` confirms with it, under the lock it
holds.

The history has no answer "not committed": a token that is not there says
nothing. And "found and not pending" is not always "in effect". The newest
entry, `Sequence` 0, is what the device runs. One found further down was
committed over. For a confirmed commit, which `commit.Note` tells, that is
how it ends either way: with the commit that confirmed it on top, or with
Junos going back when its time ran out, which Junos 26.2 enters as a commit
of the user `root` through the client `other`. Its own entry stops being
pending and looks the same in both cases, so the entry on top of it is
what tells them apart. A confirmation by a commit check leaves no entry:
the confirmed commit stays the newest. A confirmed commit keeps its
comment.

### When somebody else is in the way

Junos refuses a change when somebody else is in the way: with the lock on
the candidate, or with changes in it that are not committed. The error says
which:

```go
var rpcErr netconf.RPCError
switch {
case errors.Is(err, netconf.ErrLockDenied) && errors.As(err, &rpcErr):
	holder, _ := rpcErr.Detail("session-id")
	log.Printf("session %s has the lock on the candidate", holder)
case errors.Is(err, junos.ErrCandidateModified):
	// the candidate holds changes that nobody has committed
}
```

A lock goes away when its holder is done, so a change can wait for it:
`LockWait: time.Minute` on the `Change` asks again until the lock is free
or the minute is up, and then returns the last refusal, which still names
the holder. Uncommitted changes in the candidate are not waited for:
nothing says that they will ever go away.

Who is editing is a request away, what the CLI shows with `status` in
configuration mode:

```go
editing, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, session)
for _, edit := range editing {
	log.Printf("%s on %s since %s, at %s", edit.User, edit.Terminal, edit.Start.Format(time.RFC3339), edit.EditPath)
}
```

It lists the users of the CLI in configuration mode, with `Private` and
`Exclusive` for `configure private` and `configure exclusive`, and the
NETCONF sessions that hold the lock or a private copy, under their
session-id as `PID`. It does not list whose changes `ErrCandidateModified`
is about, the error of an `Apply`, an `rpc.Lock` or an `OpenConfiguration`
that Junos refused over them: a NETCONF session that has loaded into the candidate and not
committed takes neither a lock nor a copy and is not in the list, and
neither is somebody who left changes behind and went. An empty list does
not say that the candidate is clean; `junos.CompareRollback{}` does. Seen
on Junos 26.2 (vJunos-router, vJunos-switch) and Junos OS Evolved 26.2.

### The operations underneath

The operations underneath are there for a sequence `Change` does not cover:
`junos.LoadConfiguration`, `CompareRollback`, `CommitConfiguration`,
`Rollback`, `OpenConfiguration` and `CloseConfiguration`, and `rpc.Lock`.

`LoadConfiguration` loads three things more than configuration the program
holds:

```go
// A patch: what a compare printed, for a candidate that is where the
// compare began.  Here that is the changes in the candidate, put back
// and loaded again.
diff, err := junos.CompareRollback{}.Exec(ctx, session)
_, err = junos.Rollback(0).Exec(ctx, session)
_, err = junos.LoadConfiguration{Action: junos.LoadPatch, Config: diff}.Exec(ctx, session)

// a file that Junos reads itself, by its path on the device
_, err = junos.LoadConfiguration{Format: junos.FormatText, URL: "/var/tmp/golden.conf"}.Exec(ctx, session)

// the rescue configuration
_, err = junos.LoadRescue{}.Exec(ctx, session)
```

A `Change` takes a patch as well, with `Action: junos.LoadPatch`: its own
`ChangeResult.Diff` is one. Juniper documents an ftp or http location for `URL`
too, which was not run here. What
Junos 26.2 says when it cannot: a file it cannot open and a rescue
configuration that is not saved are errors, "Cannot open configuration
file". A patch that does not fit the candidate gets warnings only and no
`<ok/>`, and nothing of it is loaded; the load fails over the missing
`<ok/>`, with the warnings in its error. And what is no patch at all is
"noise" to Junos: such a load goes through and changes nothing, with a
warning in the reply.

### The ephemeral database

The ephemeral database is such a sequence:

```go
err := junos.OpenConfiguration{Ephemeral: true}.Exec(ctx, session)
// ...
_, err = junos.LoadConfiguration{Format: junos.FormatSet, Config: "set policy-options prefix-list blocked 192.0.2.0/24"}.Exec(ctx, session)
_, err = junos.CommitConfiguration{}.Exec(ctx, session)
set, err := junos.GetConfiguration{Format: junos.FormatSet, Candidate: true}.Exec(ctx, session) // what is in it
err = junos.CloseConfiguration{}.Exec(ctx, session)
```

What is committed there is not in the configuration and not in the commit
history, and only a session that has the database open reads it. Junos
gives it to one session at a time: `OpenConfiguration` for a database that
another session has open waits until that session closes it, so give it a
context with a deadline. The database takes no configuration groups, no
confirmed commit and no diff, which is why `junos.Change` works on the
candidate and its private copies only.
