package junos_test

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/junos"
)

// The replies in this file were captured from Junos 25.4 (EX4100).

const replyOpen = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos" message-id="%ID%" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` + "\n"

// hello is what the devices of these tests say: enough for a session with a
// Junos device.
const hello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability><capability>http://xml.juniper.net/netconf/junos/1.0</capability></capabilities><session-id>1</session-id></hello>`

// session returns a session to a device that answers every operation with
// body, and a function returning the operations it was sent.
func session(t *testing.T, body string) (sess *netconf.Session, ops func() []string) {
	t.Helper()
	return device(t, func(string) string { return body })
}

// device returns a session to a device that answers each operation with
// what answer returns for it, and a function returning the operations it was
// sent.
func device(t *testing.T, answer func(op string) string) (sess *netconf.Session, ops func() []string) {
	t.Helper()

	tr := netconftest.NewTransport(t, netconftest.Hello(hello, func(req string) []string {
		var rpc struct {
			Inner string `xml:",innerxml"`
		}
		_ = xml.Unmarshal([]byte(req), &rpc)

		open := strings.Replace(replyOpen, "%ID%", netconftest.ExtractMessageID(req), 1)
		return []string{open + answer(rpc.Inner) + "</rpc-reply>\n"}
	}))

	sess, err := netconf.Open(context.Background(), tr)
	be.NilErr(t, err)

	ops = func() []string {
		outputs := tr.Outputs()[1:]
		ops := make([]string, 0, len(outputs))
		for _, out := range outputs {
			var rpc struct {
				Inner string `xml:",innerxml"`
			}
			be.NilErr(t, xml.Unmarshal([]byte(out), &rpc))
			ops = append(ops, rpc.Inner)
		}
		return ops
	}
	return sess, ops
}

