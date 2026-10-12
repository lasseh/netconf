package junos_test

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/junos"
)

// A lock somebody else holds, captured from Junos 26.2 (vJunos-router): the
// error names the session that has it.  A load, a check and a commit that
// run into the lock get the same error.
const lockDenied = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>lock-denied</error-tag>
<error-severity>error</error-severity>
<error-message>
configuration database locked by:
  admin terminal  (pid 58225) on since 2026-10-09 09:09:20 UTC
      exclusive 
</error-message>
<error-info>
<session-id>58225</session-id>
</error-info>
</rpc-error>
`

// The candidate holds changes that nobody has committed, from the same
// capture: the lock is refused, to the one who made them as well.
const lockModified = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
configuration database modified
</error-message>
</rpc-error>
`

// What a load changed, and a load that changed nothing, as captured in a
// private copy.
const (
	diffReply = `<configuration-information>
<configuration-output>
[edit system ntp]
+   server 192.0.2.1;
</configuration-output>
</configuration-information>
`
	diffEmpty = `<configuration-information>
<configuration-output>
</configuration-output>
</configuration-information>
`
	wantDiff = "[edit system ntp]\n+   server 192.0.2.1;"
)

// Text that the device passes on, with a character in it that XML does not
// allow: behind it the reply cannot be read.
const notXML = "<note>a\x01b</note>\n"

// The steps of a change, as the scripted device names the operations it is
// sent.
const (
	stepLock      = "lock"
	stepUnlock    = "unlock"
	stepOpen      = "open"
	stepClose     = "close"
	stepLoad      = "load"
	stepRollback  = "rollback"
	stepCompare   = "compare"
	stepHistory   = "history"
	stepCheck     = "check"
	stepCommit    = "commit"
	stepConfirmed = "commit confirmed"
	stepConfirm   = "confirm"
)

// answer is what the scripted device does with one step.
type answer struct {
	body   string // the reply
	silent bool   // no reply at all
	dies   bool   // the session ends
	then   func() // called when the operation has arrived
}

// changeDevice is a device scripted for a Change.  It takes every operation
// of one, unless the test has something else in store for a step.
type changeDevice struct {
	sess *netconf.Session

	mu        sync.Mutex
	script    map[string]answer
	steps     []string
	ops       []string
	confirmed bool
}

func newChangeDevice(t *testing.T, script map[string]answer, opts ...netconf.SessionOption) *changeDevice {
	t.Helper()
	d := &changeDevice{script: script}

	var tr *netconftest.Transport
	tr = netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
		var rpc struct {
			Inner string `xml:",innerxml"`
		}
		_ = xml.Unmarshal([]byte(req), &rpc)

		a := d.receive(rpc.Inner)
		if a.then != nil {
			a.then()
		}
		switch {
		case a.dies:
			_ = tr.Close()
			return nil
		case a.silent:
			return nil
		}
		open := strings.Replace(replyOpen, "%ID%", netconftest.ExtractMessageID(req), 1)
		return []string{open + a.body + "</rpc-reply>\n"}
	}))

	sess, err := netconf.Open(context.Background(), tr, opts...)
	be.NilErr(t, err)
	d.sess = sess
	return d
}

// receive names the operation, notes it and returns what to do with it.
func (d *changeDevice) receive(op string) answer {
	d.mu.Lock()
	defer d.mu.Unlock()

	var (
		step string
		a    answer
	)
	switch {
	case strings.HasPrefix(op, "<lock"):
		step, a.body = stepLock, `<ok/>`
	case strings.HasPrefix(op, "<unlock"):
		step, a.body = stepUnlock, `<ok/>`
	case strings.HasPrefix(op, "<open-configuration"):
		step = stepOpen
	case strings.HasPrefix(op, "<close-configuration"):
		step = stepClose
	case strings.HasPrefix(op, `<load-configuration rollback="0"`):
		step, a.body = stepRollback, loadOK
	case strings.HasPrefix(op, "<load-configuration"):
		step, a.body = stepLoad, loadOK
	case strings.HasPrefix(op, `<get-configuration compare="rollback" rollback="0"`):
		step, a.body = stepCompare, diffReply
	case strings.HasPrefix(op, "<get-commit-information"):
		step, a.body = stepHistory, commitsConfirmed
	case strings.HasPrefix(op, "<commit-configuration><check>"):
		step, a.body = stepCheck, commitCheckOK
	case strings.HasPrefix(op, "<commit-configuration") && strings.Contains(op, "<confirmed>"):
		step, a.body = stepConfirmed, commitDone
		d.confirmed = true
	case strings.HasPrefix(op, "<commit-configuration") && d.confirmed:
		step, a.body = stepConfirm, commitDone
	case strings.HasPrefix(op, "<commit-configuration"):
		step, a.body = stepCommit, commitDone
	default:
		step = op
	}

	d.steps = append(d.steps, step)
	d.ops = append(d.ops, op)
	if scripted, ok := d.script[step]; ok {
		return scripted
	}
	return a
}

// answersLock changes what the device does with a request for the lock from
// now on.
func (d *changeDevice) answersLock(a answer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.script == nil {
		d.script = map[string]answer{}
	}
	d.script[stepLock] = a
}

func (d *changeDevice) received() (steps, ops []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.steps...), append([]string(nil), d.ops...)
}

