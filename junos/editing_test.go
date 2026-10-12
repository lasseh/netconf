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

// Who is editing the configuration, as Junos 26.2R1.7 answered in a lab.
// The user is not the one of the devices.
const (
	// vJunos-router: a user of the CLI in configuration mode, somewhere in
	// the hierarchy and idle for half a minute, and one in "configure
	// private".  The idle time is from a second capture of the same user.
	editingCLI = `<database-status-information>
<database-status>
<pid>34248</pid>
<user>operator</user>
<terminal>pts/0</terminal>
<start-time junos:seconds="1791708023">2026-10-11 08:40:23 UTC</start-time>
<idle-time junos:seconds="32">00:00:32</idle-time>
<edit-path>[edit system services]</edit-path>
</database-status>
<database-status>
<pid>34247</pid>
<user>operator</user>
<terminal>pts/1</terminal>
<start-time junos:seconds="1791708024">2026-10-11 08:40:24 UTC</start-time>
<edit-path>private [edit]</edit-path>
</database-status>
</database-status-information>
`

	// vJunos-switch: a NETCONF session that holds the lock on the candidate.
	editingNetconfLock = `<database-status-information>
<database-status>
<pid>98809</pid>
<user>operator</user>
<terminal></terminal>
<start-time junos:seconds="1791708154">2026-10-11 10:42:34 CEST</start-time>
<exclusive/>
<edit-path></edit-path>
</database-status>
</database-status-information>
`

	// vJunos-switch: the same session with a private copy.
	editingNetconfPrivate = `<database-status-information>
<database-status>
<pid>98809</pid>
<user>operator</user>
<terminal>non-tty</terminal>
<start-time junos:seconds="1791708155">2026-10-11 10:42:35 CEST</start-time>
<edit-path>private [edit]</edit-path>
</database-status>
</database-status-information>
`

	// vJunos-switch: a user of the CLI in "configure exclusive".
	editingCLIExclusive = `<database-status-information>
<database-status>
<pid>98939</pid>
<user>operator</user>
<terminal>pts/0</terminal>
<start-time junos:seconds="1791708435">2026-10-11 10:47:15 CEST</start-time>
<exclusive/>
<edit-path>[edit]</edit-path>
</database-status>
</database-status-information>
`

	// vJunosEvolved (Junos OS Evolved 26.2R1.7): as the router, with no
	// idle time.
	editingEvolved = `<database-status-information>
<database-status>
<pid>15337</pid>
<user>operator</user>
<terminal>pts/1</terminal>
<start-time junos:seconds="1791708174">2026-10-11 10:42:54 CEST</start-time>
<edit-path>[edit system services]</edit-path>
</database-status>
<database-status>
<pid>15336</pid>
<user>operator</user>
<terminal>pts/0</terminal>
<start-time junos:seconds="1791708175">2026-10-11 10:42:55 CEST</start-time>
<edit-path>private [edit]</edit-path>
</database-status>
</database-status-information>
`

	// All three, when nobody is editing.
	editingNobody = `<database-status-information>
</database-status-information>
`
)

func TestGetDatabaseStatusInformation(t *testing.T) {
	ctx := context.Background()
	read := func(t *testing.T, fixture string) []junos.EditSession {
		t.Helper()
		sess, ops := session(t, fixture)
		sessions, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, `<get-database-status-information></get-database-status-information>`, ops()[0])
		return sessions
	}

	t.Run("users of the CLI", func(t *testing.T) {
		sessions := read(t, editingCLI)
		be.Equal(t, 2, len(sessions))
		be.DeepEqual(t, junos.EditSession{
			User:     "operator",
			PID:      34248,
			Terminal: "pts/0",
			Start:    time.Unix(1791708023, 0),
			Idle:     32 * time.Second,
			EditPath: "[edit system services]",
		}, sessions[0])
		be.DeepEqual(t, junos.EditSession{
			User:     "operator",
			PID:      34247,
			Terminal: "pts/1",
			Start:    time.Unix(1791708024, 0),
			EditPath: "[edit]",
			Private:  true,
		}, sessions[1])
	})

	t.Run("a NETCONF session with the lock", func(t *testing.T) {
		sessions := read(t, editingNetconfLock)
		be.Equal(t, 1, len(sessions))
		be.DeepEqual(t, junos.EditSession{User: "operator", PID: 98809, Start: time.Unix(1791708154, 0), Exclusive: true}, sessions[0])
	})

	t.Run("a NETCONF session with a private copy", func(t *testing.T) {
		sessions := read(t, editingNetconfPrivate)
		be.Equal(t, 1, len(sessions))
		be.DeepEqual(t, junos.EditSession{
			User: "operator", PID: 98809, Terminal: "non-tty", Start: time.Unix(1791708155, 0), EditPath: "[edit]", Private: true,
		}, sessions[0])
	})

	t.Run("a user of the CLI in exclusive mode", func(t *testing.T) {
		sessions := read(t, editingCLIExclusive)
		be.Equal(t, 1, len(sessions))
		be.True(t, sessions[0].Exclusive)
		be.False(t, sessions[0].Private)
		be.Equal(t, "[edit]", sessions[0].EditPath)
		be.Equal(t, "pts/0", sessions[0].Terminal)
	})

	t.Run("on evolved", func(t *testing.T) {
		sessions := read(t, editingEvolved)
		be.Equal(t, 2, len(sessions))
		be.Equal(t, "[edit system services]", sessions[0].EditPath)
		be.Equal(t, time.Duration(0), sessions[0].Idle)
		be.True(t, sessions[1].Private)
	})

	t.Run("nobody", func(t *testing.T) {
		sessions := read(t, editingNobody)
		be.Equal(t, 0, len(sessions))
		be.True(t, sessions != nil)
	})

	t.Run("answered with something else", func(t *testing.T) {
		sess, _ := session(t, `<ok/>`)
		_, err := junos.GetDatabaseStatusInformation{}.Exec(ctx, sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		be.In(t, "get-database-status-information: ", err.Error())
	})
}
