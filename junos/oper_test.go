package junos_test

import (
	"context"
	"errors"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
)

// Captured from Junos 25.4 (EX4100), which wraps this answer in a routing
// engine item although it is a single switch.  Other platforms do not.
const uptimeWrapped = `<multi-routing-engine-results>

<multi-routing-engine-item>

<re-name>fpc0</re-name>

<system-uptime-information xmlns="http://xml.juniper.net/junos/25.4R1-S1.7/junos">
<current-time>
<date-time junos:seconds="1791408835">2026-10-07 23:33:55 CEST</date-time>
</current-time>
<time-source> NTP CLOCK </time-source>
</system-uptime-information>
</multi-routing-engine-item>

</multi-routing-engine-results>
`

const uptimeBare = `<system-uptime-information xmlns="http://xml.juniper.net/junos/25.4R1-S1.7/junos">
<current-time>
<date-time junos:seconds="1791408835">2026-10-07 23:33:55 CEST</date-time>
</current-time>
<time-source> NTP CLOCK </time-source>
</system-uptime-information>
`

// Captured from Junos 25.4: values padded with line breaks.
const interfacesTerse = `<interface-information xmlns="http://xml.juniper.net/junos/25.4R0/junos-interface" junos:style="terse">
<physical-interface>
<name>
lo0
</name>
<admin-status>
up
</admin-status>
<oper-status>
up
</oper-status>
<logical-interface>
<name>
lo0.0
</name>
<admin-status>
up
</admin-status>
<oper-status>
up
</oper-status>
</logical-interface>
</physical-interface>
</interface-information>
`

func TestCommand(t *testing.T) {
	sess, ops := session(t, `<output>
fpc0:
--------------------------------------------------------------------------
Current time: 2026-10-07 20:46:10 CEST
</output>
`)
	out, err := junos.Command(`show system uptime "<x>"`).Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, "fpc0:\n--------------------------------------------------------------------------\nCurrent time: 2026-10-07 20:46:10 CEST", out)
	be.Equal(t, `<command format="text">show system uptime &#34;&lt;x&gt;&#34;</command>`, ops()[0])
}

func TestCommandMultipleOutputs(t *testing.T) {
	sess, _ := session(t, `<multi-routing-engine-results>
<multi-routing-engine-item>
<re-name>fpc0</re-name>
<output>
first
</output>
</multi-routing-engine-item>
<multi-routing-engine-item>
<re-name>fpc1</re-name>
<output>
second
</output>
</multi-routing-engine-item>
</multi-routing-engine-results>
`)
	out, err := junos.Command("show version").Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, "first\nsecond", out)
}

func TestCommandUnexpectedReply(t *testing.T) {
	sess, _ := session(t, `<software-information/>`)
	_, err := junos.Command("show version").Exec(context.Background(), sess)
	be.Nonzero(t, err)
}

func TestCommandPrintsNothing(t *testing.T) {
	sess, _ := session(t, ``)
	out, err := junos.Command("show system alarms").Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, "", out)
}

// Captured from Junos 25.4: a text request that fails is answered without an
// rpc-error and without <output>, the error is plain text in the reply.  It
// must not read as a command that printed nothing.
func TestCommandError(t *testing.T) {
	sess, _ := session(t, "\nerror: device nonexistent-0/0/0 not found\n")

	out, err := junos.Command("show interfaces nonexistent-0/0/0").Exec(context.Background(), sess)
	be.Nonzero(t, err)
	be.In(t, "device nonexistent-0/0/0 not found", err.Error())
	be.Equal(t, "", out)
}

func TestCommandJSON(t *testing.T) {
	// captured from Junos 25.4, shortened
	const doc = `{
    "system-information" : [
    {
        "host-name" : [
        {
            "data" : "switch1"
        }
        ]
    }
    ]
}`
	sess, ops := session(t, doc+"\n")

	got, err := junos.Command("show system information").JSON(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, doc, string(got))
	be.Equal(t, `<command format="json">show system information</command>`, ops()[0])
}

func TestJSONNotADocument(t *testing.T) {
	for _, body := range []string{``, `<output>text</output>`, "\nerror: device not found\n"} {
		sess, _ := session(t, body)
		_, err := junos.Command("show x").JSON(context.Background(), sess)
		be.Nonzero(t, err)
	}
}

func TestCommandXML(t *testing.T) {
	sess, ops := session(t, uptimeWrapped)

	reply, err := junos.Command("show system uptime").XML(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, `<command>show system uptime</command>`, ops()[0])

	var uptime struct {
		Source string `xml:"time-source"`
	}
	be.NilErr(t, junos.Decode(reply, "system-uptime-information", &uptime))
	be.Equal(t, "NTP CLOCK", uptime.Source)
}

