package netconf

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

// A failed commit as Junos 14.1X53 sent it (from PyEZ issue 624): the error
// that names the statement, a warning, and a summary that says nothing.
const junosCommitFailed = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/14.1X53/junos" message-id="1">
<commit-results>
<rpc-error>
<error-type>application</error-type>
<error-tag>invalid-value</error-tag>
<error-severity>error</error-severity>
<error-path>[edit interfaces xe-0/0/0 unit 0 family ethernet-switching interface-mode]</error-path>
<error-message>mgd: interface-mode access is allowed only for untagged interfaces</error-message>
<error-info>
<bad-element>interface-mode access</bad-element>
</error-info>
</rpc-error>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit protocols]</error-path>
<error-message>mgd: requires 'bgp' license</error-message>
<error-info>
<bad-element>bgp</bad-element>
</error-info>
</rpc-error>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
commit failed: (statements constraint check failed)
</error-message>
</rpc-error>
</commit-results>
</rpc-reply>`

// Captured from Junos 25.4 (EX4100): Junos names the process that reported
// an error in an element RFC 6241 does not have, and pads values with line
// breaks.
const junosDaemonError = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos" message-id="10" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<interface-information xmlns="http://xml.juniper.net/junos/25.4R0/junos-interface" junos:style="terse">
</interface-information>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<source-daemon>
ifinfo
</source-daemon>
<error-message>
device nonexistent-0/0/0 not found
</error-message>
</rpc-error>
</rpc-reply>`

func TestParseReplyKeepsReplyWithErrors(t *testing.T) {
	reply, err := parseReply([]byte(junosCommitFailed))

	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
	be.Equal(t, 2, len(rpcErrs))

	// what else the device said is still there to read
	be.Nonzero(t, reply)
	be.Equal(t, junosCommitFailed, string(reply.Raw))
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "mgd: requires 'bgp' license", reply.Warnings[0].Message)
}

// Junos 16.1R4 put the error of a failed ping inside the result element,
// where it inherits that element's namespace (PyEZ issue 716).  An error is
// an error in any namespace.
func TestParseReplyErrorInForeignNamespace(t *testing.T) {
	const raw = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/16.1R4/junos" message-id="1">
    <ping-results xmlns="http://xml.juniper.net/junos/16.1R4/junos-probe-tests">
        <ping-failure>usage error</ping-failure>
        <rpc-error>
           <error-type>protocol</error-type>
           <error-tag>operation-failed</error-tag>
          <error-severity>error</error-severity>
          <source-daemon>ping</source-daemon>
          <error-message>
usage: ping [-ACNQRSXadfnqrvw] [-c count] [-i wait]
          </error-message>
        </rpc-error>
    </ping-results>
</rpc-reply>`

	_, err := parseReply([]byte(raw))

	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, ErrOperationFailed, rpcErr.Tag)
	be.Equal(t, "usage: ping [-ACNQRSXadfnqrvw] [-c count] [-i wait]", rpcErr.Message)

	daemon, ok := rpcErr.Detail("source-daemon")
	be.True(t, ok)
	be.Equal(t, "ping", daemon)
}

func TestRPCErrorDetail(t *testing.T) {
	t.Run("an element the device added", func(t *testing.T) {
		_, err := parseReply([]byte(junosDaemonError))

		var rpcErr RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "device nonexistent-0/0/0 not found", rpcErr.Message)

		daemon, ok := rpcErr.Detail("source-daemon")
		be.True(t, ok)
		be.Equal(t, "ifinfo", daemon)

		_, ok = rpcErr.Detail("bad-element")
		be.False(t, ok)
	})

	t.Run("inside error-info", func(t *testing.T) {
		_, err := parseReply([]byte(junosCommitFailed))

		var rpcErr RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "[edit interfaces xe-0/0/0 unit 0 family ethernet-switching interface-mode]", rpcErr.Path)

		bad, ok := rpcErr.Detail("bad-element")
		be.True(t, ok)
		be.Equal(t, "interface-mode access", bad)
	})

	// RFC 6241 appendix A: lock-denied names the session holding the lock
	t.Run("lock holder", func(t *testing.T) {
		const raw = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1"><rpc-error>
<error-type>protocol</error-type><error-tag>lock-denied</error-tag><error-severity>error</error-severity>
<error-message>Lock failed, lock is already held</error-message>
<error-info><session-id>454</session-id></error-info></rpc-error></rpc-reply>`

		_, err := parseReply([]byte(raw))
		var rpcErr RPCError
		be.True(t, errors.As(err, &rpcErr))

		holder, ok := rpcErr.Detail("session-id")
		be.True(t, ok)
		be.Equal(t, "454", holder)
	})

	// an error put together by hand has no more than its fields
	t.Run("built by hand", func(t *testing.T) {
		rpcErr := RPCError{Info: RawXML(`<bad-element>mtu</bad-element>`)}

		bad, ok := rpcErr.Detail("bad-element")
		be.True(t, ok)
		be.Equal(t, "mtu", bad)

		_, ok = RPCError{}.Detail("bad-element")
		be.False(t, ok)
	})
}