// ntp is the change the tests make.
func ntp() junos.Change {
	return junos.Change{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1"}
}

// applyWithin applies the change and fails the test when that does not come
// to an end: a change that waits for something that never comes hangs.
func applyWithin(ctx context.Context, t *testing.T, change junos.Change, sess *netconf.Session) (junos.ChangeResult, error) {
	t.Helper()

	type outcome struct {
		result junos.ChangeResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := change.Apply(ctx, sess)
		done <- outcome{result, err}
	}()

	select {
	case o := <-done:
		return o.result, o.err
	case <-time.After(10 * time.Second):
		t.Fatal("Apply did not return")
		return junos.ChangeResult{}, nil
	}
}

// What goes out for a change that Junos takes, in order.
func TestChangeApply(t *testing.T) {
	const (
		lock   = `<lock xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target></lock>`
		unlock = `<unlock xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target></unlock>`
		load   = `<load-configuration action="set" format="text"><configuration-set>set system ntp server 192.0.2.1</configuration-set></load-configuration>`
		diff   = `<get-configuration compare="rollback" rollback="0" format="text"></get-configuration>`
	)

	tests := []struct {
		name   string
		change func(c *junos.Change)
		want   []string
	}{
		{
			name:   "under a lock",
			change: func(c *junos.Change) { c.Comment = "add ntp server" },
			want: []string{
				lock, load, diff,
				`<commit-configuration><log>add ntp server</log></commit-configuration>`,
				unlock,
			},
		},
		{
			name:   "in a private copy",
			change: func(c *junos.Change) { c.Private = true },
			want: []string{
				`<open-configuration><private></private></open-configuration>`,
				load, diff,
				`<commit-configuration></commit-configuration>`,
				`<close-configuration></close-configuration>`,
			},
		},
		{
			// the confirmation is the empty commit, without the comment
			name: "confirmed",
			change: func(c *junos.Change) {
				c.Comment = "add ntp server"
				c.Confirm = 5 * time.Minute
			},
			want: []string{
				lock, load, diff,
				`<commit-configuration><confirmed></confirmed><confirm-timeout>5</confirm-timeout><log>add ntp server</log></commit-configuration>`,
				`<commit-configuration></commit-configuration>`,
				unlock,
			},
		},
		{
			// every process reads the whole configuration again, once: the
			// confirmation is the empty commit
			name: "full, confirmed",
			change: func(c *junos.Change) {
				c.Full = true
				c.Confirm = time.Minute
			},
			want: []string{
				lock, load, diff,
				`<commit-configuration><confirmed></confirmed><confirm-timeout>1</confirm-timeout><full></full></commit-configuration>`,
				`<commit-configuration></commit-configuration>`,
				unlock,
			},
		},
		{
			// both commits have to reach both routing engines
			name: "confirmed on both routing engines",
			change: func(c *junos.Change) {
				c.Synchronize = true
				c.Confirm = 90 * time.Second
			},
			want: []string{
				lock, load, diff,
				`<commit-configuration><confirmed></confirmed><confirm-timeout>2</confirm-timeout><synchronize></synchronize></commit-configuration>`,
				`<commit-configuration><synchronize></synchronize></commit-configuration>`,
				unlock,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dev := newChangeDevice(t, nil)
			change := ntp()
			tc.change(&change)

			result, err := change.Apply(context.Background(), dev.sess)
			be.NilErr(t, err)
			be.True(t, result.Committed)
			be.Equal(t, wantDiff, result.Diff)

			_, ops := dev.received()
			be.AllEqual(t, tc.want, ops)
		})
	}
}

// Every way out of a change: what the device is sent, and what the caller
// gets to know.  The candidate is given back each time.
func TestChangeApply_WaysOut(t *testing.T) {
	errReview := errors.New("touches the routing protocols")
	errVerify := errors.New("bgp did not come back")

	type test struct {
		name   string
		change func(c *junos.Change, cancel context.CancelFunc)
		script func(cancel context.CancelFunc) map[string]answer

		wantSteps []string
		// is are errors the error has to be, isNot errors it must not be
		is, isNot []error
		// says is in the error's text, saysNot is not
		says, saysNot string
		committed     bool
		diff          string
		warnings      int
	}

	// refusedBy is the answer of a device that says no.
	const refused = `<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>no</error-message></rpc-error>`
	private := func(c *junos.Change, _ context.CancelFunc) { c.Private = true }
	confirmed := func(c *junos.Change, _ context.CancelFunc) { c.Confirm = 5 * time.Minute }

	tests := []test{
		{
			name: "the device has it already",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCompare: {body: diffEmpty}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepUnlock},
		},
		{
			name:   "the device has it already, private copy",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCompare: {body: diffEmpty}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepCompare, stepClose},
		},
		{
			// the commit history is read first: nothing waits there
			name:      "dry run",
			change:    func(c *junos.Change, _ context.CancelFunc) { c.DryRun = true },
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepHistory, stepCheck, stepRollback, stepUnlock},
			diff:      wantDiff,
		},
		{
			// what would be committed for real does not matter to a dry run
			name: "dry run of a confirmed change, private copy",
			change: func(c *junos.Change, _ context.CancelFunc) {
				c.DryRun, c.Private = true, true
				c.Comment = "add ntp server"
			},
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCheck: {body: commitCheckPrivateOK}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepCompare, stepHistory, stepCheck, stepClose},
			diff:      wantDiff,
		},
		{
			name: "dry run that Junos refuses",
			change: func(c *junos.Change, _ context.CancelFunc) {
				c.DryRun = true
			},
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCheck: {body: commitCheckFailed}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepHistory, stepCheck, stepRollback, stepUnlock},
			says:      "junos: commit check: ",
			is:        []error{netconf.ErrOperationFailed},
			isNot:     []error{junos.ErrCommitUnknown, junos.ErrCommitPending},
			diff:      wantDiff,
			warnings:  1,
		},
		{
			// Junos would take the check for the confirmation of somebody's
			// confirmed commit: none is sent, and the result says what the
			// change would do
			name:   "dry run while a confirmed commit is pending",
			change: func(c *junos.Change, _ context.CancelFunc) { c.DryRun = true },
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepHistory: {body: commitsPending}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepHistory, stepRollback, stepUnlock},
			says:      "junos: commit check: a confirmed commit is pending and a commit check would confirm it: made by operator through netconf at ",
			is:        []error{junos.ErrCommitPending},
			diff:      wantDiff,
		},
		{
			name:   "dry run while a confirmed commit is pending, private copy",
			change: func(c *junos.Change, _ context.CancelFunc) { c.DryRun, c.Private = true, true },
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepHistory: {body: commitsPending}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepCompare, stepHistory, stepClose},
			is:        []error{junos.ErrCommitPending},
			diff:      wantDiff,
		},
		{
			// whether a commit is pending is not known then
			name:   "dry run when the commit history does not come",
			change: func(c *junos.Change, _ context.CancelFunc) { c.DryRun = true },
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepHistory: {body: refused}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepHistory, stepRollback, stepUnlock},
			says:      "junos: commit history: ",
			is:        []error{netconf.ErrOperationFailed},
			isNot:     []error{junos.ErrCommitPending},
			diff:      wantDiff,
		},
		{
			// a device that has never committed has nothing pending
			name:   "dry run on a device without a commit",
			change: func(c *junos.Change, _ context.CancelFunc) { c.DryRun = true },
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepHistory: {body: "<commit-information>\n</commit-information>\n"}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepHistory, stepCheck, stepRollback, stepUnlock},
			diff:      wantDiff,
		},
		{
			// nothing to check, so nothing to look for either
			name:   "dry run of what the device has already",
			change: func(c *junos.Change, _ context.CancelFunc) { c.DryRun = true },
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCompare: {body: diffEmpty}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepUnlock},
		},
		{
			name: "the lock is somebody else's",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepLock: {body: lockDenied}}
			},
			wantSteps: []string{stepLock},
			says:      "junos: lock candidate: ",
			is:        []error{netconf.ErrLockDenied},
			isNot:     []error{junos.ErrCandidateModified},
		},
		{
			name: "somebody has changes in the candidate",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepLock: {body: lockModified}}
			},
			wantSteps: []string{stepLock},
			says:      "configuration database modified",
			is:        []error{junos.ErrCandidateModified, netconf.ErrOperationFailed},
			isNot:     []error{netconf.ErrLockDenied},
		},
		{
			name:   "somebody has changes in the candidate, private copy",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepOpen: {body: openPrivateModified}}
			},
			wantSteps: []string{stepOpen},
			says:      "junos: open private copy: ",
			is:        []error{junos.ErrCandidateModified, netconf.ErrOperationFailed},
		},
		{
			// Junos opens the copy with a warning and refuses the commit.
			name:   "the lock is somebody else's, private copy",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepOpen: {body: openPrivateLocked}, stepCommit: {body: lockDenied}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepCompare, stepCommit, stepClose},
			says:      "junos: commit: ",
			is:        []error{netconf.ErrLockDenied},
			isNot:     []error{junos.ErrCommitUnknown},
			diff:      wantDiff,
		},
		{
			name:   "no private copy to be had",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepOpen: {body: refused}}
			},
			wantSteps: []string{stepOpen},
			says:      "junos: open private copy: ",
			isNot:     []error{junos.ErrCandidateModified},
		},
		{
			// Whether the lock was taken is not known, so it is given back.
			name: "the lock is never answered",
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{stepLock: {silent: true, then: cancel}}
			},
			wantSteps: []string{stepLock, stepUnlock},
			is:        []error{context.Canceled},
			saysNot:   "not unlocked",
		},
		{
			// Junos saying that there is no lock to give back is the good
			// outcome, not a second problem.
			name: "the lock is never answered and was not taken",
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{
					stepLock:   {silent: true, then: cancel},
					stepUnlock: {body: refused},
				}
			},
			wantSteps: []string{stepLock, stepUnlock},
			is:        []error{context.Canceled},
			saysNot:   "not unlocked",
		},
		{
			name:   "the private copy is never answered and was not opened",
			change: private,
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{
					stepOpen:  {silent: true, then: cancel},
					stepClose: {body: closeNotOpen},
				}
			},
			wantSteps: []string{stepOpen, stepClose},
			is:        []error{context.Canceled},
			saysNot:   "not closed",
		},
		{
			// Junos has applied the lines that were right.  Nothing may be
			// left of them for the next one to commit.
			name: "part of the load is refused",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepLoad: {body: loadSetPartlyFailed}}
			},
			wantSteps: []string{stepLock, stepLoad, stepRollback, stepUnlock},
			says:      "junos: load configuration: ",
			is:        []error{netconf.ErrOperationFailed},
		},
		{
			name:   "part of the load is refused, private copy",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepLoad: {body: loadSyntaxError}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepClose},
			says:      "junos: load configuration: ",
			warnings:  1,
		},
		{
			name: "the load is never answered",
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{stepLoad: {silent: true, then: cancel}}
			},
			wantSteps: []string{stepLock, stepLoad, stepRollback, stepUnlock},
			is:        []error{context.Canceled},
			isNot:     []error{junos.ErrCommitUnknown},
		},
		{
			name: "the session ends during the load",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepLoad: {dies: true}}
			},
			wantSteps: []string{stepLock, stepLoad},
			is:        []error{netconf.ErrClosed},
			saysNot:   "not unlocked",
		},
		{
			name: "no diff to be had",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCompare: {body: refused}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepRollback, stepUnlock},
			says:      "junos: compare: ",
		},
		{
			name: "the review says no",
			change: func(c *junos.Change, _ context.CancelFunc) {
				c.Review = func(_ context.Context, diff string) error {
					if diff != wantDiff {
						return errors.New("the review was shown something else")
					}
					return errReview
				}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepRollback, stepUnlock},
			says:      "junos: review: ",
			is:        []error{errReview},
			diff:      wantDiff,
		},
		{
			// With the context over the commit is not sent, so there is
			// nothing to be in doubt about.
			name: "the context ends during the review",
			change: func(c *junos.Change, cancel context.CancelFunc) {
				c.Review = func(context.Context, string) error {
					cancel()
					return nil
				}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepRollback, stepUnlock},
			is:        []error{context.Canceled},
			isNot:     []error{junos.ErrCommitUnknown},
			diff:      wantDiff,
		},
		{
			name: "Junos refuses the commit",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {body: commitFailed}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit, stepRollback, stepUnlock},
			says:      "interface-mode access is allowed only for untagged interfaces",
			is:        []error{netconf.ErrInvalidValue},
			isNot:     []error{junos.ErrCommitUnknown, junos.ErrNotConfirmed},
			diff:      wantDiff,
			warnings:  1,
		},
		{
			// Junos said no, whatever became of the rest of its reply
			name: "Junos refuses the commit in a reply that is not XML to its end",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {body: commitFailed + notXML}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit, stepRollback, stepUnlock},
			says:      "interface-mode access is allowed only for untagged interfaces",
			is:        []error{netconf.ErrInvalidValue},
			isNot:     []error{junos.ErrCommitUnknown, junos.ErrNotConfirmed},
			diff:      wantDiff,
			warnings:  1,
		},
		{
			// Somebody else committed another value for the statement.  Junos
			// commits nothing and answers with a warning, without an error
			// and without <ok/>.
			name:   "the private copy no longer fits",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {body: commitPrivateConflict}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepCompare, stepCommit, stepClose},
			says:      "statement does not match patch",
			is:        []error{netconf.ErrElementNotFound},
			isNot:     []error{junos.ErrCommitUnknown, junos.ErrNotConfirmed},
			diff:      wantDiff,
			warnings:  1,
		},
		{
			// An answer that says neither yes nor no leaves it open.
			name: "the commit is answered with nothing",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {body: ""}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit, stepRollback, stepUnlock},
			says:      "junos: commit: ",
			is:        []error{junos.ErrCommitUnknown, netconf.ErrElementNotFound},
			diff:      wantDiff,
		},
		{
			// A warning is not a refusal.  Without a count of what Junos
			// could not load, the commit may have gone through.
			name: "the commit is answered with a warning and nothing else",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {body: strings.Replace(commitSynchronizeIgnored, "<ok/>\n", "", 1)}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit, stepRollback, stepUnlock},
			says:      "'synchronize' ignored",
			is:        []error{junos.ErrCommitUnknown, netconf.ErrElementNotFound},
			diff:      wantDiff,
			warnings:  1,
		},
		{
			name: "the load is answered with nothing",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepLoad: {body: ""}}
			},
			wantSteps: []string{stepLock, stepLoad, stepRollback, stepUnlock},
			says:      "junos: load configuration: ",
			is:        []error{netconf.ErrElementNotFound},
		},
		{
			// Junos may be committing: the rollback after it changes nothing
			// if it did, and puts the candidate back if it did not.
			name: "the commit is never answered",
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {silent: true, then: cancel}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit, stepRollback, stepUnlock},
			says:      "junos: commit: ",
			is:        []error{junos.ErrCommitUnknown, context.Canceled},
			isNot:     []error{junos.ErrNotConfirmed},
			diff:      wantDiff,
		},
		{
			name: "the session ends during the commit",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepCommit: {dies: true}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit},
			is:        []error{junos.ErrCommitUnknown, netconf.ErrClosed},
			saysNot:   "not unlocked",
			diff:      wantDiff,
		},
		{
			name:   "the confirmed commit is never answered",
			change: confirmed,
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{stepConfirmed: {silent: true, then: cancel}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepRollback, stepUnlock},
			is:        []error{junos.ErrCommitUnknown},
			isNot:     []error{junos.ErrNotConfirmed},
			diff:      wantDiff,
		},
		{
			// Junos went back to the previous configuration before the
			// confirmation arrived, and took the lock with it.  The unlock
			// after it is answered with <ok/> all the same.
			name:   "the confirmation comes too late",
			change: confirmed,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepConfirm: {body: accessRevoked}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepConfirm, stepUnlock},
			says:      "access has been revoked",
			is:        []error{junos.ErrNotConfirmed, netconf.ErrOperationFailed},
			isNot:     []error{junos.ErrCommitUnknown},
			diff:      wantDiff,
		},
		{
			// The change is in effect and Junos takes it back: nothing but
			// the unlock goes out, least of all a check, which confirms.
			name: "verify says no",
			change: func(c *junos.Change, _ context.CancelFunc) {
				c.Confirm = 5 * time.Minute
				c.Verify = func(context.Context) error { return errVerify }
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepUnlock},
			says:      "junos: verify: ",
			is:        []error{junos.ErrNotConfirmed, errVerify},
			isNot:     []error{junos.ErrCommitUnknown},
			diff:      wantDiff,
		},
		{
			// A confirmation after the time is up would find the change
			// rolled back and report it committed.
			name: "verify takes longer than there is time to confirm",
			change: func(c *junos.Change, _ context.CancelFunc) {
				c.Confirm = 30 * time.Millisecond
				c.Verify = func(ctx context.Context) error {
					<-ctx.Done()
					return nil
				}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepUnlock},
			is:        []error{junos.ErrNotConfirmed, context.DeadlineExceeded},
			diff:      wantDiff,
		},
		{
			name: "the context ends during verify",
			change: func(c *junos.Change, cancel context.CancelFunc) {
				c.Confirm = 5 * time.Minute
				c.Verify = func(ctx context.Context) error {
					cancel()
					<-ctx.Done()
					return nil
				}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepUnlock},
			is:        []error{junos.ErrNotConfirmed, context.Canceled},
			diff:      wantDiff,
		},
		{
			name:   "Junos refuses the confirmation",
			change: confirmed,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepConfirm: {body: refused}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepConfirm, stepUnlock},
			says:      "junos: confirm commit: ",
			is:        []error{junos.ErrNotConfirmed, netconf.ErrOperationFailed},
			isNot:     []error{junos.ErrCommitUnknown},
			diff:      wantDiff,
		},
		{
			name:   "Junos refuses the confirmation in a reply that is not XML to its end",
			change: confirmed,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepConfirm: {body: refused + notXML}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepConfirm, stepUnlock},
			says:      "junos: confirm commit: ",
			is:        []error{junos.ErrNotConfirmed, netconf.ErrOperationFailed},
			isNot:     []error{junos.ErrCommitUnknown},
			diff:      wantDiff,
		},
		{
			name:   "the confirmation is never answered",
			change: confirmed,
			script: func(cancel context.CancelFunc) map[string]answer {
				return map[string]answer{stepConfirm: {silent: true, then: cancel}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepConfirmed, stepConfirm, stepUnlock},
			says:      "junos: confirm commit: ",
			is:        []error{junos.ErrCommitUnknown, context.Canceled},
			isNot:     []error{junos.ErrNotConfirmed},
			diff:      wantDiff,
		},
		{
			// The change went through.  That the candidate is still locked
			// is the caller's to know all the same.
			name: "Junos refuses the unlock",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepUnlock: {body: refused}}
			},
			wantSteps: []string{stepLock, stepLoad, stepCompare, stepCommit, stepUnlock},
			says:      "the candidate was not unlocked, closing the session does that",
			committed: true,
			diff:      wantDiff,
		},
		{
			name: "the candidate cannot be put back",
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{
					stepLoad:     {body: loadSetPartlyFailed},
					stepRollback: {body: refused},
				}
			},
			wantSteps: []string{stepLock, stepLoad, stepRollback, stepUnlock},
			says:      "the candidate was not put back and may hold the change",
			saysNot:   "not unlocked",
		},
		{
			name:   "Junos refuses to close the private copy",
			change: private,
			script: func(context.CancelFunc) map[string]answer {
				return map[string]answer{stepClose: {body: refused}}
			},
			wantSteps: []string{stepOpen, stepLoad, stepCompare, stepCommit, stepClose},
			says:      "the private copy was not closed, closing the session does that",
			committed: true,
			diff:      wantDiff,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var script map[string]answer
			if tc.script != nil {
				script = tc.script(cancel)
			}
			dev := newChangeDevice(t, script)

			change := ntp()
			if tc.change != nil {
				tc.change(&change, cancel)
			}
			result, err := applyWithin(ctx, t, change, dev.sess)

			steps, _ := dev.received()
			be.AllEqual(t, tc.wantSteps, steps)

			failed := len(tc.is) > 0 || tc.says != ""
			be.Equal(t, failed, err != nil)
			for _, target := range tc.is {
				if !errors.Is(err, target) {
					t.Errorf("error is not %q: %v", target, err)
				}
			}
			for _, target := range tc.isNot {
				if errors.Is(err, target) {
					t.Errorf("error is %q: %v", target, err)
				}
			}
			if tc.says != "" {
				be.In(t, tc.says, err.Error())
			}
			if tc.saysNot != "" && err != nil && strings.Contains(err.Error(), tc.saysNot) {
				t.Errorf("error says %q: %v", tc.saysNot, err)
			}

			be.Equal(t, tc.committed, result.Committed)
			be.Equal(t, tc.diff, result.Diff)
			be.Equal(t, tc.warnings, len(result.Warnings))
		})
	}
}