func TestGetConfiguration(t *testing.T) {
	const filter = `<configuration><system><host-name/></system></configuration>`

	tests := []struct {
		name   string
		op     junos.GetConfiguration
		wantOp string
		reply  string
		want   string
	}{
		{
			name:   "xml",
			op:     junos.GetConfiguration{Filter: filter},
			wantOp: `<get-configuration database="committed">` + filter + `</get-configuration>`,
			reply: `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1789149961">
    <system>
        <host-name>switch1</host-name>
    </system>
</configuration>
`,
			want: `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1789149961">
    <system>
        <host-name>switch1</host-name>
    </system>
</configuration>`,
		},
		{
			name:   "text",
			op:     junos.GetConfiguration{Format: junos.FormatText, Filter: []byte(filter), Inherit: true},
			wantOp: `<get-configuration format="text" database="committed" inherit="inherit">` + filter + `</get-configuration>`,
			reply: `<configuration-text xmlns="http://xml.juniper.net/xnm/1.1/xnm">
## Last changed: 2026-09-11 20:06:01 CEST
system {
    host-name switch1;
    login { message "a &lt; b"; }
}
</configuration-text>
`,
			want: `## Last changed: 2026-09-11 20:06:01 CEST
system {
    host-name switch1;
    login { message "a < b"; }
}`,
		},
		{
			name:   "set from the candidate",
			op:     junos.GetConfiguration{Format: junos.FormatSet, Candidate: true},
			wantOp: `<get-configuration format="set"></get-configuration>`,
			reply: `<configuration-set xmlns="http://xml.juniper.net/xnm/1.1/xnm">
set system host-name switch1
set system services netconf ssh port 830
</configuration-set>
`,
			want: "set system host-name switch1\nset system services netconf ssh port 830",
		},
		{
			name:   "json",
			op:     junos.GetConfiguration{Format: junos.FormatJSON},
			wantOp: `<get-configuration format="json" database="committed"></get-configuration>`,
			reply: `{
    "configuration" : {
        "system" : {
            "host-name" : "switch1"
        }
    }
}
`,
			want: `{
    "configuration" : {
        "system" : {
            "host-name" : "switch1"
        }
    }
}`,
		},
		{
			// As Junos 26.2R1.7 (vJunos-router) sent it in a lab:
			// everything on one line.  That request had a filter for the
			// NETCONF service, which is why the reply is this short.
			name:   "xml without indentation",
			op:     junos.GetConfiguration{Format: junos.FormatXMLMinified},
			wantOp: `<get-configuration format="xml-minified" database="committed"></get-configuration>`,
			reply:  `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:commit-seconds="1791703578" junos:commit-localtime="2026-10-11 07:26:18 UTC" junos:commit-user="operator"><system><services><netconf><ssh></ssh></netconf></services></system></configuration>` + "\n",
			want:   `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:commit-seconds="1791703578" junos:commit-localtime="2026-10-11 07:26:18 UTC" junos:commit-user="operator"><system><services><netconf><ssh></ssh></netconf></services></system></configuration>`,
		},
		{
			// The same in JSON, from the same device.  What a filter leads
			// down to is written out with its indentation, and what is
			// below it is not.
			name:   "json without indentation",
			op:     junos.GetConfiguration{Format: junos.FormatJSONMinified, Filter: filter},
			wantOp: `<get-configuration format="json-minified" database="committed">` + filter + `</get-configuration>`,
			reply: `{
    "configuration" : {
        "@" : {
            "xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", 
            "junos:commit-seconds" : "1791703578", 
            "junos:commit-localtime" : "2026-10-11 07:26:18 UTC", 
            "junos:commit-user" : "operator"
        }, 
        "system" : {
            "services" : {
"netconf":{"ssh":{}}}}}}
`,
			want: `{
    "configuration" : {
        "@" : {
            "xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", 
            "junos:commit-seconds" : "1791703578", 
            "junos:commit-localtime" : "2026-10-11 07:26:18 UTC", 
            "junos:commit-user" : "operator"
        }, 
        "system" : {
            "services" : {
"netconf":{"ssh":{}}}}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess, ops := session(t, tc.reply)

			got, err := tc.op.Exec(context.Background(), sess)
			be.NilErr(t, err)
			be.Equal(t, tc.want, string(got))
			be.Equal(t, tc.wantOp, ops()[0])
		})
	}
}

// A release that does not know a format does not refuse it: it answers in
// XML with indentation, as Junos 26.2 does for a format that is made up.
// The lab has no release that lacks the two formats without indentation, so
// the replies here are written by hand: the XML such a release would send.
func TestGetConfigurationOtherFormat(t *testing.T) {
	const indented = `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:commit-seconds="1791703578">
    <system>
        <services>
            <netconf>
                <ssh>
                </ssh>
            </netconf>
        </services>
    </system>
</configuration>
`
	for _, format := range []junos.Format{junos.FormatXMLMinified, junos.FormatJSONMinified, junos.FormatJSON, junos.FormatText, junos.FormatSet} {
		t.Run(string(format), func(t *testing.T) {
			sess, _ := session(t, indented)
			got, err := junos.GetConfiguration{Format: format}.Exec(context.Background(), sess)
			be.Nonzero(t, err)
			be.Zero(t, got)
			be.In(t, "get-configuration: ", err.Error())
		})
	}

	// the two say what happened
	sess, _ := session(t, indented)
	_, err := junos.GetConfiguration{Format: junos.FormatXMLMinified}.Exec(context.Background(), sess)
	be.In(t, `the device answered in another format than "xml-minified"`, err.Error())
	sess, _ = session(t, indented)
	_, err = junos.GetConfiguration{Format: junos.FormatJSONMinified}.Exec(context.Background(), sess)
	be.In(t, `the device answered in another format than "json-minified"`, err.Error())

	// a value with a line break in it is no indentation
	sess, _ = session(t, `<configuration><system><login><message>two`+"\n"+`lines</message></login></system></configuration>`)
	got, err := junos.GetConfiguration{Format: junos.FormatXMLMinified}.Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.In(t, "two\nlines", string(got))

	// an empty configuration has nothing to indent
	sess, _ = session(t, `<configuration/>`)
	_, err = junos.GetConfiguration{Format: junos.FormatXMLMinified}.Exec(context.Background(), sess)
	be.NilErr(t, err)

	// the formats are for reading: a load knows neither
	for _, format := range []junos.Format{junos.FormatXMLMinified, junos.FormatJSONMinified} {
		_, err := xml.Marshal(junos.LoadConfiguration{Format: format, Config: "x"})
		be.Nonzero(t, err)
	}
}

// Over chunked framing Junos 26.2 starts a reply in a format without
// indentation and never finishes it, and the session is lost to the wait.
// The two formats are refused on such a session, before anything is sent.
func TestGetConfigurationMinifiedOverChunks(t *testing.T) {
	const hello11 = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability><capability>urn:ietf:params:netconf:base:1.1</capability></capabilities><session-id>4711</session-id></hello>`

	// a session that speaks chunks, and one on the same device whose client
	// offered base:1.0 only
	open := func(t *testing.T, opts ...netconf.SessionOption) (*netconf.Session, *netconftest.Transport) {
		t.Helper()
		tr := netconftest.NewTransport(t, netconftest.Hello(hello11, func(req string) []string {
			open := strings.Replace(replyOpen, "%ID%", netconftest.ExtractMessageID(req), 1)
			return []string{open + `<configuration><system/></configuration>` + "</rpc-reply>\n"}
		}))
		sess, err := netconf.Open(context.Background(), tr, opts...)
		be.NilErr(t, err)
		return sess, tr
	}

	for _, format := range []junos.Format{junos.FormatXMLMinified, junos.FormatJSONMinified} {
		t.Run(string(format), func(t *testing.T) {
			sess, tr := open(t)
			be.True(t, tr.Upgraded())

			_, err := junos.GetConfiguration{Format: format}.Exec(context.Background(), sess)
			be.Nonzero(t, err)
			be.In(t, `get-configuration: format "`+string(format)+`" is not asked for over chunked framing`, err.Error())

			// nothing but the hello went out, and the session is as it was
			be.Equal(t, 1, len(tr.Outputs()))
			_, err = junos.GetConfiguration{}.Exec(context.Background(), sess)
			be.NilErr(t, err)
		})
	}

	// over end-of-message framing the device answers both
	sess, tr := open(t, netconf.WithCapability(netconf.CapNetConf10))
	be.False(t, tr.Upgraded())
	got, err := junos.GetConfiguration{Format: junos.FormatXMLMinified}.Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, `<configuration><system/></configuration>`, string(got))
}

func TestGetConfigurationStructFilter(t *testing.T) {
	type configuration struct {
		XMLName xml.Name  `xml:"configuration"`
		System  *struct{} `xml:"system"`
	}

	sess, ops := session(t, `<configuration/>`)
	_, err := junos.GetConfiguration{Filter: configuration{System: &struct{}{}}}.Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, `<get-configuration database="committed"><configuration><system></system></configuration></get-configuration>`, ops()[0])
}

