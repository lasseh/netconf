package junos_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
)

// A configuration change.  The commit is a confirmed one: if the change cuts
// the device off, or Verify does not like what it sees, Junos goes back to
// the old configuration by itself.
func ExampleChange() {
	var session *netconf.Session // from netconf.Open

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := junos.Change{
		Format:  junos.FormatSet,
		Config:  "set system ntp server 192.0.2.1",
		Comment: "add ntp server",
		Confirm: 5 * time.Minute,
		Verify: func(ctx context.Context) error {
			// the new configuration is in effect: is the device still well?
			_, err := junos.Command("show ntp associations").Exec(ctx, session)
			return err
		},
	}.Apply(ctx, session)

	for i := range result.Warnings {
		log.Print(result.Warnings[i])
	}
	switch {
	case errors.Is(err, junos.ErrNotConfirmed):
		log.Printf("in effect, and Junos is taking it back: %v", err)
	case errors.Is(err, junos.ErrCommitUnknown):
		log.Printf("ask the device whether it committed: %v", err)
	case err != nil && result.Committed:
		// the candidate was not given back, and stays locked until the
		// session is closed
		log.Printf("committed, and: %v", err)
	case err != nil:
		// nothing was committed; the error names the statement Junos refused
		log.Print(err)
	case !result.Committed:
		// the load changed nothing: the warnings above tell a patch that
		// Junos made nothing of from a device that had it already
		fmt.Println("nothing to commit")
	default:
		fmt.Println(result.Diff)
	}
}

// What a change would do, and whether Junos would take it, without
// committing anything.
func ExampleChange_dryRun() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	result, err := junos.Change{
		Format: junos.FormatText,
		Config: "system { ntp { server 192.0.2.1; } }",
		// a private copy does not need the lock on the candidate
		Private: true,
		DryRun:  true,
	}.Apply(ctx, session)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Diff)
}

// One session for reads from several goroutines and for changes.  Junos
// reserves the candidate for the session, not for one Apply, so the changes
// go one after the other, here from one goroutine while the others read.
// Changes that have to run at the same time each need a session of their
// own.
func ExampleChange_sharedSession() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	var reads sync.WaitGroup
	for _, name := range []string{"ge-0/0/0", "ge-0/0/1"} {
		reads.Go(func() {
			out, err := junos.Command("show interfaces terse "+name).Exec(ctx, session)
			if err != nil {
				log.Print(err)
				return
			}
			fmt.Println(out)
		})
	}

	for _, server := range []string{"192.0.2.1", "192.0.2.2"} {
		_, err := junos.Change{
			Format: junos.FormatSet,
			Config: "set system ntp server " + server,
		}.Apply(ctx, session)
		if err != nil {
			log.Print(err)
			break
		}
	}
	reads.Wait()
}

// A change that is committed only when a function of yours agrees with what
// it does to the configuration.
func ExampleChange_review() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	errTouchesBGP := errors.New("this change may not touch bgp")

	_, err := junos.Change{
		Format: junos.FormatSet,
		Config: "delete system ntp server 192.0.2.1",
		Review: func(_ context.Context, diff string) error {
			if strings.Contains(diff, "[edit protocols bgp") {
				return errTouchesBGP
			}
			return nil
		},
	}.Apply(ctx, session)
	if errors.Is(err, errTouchesBGP) {
		log.Print("left as it was")
	}
}

// Operational data, the same on a single device and on one that answers once
// per routing engine.
func ExampleDecode() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	reply, err := session.ExecReply(ctx, "<get-route-engine-information/>")
	if err != nil {
		log.Fatal(err)
	}

	var engines []struct {
		Slot  int    `xml:"slot"`
		State string `xml:"mastership-state"`
		Model string `xml:"model"`
	}
	if err := junos.Decode(reply, "route-engine", &engines); err != nil {
		log.Fatal(err)
	}
	for _, re := range engines {
		fmt.Println(re.Slot, re.State, re.Model)
	}
}

// Two refusals that Junos has no error tag for: a request the login class
// of the user does not permit, and one the platform does not have.
func ExampleErrPermissionDenied() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	_, err := session.ExecReply(ctx, "<get-chassis-cluster-status/>")
	switch {
	case errors.Is(err, junos.ErrPermissionDenied):
		fmt.Println("the login class of the user does not permit it")
	case errors.Is(err, junos.ErrUnsupported):
		fmt.Println("this platform or release has no such request")
	}
}

