package netconf

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

// Replies captured from Junos 26.2R1.7 on a vJunos-switch and from Junos OS
// Evolved 26.2R1.7 on a vJunosEvolved, to requests for something that is not
// there.  Junos answers these with its own error element, not an <rpc-error>.
const (
	// Evolved, <get-arp-table-information> for an interface: the error is two
	// elements down in the result, and nothing separates the two attributes
	// of its tag.
	evolvedARPError = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos" message-id="1" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<arp-table-information junos:style="normal">
    <arp-table-entry>
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm"xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>arpd</source-daemon>
<message>interface not found</message>
</xnm:error>
    </arp-table-entry>
</arp-table-information>
</rpc-reply>`

	// Evolved, <get-interface-information> for an interface: inside the
	// result.  Junos 26.2 answers the same request with an <rpc-error> after
	// an empty result.
	evolvedInterfaceError = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos" message-id="2" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<interface-information xmlns="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos-interface" junos:style="normal">
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm" xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>ifmand</source-daemon>
<message>device nonexistent-0/0/0 not found</message>
</xnm:error>
</interface-information>
</rpc-reply>`

	// Both, <get-bgp-neighbor-information> for an address that is none: from
	// the routing process, under <rpc-reply>.
	evolvedRoutingError = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos" message-id="8" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm" xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>routing</source-daemon>
<message>Invalid address specified.</message>
</xnm:error>
</rpc-reply>`
	junosRoutingError = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="8" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm" xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>routing</source-daemon>
<message>Invalid address specified.</message>
</xnm:error>
</rpc-reply>`

	// Both, <get-route-information> for the routes received from a BGP peer
	// that is none: the routing process has no message for it.
	junosErrorWithoutMessage = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="3" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm" xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>routing</source-daemon>
<message></message>
</xnm:error>
</rpc-reply>`

	// Junos OS Evolved 23.2R2 on an ACX7509, put together from the lines of
	// its trace file that PyEZ issue 1397 quotes: the prefix of the error
	// element is not declared, which makes the reply one that many parsers
	// refuse.
	evolved23Error = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/23.2R2-S1.6-EVO/junos" xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="urn:uuid:39b0c75a-10a1-4c6b-bb4a-5eb6d7a0c488">
<arp-table-information junos:style="no-resolve">
    <arp-table-entry>
        <xnm:error>
            <source-daemon>arp</source-daemon>
            <message>interface not found</message>
        </xnm:error>
    </arp-table-entry>
</arp-table-information>
</rpc-reply>`

	// Evolved in rfc-compliant mode.  The error element inside a result is
	// written as before, under a reply with the nc prefix.  The routing
	// process is answered for with an <rpc-error> there, which has nothing
	// in its message when the process had none.
	evolvedInterfaceErrorRFC = `<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos" message-id="2">
<interface-information xmlns="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos-interface" junos:style="normal">
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm" xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>ifmand</source-daemon>
<message>device nonexistent-0/0/0 not found</message>
</xnm:error>
</interface-information>
</nc:rpc-reply>`
	evolvedNoMessageRFC = `<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos" message-id="1">
<nc:rpc-error format="xml">
<nc:error-type>protocol</nc:error-type>
<nc:error-tag>operation-failed</nc:error-tag>
<nc:error-severity>error</nc:error-severity>
<nc:error-message>

</nc:error-message>
</nc:rpc-error>
</nc:rpc-reply>`

	// Evolved, <get-configuration> of a group in a private copy.  The
	// severities of syslog are elements called error and warning in the
	// namespace of the error element, and the file they are for is called
	// rpc-error, so the reply is gone through for errors.
	evolvedSyslogSeverities = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7-EVOI20260619095247-evo-builder-1/junos" message-id="4" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1791618939" junos:changed-localtime="2026-10-10 09:55:39 CEST">
    <groups>
        <name>netconf-lib-probe</name>
        <system>
            <syslog>
                <file>
                    <name>rpc-error</name>
                    <contents>
                        <name>any</name>
                        <error/>
                    </contents>
                    <contents>
                        <name>daemon</name>
                        <warning/>
                    </contents>
                </file>
            </syslog>
        </system>
    </groups>
</configuration>
</rpc-reply>`
)

// An error Junos writes in its own element fails the operation like an
// <rpc-error> does, wherever in the reply it is.
func TestParseReplyJunosError(t *testing.T) {
	tests := []struct {
		name, raw, message, daemon, text string
	}{
		{"evolved, two elements down", evolvedARPError, "interface not found", "arpd", "netconf error: interface not found"},
		{"evolved, inside the result", evolvedInterfaceError, "device nonexistent-0/0/0 not found", "ifmand", "netconf error: device nonexistent-0/0/0 not found"},
		{"evolved, under rpc-reply", evolvedRoutingError, "Invalid address specified.", "routing", "netconf error: Invalid address specified."},
		{"junos, under rpc-reply", junosRoutingError, "Invalid address specified.", "routing", "netconf error: Invalid address specified."},
		{"no message", junosErrorWithoutMessage, "", "routing", "netconf error: reported by routing"},
		{"evolved 23.2, prefix not declared", evolved23Error, "interface not found", "arp", "netconf error: interface not found"},
		{"evolved in rfc-compliant mode", evolvedInterfaceErrorRFC, "device nonexistent-0/0/0 not found", "ifmand", "netconf error: device nonexistent-0/0/0 not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := parseReply([]byte(tc.raw))

			var rpcErrs RPCErrors
			be.True(t, errors.As(err, &rpcErrs))
			be.Equal(t, 1, len(rpcErrs))
			be.Equal(t, SevError, rpcErrs[0].Severity)
			be.Equal(t, tc.message, rpcErrs[0].Message)
			be.Equal(t, tc.text, err.Error())

			daemon, ok := rpcErrs[0].Detail("source-daemon")
			be.True(t, ok)
			be.Equal(t, tc.daemon, daemon)

			// Junos gives such an error neither type nor tag
			be.Zero(t, rpcErrs[0].Type)
			be.Zero(t, rpcErrs[0].Tag)
			be.False(t, errors.Is(err, ErrOperationFailed))

			// what else the device said is still there to read
			be.Equal(t, tc.raw, string(reply.Raw))
			be.Zero(t, len(reply.Warnings))
		})
	}
}

// In rfc-compliant mode an error without a message is an <rpc-error> that
// names nobody.  It is an error all the same, and its text ends where there
// is nothing more to say.
func TestParseReplyErrorWithoutMessage(t *testing.T) {
	_, err := parseReply([]byte(evolvedNoMessageRFC))

	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, "", rpcErr.Message)
	be.Equal(t, "netconf error: protocol operation-failed", err.Error())
}

// A configuration is no failed request for having the syslog severity error
// in it, which is an element with the name and the namespace of the error
// element.  The two differ in how they are written: the error element has
// the prefix xnm, a statement has none.
func TestParseReplyJunosErrorLookalikes(t *testing.T) {
	t.Run("syslog severities", func(t *testing.T) {
		reply, err := parseReply([]byte(evolvedSyslogSeverities))
		be.NilErr(t, err)
		be.Zero(t, len(reply.Warnings))
	})

	// Made up: the configuration next to an error.
	t.Run("next to an error", func(t *testing.T) {
		const raw = `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1">
<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm"><system><syslog><file><name>messages</name>
<contents><name>any</name><error/></contents></file></syslog></system></configuration>
<xnm:error xmlns="http://xml.juniper.net/xnm/1.1/xnm" xmlns:xnm="http://xml.juniper.net/xnm/1.1/xnm">
<source-daemon>routing</source-daemon>
<message>Invalid address specified.</message>
</xnm:error>
</rpc-reply>`

		_, err := parseReply([]byte(raw))

		var rpcErrs RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 1, len(rpcErrs))
		be.Equal(t, "Invalid address specified.", rpcErrs[0].Message)
	})
}

// Exec has only the error to give, where it used to decode a reply without
// data.
func TestExecJunosError(t *testing.T) {
	sess := wireSession(t, func(msgID, _ string) string {
		return strings.Replace(evolvedInterfaceError, `message-id="2"`, `message-id="`+msgID+`"`, 1) + "]]>]]>"
	})

	var got struct {
		Names []string `xml:"interface-information>physical-interface>name"`
	}
	err := sess.Exec(testCtx(t), "<get-interface-information><interface-name>nonexistent-0/0/0</interface-name></get-interface-information>", &got)

	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, "device nonexistent-0/0/0 not found", rpcErr.Message)
}