func TestGetConfigurationErrors(t *testing.T) {
	t.Run("unknown format is not sent", func(t *testing.T) {
		sess, ops := session(t, `<configuration/>`)
		_, err := junos.GetConfiguration{Format: "yaml"}.Exec(context.Background(), sess)
		be.Nonzero(t, err)
		be.Equal(t, 0, len(ops()))
	})

	t.Run("warning is not part of the configuration", func(t *testing.T) {
		sess, _ := session(t, `<rpc-error><error-severity>warning</error-severity><error-message>statement not found</error-message></rpc-error>
<configuration><system/></configuration>
`)
		got, err := junos.GetConfiguration{}.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, `<configuration><system/></configuration>`, string(got))
	})

	t.Run("missing wrapper", func(t *testing.T) {
		sess, _ := session(t, `<configuration/>`)
		_, err := junos.GetConfiguration{Format: junos.FormatText}.Exec(context.Background(), sess)
		be.Nonzero(t, err)
	})

	t.Run("rpc error", func(t *testing.T) {
		sess, _ := session(t, `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>syntax error, expecting &lt;/configuration&gt;</error-message>
<error-info>
<bad-element>bogus-stanza</bad-element>
</error-info>
</rpc-error>
`)
		_, err := junos.GetConfiguration{Format: junos.FormatText}.Exec(context.Background(), sess)
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "syntax error, expecting </configuration>", rpcErr.Message)
	})
}

