package junos_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/junos"
	"github.com/lasseh/netconf/rpc"
)

// The replies in this file were captured from Junos 25.4 (EX4100) in a
// private copy of the candidate that was never committed, unless a comment
// names another source: commits, the lock and what the two answer when
// somebody else is in the way are from Junos 26.2 (vJunos-router).

// The reply to <validate> and to <commit-configuration><check/> on the shared
// candidate.
const commitCheckOK = `<commit-results>
<routing-engine junos:style="normal">
<name>fpc0</name>
</routing-engine>
<routing-engine junos:style="normal">
<name>fpc0</name>
<commit-check-success/>
</routing-engine>
</commit-results>
<ok/>
`

// The same check in a private copy says that the copy was loaded first.
const commitCheckPrivateOK = `<commit-results>
<load-success/>
<routing-engine junos:style="normal">
<name>fpc0</name>
</routing-engine>
<routing-engine junos:style="normal">
<name>fpc0</name>
<commit-check-success/>
</routing-engine>
</commit-results>
<ok/>
`

// A check that fails: a warning, the errors of the process that found them,
// each padded with line breaks, and a summary that says nothing.
const commitCheckFailed = `<commit-results>
<load-success/>
<routing-engine junos:style="normal">
<name>fpc0</name>
</routing-engine>
<rpc-error>
<error-severity>warning</error-severity>
<source-daemon>
mgd
</source-daemon>
<error-path>
[edit protocols]
</error-path>
<error-info>
<bad-element>
bgp
</bad-element>
</error-info>
<error-message>
One or more members of the VC require 'BGP' license
</error-message>
</rpc-error>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<source-daemon>
rpd
</source-daemon>
<error-message>
Policy error: Policy netconf-lib-no-such-policy referenced but not defined
</error-message>
</rpc-error>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<source-daemon>
rpd
</source-daemon>
<error-path>
[edit protocols bgp group netconf-lib-probe]
</error-path>
<error-info>
<bad-element>
export
</bad-element>
</error-info>
<error-message>
BGP: export list not applied
</error-message>
</rpc-error>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
configuration check-out failed
</error-message>
</rpc-error>
</commit-results>
`

const loadOK = `<load-configuration-results>
<ok/>
</load-configuration-results>
`

// A delete of something that is not there.
const loadWarned = `<load-configuration-results>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit groups netconf-lib-probe system location]</error-path>
<error-message>statement not found: country-code</error-message>
</rpc-error>
<ok/>
</load-configuration-results>
`

// Two set commands of which the first is wrong.  Junos runs them one by one:
// it reports the first, applies the second and says <ok/>.
const loadSetPartlyFailed = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>Invalid numeric value: 'router1'</error-message>
<error-info>
<bad-element>router1</bad-element>
</error-info>
</rpc-error>
<ok/>
</load-configuration-results>
`

// A syntax error in text format, with the warning it leaves behind.
const loadSyntaxError = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>syntax error</error-message>
<error-info>
<bad-element>bogus-stanza</bad-element>
</error-info>
</rpc-error>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit groups netconf-lib-probe]</error-path>
<error-message>mgd: statement has no contents; ignored</error-message>
<error-info>
<bad-element>system</bad-element>
</error-info>
</rpc-error>
</load-configuration-results>
`

// The same mistake in XML is counted as well.
const loadXMLError = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>syntax error</error-message>
<error-info>
<bad-element>bogus-stanza</bad-element>
</error-info>
</rpc-error>
<load-error-count>1</load-error-count>
</load-configuration-results>
`

// <close-configuration/> with nothing open.
const closeNotOpen = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Configuration database is not open
</error-message>
</rpc-error>
`

// A rejected commit as Junos 14.1X53 sent it, from PyEZ issue 624: an older
// release, and a commit rather than a check.
const commitFailed = `<commit-results>
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
`

// The replies from here on were captured from Junos 26.2 (vJunos-router), a
// router with one routing engine.

// A commit that went through says <ok/> and nothing else.  So does one with
// a comment, a confirmed commit, and the commit or check that confirms it.
const commitDone = `<ok/>
`

// A commit from a private copy says that the copy was loaded first.
const commitPrivateDone = `<load-success/>
<load-success/>
<ok/>
`

// A commit, or a check, from a private copy that no longer fits: somebody
// else committed another value for the statement in the meantime.  Junos
// commits nothing, calls that a warning and leaves out the <ok/>.
const commitPrivateConflict = `<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit groups netconf-lib-probe system location building]</error-path>
<error-message>mgd: statement does not match patch: 'conflict-a' != 'both-a'</error-message>
<error-info>
<bad-element>building conflict-a</bad-element>
</error-info>
</rpc-error>
<load-error-count>1</load-error-count>
`

// A commit that Junos rejects.  A check, the standard <commit/> and a
// confirmed commit get the same answer.  There is no <commit-results> around
// it on this router.
const commitRejected = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<source-daemon>
rpd
</source-daemon>
<error-path>
[edit]
</error-path>
<error-info>
<bad-element>
policy-options
</bad-element>
</error-info>
<error-message>
Policy error: netconf-lib-missing prefix-list referenced (in term 1) but not defined
</error-message>
</rpc-error>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
configuration check-out failed
</error-message>
</rpc-error>
`

// <synchronize/> where there is one routing engine.
const commitSynchronizeIgnored = `<commit-results>
<rpc-error>
<error-severity>warning</error-severity>
<error-message>
'synchronize' ignored in single routing engine chassis
</error-message>
</rpc-error>
</commit-results>
<ok/>
`

// A commit or a load in a session whose lock on the candidate went away
// under it: the time to confirm ran out and Junos went back to the previous
// configuration.
const accessRevoked = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
access has been revoked.
</error-message>
</rpc-error>
`

