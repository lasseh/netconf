//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/rpc"
)

// The standard write operations: <edit-config>, <validate>,
// <discard-changes>, <commit>, <cancel-commit> and <copy-config>.  Junos
// changes go through its own operations, so these are here for what RFC 6241
// promises of any device.
//
// What they write is a piece of configuration that changes nothing about
// what the device does, by flavor, see harmless.  The gates are those of the
// suite: nothing is loaded into a device without NETCONF_DUT_CHANGE_DRYRUN
// and nothing is committed without NETCONF_DUT_CHANGE_COMMIT.  The flavor
// opens neither: the netopeer2 of the compose file is there to be written
// to, and the compose file sets the variable for it.

// harmlessConfig is a piece of configuration that a test may write.
type harmlessConfig struct {
	// config returns the configuration, with value somewhere in it
	config func(value string) string
	// remove is the same place with the operation that takes it out
	remove string
	// filter is a subtree filter for the place
	filter string
}

const netconfBase = `xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0"`

// harmless returns what a test may write to a device of this flavor, and
// skips the test for a flavor that has nothing.
//
// Junos gets a configuration group that nothing applies, the one the Junos
// tests use.  netopeer2 gets a group of the access control model with a user
// that does not exist.
func harmless(t *testing.T) harmlessConfig {
	t.Helper()
	switch os.Getenv("NETCONF_DUT_FLAVOR") {
	case "junos":
		return harmlessConfig{
			config: func(value string) string {
				return `<configuration><groups><name>` + probeGroup + `</name><system><location><building>` + value + `</building></location></system></groups></configuration>`
			},
			remove: `<configuration><groups ` + netconfBase + ` nc:operation="remove"><name>` + probeGroup + `</name></groups></configuration>`,
			filter: `<configuration><groups><name>` + probeGroup + `</name></groups></configuration>`,
		}
	case "netopeer2":
		const nacm = `<nacm xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-acm"><groups>`
		return harmlessConfig{
			config: func(value string) string {
				return nacm + `<group><name>` + probeGroup + `</name><user-name>` + value + `</user-name></group></groups></nacm>`
			},
			remove: nacm + `<group ` + netconfBase + ` nc:operation="remove"><name>` + probeGroup + `</name></group></groups></nacm>`,
			filter: nacm + `<group><name>` + probeGroup + `</name></group></groups></nacm>`,
		}
	}
	t.Skip("no harmless configuration to write for this flavor, skipping test")
	return harmlessConfig{}
}

// probeAbsent fails a test that is about to commit when the device already
// runs configuration in the place the tests write to.  The test did not make
// it, and would change it and then take it out.  A run that was cut short
// leaves the same behind, which is for a person to take out: the test cannot
// tell the two apart.
func probeAbsent(ctx context.Context, t *testing.T, session *netconf.Session) {
	t.Helper()

	var there bool
	if os.Getenv("NETCONF_DUT_FLAVOR") == "junos" {
		there = strings.TrimSpace(activeProbe(ctx, t, session)) != ""
	} else {
		data, err := rpc.GetConfig{Source: rpc.Running, Filter: rpc.SubtreeFilter(harmless(t).filter)}.Exec(ctx, session)
		be.NilErr(t, err)
		there = bytes.Contains(data, []byte(probeGroup))
	}
	if there {
		t.Fatalf("the device runs configuration named %q that this test did not make, and the test would change it and take it out; "+
			"if a run that was cut short left it, take it out by hand (on Junos: delete groups %s)", probeGroup, probeGroup)
	}
}

// mayLoad skips the test unless the device may be loaded into, and mayCommit
// reports whether it may be committed to.
func mayLoad(t *testing.T) {
	t.Helper()
	if os.Getenv("NETCONF_DUT_CHANGE_DRYRUN") == "" && os.Getenv("NETCONF_DUT_CHANGE_COMMIT") == "" {
		t.Skip("NETCONF_DUT_CHANGE_DRYRUN not set, skipping test")
	}
}

func mayCommit() bool {
	return os.Getenv("NETCONF_DUT_CHANGE_COMMIT") != ""
}

// has reports whether the configuration that a subtree filter for the
// harmless place returns from source has value in it.
func has(ctx context.Context, t *testing.T, session *netconf.Session, source rpc.Datastore, cfg harmlessConfig, value string) bool {
	t.Helper()
	data, err := rpc.GetConfig{Source: source, Filter: rpc.SubtreeFilter(cfg.filter)}.Exec(ctx, session)
	be.NilErr(t, err)
	return bytes.Contains(data, []byte(value))
}

