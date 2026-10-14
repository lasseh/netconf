package junos

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
	"github.com/lasseh/netconf/rpc"
)

// Change is one change to the configuration of a device.  Apply carries it
// out as a whole: it reserves the candidate, loads the configuration, looks
// at what that changes, commits, and gives the candidate back whatever
// happened on the way.
//
//	result, err := junos.Change{
//		Format:  junos.FormatSet,
//		Config:  "set system ntp server 192.0.2.1",
//		Comment: "add ntp server",
//		Confirm: 5 * time.Minute,
//	}.Apply(ctx, session)
type Change struct {
	// Format, Action and Config say what to load, as in LoadConfiguration:
	// configuration in one of the formats, or with LoadPatch the output of
	// a compare.
	Format Format
	Action LoadAction
	Config any

	// Private makes the change in a private copy of the candidate, like the
	// CLI's "configure private".  Others can change the configuration at the
	// same time, in private copies of their own: the commit applies this
	// change only, and Junos refuses it when somebody else has committed
	// another value for a statement it changes.  The default is the
	// candidate itself under a lock, like "configure exclusive", which keeps
	// everybody else out for as long as the change takes.  Junos grants
	// neither while the candidate holds changes that nobody has committed,
	// which Apply reports as ErrCandidateModified, and commits nothing from
	// a private copy while somebody else has the lock.
	Private bool

	// LockWait is how long Apply waits for the lock when another session
	// holds it.  With zero the first refusal is the answer,
	// netconf.ErrLockDenied.  With more, Apply asks again every second until
	// it has the lock, the time is up or its context ends, and then returns
	// the last refusal, which names the holder, with why it stopped
	// asking.  A request that is on its way when the time is up is still
	// waited for, and none is sent after it.  Nothing has been loaded when
	// a lock is refused, so asking again is safe.
	//
	// Only a lock that is held is waited for.  Changes in the candidate that
	// nobody has committed, ErrCandidateModified, are not: nothing says that
	// they will ever go away.  With Private no lock is taken and the field
	// does nothing.
	LockWait time.Duration

	// Comment is recorded with the commit.
	Comment string

	// Synchronize commits on both routing engines.
	Synchronize bool

	// Full has every process read the whole configuration again, see
	// CommitConfiguration.Full.
	Full bool

	// Confirm makes the commit a confirmed one, and then confirms it.  Junos
	// goes back to the previous configuration by itself unless the
	// confirmation reaches it within this time, which protects against a
	// change that cuts the device off.  A private copy cannot be committed
	// this way.
	//
	// Apply counts the time from when it sends the confirmed commit, and
	// does not send the confirmation once it is up.  So the time has to
	// cover the commit itself, which can take a minute on a large
	// configuration, Verify and the confirmation.  Junos counts in whole
	// minutes, see CommitConfiguration.Confirm.
	Confirm time.Duration

	// Verify is called between the confirmed commit and its confirmation,
	// with the new configuration in effect.  An error from it leaves the
	// commit unconfirmed.  So does the end of its context, which comes when
	// the time to confirm is up.  Verify may use the session but must not
	// commit or check, on it or on any other session: either confirms.  It
	// needs Confirm.
	Verify func(ctx context.Context) error

	// Review is shown what the change does to the configuration before
	// anything is committed.  An error from it ends the change there.
	Review func(ctx context.Context, diff string) error

	// DryRun ends the change once Junos has checked it: the result says
	// what it would do, the error whether Junos would take it, and nothing
	// is committed.  Review is asked all the same.  Verify is not: there is
	// no commit for it to look at.
	//
	// The check is a commit check, and Junos takes one for the confirmation
	// of a confirmed commit that is pending, whoever made it.  So Apply reads
	// the commit history before the check and sends none while a commit is
	// pending: the error is ErrCommitPending then, and the result still says
	// what the change would do.  A confirmed commit that somebody makes
	// between that look and the check is confirmed all the same.
	DryRun bool
}

// ChangeResult says how far a Change got.
type ChangeResult struct {
	// Diff is what the change does to the active configuration, in the
	// patch format of the CLI's "show | compare".  It is empty when the load
	// changed nothing, and nothing is committed then: the device has what
	// was loaded already, or Junos made nothing of a patch, see LoadPatch,
	// which Warnings tells.
	Diff string

	// Committed says that the change is in the active configuration for
	// good.
	Committed bool

	// Warnings is what Junos had to say about the load, the check and the
	// commits without refusing them.
	Warnings []netconf.RPCError
}