// <open-configuration><private/> while the candidate holds changes that
// nobody has committed.
const openPrivateModified = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
shared configuration database modified
</error-message>
</rpc-error>
`

// The same while somebody else has the lock on the candidate: the copy is
// opened, and what is committed from it is refused.
const openPrivateLocked = `<rpc-error>
<error-severity>warning</error-severity>
<error-message>
changes cannot be committed while 'configure exclusive' is active
</error-message>
</rpc-error>
`

// <load-configuration rollback="49"/> where Junos has no such configuration.
const rollbackMissing = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Cannot open configuration file: /var/db/config/juniper.conf.49 ; No such file or directory
</error-message>
</rpc-error>
</load-configuration-results>
`

func TestLoadConfigurationMarshal(t *testing.T) {
	type configuration struct {
		XMLName  xml.Name `xml:"configuration"`
		HostName string   `xml:"system>host-name"`
	}

	tests := []struct {
		name string
		op   junos.LoadConfiguration
		want string
	}{
		{
			name: "set commands",
			op:   junos.LoadConfiguration{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1\ndelete system ntp server 192.0.2.2"},
			want: "<load-configuration action=\"set\" format=\"text\"><configuration-set>set system ntp server 192.0.2.1\ndelete system ntp server 192.0.2.2</configuration-set></load-configuration>",
		},
		{
			name: "text is escaped, not interpreted",
			op:   junos.LoadConfiguration{Format: junos.FormatText, Config: []byte(`system { login { message "a < b & c"; } }`)},
			want: `<load-configuration format="text"><configuration-text>system { login { message "a &lt; b &amp; c"; } }</configuration-text></load-configuration>`,
		},
		{
			name: "text, override",
			op:   junos.LoadConfiguration{Format: junos.FormatText, Action: junos.LoadOverride, Config: "system { host-name router1; }"},
			want: `<load-configuration format="text" action="override"><configuration-text>system { host-name router1; }</configuration-text></load-configuration>`,
		},
		{
			name: "xml as written",
			op:   junos.LoadConfiguration{Config: `<configuration><system><host-name>router1</host-name></system></configuration>`},
			want: `<load-configuration><configuration><system><host-name>router1</host-name></system></configuration></load-configuration>`,
		},
		{
			name: "xml from a value, replace",
			op:   junos.LoadConfiguration{Format: junos.FormatXML, Action: junos.LoadReplace, Config: configuration{HostName: "router1"}},
			want: `<load-configuration action="replace"><configuration><system><host-name>router1</host-name></system></configuration></load-configuration>`,
		},
		{
			name: "json, update",
			op:   junos.LoadConfiguration{Format: junos.FormatJSON, Action: junos.LoadUpdate, Config: `{"configuration":{"system":{"host-name":"router1"}}}`},
			want: `<load-configuration format="json" action="update"><configuration-json>{"configuration":{"system":{"host-name":"router1"}}}</configuration-json></load-configuration>`,
		},
		{
			// as a compare prints it, and as Junos 26.2 took it
			name: "a patch",
			op:   junos.LoadConfiguration{Action: junos.LoadPatch, Config: "[edit system]\n+  location building \"a < b\";"},
			want: "<load-configuration action=\"patch\" format=\"text\"><configuration-patch>[edit system]\n+  location building \"a &lt; b\";</configuration-patch></load-configuration>",
		},
		{
			name: "a patch, said to be text",
			op:   junos.LoadConfiguration{Format: junos.FormatText, Action: junos.LoadPatch, Config: []byte("[edit]\n- x;")},
			want: "<load-configuration action=\"patch\" format=\"text\"><configuration-patch>[edit]\n- x;</configuration-patch></load-configuration>",
		},
		{
			name: "a file of the device, xml",
			op:   junos.LoadConfiguration{URL: "/var/tmp/golden.xml"},
			want: `<load-configuration url="/var/tmp/golden.xml"></load-configuration>`,
		},
		{
			name: "a file of the device, text, override",
			op:   junos.LoadConfiguration{Format: junos.FormatText, Action: junos.LoadOverride, URL: "/var/tmp/golden.conf"},
			want: `<load-configuration url="/var/tmp/golden.conf" format="text" action="override"></load-configuration>`,
		},
		{
			name: "a file of set commands",
			op:   junos.LoadConfiguration{Format: junos.FormatSet, URL: `/var/tmp/a "b".set`},
			want: `<load-configuration url="/var/tmp/a &#34;b&#34;.set" action="set" format="text"></load-configuration>`,
		},
		{
			name: "a patch in a file somewhere else",
			op:   junos.LoadConfiguration{Action: junos.LoadPatch, URL: "ftp://files.example.net/change.patch"},
			want: `<load-configuration url="ftp://files.example.net/change.patch" action="patch" format="text"></load-configuration>`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := xml.Marshal(tc.op)
			be.NilErr(t, err)
			be.Equal(t, tc.want, string(got))
		})
	}

	for name, op := range map[string]junos.LoadConfiguration{
		"no config":             {Format: junos.FormatSet},
		"unknown format":        {Format: "yaml", Config: "x"},
		"unknown action":        {Action: "append", Config: "<configuration/>"},
		"set with an action":    {Format: junos.FormatSet, Action: junos.LoadOverride, Config: "set x"},
		"json replace":          {Format: junos.FormatJSON, Action: junos.LoadReplace, Config: "{}"},
		"text from a structure": {Format: junos.FormatText, Config: configuration{HostName: "router1"}},
		"a patch as xml":        {Format: junos.FormatXML, Action: junos.LoadPatch, Config: "[edit]"},
		"a patch as json":       {Format: junos.FormatJSON, Action: junos.LoadPatch, Config: "[edit]"},
		"a patch of commands":   {Format: junos.FormatSet, Action: junos.LoadPatch, Config: "[edit]"},
		"a patch from a value":  {Action: junos.LoadPatch, Config: configuration{HostName: "router1"}},
		"config and a file":     {Config: "<configuration/>", URL: "/var/tmp/golden.xml"},
		"a file, json replace":  {Format: junos.FormatJSON, Action: junos.LoadReplace, URL: "/var/tmp/golden.json"},
	} {
		t.Run("rejected: "+name, func(t *testing.T) {
			_, err := xml.Marshal(op)
			be.Nonzero(t, err)
		})
	}
}

