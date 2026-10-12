package junos

import (
	"context"
	"encoding/xml"
	"strings"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
)

// EditSession is one session that has the configuration open for editing:
// a user of the CLI in configuration mode, or a NETCONF session that holds
// the lock on the candidate or a private copy of it.
type EditSession struct {
	// User is who it is.
	User string

	// PID is the process of the session.  For a NETCONF session that is its
	// session-id, the one a refused lock names as the holder.
	PID int

	// Terminal is where the user sits: "pts/0" for a user of the CLI.  A
	// NETCONF session has none: Junos writes "non-tty" for one with a private
	// copy and nothing for one that holds the lock.
	Terminal string

	// Start is when the session began to edit.
	Start time.Time

	// Idle is how long the user has done nothing, zero when Junos does not
	// say: it leaves it out for a session that was just active, and Junos OS
	// Evolved 26.2 was not seen to give it at all.
	Idle time.Duration

	// EditPath is where in the configuration the user is, as the CLI shows
	// it: "[edit system services]".  It is empty for a NETCONF session that
	// holds the lock.
	EditPath string

	// Private says that the session works in a private copy of the
	// candidate, "configure private" or OpenConfiguration with Private.
	Private bool

	// Exclusive says that the session has the candidate to itself:
	// "configure exclusive", or the lock of a NETCONF session.
	Exclusive bool
}

// GetDatabaseStatusInformation is the Junos <get-database-status-information>
// operation: who has the configuration open for editing, what the CLI shows
// with "status" in configuration mode.
//
// It lists users of the CLI in configuration mode, and NETCONF sessions that
// hold the lock or a private copy.  It does not list what ErrCandidateModified
// is about: a NETCONF session that has loaded into the candidate and not
// committed takes neither, and is not in the list.  Neither is whoever left
// changes behind and went.  So an empty list does not say that the candidate
// is clean; CompareRollback does.
type GetDatabaseStatusInformation struct{}

// MarshalXML implements xml.Marshaler.
func (GetDatabaseStatusInformation) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	return e.Encode(struct {
		XMLName xml.Name `xml:"get-database-status-information"`
	}{})
}

// Exec returns the sessions, in the order Junos lists them, and none when
// nobody is editing.
func (op GetDatabaseStatusInformation) Exec(ctx context.Context, session *netconf.Session) ([]EditSession, error) {
	sessions, err := op.exec(ctx, session)
	return sessions, operr.Named("get-database-status-information", err)
}

func (op GetDatabaseStatusInformation) exec(ctx context.Context, session *netconf.Session) ([]EditSession, error) {
	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return nil, err
	}

	// a list without entries is an answer, a reply without a list is none
	if _, err := reply.Element("database-status-information"); err != nil {
		return nil, err
	}
	found, err := reply.Elements("database-status")
	if err != nil {
		return nil, err
	}

	sessions := make([]EditSession, 0, len(found))
	for _, el := range found {
		var entry struct {
			PID      int    `xml:"pid"`
			User     string `xml:"user"`
			Terminal string `xml:"terminal"`
			Start    struct {
				Seconds int64 `xml:"seconds,attr"`
			} `xml:"start-time"`
			Idle struct {
				Seconds int64 `xml:"seconds,attr"`
			} `xml:"idle-time"`
			Exclusive *struct{} `xml:"exclusive"`
			EditPath  string    `xml:"edit-path"`
		}
		if err := el.Decode(&entry); err != nil {
			return nil, err
		}

		// a private copy is told in front of the place: "private [edit]"
		path, private := strings.CutPrefix(strings.TrimSpace(entry.EditPath), "private ")
		edit := EditSession{
			User:      strings.TrimSpace(entry.User),
			PID:       entry.PID,
			Terminal:  strings.TrimSpace(entry.Terminal),
			Idle:      time.Duration(entry.Idle.Seconds) * time.Second,
			EditPath:  path,
			Private:   private,
			Exclusive: entry.Exclusive != nil,
		}
		if entry.Start.Seconds != 0 {
			edit.Start = time.Unix(entry.Start.Seconds, 0)
		}
		sessions = append(sessions, edit)
	}
	return sessions, nil
}
