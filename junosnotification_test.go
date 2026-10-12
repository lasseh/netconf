package netconf

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

// Notifications captured from Junos 26.2R1.7 on a vJunos-router with
// "system services netconf notification" set.  Its NETCONF stream is the
// system log: every event comes twice, in the two forms below.  This one is
// a commit with the comment "netconf-lib notification probe: Tromsø –
// Ålesund", whose letters Junos sends a byte at a time, as it does in a
// reply.  The host name is not the one of the device.
const (
	junosCommitNotification = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
<eventTime>2026-10-10T13:52:01+02:00</eventTime>
<syslog>
<junos>
  <events>
    <event>
      <id>UI_COMMIT</id>
      <type>2</type>
      <priority>189</priority>
      <facility>23</facility>
      <timestamp>
        <seconds>1791633121</seconds>
        <microseconds>978851</microseconds>
      </timestamp>
      <pid>68104</pid>
      <message>UI_COMMIT: User 'admin' requested 'commit' operation (comment: netconf-lib notification probe: Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund)</message>
      <daemon>mgd</daemon>
      <hostname>router1</hostname>
      <attributes>
        <key>username</key>
        <value>admin</value>
      </attributes>
      <attributes>
        <key>command</key>
        <value>commit</value>
      </attributes>
      <attributes>
        <key>message</key>
        <value>netconf-lib notification probe: Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund</value>
      </attributes>
    </event>
  </events>
</junos>
</syslog>
</notification>`

	junosCommitStateNotification = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
<eventTime>2026-10-10T13:52:01+02:00</eventTime>
<state-system-syslog>
<state>
  <system>
    <syslog>
      <messages>
        <message>
          <timestamp>1791633121978851</timestamp>
          <id>UI_COMMIT</id>
          <severity>NOTICE</severity>
          <facility>LOCAL7</facility>
          <priority>189</priority>
          <pid>68104</pid>
          <log-message>UI_COMMIT: User 'admin' requested 'commit' operation (comment: netconf-lib notification probe: Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund)</log-message>
          <application>mgd</application>
          <structured-data>
            <parameters>
              <parameter>
                <name>username</name>
                <value>admin</value>
              </parameter>
              <parameter>
                <name>command</name>
                <value>commit</value>
              </parameter>
              <parameter>
                <name>message</name>
                <value>netconf-lib notification probe: Troms&#195;&#184; &#226;&#128;&#147; &#195;&#133;lesund</value>
              </parameter>
            </parameters>
          </structured-data>
        </message>
      </messages>
    </syslog>
  </system>
</state>
</state-system-syslog>
</notification>`
)

// The commit that is in both notifications.
const junosCommitComment = "netconf-lib notification probe: Tromsø – Ålesund"

