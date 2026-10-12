package junos_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
)

// The operations of this package, by the name their errors begin with.
var named = map[string]func(context.Context, *netconf.Session) error{
	"get-configuration": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.GetConfiguration{}.Exec(ctx, s)
		return err
	},
	"compare rollback": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.CompareRollback{}.Exec(ctx, s)
		return err
	},
	"load-configuration": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1"}.Exec(ctx, s)
		return err
	},
	"rollback": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.Rollback(0).Exec(ctx, s)
		return err
	},
	"load rescue": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.LoadRescue{}.Exec(ctx, s)
		return err
	},
	"commit-configuration": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.CommitConfiguration{}.Exec(ctx, s)
		return err
	},
	"open-configuration":  junos.OpenConfiguration{Private: true}.Exec,
	"close-configuration": junos.CloseConfiguration{}.Exec,
	"command": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.Command("show version").Exec(ctx, s)
		return err
	},
	"command, as JSON": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.Command("show version").JSON(ctx, s)
		return err
	},
	"command, as XML": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.Command("show version").XML(ctx, s)
		return err
	},
	"get-system-information": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.GetSystemInformation{}.Exec(ctx, s)
		return err
	},
	"get-commit-information": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.GetCommitInformation{}.Exec(ctx, s)
		return err
	},
	"get-database-status-information": func(ctx context.Context, s *netconf.Session) error {
		_, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, s)
		return err
	},
}

// Every error of an operation starts with the name of the operation: what
// Junos refused, a session that is over and a context that is.  What is
// underneath stays within reach of errors.Is and errors.As.
func TestExec_ErrorsNameTheOperation(t *testing.T) {
	for name, exec := range named {
		name, _, _ = strings.Cut(name, ",")

		starts := func(t *testing.T, err error) {
			t.Helper()
			be.Nonzero(t, err)
			if !strings.HasPrefix(err.Error(), name+": ") {
				t.Errorf("error does not start with %q: %v", name+": ", err)
			}
			// and names it once
			be.Equal(t, 1, strings.Count(err.Error(), name+": "))
		}

		t.Run(name+"/refused by the device", func(t *testing.T) {
			sess, _ := session(t, lockDenied)
			err := exec(t.Context(), sess)
			starts(t, err)
			be.True(t, strings.HasPrefix(err.Error(), name+": netconf error: protocol lock-denied: configuration database locked by:"))

			be.True(t, errors.Is(err, netconf.ErrLockDenied))
			var rpcErrs netconf.RPCErrors
			be.True(t, errors.As(err, &rpcErrs))
			be.Equal(t, 1, len(rpcErrs))
			var rpcErr netconf.RPCError
			be.True(t, errors.As(err, &rpcErr))
			holder, _ := rpcErr.Detail("session-id")
			be.Equal(t, "58225", holder)
		})

		t.Run(name+"/the session is over", func(t *testing.T) {
			sess, _ := session(t, `<ok/>`)
			be.NilErr(t, sess.Close(t.Context()))

			err := exec(t.Context(), sess)
			starts(t, err)
			be.True(t, errors.Is(err, netconf.ErrClosed))
		})

		t.Run(name+"/the context is over", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			sess, _ := session(t, `<ok/>`)
			err := exec(ctx, sess)
			starts(t, err)
			be.True(t, errors.Is(err, context.Canceled))
		})
	}

	// what an operation makes of a reply that is not the one it expects
	for name, reply := range map[string]string{
		"get-configuration":               `<ok/>`,
		"compare rollback":                `<ok/>`,
		"load-configuration":              ``,
		"rollback":                        ``,
		"load rescue":                     ``,
		"commit-configuration":            ``,
		"command":                         `<software-information/>`,
		"command, as JSON":                `<software-information/>`,
		"get-system-information":          `<ok/>`,
		"get-commit-information":          `<ok/>`,
		"get-database-status-information": `<ok/>`,
	} {
		t.Run(name+"/answered with something else", func(t *testing.T) {
			sess, _ := session(t, reply)
			err := named[name](t.Context(), sess)

			name, _, _ := strings.Cut(name, ",")
			be.Nonzero(t, err)
			if !strings.HasPrefix(err.Error(), name+": ") {
				t.Errorf("error does not start with %q: %v", name+": ", err)
			}
			be.Equal(t, 1, strings.Count(err.Error(), name+": "))
		})
	}
}