// What cannot work is refused before the device hears of the change.
func TestChangeApply_RefusedUnsent(t *testing.T) {
	tests := []struct {
		name   string
		change junos.Change
		says   string
	}{
		{"nothing to load", junos.Change{}, "config cannot be nil"},
		{"unknown format", junos.Change{Format: "yaml", Config: "x"}, `unknown format "yaml"`},
		{"an action for set commands", junos.Change{Format: junos.FormatSet, Action: junos.LoadOverride, Config: "x"}, "does not apply to set commands"},
		{"confirm time below zero", junos.Change{Config: "<configuration/>", Confirm: -time.Minute}, "cannot be negative"},
		{"private and confirmed", junos.Change{Config: "<configuration/>", Private: true, Confirm: time.Minute}, "private copy cannot be committed with Confirm"},
		{
			"verify without confirm",
			junos.Change{Config: "<configuration/>", Verify: func(context.Context) error { return nil }},
			"Verify needs Confirm",
		},
		{
			// Junos refuses the commit for it, when the load has gone through
			"a comment longer than Junos takes",
			junos.Change{Config: "<configuration/>", Comment: strings.Repeat("x", 513)},
			"the comment is 513 bytes long, and Junos takes 512",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dev := newChangeDevice(t, nil)

			result, err := tc.change.Apply(context.Background(), dev.sess)
			be.Nonzero(t, err)
			be.In(t, tc.says, err.Error())
			be.False(t, result.Committed)

			steps, _ := dev.received()
			be.Equal(t, 0, len(steps))
		})
	}
}

