package junos_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/junos"
	"github.com/lasseh/netconf/rpc"
)

// refusedBy are the devices whose refusals are in testdata/refused, as
// Junos 26.2R1.7 (vJunos-router, vJunos-switch) and Junos OS Evolved
// 26.2R1.7 (vJunosEvolved) sent them in a lab: to a user whose login class
// may read and not configure, and to an administrator who asked for what
// the platform does not have.  The last is the Evolved device with
// rfc-compliant set.
var refusedBy = []string{"router", "switch", "evolved", "evolved-rfc-compliant"}

var capturedMessageID = regexp.MustCompile(`message-id="\d+"`)

// answering returns a session to a device that answers every request with
// the reply captured for it.
func answering(t *testing.T, device, request string) *netconf.Session {
	t.Helper()
	captured, err := os.ReadFile(filepath.Join("testdata", "refused", device+"-"+request+".xml"))
	be.NilErr(t, err)

	tr := netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
		id := `message-id="` + netconftest.ExtractMessageID(req) + `"`
		return []string{capturedMessageID.ReplaceAllString(string(captured), id)}
	}))
	sess, err := netconf.Open(context.Background(), tr)
	be.NilErr(t, err)
	return sess
}

// Junos has no error tag for a request that the login class of the user
// does not permit, and none for a request it does not know.  It says both
// in the message, and package junos has a target for each.
func TestRefusals(t *testing.T) {
	ctx := context.Background()

	requests := []struct {
		name string
		exec func(*netconf.Session) error
		// execOn is what is sent to a device that was asked something else
		execOn map[string]func(*netconf.Session) error

		denied, unsupported bool
		// what was not permitted, as the first error names it
		badElement string
	}{
		{
			name:   "lock",
			exec:   func(s *netconf.Session) error { return rpc.Lock{Target: rpc.Candidate}.Exec(ctx, s) },
			denied: true, badElement: "lock",
		},
		{
			name: "clear-arp-table",
			exec: func(s *netconf.Session) error {
				_, err := s.ExecReply(ctx, `<clear-arp-table><hostname>192.0.2.254</hostname></clear-arp-table>`)
				return err
			},
			denied: true, badElement: "clear-arp-table",
		},
		{
			// the word of the command that the class does not permit
			name: "command",
			exec: func(s *netconf.Session) error {
				_, err := junos.Command("clear arp hostname 192.0.2.254").Exec(ctx, s)
				return err
			},
			denied: true, badElement: "clear",
		},
		{
			// a part of the configuration that the class may not read
			name: "get-configuration",
			exec: func(s *netconf.Session) error {
				_, err := junos.GetConfiguration{Filter: `<configuration><system><services/></system></configuration>`}.Exec(ctx, s)
				return err
			},
			denied: true, badElement: "system",
		},
		{
			// Junos does not know the command "set" for this user, and
			// says that first
			name: "load-configuration",
			exec: func(s *netconf.Session) error {
				_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: "set groups netconf-lib-probe system location building reader"}.Exec(ctx, s)
				return err
			},
			denied: true, badElement: "set",
		},
		{
			name: "commit-configuration",
			exec: func(s *netconf.Session) error {
				_, err := junos.CommitConfiguration{}.Exec(ctx, s)
				return err
			},
			denied: true, badElement: "commit-configuration",
		},
		{
			// a request the platform does not have: the bad element is the
			// request
			name: "get-chassis-cluster-status",
			exec: func(s *netconf.Session) error {
				_, err := s.ExecReply(ctx, `<get-chassis-cluster-status/>`)
				return err
			},
			unsupported: true, badElement: "get-chassis-cluster-status",
		},
		{
			// A command that is none is a syntax error as well, and the
			// request, <command>, is one Junos knows.
			name: "command-unknown",
			exec: func(s *netconf.Session) error {
				_, err := junos.Command("show no-such-thing").Exec(ctx, s)
				return err
			},
			badElement: "no-such-thing",
		},
		{
			// A request Junos knows and has no use for on the platform is
			// refused in words of its own: the table of a switch on a
			// router and on Evolved, the virtual chassis on a switch that
			// is in none.
			name: "no-use-on-the-platform",
			exec: func(s *netconf.Session) error {
				_, err := s.ExecReply(ctx, `<get-ethernet-switching-table-information/>`)
				return err
			},
			execOn: map[string]func(*netconf.Session) error{"switch": func(s *netconf.Session) error {
				_, err := s.ExecReply(ctx, `<get-virtual-chassis-information/>`)
				return err
			}},
		},
	}

	for _, device := range refusedBy {
		for _, request := range requests {
			t.Run(device+"/"+request.name, func(t *testing.T) {
				exec := request.exec
				if on, ok := request.execOn[device]; ok {
					exec = on
				}
				err := exec(answering(t, device, request.name))
				be.Nonzero(t, err)
				be.Equal(t, request.denied, errors.Is(err, junos.ErrPermissionDenied))
				be.Equal(t, request.unsupported, errors.Is(err, junos.ErrUnsupported))

				// what Junos sends has no tag that tells
				be.True(t, errors.Is(err, netconf.ErrOperationFailed))
				be.False(t, errors.Is(err, netconf.ErrAccessDenied))
				be.False(t, errors.Is(err, netconf.ErrOperationNotSupported))

				var rpcErr netconf.RPCError
				be.True(t, errors.As(err, &rpcErr))
				bad, _ := rpcErr.Detail("bad-element")
				be.Equal(t, request.badElement, bad)
			})
		}
	}
}

