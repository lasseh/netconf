package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
)

// Every error of an operation starts with the name of the operation: what
// the device refused, a session that is over and a context that is.  What is
// underneath stays within reach of errors.Is and errors.As.
func TestExec_ErrorsNameTheOperation(t *testing.T) {
	const denied = `<rpc-error><error-type>protocol</error-type><error-tag>lock-denied</error-tag><error-severity>error</error-severity><error-message>held by session 7</error-message></rpc-error>`

	ops := map[string]func(context.Context, *netconf.Session) error{
		"get": func(ctx context.Context, s *netconf.Session) error {
			_, err := Get{}.Exec(ctx, s)
			return err
		},
		"get-config": func(ctx context.Context, s *netconf.Session) error {
			_, err := GetConfig{Source: Running}.Exec(ctx, s)
			return err
		},
		"edit-config":     EditConfig{Target: Candidate, Config: `<x/>`}.Exec,
		"copy-config":     CopyConfig{Source: Running, Target: Startup}.Exec,
		"delete-config":   DeleteConfig{Target: Startup}.Exec,
		"lock":            Lock{Target: Candidate}.Exec,
		"unlock":          Unlock{Target: Candidate}.Exec,
		"partial-unlock":  PartialUnlock{LockID: 1}.Exec,
		"validate":        Validate{Source: Candidate}.Exec,
		"commit":          Commit{}.Exec,
		"cancel-commit":   CancelCommit{}.Exec,
		"discard-changes": DiscardChanges{}.Exec,
		"kill-session":    KillSession{SessionID: 7}.Exec,
		"partial-lock": func(ctx context.Context, s *netconf.Session) error {
			_, err := PartialLock{Select: []string{"/x"}}.Exec(ctx, s)
			return err
		},
		"create-subscription": CreateSubscription{}.Exec,
	}

	for name, exec := range ops {
		named := func(t *testing.T, err error) {
			t.Helper()
			be.Nonzero(t, err)
			if !strings.HasPrefix(err.Error(), name+": ") {
				t.Errorf("error does not start with %q: %v", name+": ", err)
			}
			// and names it once
			be.Equal(t, 1, strings.Count(err.Error(), name+": "))
		}

		t.Run(name+"/refused by the device", func(t *testing.T) {
			err := exec(t.Context(), mockSession(t, denied))
			named(t, err)
			be.Equal(t, name+": netconf error: protocol lock-denied: held by session 7", err.Error())

			be.True(t, errors.Is(err, netconf.ErrLockDenied))
			var rpcErrs netconf.RPCErrors
			be.True(t, errors.As(err, &rpcErrs))
			be.Equal(t, 1, len(rpcErrs))
			var rpcErr netconf.RPCError
			be.True(t, errors.As(err, &rpcErr))
			be.Equal(t, "held by session 7", rpcErr.Message)
		})

		t.Run(name+"/the session is over", func(t *testing.T) {
			sess := mockSession(t, `<ok/>`)
			be.NilErr(t, sess.Close(t.Context()))

			err := exec(t.Context(), sess)
			named(t, err)
			be.True(t, errors.Is(err, netconf.ErrClosed))
		})

		t.Run(name+"/the context is over", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			err := exec(ctx, mockSession(t, `<ok/>`))
			named(t, err)
			be.True(t, errors.Is(err, context.Canceled))
		})

		t.Run(name+"/answered with something else", func(t *testing.T) {
			err := exec(t.Context(), mockSession(t, `<something-else/>`))
			named(t, err)
			be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		})
	}
}