// Verify has until the time to confirm is up, counted from when the
// confirmed commit went out, and its context says so.
func TestChangeApply_VerifyDeadline(t *testing.T) {
	// a commit takes its time, and Junos may count from when it began
	var arrived time.Time
	dev := newChangeDevice(t, map[string]answer{
		stepConfirmed: {body: commitDone, then: func() {
			arrived = time.Now()
			time.Sleep(100 * time.Millisecond)
		}},
	})

	var deadline time.Time
	change := ntp()
	change.Confirm = 90 * time.Second
	change.Verify = func(ctx context.Context) error {
		deadline, _ = ctx.Deadline()
		return nil
	}

	started := time.Now()
	result, err := change.Apply(context.Background(), dev.sess)
	be.NilErr(t, err)
	be.True(t, result.Committed)

	// Not the two minutes Junos is given, which is the time rounded up, and
	// not counted from when the commit was done.
	be.True(t, !deadline.Before(started.Add(change.Confirm)))
	be.True(t, !deadline.After(arrived.Add(change.Confirm)))
}

// The candidate is given back although the context of the change is over,
// and that does not take for ever either.
func TestChangeApply_ReleaseIsBounded(t *testing.T) {
	defer junos.SetReleaseTimeout(50 * time.Millisecond)()

	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlock", true: "close"}[private], func(t *testing.T) {
			dev := newChangeDevice(t, map[string]answer{
				stepUnlock: {silent: true},
				stepClose:  {silent: true},
			})
			change := ntp()
			change.Private = private

			done := make(chan error, 1)
			go func() {
				result, err := change.Apply(context.Background(), dev.sess)
				if !result.Committed {
					err = errors.Join(err, errors.New("not committed"))
				}
				done <- err
			}()

			select {
			case err := <-done:
				be.Nonzero(t, err)
				be.True(t, errors.Is(err, context.DeadlineExceeded))
				be.In(t, "closing the session does that", err.Error())
				be.False(t, strings.Contains(err.Error(), "not committed"))
			case <-time.After(5 * time.Second):
				t.Fatal("Apply waits for a release that is never answered")
			}
		})
	}
}