// The request <command> is one every Junos has, so a line given to it that
// Junos stumbles over at the word "command" is no request it does not know.
func TestErrUnsupported_NotACommand(t *testing.T) {
	const stumbled = `<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>syntax error</error-message><error-info><bad-element>command</bad-element></error-info></rpc-error>`
	sess, _ := session(t, stumbled)
	_, err := junos.Command("show version command").Exec(context.Background(), sess)
	be.Nonzero(t, err)
	be.False(t, errors.Is(err, junos.ErrUnsupported))
}

// A load of a statement Junos does not know is a syntax error with a bad
// element, like a request it does not know.  The bad element is not the
// request, and that is what tells the two apart.
func TestErrUnsupported_NotALoad(t *testing.T) {
	sess, _ := session(t, loadSyntaxError)
	_, err := junos.LoadConfiguration{Format: junos.FormatText, Config: "bogus-stanza;"}.Exec(context.Background(), sess)
	be.Nonzero(t, err)
	be.False(t, errors.Is(err, junos.ErrUnsupported))
	be.False(t, errors.Is(err, junos.ErrPermissionDenied))

	// and neither is an error that no session read from a reply
	made := netconf.RPCError{Message: "syntax error", Info: netconf.RawXML("<bad-element>x</bad-element>")}
	be.False(t, errors.Is(made, junos.ErrUnsupported))
	be.True(t, errors.Is(netconf.RPCError{Message: "permission denied"}, junos.ErrPermissionDenied))
}

// A change that the user may not make says so, whatever step it fails at.
func TestChangeApply_PermissionDenied(t *testing.T) {
	sess := answering(t, "router", "lock")
	_, err := junos.Change{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1"}.Apply(context.Background(), sess)
	be.True(t, errors.Is(err, junos.ErrPermissionDenied))
	be.In(t, "junos: lock candidate: netconf error: protocol operation-failed: 'lock': permission denied", err.Error())
}

// A candidate that holds changes nobody has committed is refused with one
// message, whichever operation asked for it: the lock and the private copy
// of a Change, and the same two asked for by themselves.
func TestErrCandidateModified_AnyOperation(t *testing.T) {
	t.Run("a lock", func(t *testing.T) {
		sess, _ := session(t, lockModified)
		err := rpc.Lock{Target: rpc.Candidate}.Exec(t.Context(), sess)
		be.True(t, errors.Is(err, junos.ErrCandidateModified))
		be.True(t, errors.Is(err, netconf.ErrOperationFailed))
	})

	t.Run("a private copy", func(t *testing.T) {
		sess, _ := session(t, openPrivateModified)
		err := junos.OpenConfiguration{Private: true}.Exec(t.Context(), sess)
		be.True(t, errors.Is(err, junos.ErrCandidateModified))
	})

	t.Run("a lock that somebody else has", func(t *testing.T) {
		sess, _ := session(t, lockDenied)
		err := rpc.Lock{Target: rpc.Candidate}.Exec(t.Context(), sess)
		be.False(t, errors.Is(err, junos.ErrCandidateModified))
	})

	// The error is about a candidate that was not handed out.  The same
	// words in the refusal of anything else, a commit, are another matter:
	// there something had been loaded.
	t.Run("the same message from a commit", func(t *testing.T) {
		sess, _ := session(t, openPrivateModified)
		_, err := junos.CommitConfiguration{}.Exec(t.Context(), sess)
		be.True(t, errors.Is(err, netconf.ErrOperationFailed))
		be.False(t, errors.Is(err, junos.ErrCandidateModified))
	})
}