// tidy reports the error of a step that puts a device back, and lets the
// steps after it run: the next one may still be what leaves the device as
// it was.  A cleanup that stops at its first failure keeps the lock, or
// what it wrote.
func tidy(t *testing.T, doing string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v", doing, err)
	}
}

// left reports whether value is still in the running configuration, for a
// cleanup: a read that fails counts as yes.
func left(ctx context.Context, session *netconf.Session, cfg harmlessConfig, value string) bool {
	data, err := rpc.GetConfig{Source: rpc.Running, Filter: rpc.SubtreeFilter(cfg.filter)}.Exec(ctx, session)
	return err != nil || bytes.Contains(data, []byte(value))
}

// A read with a subtree filter returns that subtree and not the rest.
func TestGetConfigSubtreeFilter(t *testing.T) {
	session := setupSSH(t)
	ctx := testCtx(t)

	var filter, root string
	switch os.Getenv("NETCONF_DUT_FLAVOR") {
	case "junos":
		filter, root = `<configuration><system><services/></system></configuration>`, "<services"
	case "netopeer2":
		filter, root = `<netconf-server xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-server"><listen/></netconf-server>`, "<listen"
	default:
		t.Skip("no subtree to ask for with this flavor, skipping test")
	}

	whole, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
	be.NilErr(t, err)
	part, err := rpc.GetConfig{Source: rpc.Running, Filter: rpc.SubtreeFilter(filter)}.Exec(ctx, session)
	be.NilErr(t, err)

	be.In(t, root, string(part))
	be.True(t, len(part) < len(whole))
	t.Logf("the subtree is %d of %d bytes", len(part), len(whole))

	// a subtree that is not there is an empty answer and no error
	none, err := rpc.GetConfig{Source: rpc.Running, Filter: rpc.SubtreeFilter(`<no-such-subtree xmlns="urn:example:none"/>`)}.Exec(ctx, session)
	if err != nil {
		// a device may refuse a namespace it does not know
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		t.Logf("a subtree the device does not know is refused: %v", err)
		return
	}
	be.Equal(t, "", string(bytes.TrimSpace(none)))
}

// The candidate: under its lock a configuration is edited in, checked where
// the device can, and dropped again.  With commits allowed it is also
// committed and taken out with a second commit, and where the device can
// cancel a confirmed commit, one is made and cancelled.
func TestEditConfigCandidate(t *testing.T) {
	session := setupSSH(t)
	if !session.ServerCaps().Has(":candidate:1.0") {
		t.Skip("the device does not advertise :candidate, skipping test")
	}
	cfg := harmless(t)
	mayLoad(t)
	if os.Getenv("NETCONF_DUT_FLAVOR") == "junos" {
		// the <validate> below is a commit check to Junos
		skipIfCommitPending(t, session)
	}
	ctx := changeCtx(t)
	value := "std-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if mayCommit() {
		probeAbsent(ctx, t, session)
	}

	be.NilErr(t, rpc.Lock{Target: rpc.Candidate}.Exec(ctx, session))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// whatever became of the test, nothing of it stays in the candidate,
		// and nothing in the running configuration
		tidy(t, "discard-changes", rpc.DiscardChanges{}.Exec(ctx, session))
		if mayCommit() && left(ctx, session, cfg, value) {
			tidy(t, "taking the configuration out", rpc.EditConfig{Target: rpc.Candidate, Config: cfg.remove}.Exec(ctx, session))
			tidy(t, "committing that", rpc.Commit{}.Exec(ctx, session))
		}
		tidy(t, "unlock", rpc.Unlock{Target: rpc.Candidate}.Exec(ctx, session))
	}()
	edit := func(t *testing.T, config string) {
		t.Helper()
		be.NilErr(t, rpc.EditConfig{Target: rpc.Candidate, Config: config}.Exec(ctx, session))
	}

	t.Run("edit, validate, discard", func(t *testing.T) {
		edit(t, cfg.config(value))
		be.True(t, has(ctx, t, session, rpc.Candidate, cfg, value))
		be.False(t, has(ctx, t, session, rpc.Running, cfg, value))

		if session.ServerCaps().Has(":validate:1.0") || session.ServerCaps().Has(":validate:1.1") {
			be.NilErr(t, rpc.Validate{Source: rpc.Candidate}.Exec(ctx, session))
		} else {
			t.Log("the device does not advertise :validate")
		}

		be.NilErr(t, rpc.DiscardChanges{}.Exec(ctx, session))
		be.False(t, has(ctx, t, session, rpc.Candidate, cfg, value))
	})

	t.Run("commit", func(t *testing.T) {
		if !mayCommit() {
			t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
		}
		edit(t, cfg.config(value))
		be.NilErr(t, rpc.Commit{}.Exec(ctx, session))
		be.True(t, has(ctx, t, session, rpc.Running, cfg, value))

		edit(t, cfg.remove)
		be.NilErr(t, rpc.Commit{}.Exec(ctx, session))
		be.False(t, has(ctx, t, session, rpc.Running, cfg, value))
	})

	t.Run("confirmed commit, cancelled", func(t *testing.T) {
		if !session.ServerCaps().Has(":confirmed-commit:1.1") {
			t.Skip("the device does not advertise :confirmed-commit:1.1, skipping test")
		}
		if !mayCommit() {
			t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
		}
		edit(t, cfg.config(value))
		be.NilErr(t, rpc.Commit{Confirmed: true, ConfirmTimeout: 2 * time.Minute}.Exec(ctx, session))
		be.True(t, has(ctx, t, session, rpc.Running, cfg, value))

		be.NilErr(t, rpc.CancelCommit{}.Exec(ctx, session))
		be.False(t, has(ctx, t, session, rpc.Running, cfg, value))
	})
}

