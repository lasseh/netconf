// Command standard-edit commits a model-specific XML payload through the candidate.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/examples/internal/exampleutil"
	"github.com/lasseh/netconf/rpc"
)

func main() {
	configFile := flag.String("config", "", "required file containing configuration XML, without a <config> wrapper")
	commit := flag.Bool("commit", false, "required acknowledgement that this example commits without post-commit verification")
	exampleutil.Main(func(ctx context.Context, session *netconf.Session) error {
		if !*commit || *configFile == "" {
			return errors.New("both -config and -commit are required; this example permanently changes running configuration")
		}
		config, err := os.ReadFile(*configFile)
		if err != nil {
			return fmt.Errorf("read configuration: %w", err)
		}
		changeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return editCandidate(changeCtx, session, config)
	})
}

// editCandidate assumes exclusive application ownership of this session.
// Its caller must close the session on every exit, including uncertain lock
// or commit outcomes. Do not turn this into an automatic retry loop.
func editCandidate(ctx context.Context, session *netconf.Session, config []byte) (err error) {
	caps := session.ServerCaps()
	if !caps.Has(":candidate:1.0") || !caps.Has(":validate") {
		return errors.New("this example requires candidate and validate capabilities")
	}
	if err := (rpc.Lock{Target: rpc.Candidate}).Exec(ctx, session); err != nil {
		return fmt.Errorf("lock candidate; caller must close session if ownership is uncertain: %w", err)
	}

	dirty, uncertain := false, false
	committed := false
	defer func() {
		if uncertain {
			// No more writes on a session with an unknown commit outcome.
			// The runner closes it; reconcile running state before retrying.
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if dirty {
			if discardErr := (rpc.DiscardChanges{}).Exec(cleanupCtx, session); discardErr != nil {
				err = errors.Join(err, fmt.Errorf("discard failed; candidate may still hold edits: %w", discardErr))
				return // do not deliberately unlock a candidate with leftover edits
			}
		}
		if unlockErr := (rpc.Unlock{Target: rpc.Candidate}).Exec(cleanupCtx, session); unlockErr != nil {
			if committed {
				err = errors.Join(err, fmt.Errorf("commit succeeded, but unlock failed; do not apply again: %w", unlockErr))
			} else {
				err = errors.Join(err, fmt.Errorf("unlock failed; caller must close session: %w", unlockErr))
			}
		}
	}()

	running, err := (rpc.GetConfig{Source: rpc.Running}).Exec(ctx, session)
	if err != nil {
		return fmt.Errorf("read running: %w", err)
	}
	candidate, err := (rpc.GetConfig{Source: rpc.Candidate}).Exec(ctx, session)
	if err != nil {
		return fmt.Errorf("read candidate: %w", err)
	}
	// Deliberately conservative: XML formatting differences can refuse an
	// otherwise clean candidate. Never discard pre-existing edits here.
	if !bytes.Equal(bytes.TrimSpace(running), bytes.TrimSpace(candidate)) {
		return errors.New("candidate differs from running; refusing to commit or discard pre-existing changes")
	}
	dirty = true // a refused edit can still have loaded accepted statements
	if err := (rpc.EditConfig{Target: rpc.Candidate, Config: config}).Exec(ctx, session); err != nil {
		return fmt.Errorf("edit candidate: %w", err)
	}
	if err := (rpc.Validate{Source: rpc.Candidate}).Exec(ctx, session); err != nil {
		return fmt.Errorf("validate candidate: %w", err)
	}
	if err := (rpc.Commit{}).Exec(ctx, session); err != nil {
		var deviceErrors netconf.RPCErrors
		var syntaxErr *netconf.SyntaxError
		if !errors.As(err, &deviceErrors) || errors.As(err, &syntaxErr) {
			uncertain = true
			return fmt.Errorf("commit outcome unknown; inspect running on a new session before retrying: %w", err)
		}
		return fmt.Errorf("commit refused: %w", err)
	}
	dirty = false
	committed = true
	return nil
}
