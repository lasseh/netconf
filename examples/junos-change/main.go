// Command junos-change previews a Junos set-format change, or explicitly commits it.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/examples/internal/exampleutil"
	"github.com/lasseh/netconf/junos"
)

func main() {
	configFile := flag.String("config", "", "required file containing Junos set/delete commands")
	commit := flag.Bool("commit", false, "commit with confirmation; default a private-copy dry run")
	exampleutil.Main(func(ctx context.Context, session *netconf.Session) error {
		if *configFile == "" {
			return errors.New("-config is required")
		}
		if !junos.IsJunos(session) {
			return errors.New("device does not advertise the Junos capability")
		}
		config, err := os.ReadFile(*configFile)
		if err != nil {
			return fmt.Errorf("read configuration: %w", err)
		}
		token := "example-" + rand.Text()
		log.Printf("change token: %s", token) // keep this to reconcile a lost commit reply
		change := junos.Change{
			Format:  junos.FormatSet,
			Config:  string(config),
			Private: !*commit,
			DryRun:  !*commit,
			Comment: "netconf example " + token,
			Review: func(_ context.Context, diff string) error {
				_, err := fmt.Print(diff) // may contain configuration secrets
				if err != nil {
					return fmt.Errorf("write diff: %w", err)
				}
				return nil
			},
		}
		if *commit {
			change.Confirm = 5 * time.Minute
			change.Verify = func(ctx context.Context) error {
				// Reachability only. Replace this with checks for your change
				// before using the pattern for production configuration.
				_, err := (junos.GetSystemInformation{}).Exec(ctx, session)
				return err
			}
		}
		changeCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
		defer cancel()
		result, err := change.Apply(changeCtx, session)
		for i := range result.Warnings {
			log.Printf("device warning: %s", result.Warnings[i].Message)
		}
		switch {
		case errors.Is(err, junos.ErrCommitUnknown):
			return fmt.Errorf("do not retry; inspect commit history for token %s using a fresh context/session: %w", token, err)
		case errors.Is(err, junos.ErrNotConfirmed):
			return fmt.Errorf("commit was not confirmed; check that Junos rolls it back: %w", err)
		case err != nil && result.Committed:
			return fmt.Errorf("committed, but cleanup failed; do not apply again: %w", err)
		case err != nil:
			return fmt.Errorf("change: %w", err)
		default:
			log.Printf("committed=%t dry_run=%t changed=%t", result.Committed, change.DryRun, result.Diff != "")
			return nil
		}
	})
}