// An error is printed with where it is, when the device says so: that is
// what tells one rejected statement from another.
func TestRPCErrorString(t *testing.T) {
	tests := []struct {
		name string
		err  RPCError
		want string
	}{
		{
			name: "message only",
			err:  RPCError{Type: ErrTypeProtocol, Tag: ErrOperationFailed, Severity: SevError, Message: "syntax error"},
			want: "netconf error: protocol operation-failed: syntax error",
		},
		{
			name: "bad element",
			err: RPCError{
				Type: ErrTypeProtocol, Tag: ErrOperationFailed, Severity: SevError, Message: "syntax error",
				Info: RawXML("\n<bad-element>bogus-stanza</bad-element>\n"),
			},
			want: "netconf error: protocol operation-failed: 'bogus-stanza': syntax error",
		},
		{
			name: "path and bad element",
			err: RPCError{
				Type: ErrTypeApp, Tag: ErrInvalidValue, Severity: SevError,
				Path:    "[edit system ntp]",
				Message: "server address is a local address",
				Info:    RawXML("<bad-element>server 192.0.2.1</bad-element>"),
			},
			want: "netconf error: application invalid-value: [edit system ntp] 'server 192.0.2.1': server address is a local address",
		},
		{
			name: "path only",
			err: RPCError{
				Type: ErrTypeApp, Tag: ErrDataMissing, Severity: SevError,
				Path: "/rpc/edit-config/config/top", Message: "no such thing",
			},
			want: "netconf error: application data-missing: /rpc/edit-config/config/top: no such thing",
		},
		{
			// Junos warnings come without type and tag
			name: "warning",
			err:  RPCError{Severity: SevWarning, Message: "statement not found"},
			want: "netconf warning: statement not found",
		},
		{
			name: "padded message",
			err:  RPCError{Type: ErrTypeProtocol, Tag: ErrOperationFailed, Message: "\nconfiguration check-out failed\n"},
			want: "netconf error: protocol operation-failed: configuration check-out failed",
		},
		{
			// Junos has errors that say nothing
			name: "no message",
			err:  RPCError{Type: ErrTypeProtocol, Tag: ErrOperationFailed, Severity: SevError},
			want: "netconf error: protocol operation-failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			be.Equal(t, tc.want, tc.err.Error())
		})
	}
}

// errors.Is(err, netconf.ErrLockDenied) is what one writes first.
func TestErrorsIsTag(t *testing.T) {
	_, err := parseReply([]byte(junosCommitFailed))

	be.True(t, errors.Is(err, ErrInvalidValue))
	be.True(t, errors.Is(err, ErrOperationFailed))
	be.False(t, errors.Is(err, ErrLockDenied))

	single := RPCError{Tag: ErrLockDenied}
	be.True(t, errors.Is(single, ErrLockDenied))
	be.False(t, errors.Is(single, ErrInUse))
	be.False(t, errors.Is(errors.New("lock-denied"), ErrLockDenied))
}

// A rejected change is reported with everything the device said about it:
// each error with its place, and the reply itself for the rest.
func TestExecReplyFailedCommit(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string {
		return strings.Replace(junosCommitFailed, `message-id="1"`, `message-id="`+msgID+`"`, 1) + "]]>]]>"
	})

	reply, err := sess.ExecReply(testCtx(t), "<commit-configuration/>")

	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
	be.Equal(t, 2, len(rpcErrs))
	be.Equal(t,
		"netconf error: application invalid-value: [edit interfaces xe-0/0/0 unit 0 family ethernet-switching interface-mode] 'interface-mode access': mgd: interface-mode access is allowed only for untagged interfaces",
		rpcErrs[0].Error())
	be.Equal(t,
		"netconf error: protocol operation-failed: commit failed: (statements constraint check failed)",
		rpcErrs[1].Error())

	be.Nonzero(t, reply)
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "netconf warning: [edit protocols] 'bgp': mgd: requires 'bgp' license", reply.Warnings[0].Error())

	// Exec has only the error to give
	be.True(t, errors.Is(sess.Exec(testCtx(t), "<commit-configuration/>", nil), ErrInvalidValue))
}

// messageError is a target for errors.Is that is no tag: it matches an RPC
// error by what the device wrote in it.
type messageError string

func (m messageError) Error() string { return "message " + string(m) }

func (m messageError) MatchRPCError(e RPCError) bool { return e.Message == string(m) }