// A lock that got no answer may be held all the same.  When giving it back
// gets no answer either, the caller has to know: the candidate may stay
// locked until the session is closed.
func TestChangeApply_ReservationInDoubtNotReleased(t *testing.T) {
	defer junos.SetReleaseTimeout(50 * time.Millisecond)()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dev := newChangeDevice(t, map[string]answer{
		stepLock:   {silent: true, then: cancel},
		stepUnlock: {silent: true},
	})

	_, err := applyWithin(ctx, t, ntp(), dev.sess)
	be.True(t, errors.Is(err, context.Canceled))
	be.In(t, "junos: lock candidate: ", err.Error())
	be.In(t, "the candidate was not unlocked, closing the session does that", err.Error())

	steps, _ := dev.received()
	be.AllEqual(t, []string{stepLock, stepUnlock}, steps)
}

// The warnings of every step end up in the result.
func TestChangeApply_Warnings(t *testing.T) {
	const warned = `<rpc-error><error-severity>warning</error-severity><error-message>uncommitted changes will be discarded on exit</error-message></rpc-error>`

	dev := newChangeDevice(t, map[string]answer{
		stepLoad:      {body: loadWarned},
		stepConfirmed: {body: warned + commitDone},
		stepConfirm:   {body: warned + commitDone},
	})
	change := ntp()
	change.Confirm = time.Minute

	result, err := change.Apply(context.Background(), dev.sess)
	be.NilErr(t, err)
	be.True(t, result.Committed)
	be.Equal(t, 3, len(result.Warnings))
	be.Equal(t, "statement not found: country-code", result.Warnings[0].Message)
}

