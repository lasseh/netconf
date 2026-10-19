package junos

import (
	"context"
	"encoding/xml"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
)

// Commit is one entry of the commit history of a device, what "show system
// commit" lists.
type Commit struct {
	// Sequence is the place of the commit in the history: 0 is the newest,
	// and the number is the one Rollback and CompareRollback take.
	Sequence int

	// User is who committed.
	User string

	// Client is what the commit came through, as Junos names it: "cli",
	// "netconf", and "other" for what it has no name for.
	Client string

	// Time is when the commit was made.  It is read from the seconds Junos
	// gives next to the time it prints, which is in the time zone of the
	// device.  It has a resolution of one second, and is the zero time when
	// Junos gave no seconds.
	Time time.Time

	// Comment is what the commit was told to say about itself, empty for a
	// commit that said nothing.
	Comment string

	// Note is what Junos says about the commit by itself.  On a confirmed
	// commit it reads like "commit confirmed, rollback in 5mins", with the
	// time it was given, and it stays when the commit has been confirmed:
	// Pending is what tells whether it still waits.
	Note string

	// Pending says that this is a confirmed commit that still waits for its
	// confirmation: the configuration is in effect, and Junos goes back to
	// the one before unless a commit or a commit check reaches it in time.
	Pending bool
}

// GetCommitInformation is the Junos <get-commit-information> operation: the
// commit history of the device, as the CLI's "show system commit" lists it.
//
// It answers what a program wants to know after a commit whose outcome it
// never saw, ErrCommitUnknown, and whether a confirmed commit, made by
// whoever, still waits for its confirmation.  The newest entry also tells
// cheaply whether anything was committed since a program last looked.  Two
// things escape that: a commit to the ephemeral database leaves no entry,
// and two commits within one second have the same time.
//
// Junos keeps the 50 newest commits.
type GetCommitInformation struct{}

// MarshalXML implements xml.Marshaler.
func (GetCommitInformation) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	return e.Encode(struct {
		XMLName xml.Name `xml:"get-commit-information"`
	}{})
}

// Exec returns the commits, newest first.  From a device that answers once
// for each routing engine the history is that of the first answer.
//
// How long a pending commit has left is not told.  Note has the time it was
// given, and counted from Time that comes out short: Junos starts counting
// when the commit has finished.
func (op GetCommitInformation) Exec(ctx context.Context, session *netconf.Session) ([]Commit, error) {
	commits, err := op.exec(ctx, session)
	return commits, operr.Named("get-commit-information", err)
}

func (op GetCommitInformation) exec(ctx context.Context, session *netconf.Session) ([]Commit, error) {
	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return nil, err
	}

	// a history without entries is an answer, a reply without a history is
	// none
	history, err := reply.Element("commit-information")
	if err != nil {
		return nil, err
	}
	found, err := (&netconf.Reply{Raw: history.Raw}).Elements("commit-history")
	if err != nil {
		return nil, err
	}

	commits := make([]Commit, 0, len(found))
	for _, el := range found {
		var entry struct {
			Sequence int    `xml:"sequence-number"`
			User     string `xml:"user"`
			Client   string `xml:"client"`
			Time     struct {
				Seconds int64 `xml:"seconds,attr"`
			} `xml:"date-time"`
			Log     string `xml:"log"`
			Comment string `xml:"comment"`
			// "rollback pending" on a confirmed commit that nobody has
			// confirmed
			Confirmed string `xml:"commit-confirmed"`
		}
		if err := el.Decode(&entry); err != nil {
			return nil, err
		}

		commit := Commit{
			Sequence: entry.Sequence,
			User:     strings.TrimSpace(entry.User),
			Client:   strings.TrimSpace(entry.Client),
			Comment:  strings.TrimSpace(entry.Log),
			Note:     strings.TrimSpace(entry.Comment),
			Pending:  entry.Confirmed != "",
		}
		if entry.Time.Seconds != 0 {
			commit.Time = time.Unix(entry.Time.Seconds, 0)
		}
		commits = append(commits, commit)
	}
	return commits, nil
}

// FindCommit looks for the commit whose comment has token in it, and
// returns the newest such entry of commits and whether there is one.  It is
// for a commit whose outcome never arrived, ErrCommitUnknown: put a token
// that no other commit has into the comment before the commit is sent, an
// id made for the change, and ask the history for it afterwards, on a new
// session if the old one is gone.
//
//	commits, err := junos.GetCommitInformation{}.Exec(ctx, session)
//	if err != nil { ... }
//	commit, found := junos.FindCommit(commits, token)
//
// What the three answers say:
//
//   - Found and Pending: the commit was carried out and is in effect.  It is
//     a confirmed commit that nobody has confirmed, and Junos takes it back
//     unless a commit or a commit check reaches it in time, from any
//     session.
//   - Found and not Pending: the commit was carried out.  With Sequence 0
//     it is the newest commit, and what the device runs.  Further down,
//     something was committed over it.  For a confirmed commit, which Note
//     tells, that is either the commit that confirmed it or Junos taking it
//     back when its time ran out, and its own entry is the same in both
//     cases: the entry on top of it tells, as Junos 26.2 takes a commit
//     back as the user root, through the client "other".  A commit check
//     confirms as well and leaves no entry.
//   - Not found: nothing is known.  The commit may still be running, Junos
//     keeps 50 entries, and a commit to the ephemeral database leaves none.
//
// There is no answer "not committed".  An empty token finds nothing.
//
// The token is found where it stands by itself in a comment: with no
// letter, digit, "-" or "_" in front of it or behind it.  So "change-1" is
// not found in the comment of "change-12", and the id of one change may be
// the beginning of another's.
func FindCommit(commits []Commit, token string) (Commit, bool) {
	if token == "" {
		return Commit{}, false
	}
	for _, commit := range commits {
		if hasToken(commit.Comment, token) {
			return commit, true
		}
	}
	return Commit{}, false
}

// hasToken reports whether token stands by itself somewhere in comment.
func hasToken(comment, token string) bool {
	for from := 0; from <= len(comment)-len(token); {
		at := strings.Index(comment[from:], token)
		if at < 0 {
			return false
		}
		at += from
		before, _ := utf8.DecodeLastRuneInString(comment[:at])
		behind, _ := utf8.DecodeRuneInString(comment[at+len(token):])
		// at either end of the comment there is no rune, which is no part
		// of a token
		if !partOfToken(before) && !partOfToken(behind) {
			return true
		}
		from = at + 1
	}
	return false
}

// partOfToken reports whether r is what a token is made of.
func partOfToken(r rune) bool {
	return r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