// A notification tells when something happened and, in its content, what.
// The content is whatever the device sends: the handler looks at its name
// and decodes it into what fits.
func TestNotificationContent(t *testing.T) {
	// as a session with a Junos device hands them over: what the handler
	// decodes the message into
	receive := func(t *testing.T, raw string, v any) {
		t.Helper()

		decoded := make(chan error, 1)
		sess := wireSessionAfter(t, junosHello,
			func(msgID, _ string) string { return raw + "\n]]>]]>\n" + framed(msgID, "<ok/>") },
			WithNotifHandler(func(_ context.Context, msg *Message) { decoded <- msg.Decode(v) }),
		)
		be.NilErr(t, sess.Exec(testCtx(t), "<create-subscription/>", nil))

		select {
		case err := <-decoded:
			be.NilErr(t, err)
		case <-testCtx(t).Done():
			t.Fatal("no notification")
		}
	}
	received := func(t *testing.T, raw string) Notification {
		t.Helper()

		var notif Notification
		receive(t, raw, &notif)
		return notif
	}
	at := time.Date(2026, 10, 10, 13, 52, 1, 0, time.FixedZone("", 2*60*60))

	t.Run("a junos system log event", func(t *testing.T) {
		notif := received(t, junosCommitNotification)
		be.Equal(t, xml.Name{Space: NotificationNamespace, Local: "notification"}, notif.XMLName)
		be.True(t, at.Equal(notif.EventTime))
		be.Equal(t, xml.Name{Space: NotificationNamespace, Local: "syslog"}, notif.Content.XMLName)

		var event struct {
			ID         string `xml:"junos>events>event>id"`
			Daemon     string `xml:"junos>events>event>daemon"`
			Message    string `xml:"junos>events>event>message"`
			Attributes []struct {
				Key   string `xml:"key"`
				Value string `xml:"value"`
			} `xml:"junos>events>event>attributes"`
		}
		be.NilErr(t, notif.Content.Decode(&event))
		be.Equal(t, "UI_COMMIT", event.ID)
		be.Equal(t, "mgd", event.Daemon)
		be.Equal(t, "UI_COMMIT: User 'admin' requested 'commit' operation (comment: "+junosCommitComment+")", event.Message)
		be.Equal(t, 3, len(event.Attributes))
		be.Equal(t, "message", event.Attributes[2].Key)
		be.Equal(t, junosCommitComment, event.Attributes[2].Value)
	})

	t.Run("the same event as state", func(t *testing.T) {
		notif := received(t, junosCommitStateNotification)
		be.True(t, at.Equal(notif.EventTime))
		be.Equal(t, "state-system-syslog", notif.Content.XMLName.Local)

		var event struct {
			ID      string `xml:"state>system>syslog>messages>message>id"`
			Message string `xml:"state>system>syslog>messages>message>log-message"`
		}
		be.NilErr(t, notif.Content.Decode(&event))
		be.Equal(t, "UI_COMMIT", event.ID)
		be.In(t, junosCommitComment, event.Message)
	})

	// made up after RFC 6470: Junos 26.2 sends no notification of that kind
	t.Run("a config change of RFC 6470", func(t *testing.T) {
		const raw = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
  <eventTime>2026-10-10T11:52:01Z</eventTime>
  <netconf-config-change xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-notifications">
    <changed-by>
      <username>admin</username>
      <session-id>68104</session-id>
    </changed-by>
    <datastore>running</datastore>
  </netconf-config-change>
</notification>`

		var notif Notification
		be.NilErr(t, xml.Unmarshal([]byte(raw), &notif))
		be.True(t, at.Equal(notif.EventTime))
		be.Equal(t, xml.Name{Space: "urn:ietf:params:xml:ns:yang:ietf-netconf-notifications", Local: "netconf-config-change"}, notif.Content.XMLName)

		// the element of the content is the one a struct can ask for
		var change struct {
			XMLName   xml.Name `xml:"urn:ietf:params:xml:ns:yang:ietf-netconf-notifications netconf-config-change"`
			User      string   `xml:"changed-by>username"`
			Datastore string   `xml:"datastore"`
		}
		be.NilErr(t, notif.Content.Decode(&change))
		be.Equal(t, "admin", change.User)
		be.Equal(t, "running", change.Datastore)

		var other struct {
			XMLName xml.Name `xml:"urn:example:other netconf-config-change"`
		}
		be.Nonzero(t, notif.Content.Decode(&other))
	})

	// made up: what the notification declares counts for its content, and
	// the attributes of the content are there to read
	t.Run("attributes and namespaces", func(t *testing.T) {
		const raw = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0" xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos">
  <eventTime>2026-10-10T11:52:01Z</eventTime>
  <p:event xmlns:p="urn:example:events" junos:seconds="1791633121" kind="commit">
    <p:by junos:style="brief">admin</p:by>
    <plain>text</plain>
  </p:event>
</notification>`

		notif := received(t, raw)
		be.Equal(t, xml.Name{Space: "urn:example:events", Local: "event"}, notif.Content.XMLName)
		be.In(t, "<plain>text</plain>", string(notif.Content.Inner))

		want := map[xml.Name]string{
			{Space: "xmlns", Local: "p"}: "urn:example:events",
			{Space: "http://xml.juniper.net/junos/26.2R1.7/junos", Local: "seconds"}: "1791633121",
			{Local: "kind"}: "commit",
		}
		be.Equal(t, len(want), len(notif.Content.Attr))
		for _, a := range notif.Content.Attr {
			be.Equal(t, want[a.Name], a.Value)
		}

		var event struct {
			XMLName xml.Name `xml:"urn:example:events event"`
			Seconds int64    `xml:"http://xml.juniper.net/junos/26.2R1.7/junos seconds,attr"`
			Kind    string   `xml:"kind,attr"`
			By      struct {
				Style string `xml:"http://xml.juniper.net/junos/26.2R1.7/junos style,attr"`
				Name  string `xml:",chardata"`
			} `xml:"urn:example:events by"`
			// an element without a prefix is in the namespace of the notification
			Plain string `xml:"urn:ietf:params:xml:ns:netconf:notification:1.0 plain"`
		}
		be.NilErr(t, notif.Content.Decode(&event))
		be.Equal(t, int64(1791633121), event.Seconds)
		be.Equal(t, "commit", event.Kind)
		be.Equal(t, "brief", event.By.Style)
		be.Equal(t, "admin", event.By.Name)
		be.Equal(t, "text", event.Plain)
	})

	// a content that is no part of a notification: decoded by itself, as a
	// field of somebody's own struct
	t.Run("a content by itself", func(t *testing.T) {
		var content NotificationContent
		be.NilErr(t, xml.Unmarshal([]byte(`<event xmlns="urn:example:events" kind="commit"><by>admin</by></event>`), &content))

		var event struct {
			XMLName xml.Name `xml:"urn:example:events event"`
			By      string   `xml:"by"`
		}
		be.NilErr(t, content.Decode(&event))
		be.Equal(t, "admin", event.By)

		be.NilErr(t, NotificationContent{XMLName: xml.Name{Local: "event"}, Inner: []byte("<by>hand</by>")}.Decode(&struct{}{}))

		// the prefixes the element declares are those of what is in it
		content = NotificationContent{}
		be.NilErr(t, xml.Unmarshal([]byte(`<p:event xmlns:p="urn:example:events" xmlns:q="urn:example:who&amp;what"><p:by><q:name>admin</q:name></p:by></p:event>`), &content))
		var prefixed struct {
			XMLName xml.Name `xml:"urn:example:events event"`
			By      struct {
				Name string `xml:"urn:example:who&what name"`
			} `xml:"urn:example:events by"`
		}
		be.NilErr(t, content.Decode(&prefixed))
		be.Equal(t, "admin", prefixed.By.Name)

		// a prefix on the element, and another namespace for what has none
		content = NotificationContent{}
		be.NilErr(t, xml.Unmarshal([]byte(`<p:event xmlns:p="urn:example:events" xmlns="urn:example:other"><by>admin</by></p:event>`), &content))
		var other struct {
			XMLName xml.Name `xml:"urn:example:events event"`
			By      string   `xml:"urn:example:other by"`
		}
		be.NilErr(t, content.Decode(&other))
		be.Equal(t, "admin", other.By)
	})

	// A content that encoding/xml decoded, as part of a notification: its
	// element is in the namespace of the notification without saying so,
	// and declares a prefix for that namespace that it does not use.  What
	// it contains is in that namespace too.  A content like this one and an
	// element written with that prefix look the same once they are decoded,
	// so only one of the two can be put together again right.
	t.Run("an inherited namespace and a prefix for it", func(t *testing.T) {
		const raw = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-10T11:52:01Z</eventTime>` +
			`<event xmlns:n="urn:ietf:params:xml:ns:netconf:notification:1.0"><by>admin</by></event></notification>`
		var notif Notification
		be.NilErr(t, xml.Unmarshal([]byte(raw), &notif))

		var event struct {
			By string `xml:"urn:ietf:params:xml:ns:netconf:notification:1.0 by"`
		}
		be.NilErr(t, notif.Content.Decode(&event))
		be.Equal(t, "admin", event.By)
	})

	// What a notification hands out is the caller's to append to.  Its
	// Decode reads the content as the device sent it, whatever was done with
	// Inner since.
	t.Run("an append to what is in the content", func(t *testing.T) {
		// white space in front of the content moves where the buffer of
		// encoding/xml has room behind what it has read
		for pad := 0; pad <= 2000; pad += 10 {
			raw := []byte(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-10T11:52:01Z</eventTime>` +
				strings.Repeat(" ", pad) + `<event><by>admin</by></event></notification>`)
			var notif Notification
			be.NilErr(t, (&Message{body: raw, reader: io.NopCloser(bytes.NewReader(raw))}).Decode(&notif))

			_ = append(notif.Content.Inner, "<!-- and more, and more, and more -->"...)

			var event struct {
				By string `xml:"by"`
			}
			if err := notif.Content.Decode(&event); err != nil || event.By != "admin" {
				t.Fatalf("with %d bytes in front of the content: by %q, %v", pad, event.By, err)
			}
		}
	})

	// RFC 5277 has one element next to the time.  With two, the content is
	// the last, with its own attributes.
	t.Run("two elements next to the time", func(t *testing.T) {
		const raw = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><first a="1"/><eventTime>2026-10-10T11:52:01Z</eventTime><second b="2">x</second></notification>`
		notif := received(t, raw)
		be.Equal(t, "second", notif.Content.XMLName.Local)
		be.Equal(t, 1, len(notif.Content.Attr))
		be.Equal(t, "b", notif.Content.Attr[0].Name.Local)

		var second struct {
			B    int    `xml:"b,attr"`
			Text string `xml:",chardata"`
		}
		be.NilErr(t, notif.Content.Decode(&second))
		be.Equal(t, 2, second.B)
		be.Equal(t, "x", second.Text)
	})

	// A struct of the program's own that embeds the notification, with a
	// field for what the device sends, decodes like any other struct.
	t.Run("embedded in a struct", func(t *testing.T) {
		const raw = `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">
  <eventTime>2026-10-10T11:52:01Z</eventTime>
  <netconf-config-change xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-notifications">
    <changed-by><username>admin</username></changed-by>
  </netconf-config-change>
</notification>`
		type change struct {
			User string `xml:"changed-by>username"`
		}

		var byValue struct {
			Notification
			Change change `xml:"netconf-config-change"`
		}
		receive(t, raw, &byValue)
		be.True(t, at.Equal(byValue.EventTime))
		be.Equal(t, "admin", byValue.Change.User)

		var byPointer struct {
			*Notification
			Change change `xml:"netconf-config-change"`
		}
		receive(t, raw, &byPointer)
		be.True(t, at.Equal(byPointer.EventTime))
		be.Equal(t, "admin", byPointer.Change.User)
	})

	// Without the message the content is put together again from its name
	// and what is in it.
	t.Run("decoded by encoding/xml alone", func(t *testing.T) {
		var notif Notification
		be.NilErr(t, xml.Unmarshal([]byte(junosCommitNotification), &notif))
		be.Equal(t, xml.Name{Space: NotificationNamespace, Local: "notification"}, notif.XMLName)
		be.True(t, at.Equal(notif.EventTime))
		be.Equal(t, xml.Name{Space: NotificationNamespace, Local: "syslog"}, notif.Content.XMLName)

		var event struct {
			XMLName xml.Name `xml:"urn:ietf:params:xml:ns:netconf:notification:1.0 syslog"`
			ID      string   `xml:"junos>events>event>id"`
		}
		be.NilErr(t, notif.Content.Decode(&event))
		be.Equal(t, "UI_COMMIT", event.ID)
	})

	// something else than a notification is not decoded as one
	t.Run("not a notification", func(t *testing.T) {
		var notif Notification
		be.Nonzero(t, xml.Unmarshal([]byte(`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/></rpc-reply>`), &notif))
		be.Nonzero(t, xml.Unmarshal([]byte(`<notification xmlns="urn:example:other"><eventTime>2026-10-10T11:52:01Z</eventTime></notification>`), &notif))
	})

	t.Run("nothing but the time", func(t *testing.T) {
		var notif Notification
		be.NilErr(t, xml.Unmarshal([]byte(`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-10T11:52:01Z</eventTime></notification>`), &notif))
		be.Equal(t, xml.Name{}, notif.Content.XMLName)

		var v struct{}
		err := notif.Content.Decode(&v)
		be.Nonzero(t, err)
		be.In(t, "the notification has no content", err.Error())
	})
}