// A trace of the session sees every operation a change is made of, each
// under the name of its element, with the ones that give the candidate back.
func TestChangeApply_Traced(t *testing.T) {
	tests := []struct {
		name   string
		change func(c *junos.Change)
		script map[string]answer
		want   []string
	}{
		{
			name: "under a lock",
			want: []string{"lock", "load-configuration", "get-configuration", "commit-configuration", "unlock"},
		},
		{
			name:   "in a private copy, checked",
			change: func(c *junos.Change) { c.Private, c.DryRun = true, true },
			want:   []string{"open-configuration", "load-configuration", "get-configuration", "get-commit-information", "commit-configuration", "close-configuration"},
		},
		{
			name:   "confirmed",
			change: func(c *junos.Change) { c.Confirm = time.Minute },
			want:   []string{"lock", "load-configuration", "get-configuration", "commit-configuration", "commit-configuration", "unlock"},
		},
		{
			// the candidate is put back, which is a load as well
			name:   "a load that Junos refuses",
			script: map[string]answer{stepLoad: {body: loadSyntaxError}},
			want:   []string{"lock", "load-configuration", "load-configuration", "unlock"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu             sync.Mutex
				started, ended []string
				failed         int
			)
			dev := newChangeDevice(t, tc.script, netconf.WithTrace(&netconf.Trace{
				OperationStart: func(op netconf.OperationInfo) {
					mu.Lock()
					defer mu.Unlock()
					started = append(started, op.Name)
				},
				OperationDone: func(op netconf.OperationInfo, result netconf.OperationResult) {
					mu.Lock()
					defer mu.Unlock()
					ended = append(ended, op.Name)
					if result.Err != nil {
						failed++
					}
				},
			}))

			change := ntp()
			if tc.change != nil {
				tc.change(&change)
			}
			_, err := applyWithin(t.Context(), t, change, dev.sess)
			be.Equal(t, tc.script != nil, err != nil)

			mu.Lock()
			defer mu.Unlock()
			be.AllEqual(t, tc.want, started)
			be.AllEqual(t, tc.want, ended)
			be.Equal(t, len(tc.script), failed)
		})
	}
}