// sentJSON returns the JSON document that a load of doc sends: what the
// JSON parser of Junos gets to read once its XML parser is done.
func sentJSON(t *testing.T, doc string) string {
	t.Helper()
	raw, err := xml.Marshal(junos.LoadConfiguration{Format: junos.FormatJSON, Config: doc})
	be.NilErr(t, err)

	var sent struct {
		Doc string `xml:"configuration-json"`
	}
	be.NilErr(t, xml.Unmarshal(raw, &sent))
	return sent.Doc
}

// Junos 26.2 reads two escapes in a JSON string, \" and \\, and stores
// every other one as written: "a\u003cb" loaded there is the eight characters
// a\u003cb in the configuration, and "a\/b" is a\/b.  A load spells out the
// escapes that have another spelling.
func TestLoadConfigurationJSONEscapes(t *testing.T) {
	tests := []struct {
		name, doc, want string
	}{
		{"what encoding/json escapes", `{"a":"R\u0026D \u003clab\u003e"}`, `{"a":"R&D <lab>"}`},
		{"a letter", `{"a":"Troms\u00f8 \u00C5lesund"}`, `{"a":"Tromsø Ålesund"}`},
		{"a surrogate pair", `{"a":"\ud83d\ude00"}`, `{"a":"😀"}`},
		{"a line separator, escaped by encoding/json", `{"a":"x\u2028y"}`, "{\"a\":\"x\u2028y\"}"},
		{"a slash", `{"a":"ge-0\/0\/0"}`, `{"a":"ge-0/0/0"}`},
		{"in the name of a member", `{"a\u0026b":1}`, `{"a&b":1}`},

		// what has to stay escaped
		{"an escaped quote", `{"a":"x\u0022y"}`, `{"a":"x\"y"}`},
		{"an escaped backslash", `{"a":"x\u005cy"}`, `{"a":"x\\y"}`},
		{"the two Junos reads", `{"a":"x\"y\\z"}`, `{"a":"x\"y\\z"}`},
		{"a backslash before a u", `{"a":"x\\u003cy"}`, `{"a":"x\\u003cy"}`},
		{"a backslash before an escape", `{"a":"x\\\u003cy"}`, `{"a":"x\\<y"}`},
		{"a quote before an escape", `{"a":"\"\u003c","b":"\u003e"}`, `{"a":"\"<","b":">"}`},

		// JSON has no other spelling for a control character
		{"control characters", `{"a":"x\ny\tz\r\b\f"}`, `{"a":"x\ny\tz\r\b\f"}`},
		{"an escaped control character", `{"a":"x\u000ay\u0007"}`, `{"a":"x\u000ay\u0007"}`},
		// and XML cannot carry these two
		{"not a character", `{"a":"\ufffe\uffff"}`, `{"a":"\ufffe\uffff"}`},

		// half a surrogate pair is JSON, and has no other spelling
		{"half a surrogate pair", `{"a":"\ud83d","b":"\ude00","c":"\ud83d\u0041"}`, `{"a":"\ud83d","b":"\ude00","c":"\ud83dA"}`},

		// what is not JSON goes out as it came
		{"not an escape", `{"a":"\u00","b":"\u003c"}`, `{"a":"\u00","b":"\u003c"}`},
		{"a string without an end", `{"a":"x\u003c`, `{"a":"x\u003c`},
		{"an escape outside a string", `{"a":\u003c,"b":"\u003c"}`, `{"a":\u003c,"b":"\u003c"}`},
		{"a line break in a string", "{\"a\":\"x\ny\\u003c\"}", "{\"a\":\"x\ny\\u003c\"}"},

		{"nothing but the strings", `{ "n" : 12345678901234567890, "f" : 1.10, "b" : [ true, null ] }`, `{ "n" : 12345678901234567890, "f" : 1.10, "b" : [ true, null ] }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			be.Equal(t, tc.want, sentJSON(t, tc.doc))
		})
	}

	// The only escapes Junos reads, as captured: everything else is the text
	// that goes into the configuration.
	junosReads := strings.NewReplacer(`\"`, `"`, `\\`, `\`)

	t.Run("what encoding/json writes is what Junos stores", func(t *testing.T) {
		for _, value := range []string{
			`R&D <lab>`,
			`a "quoted" word and a back\slash`,
			`Tromsø – Ålesund`,
			"日本 😀 x\u2028y",
			`a\u003cb, with the backslash`,
			`ge-0/0/0`,
			`\\"\`,
		} {
			doc, err := json.Marshal(map[string]string{"v": value})
			be.NilErr(t, err)

			sent := sentJSON(t, string(doc))
			text, ok := strings.CutPrefix(sent, `{"v":"`)
			be.True(t, ok)
			text, ok = strings.CutSuffix(text, `"}`)
			be.True(t, ok)
			be.Equal(t, value, junosReads.Replace(text))
		}
	})

	t.Run("the document means the same", func(t *testing.T) {
		value := map[string]any{
			"configuration": map[string]any{
				"R&D <lab>": []any{"a\nb\tc\x00\x1f", `"\`, "å\u2028😀", 1.5, true, nil},
			},
		}
		doc, err := json.Marshal(value)
		be.NilErr(t, err)

		var got any
		be.NilErr(t, json.Unmarshal([]byte(sentJSON(t, string(doc))), &got))
		be.DeepEqual(t, any(value), got)
	})
}

func TestLoadConfiguration(t *testing.T) {
	op := junos.LoadConfiguration{Format: junos.FormatSet, Config: "delete groups netconf-lib-probe system location country-code"}

	t.Run("loaded", func(t *testing.T) {
		sess, _ := session(t, loadOK)
		reply, err := op.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, 0, len(reply.Warnings))
	})

	t.Run("loaded with a warning", func(t *testing.T) {
		sess, _ := session(t, loadWarned)
		reply, err := op.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, 1, len(reply.Warnings))
		be.Equal(t, "netconf warning: [edit groups netconf-lib-probe system location]: statement not found: country-code", reply.Warnings[0].Error())
	})

	// Junos says <ok/> although a command failed.  The error decides.
	t.Run("set commands of which one fails", func(t *testing.T) {
		sess, _ := session(t, loadSetPartlyFailed)
		_, err := op.Exec(context.Background(), sess)

		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "netconf error: protocol operation-failed: 'router1': Invalid numeric value: 'router1'", rpcErr.Error())
	})

	t.Run("syntax error in text", func(t *testing.T) {
		sess, _ := session(t, loadSyntaxError)
		reply, err := op.Exec(context.Background(), sess)

		var rpcErrs netconf.RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 1, len(rpcErrs))
		be.Equal(t, "netconf error: protocol operation-failed: 'bogus-stanza': syntax error", rpcErrs[0].Error())
		be.Equal(t, 1, len(reply.Warnings))
	})

	t.Run("syntax error in xml", func(t *testing.T) {
		sess, _ := session(t, loadXMLError)
		reply, err := op.Exec(context.Background(), sess)
		be.Nonzero(t, err)

		count, err := reply.Element("load-error-count")
		be.NilErr(t, err)
		be.Equal(t, "1", count.Text)
	})

	// A load that went through says so.  Anything else is not one.
	t.Run("neither ok nor error", func(t *testing.T) {
		sess, _ := session(t, "<load-configuration-results>\n</load-configuration-results>\n")
		_, err := op.Exec(context.Background(), sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		be.In(t, "load-configuration: ", err.Error())
	})
}

// What Junos 26.2R1.7 (vJunos-router) answered four loads in a private copy,
// captured in a lab; the switch and Junos OS Evolved answered the same.
const (
	// A patch that takes out what is not there: warnings, and no <ok/>.
	loadPatchNotApplied = `<load-configuration-results>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit]</error-path>
<error-message>mgd: statement does not exist</error-message>
<error-info>
<bad-element>groups netconf-lib-probe</bad-element>
</error-info>
</rpc-error>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit groups netconf-lib-probe]</error-path>
<error-message>mgd: statement does not exist</error-message>
<error-info>
<bad-element>system</bad-element>
</error-info>
</rpc-error>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit groups netconf-lib-probe system]</error-path>
<error-message>mgd: statement does not exist</error-message>
<error-info>
<bad-element>location</bad-element>
</error-info>
</rpc-error>
<rpc-error>
<error-severity>warning</error-severity>
<error-path>[edit groups netconf-lib-probe system]</error-path>
<error-message>mgd: statement not found: 'building no-such-building'</error-message>
<error-info>
<bad-element>location</bad-element>
</error-info>
</rpc-error>
</load-configuration-results>
`

	// A patch that is no patch: a warning, and <ok/>.
	loadPatchNoise = `<load-configuration-results>
<rpc-error>
<error-severity>warning</error-severity>
<error-message>
ignoring noise in patch:
</error-message>
</rpc-error>
<patch-noise>this is no patch
</patch-noise>
<ok/>
</load-configuration-results>
`

	// A file that is not there.
	loadURLMissing = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Cannot open configuration file: /var/tmp/netconf-lib-no-such-file.conf ; No such file or directory
</error-message>
</rpc-error>
</load-configuration-results>
`

	// The rescue configuration of a device that has none.
	loadRescueMissing = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Cannot open configuration file: /config/rescue.conf.gz ; No such file or directory
</error-message>
</rpc-error>
</load-configuration-results>
`
)

// Junos 26.2 takes a commit comment of up to 512 bytes, and refuses the
// commit for a longer one: "Length 513 is not within range (1..512)".  It
// counts bytes, so a letter that is two of them counts twice.  Such a
// commit is not sent.
func TestCommitConfiguration_CommentLength(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		comment string
		refused string
	}{
		{name: "512 bytes", comment: strings.Repeat("x", 512)},
		{name: "513 bytes", comment: strings.Repeat("x", 513), refused: "513 bytes"},
		{name: "256 letters of two bytes", comment: strings.Repeat("å", 256)},
		{name: "257 letters of two bytes", comment: strings.Repeat("å", 257), refused: "514 bytes"},
		// half a letter is written as U+FFFD, which is three bytes
		{name: "cut through a letter", comment: (strings.Repeat("x", 511) + "å")[:512], refused: "514 bytes"},
		// and so is every byte that is no UTF-8, and a control character
		{name: "bytes that are no UTF-8", comment: strings.Repeat("\xff", 200), refused: "600 bytes"},
		{name: "a control character", comment: strings.Repeat("x", 511) + "\x01", refused: "514 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, ops := session(t, `<ok/>`)
			_, err := junos.CommitConfiguration{Comment: tc.comment}.Exec(ctx, sess)
			if tc.refused == "" {
				be.NilErr(t, err)
				be.In(t, "<log>"+tc.comment+"</log>", ops()[0])
				return
			}
			be.Nonzero(t, err)
			be.In(t, "commit-configuration: the comment is "+tc.refused+" long, and Junos takes 512", err.Error())
			be.Equal(t, 0, len(ops()))
		})
	}

	// a check has no comment to keep, and Junos looks at its length all the same
	sess, ops := session(t, `<ok/>`)
	_, err := junos.CommitConfiguration{Check: true, Comment: strings.Repeat("x", 513)}.Exec(ctx, sess)
	be.Nonzero(t, err)
	be.Equal(t, 0, len(ops()))
}

// A patch, a file of the device and the rescue configuration: what goes
// out, and what Junos said when it could not do it.
func TestLoadPatchURLRescue(t *testing.T) {
	ctx := context.Background()

	t.Run("a patch that Junos takes", func(t *testing.T) {
		sess, ops := session(t, loadOK)
		reply, err := junos.LoadConfiguration{Action: junos.LoadPatch, Config: wantDiff}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, 0, len(reply.Warnings))
		be.Equal(t, "<load-configuration action=\"patch\" format=\"text\"><configuration-patch>"+wantDiff+"</configuration-patch></load-configuration>", ops()[0])
	})

	// Junos has only warnings for a patch that does not apply, and loads
	// none of it.  The <ok/> that is missing is what says so.
	t.Run("a patch that does not apply", func(t *testing.T) {
		sess, _ := session(t, loadPatchNotApplied)
		reply, err := junos.LoadConfiguration{Action: junos.LoadPatch, Config: "[edit groups]\n-  netconf-lib-probe { ... }"}.Exec(ctx, sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		be.In(t, "load-configuration: ", err.Error())
		be.In(t, "statement does not exist", err.Error())
		be.In(t, "statement not found: 'building no-such-building'", err.Error())
		be.Equal(t, 4, len(reply.Warnings))
	})

	t.Run("a patch that is no text", func(t *testing.T) {
		sess, ops := session(t, loadOK)
		_, err := junos.LoadConfiguration{Action: junos.LoadPatch, Config: struct{}{}}.Exec(ctx, sess)
		be.Nonzero(t, err)
		be.In(t, "load-configuration: a patch must be a string or []byte, not struct {}", err.Error())
		be.Equal(t, 0, len(ops()))
	})

	// Junos takes anything for a patch: what it cannot read is noise to it,
	// and the load goes through with nothing loaded.  The warning is all
	// there is to tell.
	t.Run("a patch that is none", func(t *testing.T) {
		sess, _ := session(t, loadPatchNoise)
		reply, err := junos.LoadConfiguration{Action: junos.LoadPatch, Config: "this is no patch"}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, 1, len(reply.Warnings))
		be.Equal(t, "ignoring noise in patch:", reply.Warnings[0].Message)
	})

	t.Run("a file of the device", func(t *testing.T) {
		sess, ops := session(t, loadOK)
		_, err := junos.LoadConfiguration{Format: junos.FormatText, URL: "/var/tmp/golden.conf"}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, `<load-configuration url="/var/tmp/golden.conf" format="text"></load-configuration>`, ops()[0])
	})

	t.Run("a file that is not there", func(t *testing.T) {
		sess, _ := session(t, loadURLMissing)
		_, err := junos.LoadConfiguration{Format: junos.FormatText, URL: "/var/tmp/netconf-lib-no-such-file.conf"}.Exec(ctx, sess)
		be.True(t, errors.Is(err, netconf.ErrOperationFailed))
		be.In(t, "Cannot open configuration file: /var/tmp/netconf-lib-no-such-file.conf", err.Error())
	})

	t.Run("the rescue configuration", func(t *testing.T) {
		sess, ops := session(t, loadOK)
		_, err := junos.LoadRescue{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.Equal(t, `<load-configuration rescue="rescue"></load-configuration>`, ops()[0])
	})

	t.Run("no rescue configuration", func(t *testing.T) {
		sess, _ := session(t, loadRescueMissing)
		_, err := junos.LoadRescue{}.Exec(ctx, sess)
		be.True(t, errors.Is(err, netconf.ErrOperationFailed))
		be.True(t, strings.HasPrefix(err.Error(), "load rescue: "))
		be.In(t, "Cannot open configuration file: /config/rescue.conf.gz", err.Error())
	})
}

func TestRollback(t *testing.T) {
	sess, ops := session(t, loadOK)
	_, err := junos.Rollback(1).Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, `<load-configuration rollback="1"></load-configuration>`, ops()[0])

	t.Run("a configuration Junos does not have", func(t *testing.T) {
		sess, _ := session(t, rollbackMissing)
		_, err := junos.Rollback(49).Exec(context.Background(), sess)
		be.True(t, errors.Is(err, netconf.ErrOperationFailed))
		be.In(t, "Cannot open configuration file", err.Error())
	})

	for _, n := range []junos.Rollback{-1, 50} {
		_, err := xml.Marshal(n)
		be.Nonzero(t, err)
	}
}

func TestCommitConfigurationMarshal(t *testing.T) {
	tests := []struct {
		name string
		op   junos.CommitConfiguration
		want string
	}{
		{"commit", junos.CommitConfiguration{}, `<commit-configuration></commit-configuration>`},
		{"check", junos.CommitConfiguration{Check: true}, `<commit-configuration><check></check></commit-configuration>`},
		{
			"comment is text",
			junos.CommitConfiguration{Comment: "ticket <42> & co"},
			`<commit-configuration><log>ticket &lt;42&gt; &amp; co</log></commit-configuration>`,
		},
		{
			"confirmed, Junos counts minutes",
			junos.CommitConfiguration{Confirm: 5 * time.Minute, Comment: "add ntp server"},
			`<commit-configuration><confirmed></confirmed><confirm-timeout>5</confirm-timeout><log>add ntp server</log></commit-configuration>`,
		},
		{
			"confirmed, rounded up",
			junos.CommitConfiguration{Confirm: 90 * time.Second},
			`<commit-configuration><confirmed></confirmed><confirm-timeout>2</confirm-timeout></commit-configuration>`,
		},
		{
			"confirmed, never less than a minute",
			junos.CommitConfiguration{Confirm: time.Second},
			`<commit-configuration><confirmed></confirmed><confirm-timeout>1</confirm-timeout></commit-configuration>`,
		},
		{
			"synchronize",
			junos.CommitConfiguration{Synchronize: true},
			`<commit-configuration><synchronize></synchronize></commit-configuration>`,
		},
		{
			"full",
			junos.CommitConfiguration{Full: true, Comment: "after the upgrade"},
			`<commit-configuration><full></full><log>after the upgrade</log></commit-configuration>`,
		},
		{
			"force implies synchronize",
			junos.CommitConfiguration{ForceSynchronize: true},
			`<commit-configuration><synchronize></synchronize><force-synchronize></force-synchronize></commit-configuration>`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := xml.Marshal(tc.op)
			be.NilErr(t, err)
			be.Equal(t, tc.want, string(got))
		})
	}

	for name, op := range map[string]junos.CommitConfiguration{
		"confirmed check":        {Check: true, Confirm: time.Minute},
		"forced check":           {Check: true, ForceSynchronize: true},
		"forced confirmed":       {Confirm: time.Minute, ForceSynchronize: true},
		"negative confirm time":  {Confirm: -time.Minute},
		"confirm time too large": {Confirm: 65536 * time.Minute},
	} {
		t.Run("rejected: "+name, func(t *testing.T) {
			_, err := xml.Marshal(op)
			be.Nonzero(t, err)
		})
	}
}

func TestCommitConfiguration(t *testing.T) {
	for name, body := range map[string]string{"shared candidate": commitCheckOK, "private copy": commitCheckPrivateOK} {
		t.Run("check passed, "+name, func(t *testing.T) {
			sess, _ := session(t, body)
			reply, err := junos.CommitConfiguration{Check: true}.Exec(context.Background(), sess)
			be.NilErr(t, err)

			engines, err := reply.Elements("routing-engine")
			be.NilErr(t, err)
			be.Equal(t, 2, len(engines))
		})
	}

	t.Run("check failed", func(t *testing.T) {
		sess, _ := session(t, commitCheckFailed)
		reply, err := junos.CommitConfiguration{Check: true}.Exec(context.Background(), sess)

		var rpcErrs netconf.RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 3, len(rpcErrs))
		be.Equal(t, "netconf error: protocol operation-failed: Policy error: Policy netconf-lib-no-such-policy referenced but not defined", rpcErrs[0].Error())
		be.Equal(t, "netconf error: protocol operation-failed: [edit protocols bgp group netconf-lib-probe] 'export': BGP: export list not applied", rpcErrs[1].Error())
		be.Equal(t, "netconf error: protocol operation-failed: configuration check-out failed", rpcErrs[2].Error())

		daemon, ok := rpcErrs[0].Detail("source-daemon")
		be.True(t, ok)
		be.Equal(t, "rpd", daemon)

		// the warning that came with the errors is still there
		be.Equal(t, 1, len(reply.Warnings))
		be.Equal(t, "netconf warning: [edit protocols] 'bgp': One or more members of the VC require 'BGP' license", reply.Warnings[0].Error())
	})

	for name, body := range map[string]string{"candidate": commitDone, "private copy": commitPrivateDone} {
		t.Run("committed, "+name, func(t *testing.T) {
			sess, _ := session(t, body)
			reply, err := junos.CommitConfiguration{}.Exec(context.Background(), sess)
			be.NilErr(t, err)
			be.Equal(t, 0, len(reply.Warnings))
		})
	}

	t.Run("committed with a warning", func(t *testing.T) {
		sess, _ := session(t, commitSynchronizeIgnored)
		reply, err := junos.CommitConfiguration{Synchronize: true}.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, 1, len(reply.Warnings))
		be.Equal(t, "netconf warning: 'synchronize' ignored in single routing engine chassis", reply.Warnings[0].Error())
	})

	// Junos commits nothing and has only a warning to say about it.  Taken
	// by the absence of errors alone this reads as a commit that went
	// through.
	t.Run("private copy that no longer fits", func(t *testing.T) {
		for _, op := range []junos.CommitConfiguration{{}, {Check: true}} {
			sess, _ := session(t, commitPrivateConflict)
			reply, err := op.Exec(context.Background(), sess)
			be.Nonzero(t, err)
			be.True(t, errors.Is(err, netconf.ErrElementNotFound))
			be.In(t, "commit-configuration: ", err.Error())
			be.In(t, "statement does not match patch: 'conflict-a' != 'both-a'", err.Error())

			// what Junos said is in the error, for a program to look at
			var said netconf.RPCErrors
			be.True(t, errors.As(err, &said))
			be.Equal(t, 1, len(said))
			be.Equal(t, "[edit groups netconf-lib-probe system location building]", said[0].Path)
			be.Equal(t, 1, len(reply.Warnings))
		}
	})

	t.Run("neither ok nor error", func(t *testing.T) {
		sess, _ := session(t, "")
		_, err := junos.CommitConfiguration{}.Exec(context.Background(), sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))

		var said netconf.RPCErrors
		be.False(t, errors.As(err, &said))
	})

	// No device was seen to answer like this.  A warning by itself does not
	// say that nothing was committed, so the error must not read as a
	// refusal: it has no RPCErrors in it, only their text.
	t.Run("a warning and no ok", func(t *testing.T) {
		sess, _ := session(t, strings.Replace(commitSynchronizeIgnored, "<ok/>\n", "", 1))
		reply, err := junos.CommitConfiguration{Synchronize: true}.Exec(context.Background(), sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
		be.In(t, "'synchronize' ignored in single routing engine chassis", err.Error())

		var said netconf.RPCErrors
		be.False(t, errors.As(err, &said))
		be.Equal(t, 1, len(reply.Warnings))
	})

	t.Run("commit rejected", func(t *testing.T) {
		sess, _ := session(t, commitRejected)
		_, err := junos.CommitConfiguration{}.Exec(context.Background(), sess)

		var rpcErrs netconf.RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 2, len(rpcErrs))
		be.Equal(t,
			"netconf error: protocol operation-failed: [edit] 'policy-options': Policy error: netconf-lib-missing prefix-list referenced (in term 1) but not defined",
			rpcErrs[0].Error())
		daemon, ok := rpcErrs[0].Detail("source-daemon")
		be.True(t, ok)
		be.Equal(t, "rpd", daemon)
		be.Equal(t, "configuration check-out failed", rpcErrs[1].Message)
	})

	t.Run("commit rejected, older release", func(t *testing.T) {
		sess, _ := session(t, commitFailed)
		reply, err := junos.CommitConfiguration{Comment: "access port"}.Exec(context.Background(), sess)

		var rpcErrs netconf.RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 2, len(rpcErrs))
		be.Equal(t,
			"netconf error: application invalid-value: [edit interfaces xe-0/0/0 unit 0 family ethernet-switching interface-mode] 'interface-mode access': mgd: interface-mode access is allowed only for untagged interfaces",
			rpcErrs[0].Error())
		be.Equal(t, 1, len(reply.Warnings))
		be.Equal(t, "[edit protocols]", reply.Warnings[0].Path)
	})
}

func TestOpenConfiguration(t *testing.T) {
	tests := []struct {
		op   junos.OpenConfiguration
		want string
	}{
		{junos.OpenConfiguration{Private: true}, `<open-configuration><private></private></open-configuration>`},
		{junos.OpenConfiguration{Ephemeral: true}, `<open-configuration><ephemeral></ephemeral></open-configuration>`},
		{junos.OpenConfiguration{EphemeralInstance: "sdn"}, `<open-configuration><ephemeral-instance>sdn</ephemeral-instance></open-configuration>`},
	}
	for _, tc := range tests {
		got, err := xml.Marshal(tc.op)
		be.NilErr(t, err)
		be.Equal(t, tc.want, string(got))
	}

	for _, op := range []junos.OpenConfiguration{{}, {Private: true, Ephemeral: true}, {Ephemeral: true, EphemeralInstance: "sdn"}} {
		_, err := xml.Marshal(op)
		be.Nonzero(t, err)
	}

	// An empty reply, no <ok/>.
	t.Run("answered with an empty reply", func(t *testing.T) {
		sess, ops := session(t, ``)
		be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(context.Background(), sess))
		be.NilErr(t, junos.CloseConfiguration{}.Exec(context.Background(), sess))
		be.Equal(t, `<close-configuration></close-configuration>`, ops()[1])
	})

	t.Run("nothing open to close", func(t *testing.T) {
		sess, _ := session(t, closeNotOpen)
		err := junos.CloseConfiguration{}.Exec(context.Background(), sess)
		be.Nonzero(t, err)
		be.In(t, "Configuration database is not open", err.Error())
	})

	t.Run("the candidate holds uncommitted changes", func(t *testing.T) {
		sess, _ := session(t, openPrivateModified)
		err := junos.OpenConfiguration{Private: true}.Exec(context.Background(), sess)
		be.True(t, errors.Is(err, netconf.ErrOperationFailed))
		be.In(t, "shared configuration database modified", err.Error())
	})

	t.Run("somebody else has the lock", func(t *testing.T) {
		sess, _ := session(t, openPrivateLocked)
		be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(context.Background(), sess))
	})

	// Before 18.2R1 Junos warned when a private copy was opened (Juniper's
	// documentation; not captured).
	t.Run("a warning is not a failure", func(t *testing.T) {
		sess, _ := session(t, `<rpc-error><error-severity>warning</error-severity><error-message>
uncommitted changes will be discarded on exit
</error-message></rpc-error>`)
		be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(context.Background(), sess))
	})
}

// A whole change against a device that takes it: what goes out, in order.
func TestChangeWorkflow(t *testing.T) {
	sess, ops := device(t, func(op string) string {
		switch {
		case strings.HasPrefix(op, "<lock"), strings.HasPrefix(op, "<unlock"):
			return `<ok/>`
		case strings.HasPrefix(op, "<load-configuration"):
			return loadOK
		case strings.HasPrefix(op, "<get-configuration"):
			return "<configuration-information>\n<configuration-output>\n[edit system ntp]\n+   server 192.0.2.1;\n</configuration-output>\n</configuration-information>\n"
		default:
			return commitCheckOK
		}
	})
	ctx := context.Background()

	be.NilErr(t, rpc.Lock{Target: rpc.Candidate}.Exec(ctx, sess))

	_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1"}.Exec(ctx, sess)
	be.NilErr(t, err)

	diff, err := junos.CompareRollback{}.Exec(ctx, sess)
	be.NilErr(t, err)
	be.Equal(t, "[edit system ntp]\n+   server 192.0.2.1;", diff)

	_, err = junos.CommitConfiguration{Check: true}.Exec(ctx, sess)
	be.NilErr(t, err)
	_, err = junos.CommitConfiguration{Comment: "add ntp server", Confirm: 5 * time.Minute}.Exec(ctx, sess)
	be.NilErr(t, err)
	_, err = junos.CommitConfiguration{}.Exec(ctx, sess)
	be.NilErr(t, err)

	be.NilErr(t, rpc.Unlock{Target: rpc.Candidate}.Exec(ctx, sess))

	be.AllEqual(t, []string{
		`<lock xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target></lock>`,
		`<load-configuration action="set" format="text"><configuration-set>set system ntp server 192.0.2.1</configuration-set></load-configuration>`,
		`<get-configuration compare="rollback" rollback="0" format="text"></get-configuration>`,
		`<commit-configuration><check></check></commit-configuration>`,
		`<commit-configuration><confirmed></confirmed><confirm-timeout>5</confirm-timeout><log>add ntp server</log></commit-configuration>`,
		`<commit-configuration></commit-configuration>`,
		`<unlock xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><target><candidate/></target></unlock>`,
	}, ops())
}

// A session in rfc-compliant mode (set system services netconf
// rfc-compliant), captured from Junos 26.2 (vJunos-router): the elements of
// the protocol come with the nc prefix, <ok/> and the errors inside Junos'
// own result elements included, and Junos names the process that found an
// error inside <error-info>.
func TestRFCCompliant(t *testing.T) {
	const (
		loaded = `<load-configuration-results action="set" format="text">
<nc:ok/>
</load-configuration-results>
`
		deletedNothing = `<load-configuration-results action="set" format="text">
<nc:rpc-error>
<nc:error-severity>warning</nc:error-severity>
<nc:error-message>
statement not found
</nc:error-message>
</nc:rpc-error>
<nc:ok/>
</load-configuration-results>
`
		loadFailed = `<load-configuration-results action="set" format="text">
<nc:rpc-error>
<nc:error-type>protocol</nc:error-type>
<nc:error-tag>operation-failed</nc:error-tag>
<nc:error-severity>error</nc:error-severity>
<nc:error-message>syntax error</nc:error-message>
<nc:error-info>
<nc:bad-element>no-such-statement</nc:bad-element>
</nc:error-info>
</nc:rpc-error>
<nc:ok/>
</load-configuration-results>
`
		committedPrivate = `<load-success/>
<load-success/>
<nc:ok/>
`
		rejected = `<load-success/>
<nc:rpc-error>
<nc:error-type>protocol</nc:error-type>
<nc:error-tag>operation-failed</nc:error-tag>
<nc:error-severity>error</nc:error-severity>
<nc:error-path>
[edit]
</nc:error-path>
<nc:error-info>
<junos:source-daemon>rpd</junos:source-daemon>
<nc:bad-element>
policy-options
</nc:bad-element>
</nc:error-info>
<nc:error-message>
Policy error: netconf-lib-missing prefix-list referenced (in term 1) but not defined
</nc:error-message>
</nc:rpc-error>
<nc:rpc-error>
<nc:error-type>protocol</nc:error-type>
<nc:error-tag>operation-failed</nc:error-tag>
<nc:error-severity>error</nc:error-severity>
<nc:error-message>
configuration check-out failed
</nc:error-message>
</nc:rpc-error>
`
		locked = `<nc:rpc-error>
<nc:error-type>protocol</nc:error-type>
<nc:error-tag>lock-denied</nc:error-tag>
<nc:error-severity>error</nc:error-severity>
<nc:error-message>
configuration database locked by:
  admin terminal  (pid 77080) on since 2026-10-09 09:37:36 UTC
      exclusive 
</nc:error-message>
<nc:error-info>
<nc:session-id>77080</nc:session-id>
</nc:error-info>
</nc:rpc-error>
`
		compared = `<configuration-information compare="rollback" rollback="0" format="text">
<configuration-output>
[edit groups netconf-lib-probe system location]
-    building conflict-a;
+    building conflict-b;
</configuration-output>
</configuration-information>
`
	)

	// the session answers every operation with body
	session := func(t *testing.T, body string) *netconf.Session {
		t.Helper()
		const hello = `<nc:hello xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0">
   <nc:capabilities>
    <nc:capability>urn:ietf:params:netconf:base:1.0</nc:capability>
    <nc:capability>http://xml.juniper.net/netconf/junos/1.0</nc:capability>
  </nc:capabilities>
  <nc:session-id>76668</nc:session-id>
</nc:hello>`
		tr := netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
			return []string{`<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="` +
				netconftest.ExtractMessageID(req) + "\">\n" + body + "</nc:rpc-reply>\n"}
		}))
		sess, err := netconf.Open(context.Background(), tr)
		be.NilErr(t, err)
		return sess
	}
	ctx := context.Background()
	load := junos.LoadConfiguration{Format: junos.FormatSet, Config: "set system location building x"}

	t.Run("loaded", func(t *testing.T) {
		_, err := load.Exec(ctx, session(t, loaded))
		be.NilErr(t, err)
	})

	t.Run("loaded with a warning", func(t *testing.T) {
		reply, err := load.Exec(ctx, session(t, deletedNothing))
		be.NilErr(t, err)
		be.Equal(t, 1, len(reply.Warnings))
		be.Equal(t, "statement not found", reply.Warnings[0].Message)
	})

	t.Run("load failed", func(t *testing.T) {
		_, err := load.Exec(ctx, session(t, loadFailed))
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "netconf error: protocol operation-failed: 'no-such-statement': syntax error", rpcErr.Error())
	})

	t.Run("committed from a private copy", func(t *testing.T) {
		_, err := junos.CommitConfiguration{}.Exec(ctx, session(t, committedPrivate))
		be.NilErr(t, err)
	})

	t.Run("commit rejected", func(t *testing.T) {
		_, err := junos.CommitConfiguration{}.Exec(ctx, session(t, rejected))
		var rpcErrs netconf.RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 2, len(rpcErrs))
		be.Equal(t,
			"netconf error: protocol operation-failed: [edit] 'policy-options': Policy error: netconf-lib-missing prefix-list referenced (in term 1) but not defined",
			rpcErrs[0].Error())
		daemon, ok := rpcErrs[0].Detail("source-daemon")
		be.True(t, ok)
		be.Equal(t, "rpd", daemon)
	})

	t.Run("the lock is somebody else's", func(t *testing.T) {
		err := rpc.Lock{Target: rpc.Candidate}.Exec(ctx, session(t, locked))
		be.True(t, errors.Is(err, netconf.ErrLockDenied))
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		holder, ok := rpcErr.Detail("session-id")
		be.True(t, ok)
		be.Equal(t, "77080", holder)
	})

	t.Run("unlocked", func(t *testing.T) {
		be.NilErr(t, rpc.Unlock{Target: rpc.Candidate}.Exec(ctx, session(t, "<nc:ok/>\n")))
	})

	t.Run("compared", func(t *testing.T) {
		diff, err := junos.CompareRollback{}.Exec(ctx, session(t, compared))
		be.NilErr(t, err)
		be.Equal(t, "[edit groups netconf-lib-probe system location]\n-    building conflict-a;\n+    building conflict-b;", diff)
	})
}