// A Change names its steps, and the operation of a step is not named next
// to it: "lock candidate: lock: ..." says the same thing twice.
func TestChangeApply_NamesAStepOnce(t *testing.T) {
	defer junos.SetReleaseTimeout(50 * time.Millisecond)()

	const refused = `<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>refused by the test</error-message></rpc-error>`

	// the names of the operations a change is made of
	operations := []string{
		"lock: ", "unlock: ", "open-configuration: ", "close-configuration: ",
		"load-configuration: ", "rollback: ", "compare rollback: ", "commit-configuration: ",
	}

	// a change that loads a patch is a change like any other
	t.Run("a patch", func(t *testing.T) {
		dev := newChangeDevice(t, map[string]answer{stepLoad: {body: loadPatchNotApplied}})
		_, err := applyWithin(t.Context(), t, junos.Change{Action: junos.LoadPatch, Config: wantDiff}, dev.sess)
		be.In(t, "junos: load configuration: element not found in reply: <ok>", err.Error())
		be.In(t, "statement does not exist", err.Error())
		_, ops := dev.received()
		be.In(t, `<load-configuration action="patch" format="text"><configuration-patch>`, ops[1])
	})

	tests := []struct {
		name   string
		change func(c *junos.Change)
		script map[string]answer
		step   string
	}{
		{name: "lock", script: map[string]answer{stepLock: {body: lockDenied}}, step: "junos: lock candidate: netconf error"},
		{
			name:   "open",
			change: func(c *junos.Change) { c.Private = true },
			script: map[string]answer{stepOpen: {body: openPrivateModified}},
			step:   "junos: open private copy: the candidate holds changes that nobody has committed: netconf error",
		},
		{name: "load", script: map[string]answer{stepLoad: {body: refused}}, step: "junos: load configuration: netconf error"},
		{name: "load without ok", script: map[string]answer{stepLoad: {body: ""}}, step: "junos: load configuration: element not found in reply: <ok>"},
		{name: "compare", script: map[string]answer{stepCompare: {body: refused}}, step: "junos: compare: netconf error"},
		{name: "compare without a diff", script: map[string]answer{stepCompare: {body: "<ok/>"}}, step: "junos: compare: element not found in reply"},
		{
			name:   "check",
			change: func(c *junos.Change) { c.DryRun = true },
			script: map[string]answer{stepCheck: {body: refused}},
			step:   "junos: commit check: netconf error",
		},
		{name: "commit", script: map[string]answer{stepCommit: {body: refused}}, step: "junos: commit: netconf error"},
		{name: "commit without an answer", script: map[string]answer{stepCommit: {dies: true}}, step: "junos: commit: outcome of the commit unknown: netconf: session closed"},
		{
			name:   "confirm",
			change: func(c *junos.Change) { c.Confirm = time.Minute },
			script: map[string]answer{stepConfirm: {body: refused}},
			step:   "junos: confirm commit: commit not confirmed, Junos goes back to the previous configuration by itself: netconf error",
		},
		{
			name:   "rollback and unlock",
			script: map[string]answer{stepLoad: {body: refused}, stepRollback: {body: refused}, stepUnlock: {body: refused}},
			step:   "junos: the candidate was not unlocked, closing the session does that: netconf error",
		},
		{
			name:   "rollback",
			script: map[string]answer{stepLoad: {body: refused}, stepRollback: {body: refused}},
			step:   "junos: the candidate was not put back and may hold the change: netconf error",
		},
		{
			name:   "close",
			change: func(c *junos.Change) { c.Private = true },
			script: map[string]answer{stepClose: {body: refused}},
			step:   "junos: the private copy was not closed, closing the session does that: netconf error",
		},
		{
			name:   "unlock without an answer",
			script: map[string]answer{stepUnlock: {silent: true}},
			step:   "junos: the candidate was not unlocked, closing the session does that: context deadline exceeded",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dev := newChangeDevice(t, tc.script)
			change := ntp()
			if tc.change != nil {
				tc.change(&change)
			}

			_, err := applyWithin(t.Context(), t, change, dev.sess)
			be.Nonzero(t, err)
			be.In(t, tc.step, err.Error())
			for _, operation := range operations {
				if strings.Contains(err.Error(), operation) {
					t.Errorf("error names the operation %q next to the step: %v", operation, err)
				}
			}
		})
	}
}