// Some devices say what went wrong in the message and not in the tag.  A
// target that knows how to read an RPC error is asked about each one.
func TestRPCErrorIsMatcher(t *testing.T) {
	denied := RPCError{Tag: ErrOperationFailed, Message: "permission denied"}
	other := RPCError{Tag: ErrOperationFailed, Message: "syntax error"}

	be.True(t, errors.Is(denied, messageError("permission denied")))
	be.False(t, errors.Is(other, messageError("permission denied")))
	be.True(t, errors.Is(denied, ErrOperationFailed))

	// in a list, and wrapped by whoever passed it on
	errs := error(RPCErrors{other, denied})
	be.True(t, errors.Is(errs, messageError("permission denied")))
	be.True(t, errors.Is(fmt.Errorf("lock: %w", errs), messageError("permission denied")))
	be.False(t, errors.Is(errs, messageError("nothing like it")))
}

// An RPC error knows which request it answers: the device may name that
// request in the error, and only the two together say what it means.
func TestRPCErrorRequest(t *testing.T) {
	const refused = `<rpc-error><error-type>protocol</error-type><error-tag>operation-failed</error-tag><error-severity>error</error-severity><error-message>syntax error</error-message><error-info><bad-element>get-chassis-cluster-status</bad-element></error-info></rpc-error>`
	sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, refused) })

	request := func(t *testing.T, err error) string {
		t.Helper()
		var rpcErr RPCError
		be.True(t, errors.As(err, &rpcErr))
		return rpcErr.Request()
	}

	_, err := sess.ExecReply(testCtx(t), "<get-chassis-cluster-status/>")
	be.Equal(t, "get-chassis-cluster-status", request(t, err))

	// a prefix is no part of the name, and neither is what the request holds
	_, err = sess.ExecReply(testCtx(t), `<j:get-chassis-cluster-status xmlns:j="urn:example"><detail/></j:get-chassis-cluster-status>`)
	be.Equal(t, "get-chassis-cluster-status", request(t, err))

	// through Exec, and for every error of the reply
	err = sess.Exec(testCtx(t), "<lock><target><candidate/></target></lock>", nil)
	be.Equal(t, "lock", request(t, err))
	var rpcErrs RPCErrors
	be.True(t, errors.As(err, &rpcErrs))
	for _, rpcErr := range rpcErrs {
		be.Equal(t, "lock", rpcErr.Request())
	}

	// an error that came from no session answers no request
	be.Equal(t, "", RPCError{Message: "syntax error"}.Request())

	// a warning knows its request as well
	const warned = `<rpc-error><error-severity>warning</error-severity><error-message>statement not found</error-message></rpc-error><ok/>`
	warning := wireSession(t, func(msgID, _ string) string { return framed(msgID, warned) })
	reply, err := warning.ExecReply(testCtx(t), `<load-configuration rollback="0"/>`)
	be.NilErr(t, err)
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "load-configuration", reply.Warnings[0].Request())
}

// A reply can stop being XML behind its errors: a character XML does not
// allow, in text the device passes on.  The errors that were read still say
// that the device refused, and come with the error of the reply.
func TestReplyNotXMLBehindItsErrors(t *testing.T) {
	const denied = `<rpc-error><error-type>protocol</error-type><error-tag>lock-denied</error-tag><error-severity>error</error-severity><error-message>configuration database locked</error-message></rpc-error>`

	tests := []struct {
		name string
		body string
		// refused says that the error has the refusal of the device in it,
		// notXML that it has where the reply stops being XML
		refused, notXML bool
	}{
		{name: "a reply that is XML", body: denied, refused: true},
		{name: "behind the error", body: denied + "<note>a\x01b</note>", refused: true, notXML: true},
		{name: "between two errors", body: denied + "<note>a\x01b</note>" + denied, refused: true, notXML: true},
		{name: "a reply that ends early", body: denied + "<note>", refused: true, notXML: true},
		// the one error cannot be read, so nothing says what the device answered
		{name: "inside the only error", body: strings.Replace(denied, "database locked", "database\x01locked", 1), notXML: true},
		{name: "behind a warning", body: strings.Replace(denied, ">error<", ">warning<", 1) + "<note>a\x01b</note>", notXML: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := wireSession(t, func(msgID, _ string) string { return framed(msgID, tc.body) })

			reply, err := sess.ExecReply(testCtx(t), "<lock><target><candidate/></target></lock>")
			be.Nonzero(t, err)
			be.In(t, "configuration database", string(reply.Raw))

			var rpcErrs RPCErrors
			be.Equal(t, tc.refused, errors.As(err, &rpcErrs))
			be.Equal(t, tc.refused, errors.Is(err, ErrLockDenied))
			if tc.refused {
				// the errors in front of the damage, each with its request
				be.Equal(t, 1, len(rpcErrs))
				be.Equal(t, "lock", rpcErrs[0].Request())
			}

			var syntaxErr *SyntaxError
			be.Equal(t, tc.notXML, errors.As(err, &syntaxErr))
		})
	}
}