// What a program asks a Junos device first: what it is, what it is
// configured to do, and what it says to a command.
func ExampleGetConfiguration() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	info, err := junos.GetSystemInformation{}.Exec(ctx, session) // name, model, version, serial
	if err != nil {
		log.Fatal(err)
	}

	text, err := junos.GetConfiguration{Format: junos.FormatText}.Exec(ctx, session)
	if err != nil {
		log.Fatal(err)
	}
	small, err := junos.GetConfiguration{Format: junos.FormatJSONMinified}.Exec(ctx, session) // or FormatXMLMinified
	if err != nil {
		log.Fatal(err)
	}
	diff, err := junos.CompareRollback{Rollback: 1}.Exec(ctx, session)
	if err != nil {
		log.Fatal(err)
	}

	out, err := junos.Command("show bgp summary").Exec(ctx, session) // as the CLI prints it
	if err != nil {
		log.Fatal(err)
	}
	doc, err := junos.Command("show bgp summary").JSON(ctx, session) // as JSON
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(info, len(text), len(small), diff, out, len(doc))
}

func ExampleCommand() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	out, err := junos.Command("show system uptime").Exec(ctx, session)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
}

// The commit history: who committed what and when, and whether a confirmed
// commit still waits for its confirmation.
func ExampleGetCommitInformation() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	commits, err := junos.GetCommitInformation{}.Exec(ctx, session)
	if err != nil {
		log.Print(err)
		return
	}
	for _, commit := range commits {
		fmt.Printf("%d %s %s by %s via %s\n", commit.Sequence, commit.Time.Format(time.RFC3339), commit.Comment, commit.User, commit.Client)
	}
	if len(commits) > 0 && commits[0].Pending {
		fmt.Println("a confirmed commit still waits for its confirmation")
	}
}

// What to do when the answer to a commit never came.  The commit was given
// a token in its comment, and the commit history says what became of it.
func ExampleFindCommit() {
	var session, other *netconf.Session // from netconf.Open; other is a new one
	ctx := context.Background()

	token := "change-7f3a" // an id of your own
	_, err := junos.Change{
		Format:  junos.FormatSet,
		Config:  "set system ntp server 192.0.2.1",
		Comment: "add ntp server, " + token,
		Confirm: 5 * time.Minute,
	}.Apply(ctx, session)
	if !errors.Is(err, junos.ErrCommitUnknown) {
		log.Print(err)
		return
	}

	commits, err := junos.GetCommitInformation{}.Exec(ctx, other)
	if err != nil {
		log.Print(err)
		return
	}
	commit, found := junos.FindCommit(commits, token)
	switch {
	case !found:
		fmt.Println("nothing is known: the commit may still be running, ask again")
	case commit.Pending:
		// In effect, and Junos takes it back unless it is confirmed.  Any
		// session confirms it, with a commit check.
		if _, err := (junos.CommitConfiguration{Check: true}).Exec(ctx, other); err != nil {
			log.Print(err)
		}
	case commit.Sequence == 0:
		fmt.Println("carried out, and what the device runs")
	default:
		fmt.Println("carried out, and committed over since")
	}
}

// Who has the configuration open for editing, for a program that has to
// decide between waiting and telling an operator.
func ExampleGetDatabaseStatusInformation() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	editing, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, session)
	if err != nil {
		log.Print(err)
		return
	}
	for _, edit := range editing {
		log.Printf("%s on %s since %s, at %s", edit.User, edit.Terminal, edit.Start.Format(time.RFC3339), edit.EditPath)
	}
}

// Three things to load that are not configuration the program holds: a
// patch, a file of the device, and the rescue configuration.
func ExampleLoadConfiguration_patch() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	// A patch: what a compare printed, for a candidate that is where the
	// compare began.  Here that is the changes in the candidate, put back
	// and loaded again.
	diff, err := junos.CompareRollback{}.Exec(ctx, session)
	if err != nil {
		log.Print(err)
		return
	}
	if _, err := junos.Rollback(0).Exec(ctx, session); err != nil {
		log.Print(err)
		return
	}
	if _, err := (junos.LoadConfiguration{Action: junos.LoadPatch, Config: diff}).Exec(ctx, session); err != nil {
		log.Print(err)
	}

	// a file that Junos reads itself, by its path on the device
	if _, err := (junos.LoadConfiguration{Format: junos.FormatText, URL: "/var/tmp/golden.conf"}).Exec(ctx, session); err != nil {
		log.Print(err)
	}

	// the rescue configuration
	if _, err := (junos.LoadRescue{}).Exec(ctx, session); err != nil {
		log.Print(err)
	}
}
