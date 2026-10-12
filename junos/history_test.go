package junos_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
)

// The commit history of Junos 26.2R1.7 (vJunos-router) in a lab, captured
// while a confirmed commit waited for its confirmation, and again when it
// had been confirmed: the two newest entries each time, and the first commit
// of the device, which came through nothing Junos has a name for.  Junos
// notes on a confirmed commit that it is one, and goes on doing so when it
// is confirmed; what says that it still waits is another element.  The
// confirmation is a commit of its own.  The user is not the one of the
// device, and the comment of one commit is from another capture.
const (
	commitsPending = `<commit-information>
<commit-history>
<sequence-number>0</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791707625">2026-10-11 08:33:45 UTC</date-time>
<comment>commit confirmed, rollback in 1mins</comment>
<commit-confirmed>rollback pending</commit-confirmed>
</commit-history>
<commit-history>
<sequence-number>1</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791552371">2026-10-09 13:26:11 UTC</date-time>
<log>netconf-lib probe: test users removed</log>
</commit-history>
<commit-history>
<sequence-number>2</sequence-number>
<user>operator</user>
<client>other</client>
<date-time junos:seconds="1781897154">2026-06-19 19:25:54 UTC</date-time>
</commit-history>
</commit-information>
`

	commitsConfirmed = `<commit-information>
<commit-history>
<sequence-number>0</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791707627">2026-10-11 08:33:47 UTC</date-time>
</commit-history>
<commit-history>
<sequence-number>1</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791707625">2026-10-11 08:33:45 UTC</date-time>
<comment>commit confirmed, rollback in 1mins</comment>
</commit-history>
<commit-history>
<sequence-number>2</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791552371">2026-10-09 13:26:11 UTC</date-time>
</commit-history>
</commit-information>
`
)

func TestGetCommitInformation(t *testing.T) {
	ctx := context.Background()

	t.Run("the history, newest first", func(t *testing.T) {
		sess, ops := session(t, commitsPending)
		commits, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, `<get-commit-information></get-commit-information>`, ops()[0])
		be.Equal(t, 3, len(commits))

		be.DeepEqual(t, junos.Commit{
			Sequence: 0,
			User:     "operator",
			Client:   "netconf",
			Time:     time.Unix(1791707625, 0),
			Note:     "commit confirmed, rollback in 1mins",
			Pending:  true,
		}, commits[0])
		be.DeepEqual(t, junos.Commit{
			Sequence: 1,
			User:     "operator",
			Client:   "netconf",
			Time:     time.Unix(1791552371, 0),
			Comment:  "netconf-lib probe: test users removed",
		}, commits[1])
		be.DeepEqual(t, junos.Commit{Sequence: 2, User: "operator", Client: "other", Time: time.Unix(1781897154, 0)}, commits[2])

		// the time is that of the attribute, whatever zone the text is in
		be.Equal(t, "2026-10-11T08:33:45Z", commits[0].Time.UTC().Format(time.RFC3339))
	})

	// The confirmation is the newest commit then, and says nothing.  The
	// commit it confirmed is still noted as a confirmed one, and waits no
	// longer.
	t.Run("once the commit is confirmed", func(t *testing.T) {
		sess, _ := session(t, commitsConfirmed)
		commits, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, 3, len(commits))

		be.DeepEqual(t, junos.Commit{Sequence: 0, User: "operator", Client: "netconf", Time: time.Unix(1791707627, 0)}, commits[0])
		be.Equal(t, "commit confirmed, rollback in 1mins", commits[1].Note)
		for _, commit := range commits {
			be.False(t, commit.Pending)
		}
	})

	// the comment of a commit with letters outside ASCII, which Junos sends
	// a byte at a time
	t.Run("a comment that is not ASCII", func(t *testing.T) {
		sess, _ := session(t, textCommits)
		commits, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, 2, len(commits))
		be.Equal(t, "netconf-lib probe: Tromsø – Ålesund", commits[0].Comment)
		be.False(t, commits[0].Pending)
		be.Equal(t, "", commits[1].Comment)
	})

	t.Run("a history without entries", func(t *testing.T) {
		sess, _ := session(t, "<commit-information>\n</commit-information>\n")
		commits, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, 0, len(commits))
	})

	// a device with two routing engines answers once for each: the history
	// is that of the first.  Made up, no such device was at hand.
	t.Run("one answer per routing engine", func(t *testing.T) {
		sess, _ := session(t, `<multi-routing-engine-results>
<multi-routing-engine-item><re-name>re0</re-name>`+commitsPending+`</multi-routing-engine-item>
<multi-routing-engine-item><re-name>re1</re-name>`+textCommits+`</multi-routing-engine-item>
</multi-routing-engine-results>`)
		commits, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, 3, len(commits))
		be.True(t, commits[0].Pending)
	})

	t.Run("answered with something else", func(t *testing.T) {
		sess, _ := session(t, `<ok/>`)
		_, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		be.In(t, "get-commit-information: ", err.Error())
	})

	t.Run("refused", func(t *testing.T) {
		sess, _ := session(t, lockDenied)
		_, err := junos.GetCommitInformation{}.Exec(ctx, sess)
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
		be.In(t, "get-commit-information: ", err.Error())
	})
}