// A confirmed commit with a token does not belong to its session.  Another
// session gives it more time and confirms it, and another session cancels
// the next one.
func TestCommitPersist(t *testing.T) {
	first := setupSSH(t)
	if !first.ServerCaps().Has(":confirmed-commit:1.1") {
		t.Skip("the device does not advertise :confirmed-commit:1.1, skipping test")
	}
	cfg := harmless(t)
	if !mayCommit() {
		t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
	}
	second := setupSSH(t)
	ctx := changeCtx(t)
	value := "std-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	kept, cancelled := value+"-kept", value+"-cancelled"
	probeAbsent(ctx, t, first)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// whatever became of the test, nothing of it stays: no confirmed
		// commit that waits, and nothing in either configuration
		for _, token := range []string{kept, cancelled} {
			_ = rpc.CancelCommit{PersistID: token}.Exec(ctx, first)
		}
		tidy(t, "discard-changes", rpc.DiscardChanges{}.Exec(ctx, first))
		if left(ctx, first, cfg, value) {
			tidy(t, "taking the configuration out", rpc.EditConfig{Target: rpc.Candidate, Config: cfg.remove}.Exec(ctx, first))
			tidy(t, "committing that", rpc.Commit{}.Exec(ctx, first))
		}
	}()

	t.Run("given more time and confirmed by a second session", func(t *testing.T) {
		// so little time that the commit is gone, unless it is given more
		const little = 10 * time.Second

		be.NilErr(t, rpc.EditConfig{Target: rpc.Candidate, Config: cfg.config(kept)}.Exec(ctx, first))
		be.NilErr(t, rpc.Commit{Confirmed: true, ConfirmTimeout: little, Persist: kept}.Exec(ctx, first))
		made := time.Now()
		be.True(t, has(ctx, t, second, rpc.Running, cfg, kept))

		// A further confirmed commit gives it more time.  RFC 6241 has it
		// name the first one with <persist-id>.  A device that refuses that
		// is asked with the <persist> of the first one once more.
		more := rpc.Commit{Confirmed: true, ConfirmTimeout: 5 * time.Minute, PersistID: kept}
		if err := more.Exec(ctx, second); err != nil {
			t.Logf("a further confirmed commit with <persist-id>: %v", err)
			more.Persist, more.PersistID = kept, ""
			be.NilErr(t, more.Exec(ctx, second))
			t.Log("a further confirmed commit is taken with the <persist> of the first")
		}

		// it is still there when the time of the first one is over
		time.Sleep(time.Until(made.Add(little + 5*time.Second)))
		be.True(t, has(ctx, t, second, rpc.Running, cfg, kept))

		// the token is what lets another session in
		err := rpc.Commit{}.Exec(ctx, second)
		be.Nonzero(t, err)
		t.Logf("a confirmation from another session without the token: %v", err)
		err = rpc.Commit{PersistID: kept + "-wrong"}.Exec(ctx, second)
		be.Nonzero(t, err)
		t.Logf("a confirmation with a wrong token: %v", err)

		be.NilErr(t, rpc.Commit{PersistID: kept}.Exec(ctx, second))
		be.True(t, has(ctx, t, second, rpc.Running, cfg, kept))

		// confirmed, so there is nothing left to cancel
		err = rpc.CancelCommit{PersistID: kept}.Exec(ctx, second)
		be.Nonzero(t, err)
		t.Logf("a cancel after the confirmation: %v", err)
		be.True(t, has(ctx, t, second, rpc.Running, cfg, kept))

		be.NilErr(t, rpc.EditConfig{Target: rpc.Candidate, Config: cfg.remove}.Exec(ctx, first))
		be.NilErr(t, rpc.Commit{}.Exec(ctx, first))
		be.False(t, has(ctx, t, second, rpc.Running, cfg, kept))
	})

	t.Run("cancelled by a second session", func(t *testing.T) {
		be.NilErr(t, rpc.EditConfig{Target: rpc.Candidate, Config: cfg.config(cancelled)}.Exec(ctx, first))
		be.NilErr(t, rpc.Commit{Confirmed: true, ConfirmTimeout: 2 * time.Minute, Persist: cancelled}.Exec(ctx, first))
		be.True(t, has(ctx, t, second, rpc.Running, cfg, cancelled))

		// without the token only the session of the commit may cancel it
		err := rpc.CancelCommit{}.Exec(ctx, second)
		be.Nonzero(t, err)
		t.Logf("a cancel from another session without the token: %v", err)
		be.True(t, has(ctx, t, second, rpc.Running, cfg, cancelled))

		be.NilErr(t, rpc.CancelCommit{PersistID: cancelled}.Exec(ctx, second))
		be.False(t, has(ctx, t, second, rpc.Running, cfg, cancelled))
	})
}