// The same request is answered inside a routing engine wrapper on one
// platform and without it on the next.  Decode reads both.
func TestDecode(t *testing.T) {
	type uptime struct {
		Now struct {
			Seconds int64  `xml:"seconds,attr"`
			Text    string `xml:",chardata"`
		} `xml:"current-time>date-time"`
		Source string `xml:"time-source"`
	}

	for name, body := range map[string]string{"wrapped": uptimeWrapped, "bare": uptimeBare} {
		t.Run(name, func(t *testing.T) {
			sess, _ := session(t, body)
			reply, err := sess.ExecReply(context.Background(), "<get-system-uptime-information/>")
			be.NilErr(t, err)

			var got uptime
			be.NilErr(t, junos.Decode(reply, "system-uptime-information", &got))
			be.Equal(t, int64(1791408835), got.Now.Seconds)
			be.Equal(t, "2026-10-07 23:33:55 CEST", got.Now.Text)
			be.Equal(t, "NTP CLOCK", got.Source)
		})
	}

	t.Run("every match into a slice, values trimmed", func(t *testing.T) {
		sess, _ := session(t, interfacesTerse)
		reply, err := sess.ExecReply(context.Background(), "<get-interface-information><terse/></get-interface-information>")
		be.NilErr(t, err)

		type iface struct {
			Name   string   `xml:"name"`
			Oper   string   `xml:"oper-status"`
			Raw    []byte   `xml:",innerxml"`
			Units  []*iface `xml:"logical-interface"`
			hidden string
		}
		var got []iface
		be.NilErr(t, junos.Decode(reply, "physical-interface", &got))

		be.Equal(t, 1, len(got))
		be.Equal(t, "lo0", got[0].Name)
		be.Equal(t, "up", got[0].Oper)
		be.Equal(t, 1, len(got[0].Units))
		be.Equal(t, "lo0.0", got[0].Units[0].Name)
		// bytes are kept as received
		be.True(t, got[0].Raw[0] == '\n')
		be.Equal(t, "", got[0].hidden)
	})

	t.Run("per routing engine", func(t *testing.T) {
		sess, _ := session(t, uptimeWrapped)
		reply, err := sess.ExecReply(context.Background(), "<get-system-uptime-information/>")
		be.NilErr(t, err)

		var items []struct {
			RE     string `xml:"re-name"`
			Uptime uptime `xml:"system-uptime-information"`
		}
		be.NilErr(t, junos.Decode(reply, "multi-routing-engine-item", &items))
		be.Equal(t, 1, len(items))
		be.Equal(t, "fpc0", items[0].RE)
		be.Equal(t, "NTP CLOCK", items[0].Uptime.Source)
	})

	t.Run("not in the reply", func(t *testing.T) {
		sess, _ := session(t, uptimeBare)
		reply, err := sess.ExecReply(context.Background(), "<get-system-uptime-information/>")
		be.NilErr(t, err)

		var one uptime
		be.True(t, errors.Is(junos.Decode(reply, "bgp-information", &one), netconf.ErrElementNotFound))
		var many []uptime
		be.True(t, errors.Is(junos.Decode(reply, "bgp-information", &many), netconf.ErrElementNotFound))
	})

	t.Run("needs a pointer", func(t *testing.T) {
		be.Nonzero(t, junos.Decode(&netconf.Reply{}, "x", uptime{}))
		be.Nonzero(t, junos.Decode(&netconf.Reply{}, "x", nil))
	})
}

func TestGetSystemInformation(t *testing.T) {
	// captured from Junos 25.4, serial number replaced
	const info = `<system-information>
<host-name>switch1</host-name>
<hardware-model>ex4100-f-12p</hardware-model>
<os-name>junos-ex</os-name>
<os-version>25.4R1-S1.7</os-version>
<serial-number>XX0000000000</serial-number>
<configured-host-name>switch1</configured-host-name>
</system-information>
`
	sess, ops := session(t, info)

	got, err := junos.GetSystemInformation{}.Exec(context.Background(), sess)
	be.NilErr(t, err)
	be.Equal(t, junos.SystemInformation{
		HostName:     "switch1",
		Model:        "ex4100-f-12p",
		OSName:       "junos-ex",
		Version:      "25.4R1-S1.7",
		SerialNumber: "XX0000000000",
	}, *got)
	be.Equal(t, `<get-system-information></get-system-information>`, ops()[0])

	t.Run("answered with something else", func(t *testing.T) {
		sess, _ := session(t, `<ok/>`)
		_, err := junos.GetSystemInformation{}.Exec(context.Background(), sess)
		be.True(t, errors.Is(err, netconf.ErrElementNotFound))
	})
}

func TestIsJunos(t *testing.T) {
	sess, _ := session(t, ``)
	be.True(t, junos.IsJunos(sess))
}