// The errors of a Change that is neither in effect for good nor known to be
// without effect.  Look for them with errors.Is.
var (
	// ErrNotConfirmed says that the confirmed commit went through and was
	// not confirmed: Verify failed, the time to confirm ran out, or Junos
	// refused the confirmation.  The change is in effect, and Junos goes
	// back to the previous configuration when the time is up.
	ErrNotConfirmed = errors.New("commit not confirmed, Junos goes back to the previous configuration by itself")

	// ErrCommitUnknown says that a commit was sent and its outcome is not
	// known: the context ended before the answer came, or the session did,
	// or the answer says neither yes nor no, or cannot be read to its end.
	// The change may be in effect or not, and after a confirmed commit it
	// may be confirmed or not.  The device knows.
	ErrCommitUnknown = errors.New("outcome of the commit unknown")
)

// ErrCandidateModified says that Junos did not hand out the candidate, as a
// lock or as a private copy, because it holds changes that nobody has
// committed.  Nothing was loaded.  Waiting does not help the way it does
// with a lock, netconf.ErrLockDenied: somebody has to commit those changes
// or drop them.  Look for it with errors.Is, on the error of Apply and on
// that of a lock or a private copy asked for by itself, with rpc.Lock or
// OpenConfiguration.
//
// Junos has no error tag for this, so it is told by the message, as Junos
// 26.2 words it, in the refusal of a lock or of a private copy.  The same
// words in the refusal of anything else do not match: by then something may
// have been loaded.
var ErrCandidateModified error = &refusal{"the candidate holds changes that nobody has committed", func(e netconf.RPCError) bool {
	switch e.Request() {
	case "lock", "open-configuration":
		return strings.Contains(e.Message, candidateModified)
	}
	return false
}}

// ErrCommitPending says that a dry run did not have its change checked,
// because a confirmed commit waits for its confirmation on the device and
// Junos would take the check for it, see Change.DryRun.  The error names who
// made that commit.  Look for it with errors.Is.
var ErrCommitPending = errors.New("a confirmed commit is pending and a commit check would confirm it")

// candidateModified is in the message of both refusals: "configuration
// database modified" for the lock and "shared configuration database
// modified" for a private copy.
const candidateModified = "configuration database modified"

// releaseTimeout is how long giving the candidate back may take.  It is a
// variable for the tests.
var releaseTimeout = time.Minute

// lockRetry is how long Apply waits before it asks again for a lock that
// another session holds, see Change.LockWait.  It is a variable for the
// tests.
var lockRetry = time.Second

// Apply carries out the change on session.  The result says how far it got,
// also when it failed.
//
// The candidate is given back on every way out: unlocked, after it was put
// back to the active configuration if the change did not get that far, or
// the private copy closed.  This has a minute of its own, so it also happens
// when the context is over.  When it fails the error says so, and the
// candidate stays reserved until the session is closed.
//
// The context covers everything else, Review and Verify included.  A commit
// that it interrupts may still be carried out by Junos; the error is
// ErrCommitUnknown then.
//
// Junos reserves the candidate for the session, not for the call: two
// changes on one session at the same time would be one to Junos.  Apply them
// one after the other, or on sessions of their own.
func (c Change) Apply(ctx context.Context, session *netconf.Session) (result ChangeResult, err error) {
	load := LoadConfiguration{Format: c.Format, Action: c.Action, Config: c.Config}
	commit := CommitConfiguration{Comment: c.Comment, Confirm: c.Confirm, Synchronize: c.Synchronize, Full: c.Full}
	if err := c.validate(load, commit); err != nil {
		return result, err
	}

	if err := c.reserve(ctx, session); err != nil {
		return result, err
	}
	// dirty says that the candidate may hold configuration that was loaded
	// and not committed
	dirty := false
	defer func() {
		if relErr := c.release(ctx, session, dirty); relErr != nil {
			err = errors.Join(err, relErr)
		}
	}()

	note := func(reply *netconf.Reply) {
		if reply != nil {
			result.Warnings = append(result.Warnings, reply.Warnings...)
		}
	}

	// a load that fails may have gone through in part
	dirty = true
	reply, err := load.Exec(ctx, session)
	note(reply)
	if err != nil {
		return result, step("load configuration", err)
	}

	if result.Diff, err = (CompareRollback{}).Exec(ctx, session); err != nil {
		return result, step("compare", err)
	}
	if result.Diff == "" {
		dirty = false
		return result, nil
	}

	if c.Review != nil {
		if err := c.Review(ctx, result.Diff); err != nil {
			return result, fmt.Errorf("junos: review: %w", err)
		}
	}

	if c.DryRun {
		if err := nothingPending(ctx, session); err != nil {
			return result, err
		}
		reply, err := CommitConfiguration{Check: true}.Exec(ctx, session)
		note(reply)
		if err != nil {
			return result, step("commit check", err)
		}
		return result, nil
	}

	// With the context over nothing is sent, and nothing is in doubt.
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("junos: commit: %w", err)
	}
	sent := time.Now()
	reply, err = commit.Exec(ctx, session)
	note(reply)
	if err != nil {
		return result, step("commit", inDoubt(err))
	}
	dirty = false

	if c.Confirm > 0 {
		reply, err := c.confirm(ctx, session, sent)
		note(reply)
		if err != nil {
			return result, err
		}
	}

	result.Committed = true
	return result, nil
}