// The history of a router whose confirmed commit ran out, captured from the
// same lab: Junos went back by itself, which is the entry on top, by root
// and through nothing it has a name for.  The entry of the commit it took
// back waits no longer, and looks like one that was confirmed.
const commitsTakenBack = `<commit-information>
<commit-history>
<sequence-number>0</sequence-number>
<user>root</user>
<client>other</client>
<date-time junos:seconds="1791705854">2026-10-11 08:04:14 UTC</date-time>
</commit-history>
<commit-history>
<sequence-number>1</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791705551">2026-10-11 07:59:11 UTC</date-time>
<comment>commit confirmed, rollback in 5mins</comment>
<log>netconf-lib probe: netconf service off, runs out by itself</log>
</commit-history>
<commit-history>
<sequence-number>2</sequence-number>
<user>operator</user>
<client>netconf</client>
<date-time junos:seconds="1791461033">2026-10-08 12:03:53 UTC</date-time>
<log>configured by junos_config</log>
</commit-history>
</commit-information>
`

// A commit is looked for by a token in its comment.  Whether it is found
// and whether it still waits are the two things the history says.
func TestFindCommit(t *testing.T) {
	history := func(t *testing.T, fixture string) []junos.Commit {
		t.Helper()
		sess, _ := session(t, fixture)
		commits, err := junos.GetCommitInformation{}.Exec(context.Background(), sess)
		be.NilErr(t, err)
		return commits
	}

	t.Run("found, and in effect", func(t *testing.T) {
		commit, found := junos.FindCommit(history(t, commitsPending), "probe: test users removed")
		be.True(t, found)
		be.Equal(t, 1, commit.Sequence)
		be.False(t, commit.Pending)
		be.Equal(t, "", commit.Note)
	})

	// the token is part of a longer comment, as an id next to what the
	// change is about
	t.Run("a token inside a comment", func(t *testing.T) {
		commit, found := junos.FindCommit(history(t, commitsTakenBack), "service off")
		be.True(t, found)
		be.Equal(t, "netconf-lib probe: netconf service off, runs out by itself", commit.Comment)
	})

	// A token is found where it stands by itself.  The id of one change is
	// not found in the id of another that begins or ends the same.
	t.Run("a token that is part of another", func(t *testing.T) {
		commits := []junos.Commit{
			{Sequence: 0, Comment: "add ntp server, change-12"},
			{Sequence: 1, Comment: "xchange-1: add a prefix list"},
			{Sequence: 2, Comment: "change_1b"},
			{Sequence: 3, Comment: "(change-1) add ntp server."},
		}
		commit, found := junos.FindCommit(commits, "change-1")
		be.True(t, found)
		be.Equal(t, 3, commit.Sequence)

		commit, found = junos.FindCommit(commits, "change-12")
		be.True(t, found)
		be.Equal(t, 0, commit.Sequence)

		_, found = junos.FindCommit(commits[:3], "change-1")
		be.False(t, found)

		// next to anything that is no letter, digit, "-" or "_", and at
		// either end of the comment
		for _, comment := range []string{"change-1", "change-1: x", "x, change-1", "x [change-1]", "ticket/change-1.", "æ change-1 å"} {
			_, found := junos.FindCommit([]junos.Commit{{Comment: comment}}, "change-1")
			be.True(t, found)
		}
		for _, comment := range []string{"change-1å", "åchange-1", "change-1_x", "change-1-x", "9change-1"} {
			_, found := junos.FindCommit([]junos.Commit{{Comment: comment}}, "change-1")
			be.False(t, found)
		}
	})

	t.Run("found, and waiting for its confirmation", func(t *testing.T) {
		pending := history(t, commitsPending)
		pending[0].Comment = "change 7f3a: add ntp server"

		commit, found := junos.FindCommit(pending, "7f3a")
		be.True(t, found)
		be.True(t, commit.Pending)
		be.Equal(t, 0, commit.Sequence)
	})

	// The entry of a confirmed commit that Junos took back is like one that
	// was confirmed: found, and not pending.  Note says that there is more
	// to ask.
	t.Run("found, a confirmed commit that waits no longer", func(t *testing.T) {
		commit, found := junos.FindCommit(history(t, commitsTakenBack), "runs out by itself")
		be.True(t, found)
		be.False(t, commit.Pending)
		be.Equal(t, 1, commit.Sequence)
		be.Equal(t, "commit confirmed, rollback in 5mins", commit.Note)
	})

	t.Run("not found", func(t *testing.T) {
		for _, token := range []string{"no-such-token", "", "commit confirmed"} {
			commit, found := junos.FindCommit(history(t, commitsTakenBack), token)
			be.False(t, found)
			be.DeepEqual(t, junos.Commit{}, commit)
		}
		_, found := junos.FindCommit(nil, "token")
		be.False(t, found)
	})

	// two commits have the token in their comment
	t.Run("the newest of several", func(t *testing.T) {
		commits := []junos.Commit{
			{Sequence: 0, Comment: "something else"},
			{Sequence: 1, Comment: "change-7, second try"},
			{Sequence: 2, Comment: "change-7"},
		}
		commit, found := junos.FindCommit(commits, "change-7")
		be.True(t, found)
		be.Equal(t, 1, commit.Sequence)
	})
}