func TestCompareRollback(t *testing.T) {
	t.Run("differences", func(t *testing.T) {
		sess, ops := session(t, `<configuration-information>
<configuration-output>
[edit chassis alarm]
!     active: management-ethernet { ... }
</configuration-output>
</configuration-information>
`)
		diff, err := junos.CompareRollback{Rollback: 1}.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, "[edit chassis alarm]\n!     active: management-ethernet { ... }", diff)
		be.Equal(t, `<get-configuration compare="rollback" rollback="1" format="text"></get-configuration>`, ops()[0])
	})

	t.Run("no differences", func(t *testing.T) {
		sess, _ := session(t, `<configuration-information>
<configuration-output>
</configuration-output>
</configuration-information>
`)
		diff, err := junos.CompareRollback{}.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, "", diff)
	})

	t.Run("unexpected reply is not an empty diff", func(t *testing.T) {
		sess, _ := session(t, `<ok/>`)
		_, err := junos.CompareRollback{Rollback: 1}.Exec(context.Background(), sess)
		be.Nonzero(t, err)
	})

	t.Run("negative rollback", func(t *testing.T) {
		sess, _ := session(t, ``)
		_, err := junos.CompareRollback{Rollback: -1}.Exec(context.Background(), sess)
		be.Nonzero(t, err)
	})

	t.Run("missing rollback", func(t *testing.T) {
		// Junos pads the message with line breaks; the error text must not.
		sess, _ := session(t, `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Cannot open configuration file: /var/db/config/juniper.conf.99 ; No such file or directory
</error-message>
</rpc-error>
`)
		_, err := junos.CompareRollback{Rollback: 99}.Exec(context.Background(), sess)
		be.Nonzero(t, err)
		be.Equal(t, "compare rollback: netconf error: protocol operation-failed: Cannot open configuration file: /var/db/config/juniper.conf.99 ; No such file or directory", err.Error())
	})
}

// Junos returns partial data together with an rpc-error; the error wins.
func TestDataWithRPCError(t *testing.T) {
	sess, _ := session(t, `<interface-information xmlns="http://xml.juniper.net/junos/25.4R0/junos-interface" junos:style="terse">
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
`)
	_, err := sess.ExecReply(context.Background(), `<get-interface-information><interface-name>nonexistent-0/0/0</interface-name></get-interface-information>`)
	var rpcErr netconf.RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.In(t, "device nonexistent-0/0/0 not found", err.Error())
}

func TestGetConfigurationJSONMissing(t *testing.T) {
	sess, _ := session(t, `<ok/>`)
	_, err := junos.GetConfiguration{Format: junos.FormatJSON}.Exec(context.Background(), sess)
	be.Nonzero(t, err)
}