// nothingPending fails when the newest commit of the device is a confirmed
// one that still waits for its confirmation, with ErrCommitPending, and when
// the commit history does not say.
func nothingPending(ctx context.Context, session *netconf.Session) error {
	commits, err := GetCommitInformation{}.Exec(ctx, session)
	if err != nil {
		return step("commit history", err)
	}
	if len(commits) == 0 || !commits[0].Pending {
		return nil
	}

	newest := commits[0]
	made := fmt.Sprintf("made by %s through %s", newest.User, newest.Client)
	if !newest.Time.IsZero() {
		made += " at " + newest.Time.Format(time.RFC3339)
	}
	return fmt.Errorf("junos: commit check: %w: %s", ErrCommitPending, made)
}

// validate refuses what cannot work before the candidate is taken for it.
func (c Change) validate(load LoadConfiguration, commit CommitConfiguration) error {
	switch {
	case c.Private && c.Confirm > 0:
		return errors.New("junos: a private copy cannot be committed with Confirm")
	case c.Verify != nil && c.Confirm <= 0:
		return errors.New("junos: Verify needs Confirm, there is no going back from a commit without it")
	case c.LockWait < 0:
		return errors.New("junos: LockWait cannot be negative")
	}

	// the operations know what Junos does not have
	for _, op := range []any{load, commit} {
		if _, err := xml.Marshal(op); err != nil {
			return fmt.Errorf("junos: %w", err)
		}
	}
	return nil
}

// reserve takes the candidate for the change: the lock on it, or a private
// copy of it.
func (c Change) reserve(ctx context.Context, session *netconf.Session) error {
	var (
		err error
		// answered says that Junos answered the request, or the last of
		// them
		answered bool
	)
	if c.Private {
		err = OpenConfiguration{Private: true}.Exec(ctx, session)
		if answered = err == nil || refused(err); err != nil {
			err = step("open private copy", modified(err))
		}
	} else if answered, err = c.lock(ctx, session); err != nil {
		err = step("lock candidate", modified(err))
	}

	// Junos saying no settles it, and so does the end of the session.
	// Anything else leaves open whether the request got through, and a
	// candidate reserved without anybody knowing of it stays reserved: it
	// is given back for the case that it was.  Junos refusing that is the
	// good outcome.
	if err != nil && !answered && !errors.Is(err, netconf.ErrClosed) {
		if relErr := c.release(ctx, session, false); relErr != nil && !refused(relErr) {
			err = errors.Join(err, relErr)
		}
	}
	return err
}

// lock takes the lock on the candidate.  With LockWait it asks again while
// another session holds the lock.  answered reports whether Junos answered
// the last request for it: one that failed without an answer may have got
// through all the same.
func (c Change) lock(ctx context.Context, session *netconf.Session) (answered bool, err error) {
	lock := rpc.Lock{Target: rpc.Candidate}
	err = lock.Exec(ctx, session)
	if c.LockWait <= 0 || !errors.Is(err, netconf.ErrLockDenied) {
		return err == nil || refused(err), err
	}

	// denied is the last refusal of the holder.  It is the error when the
	// wait ends, with why it ended behind it.
	denied := operr.Bare(err)
	waited := time.NewTimer(c.LockWait)
	defer waited.Stop()
	again := time.NewTicker(lockRetry)
	defer again.Stop()
	gaveUp := func() error {
		return fmt.Errorf("%w: gave up after waiting %s for the lock", denied, c.LockWait)
	}
	for {
		select {
		case <-ctx.Done():
			return true, fmt.Errorf("%w: %w", denied, ctx.Err())
		case <-waited.C:
			return true, gaveUp()
		case <-again.C:
		}
		// The context may have ended with the tick, or the time be up, as
		// it is after a request that took long: then nothing is sent.
		if ctx.Err() != nil {
			return true, fmt.Errorf("%w: %w", denied, ctx.Err())
		}
		select {
		case <-waited.C:
			return true, gaveUp()
		default:
		}

		err = lock.Exec(ctx, session)
		switch {
		case errors.Is(err, netconf.ErrLockDenied):
			denied = operr.Bare(err)
		case err == nil || refused(err):
			return true, err
		default:
			// the context or the session ended under the request
			return false, fmt.Errorf("%w: %w", denied, operr.Bare(err))
		}
	}
}