// The running configuration, written to directly, on a device that takes
// that.  There is no going back from it but the second edit, so it is a
// commit as far as the gates go.
func TestEditConfigRunning(t *testing.T) {
	session := setupSSH(t)
	if !session.ServerCaps().Has(":writable-running:1.0") {
		t.Skip("the device does not advertise :writable-running, skipping test")
	}
	cfg := harmless(t)
	if !mayCommit() {
		t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
	}
	ctx := changeCtx(t)
	value := "std-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	probeAbsent(ctx, t, session)

	be.NilErr(t, rpc.EditConfig{Target: rpc.Running, Config: cfg.config(value)}.Exec(ctx, session))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		tidy(t, "taking the configuration out", rpc.EditConfig{Target: rpc.Running, Config: cfg.remove}.Exec(ctx, session))
		if left(ctx, session, cfg, value) {
			t.Error("the configuration of the test is still in running")
		}
	}()
	be.True(t, has(ctx, t, session, rpc.Running, cfg, value))

	// only what is valid is taken: test-only loads nothing
	other := value + "-not-loaded"
	if session.ServerCaps().Has(":validate:1.1") {
		be.NilErr(t, rpc.EditConfig{Target: rpc.Running, TestOption: rpc.TestOnly, Config: cfg.config(other)}.Exec(ctx, session))
		be.False(t, has(ctx, t, session, rpc.Running, cfg, other))
	}
}

// The startup configuration of a device that has one apart from the running
// one: the running configuration is copied over it, which is what makes a
// change last a restart there.  The test writes to running to have
// something to copy, takes it out and copies again, so that startup ends
// as running was before.
func TestCopyConfigStartup(t *testing.T) {
	session := setupSSH(t)
	if !session.ServerCaps().Has(":startup:1.0") {
		t.Skip("the device does not advertise :startup, skipping test")
	}
	if !session.ServerCaps().Has(":writable-running:1.0") {
		t.Skip("the device does not advertise :writable-running, which the test writes with, skipping test")
	}
	cfg := harmless(t)
	if !mayCommit() {
		t.Skip("NETCONF_DUT_CHANGE_COMMIT not set, skipping test")
	}
	ctx := changeCtx(t)
	value := "std-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	toStartup := rpc.CopyConfig{Source: rpc.Running, Target: rpc.Startup}
	probeAbsent(ctx, t, session)

	be.NilErr(t, rpc.EditConfig{Target: rpc.Running, Config: cfg.config(value)}.Exec(ctx, session))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// out of running, and out of startup with a copy of that
		if left(ctx, session, cfg, value) {
			tidy(t, "taking the configuration out", rpc.EditConfig{Target: rpc.Running, Config: cfg.remove}.Exec(ctx, session))
		}
		tidy(t, "copying running to startup", toStartup.Exec(ctx, session))
	}()

	// running has what startup has not, until it is copied
	be.True(t, has(ctx, t, session, rpc.Running, cfg, value))
	be.False(t, has(ctx, t, session, rpc.Startup, cfg, value))
	be.NilErr(t, toStartup.Exec(ctx, session))
	be.True(t, has(ctx, t, session, rpc.Startup, cfg, value))

	// and the other way: what is gone from running goes with the next copy
	be.NilErr(t, rpc.EditConfig{Target: rpc.Running, Config: cfg.remove}.Exec(ctx, session))
	be.NilErr(t, toStartup.Exec(ctx, session))
	be.False(t, has(ctx, t, session, rpc.Startup, cfg, value))
}