// With LockWait a change waits for a lock that another session holds: it
// asks again until it has the lock, the time is up or its context ends.
func TestChangeApply_LockWait(t *testing.T) {
	defer junos.SetLockRetry(5 * time.Millisecond)()

	// refusedTimes has the device refuse the lock n times and then grant it.
	refusedTimes := func(dev *changeDevice, n int) {
		refusals := 0
		dev.answersLock(answer{body: lockDenied, then: func() {
			refusals++
			if refusals == n {
				dev.answersLock(answer{body: `<ok/>`})
			}
		}})
	}

	t.Run("a lock that is given back in time", func(t *testing.T) {
		dev := newChangeDevice(t, nil)
		refusedTimes(dev, 2)
		change := ntp()
		change.LockWait = 5 * time.Second

		result, err := applyWithin(t.Context(), t, change, dev.sess)
		be.NilErr(t, err)
		be.True(t, result.Committed)

		steps, _ := dev.received()
		be.AllEqual(t, []string{stepLock, stepLock, stepLock, stepLoad, stepCompare, stepCommit, stepUnlock}, steps)
	})

	t.Run("a lock that is held for longer", func(t *testing.T) {
		dev := newChangeDevice(t, map[string]answer{stepLock: {body: lockDenied}})
		change := ntp()
		change.LockWait = 250 * time.Millisecond

		start := time.Now()
		_, err := applyWithin(t.Context(), t, change, dev.sess)
		be.True(t, time.Since(start) >= change.LockWait)

		// the last refusal, with who holds the lock, and why the wait ended
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		holder, _ := rpcErr.Detail("session-id")
		be.Equal(t, "58225", holder)
		be.In(t, "junos: lock candidate: netconf error: protocol lock-denied: ", err.Error())
		be.In(t, "gave up after waiting 250ms", err.Error())

		// nothing but the lock was asked for, more than once
		steps, _ := dev.received()
		be.True(t, len(steps) > 2)
		for _, step := range steps {
			be.Equal(t, stepLock, step)
		}
	})

	// The time is up while a request for the lock is on its way.  Its
	// refusal is the last one, and nothing more is asked.
	t.Run("the time is up under a request", func(t *testing.T) {
		// When that refusal arrives the next request is due as well, and
		// which of the two Go looks at first is a draw: often enough here
		// for every draw to have gone both ways.
		for range 12 {
			dev := newChangeDevice(t, nil)
			requests := 0
			dev.answersLock(answer{body: lockDenied, then: func() {
				// the first is refused at once, and the next takes longer
				// than the wait
				requests++
				if requests > 1 {
					time.Sleep(60 * time.Millisecond)
				}
			}})
			change := ntp()
			change.LockWait = 30 * time.Millisecond

			_, err := applyWithin(t.Context(), t, change, dev.sess)
			be.True(t, errors.Is(err, netconf.ErrLockDenied))
			be.In(t, "gave up after waiting 30ms", err.Error())

			// two requests, or one on a machine so slow that the time was
			// up before the second went out: never a third
			steps, _ := dev.received()
			be.True(t, len(steps) <= 2)
			for _, step := range steps {
				be.Equal(t, stepLock, step)
			}
		}
	})

	// A refusal is one also when the reply is not XML behind it: the wait
	// goes on, and no lock is given back that the session never had.
	t.Run("a refusal in a reply that is not XML to its end", func(t *testing.T) {
		dev := newChangeDevice(t, map[string]answer{stepLock: {body: lockDenied + notXML}})
		change := ntp()
		change.LockWait = 250 * time.Millisecond

		_, err := applyWithin(t.Context(), t, change, dev.sess)
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
		be.In(t, "gave up after waiting 250ms", err.Error())

		steps, _ := dev.received()
		be.True(t, len(steps) > 2)
		for _, step := range steps {
			be.Equal(t, stepLock, step)
		}
	})

	// The context ends between two requests for the lock: the last refusal
	// is the answer, with the end of the context behind it.
	t.Run("a context that ends during the wait", func(t *testing.T) {
		defer junos.SetLockRetry(time.Hour)()
		dev := newChangeDevice(t, map[string]answer{stepLock: {body: lockDenied}})
		change := ntp()
		change.LockWait = 2 * time.Hour

		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		_, err := applyWithin(ctx, t, change, dev.sess)
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		be.True(t, errors.Is(err, netconf.ErrLockDenied))

		steps, _ := dev.received()
		be.AllEqual(t, []string{stepLock}, steps)
	})

	// The context ends while a request for the lock has no answer.  The
	// error is the same: the last refusal, with the end of the context
	// behind it.  Whether Junos granted the request that got no answer is
	// not known, so the lock is given back for the case that it did.
	t.Run("a context that ends during a request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		dev := newChangeDevice(t, nil)
		dev.answersLock(answer{body: lockDenied, then: func() {
			dev.answersLock(answer{silent: true, then: cancel})
		}})
		change := ntp()
		change.LockWait = time.Hour

		_, err := applyWithin(ctx, t, change, dev.sess)
		be.True(t, errors.Is(err, context.Canceled))
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		holder, _ := rpcErr.Detail("session-id")
		be.Equal(t, "58225", holder)
		be.In(t, "junos: lock candidate: netconf error: protocol lock-denied: ", err.Error())
		// the operation is not named next to the step, for either request
		be.Equal(t, 0, strings.Count(err.Error(), "lock: "))

		steps, _ := dev.received()
		be.AllEqual(t, []string{stepLock, stepLock, stepUnlock}, steps)
	})

	// The context ends as a refusal arrives.  Whichever of the two the
	// change sees first, the error is the same.
	t.Run("a context that ends with a refusal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		dev := newChangeDevice(t, nil)
		dev.answersLock(answer{body: lockDenied, then: func() {
			dev.answersLock(answer{body: lockDenied, then: cancel})
		}})
		change := ntp()
		change.LockWait = time.Hour

		_, err := applyWithin(ctx, t, change, dev.sess)
		be.True(t, errors.Is(err, context.Canceled))
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
	})

	// Uncommitted changes in the candidate are no lock to wait for: nothing
	// says that they will ever go away.
	t.Run("a candidate with changes in it is not waited for", func(t *testing.T) {
		for _, wait := range []time.Duration{0, time.Hour} {
			dev := newChangeDevice(t, map[string]answer{stepLock: {body: lockModified}})
			change := ntp()
			change.LockWait = wait

			_, err := applyWithin(t.Context(), t, change, dev.sess)
			be.True(t, errors.Is(err, junos.ErrCandidateModified))
			steps, _ := dev.received()
			be.AllEqual(t, []string{stepLock}, steps)
		}
	})

	t.Run("without it the first refusal is the answer", func(t *testing.T) {
		dev := newChangeDevice(t, nil)
		refusedTimes(dev, 1)

		_, err := applyWithin(t.Context(), t, ntp(), dev.sess)
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
		steps, _ := dev.received()
		be.AllEqual(t, []string{stepLock}, steps)
	})

	// a private copy takes no lock, so there is nothing to wait for
	t.Run("a private copy", func(t *testing.T) {
		dev := newChangeDevice(t, nil)
		change := ntp()
		change.Private, change.LockWait = true, time.Hour

		result, err := applyWithin(t.Context(), t, change, dev.sess)
		be.NilErr(t, err)
		be.True(t, result.Committed)
		steps, _ := dev.received()
		be.AllEqual(t, []string{stepOpen, stepLoad, stepCompare, stepCommit, stepClose}, steps)
	})

	t.Run("a wait below zero", func(t *testing.T) {
		dev := newChangeDevice(t, nil)
		change := ntp()
		change.LockWait = -time.Second

		_, err := applyWithin(t.Context(), t, change, dev.sess)
		be.Nonzero(t, err)
		be.In(t, "LockWait cannot be negative", err.Error())
		steps, _ := dev.received()
		be.Equal(t, 0, len(steps))
	})
}
