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

// What Junos 26.2R1.7 on a vJunos-router answered in the default instance of
// the ephemeral database to the three things that database does not do.
// Junos OS Evolved 26.2R1.7 answered the same, word for word.
const (
	ephemeralCompare = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Unsupported operation config compare in ephemeral database
</error-message>
</rpc-error>
`

	ephemeralConfirmed = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
"commit confirmed" is not supported with ephemeral
</error-message>
</rpc-error>
`

	// A configuration group.  Junos says <ok/> next to the error.
	ephemeralGroup = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>statement creation failed</error-message>
<error-info>
<bad-element>netconf-lib-probe</bad-element>
</error-info>
</rpc-error>
<ok/>
</load-configuration-results>
`
)

// The ephemeral database takes no diff, no confirmed commit and no
// configuration group, and each of them is an error that says so.
func TestEphemeralRefusals(t *testing.T) {
	tests := []struct {
		name, reply, want string
		try               func(context.Context, *netconf.Session) error
	}{
		{"a diff", ephemeralCompare, "Unsupported operation config compare in ephemeral database", func(ctx context.Context, sess *netconf.Session) error {
			_, err := junos.CompareRollback{}.Exec(ctx, sess)
			return err
		}},
		{"a confirmed commit", ephemeralConfirmed, `"commit confirmed" is not supported with ephemeral`, func(ctx context.Context, sess *netconf.Session) error {
			_, err := junos.CommitConfiguration{Confirm: time.Minute}.Exec(ctx, sess)
			return err
		}},
		{"a group", ephemeralGroup, "'netconf-lib-probe': statement creation failed", func(ctx context.Context, sess *netconf.Session) error {
			_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: "set groups netconf-lib-probe system location building x"}.Exec(ctx, sess)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess, _ := session(t, tc.reply)

			err := tc.try(context.Background(), sess)
			be.True(t, errors.Is(err, netconf.ErrOperationFailed))
			be.In(t, tc.want, err.Error())
		})
	}
}