// Text that is not ASCII, as Junos 26.2 (vJunos-router) sent it back: a
// character reference for every byte above 127.  The statement was loaded as
// "xåx" in UTF-8, and the commit had the comment "netconf-lib probe: Tromsø
// – Ålesund".  The session puts such text back together, so every way of
// reading a reply has it.
const (
	textConfigXML = `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1791538182" junos:changed-localtime="2026-10-09 09:29:42 UTC">
    <groups>
        <name>netconf-lib-probe</name>
        <system>
            <location>
                <building>x&#195;&#165;x</building>
                <floor>7</floor>
            </location>
        </system>
    </groups>
</configuration>
`

	textConfigText = `<configuration-text xmlns="http://xml.juniper.net/xnm/1.1/xnm">
## Last changed: 2026-10-09 09:29:42 UTC
groups {
    netconf-lib-probe {
        system {
            location {
                building "x&#195;&#165;x";
                floor 7;
            }
        }
    }
}
</configuration-text>
`

	textConfigSet = `<configuration-set xmlns="http://xml.juniper.net/xnm/1.1/xnm">
set groups netconf-lib-probe system location building "x&#195;&#165;x"
set groups netconf-lib-probe system location floor 7
</configuration-set>
`

	textConfigJSON = `{
    "configuration" : {
        "@" : {
            "xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", 
            "junos:changed-seconds" : "1791538182", 
            "junos:changed-localtime" : "2026-10-09 09:29:42 UTC"
        }, 
        "groups" : [
        {
            "name" : "netconf-lib-probe", 
            "system" : {
                "location" : {
                    "building" : "x&#195;&#165;x", 
                    "floor" : 7
                }
            }
        }
        ]
    }
}
`

	textDiff = `<configuration-information>
<configuration-output>
[edit groups netconf-lib-probe system location]
-    building synchronize;
+    building "x&#195;&#165;x";
</configuration-output>
</configuration-information>
`

	// The two newest of the commits.
	textCommits = `<commit-information>
<commit-history>
<sequence-number>0</sequence-number>
<user>admin</user>
<client>netconf</client>
<date-time junos:seconds="1791538288">2026-10-09 09:31:28 UTC</date-time>
<log>netconf-lib probe: Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund</log>
</commit-history>
<commit-history>
<sequence-number>1</sequence-number>
<user>admin</user>
<client>netconf</client>
<date-time junos:seconds="1791538137">2026-10-09 09:28:57 UTC</date-time>
</commit-history>
</commit-information>
`

	textCommitsOutput = `<output>
0   2026-10-09 09:31:28 UTC by admin via netconf
    netconf-lib probe: Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund
1   2026-10-09 09:28:57 UTC by admin via netconf
</output>
`

	// The answer to "show interfaces ø", in XML.
	textCommandError = `<interface-information xmlns="http://xml.juniper.net/junos/26.2R0/junos-interface" junos:style="normal">
</interface-information>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<source-daemon>
ifinfo
</source-daemon>
<error-message>
device &#195;&#184; not found
</error-message>
</rpc-error>
`

	// The same as text.  This is the one place where Junos sends the bytes
	// themselves.
	textCommandErrorText = "\nerror: device ø not found\n"
)

func TestTextNotASCII(t *testing.T) {
	ctx := context.Background()
	filter := `<configuration><groups><name>netconf-lib-probe</name></groups></configuration>`

	for format, tc := range map[junos.Format]struct{ reply, want string }{
		junos.FormatXML:  {textConfigXML, `<building>xåx</building>`},
		junos.FormatText: {textConfigText, `building "xåx";`},
		junos.FormatSet:  {textConfigSet, `set groups netconf-lib-probe system location building "xåx"`},
		junos.FormatJSON: {textConfigJSON, `"building" : "xåx"`},
	} {
		t.Run("configuration as "+string(format), func(t *testing.T) {
			sess, _ := session(t, tc.reply)
			got, err := junos.GetConfiguration{Format: format, Candidate: true, Filter: filter}.Exec(ctx, sess)
			be.NilErr(t, err)
			be.In(t, tc.want, string(got))
		})
	}

	t.Run("a diff", func(t *testing.T) {
		sess, _ := session(t, textDiff)
		diff, err := junos.CompareRollback{}.Exec(ctx, sess)
		be.NilErr(t, err)
		be.In(t, `+    building "xåx";`, diff)
	})

	t.Run("the result of a request", func(t *testing.T) {
		sess, _ := session(t, textCommits)
		reply, err := sess.ExecReply(ctx, "<get-commit-information/>")
		be.NilErr(t, err)

		var commits []struct {
			Log string `xml:"log"`
		}
		be.NilErr(t, junos.Decode(reply, "commit-history", &commits))
		be.Equal(t, 2, len(commits))
		be.Equal(t, "netconf-lib probe: Tromsø – Ålesund", commits[0].Log)
	})

	t.Run("the output of a command", func(t *testing.T) {
		sess, _ := session(t, textCommitsOutput)
		out, err := junos.Command("show system commit").Exec(ctx, sess)
		be.NilErr(t, err)
		be.In(t, "    netconf-lib probe: Tromsø – Ålesund\n", out)
	})

	t.Run("the error of a command", func(t *testing.T) {
		sess, _ := session(t, textCommandError)
		_, err := junos.Command("show interfaces ø").XML(ctx, sess)
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "device ø not found", rpcErr.Message)

		sess, _ = session(t, textCommandErrorText)
		_, err = junos.Command("show interfaces ø").Exec(ctx, sess)
		be.Nonzero(t, err)
		be.In(t, "error: device ø not found", err.Error())
	})
}