// release gives the candidate back.  dirty says that it may hold
// configuration that was loaded and not committed.
func (c Change) release(ctx context.Context, session *netconf.Session, dirty bool) error {
	// the context of the change may be why the change is being given up
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()

	// the end of the session gives the candidate back as well
	gone := func(err error) bool { return err == nil || errors.Is(err, netconf.ErrClosed) }

	if c.Private {
		// closing a private copy drops what is in it
		if err := (CloseConfiguration{}).Exec(ctx, session); !gone(err) {
			return step("the private copy was not closed, closing the session does that", err)
		}
		return nil
	}

	// Unlocking drops what was not committed: Juniper documents it and
	// Junos 26.2 does it, as it does when the session that has the lock
	// ends.  A statement left behind would go out with the next commit of
	// whoever comes after, so the candidate is put back first all the same,
	// for the releases nobody has looked at.  Nobody else has changes in
	// it: Junos does not lock a candidate that has any.
	var left error
	if dirty {
		if _, err := Rollback(0).Exec(ctx, session); !gone(err) {
			left = step("the candidate was not put back and may hold the change", err)
		}
	}
	if err := (rpc.Unlock{Target: rpc.Candidate}).Exec(ctx, session); !gone(err) {
		return errors.Join(left, step("the candidate was not unlocked, closing the session does that", err))
	}
	return left
}

// confirm makes a confirmed commit permanent, unless Verify or the clock is
// against it.  sent is when the confirmed commit went out.
func (c Change) confirm(ctx context.Context, session *netconf.Session, sent time.Time) (*netconf.Reply, error) {
	// Junos counts from when it has committed and takes its time after
	// that: a vJunos-router went back 109 seconds after a commit to confirm
	// within one minute.  So its deadline is not before this one.  A
	// confirmation that comes after it is refused, as Junos takes the lock
	// away together with the change.
	ctx, cancel := context.WithDeadline(ctx, sent.Add(c.Confirm))
	defer cancel()

	if c.Verify != nil {
		if err := c.Verify(ctx); err != nil {
			return nil, fmt.Errorf("junos: verify: %w: %w", ErrNotConfirmed, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("junos: confirm commit: %w: %w", ErrNotConfirmed, err)
	}

	// The confirmation is the empty commit Juniper documents for it.  It
	// has to reach the routing engines the confirmed commit went to.
	reply, err := CommitConfiguration{Synchronize: c.Synchronize}.Exec(ctx, session)
	switch {
	case err == nil:
		return reply, nil
	case refused(err):
		return reply, fmt.Errorf("junos: confirm commit: %w: %w", ErrNotConfirmed, operr.Bare(err))
	default:
		return reply, step("confirm commit", inDoubt(err))
	}
}

// step names what the change was doing in front of err.  It is the name of
// the step that the error keeps: the operation the step was made with, which
// its error begins with, would say the same thing again.
func step(doing string, err error) error {
	return fmt.Errorf("junos: %s: %w", doing, operr.Bare(err))
}

// refused reports whether err is the device's answer to what it was asked,
// as opposed to an answer that never came.
func refused(err error) bool {
	var rpcErrors netconf.RPCErrors
	return errors.As(err, &rpcErrors)
}

// modified says in front of the error of a candidate that was refused
// because it holds uncommitted changes what the refusal means.
func modified(err error) error {
	if errors.Is(err, ErrCandidateModified) {
		return fmt.Errorf("%w: %w", ErrCandidateModified, operr.Bare(err))
	}
	return err
}

// inDoubt marks the error of a commit whose outcome is not known.
func inDoubt(err error) error {
	if refused(err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrCommitUnknown, operr.Bare(err))
}
