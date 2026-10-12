package netconf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
)

// Wire bytes as sent by Junos 25.4 (EX4100) without rfc-compliant mode,
// reduced to the parts that matter: XML comments before <hello>, a junos
// namespace declared on every <rpc-reply>, and an unframed comment after the
// reply to <close-session>.
const (
	junosHello = `<!-- No zombies were killed during the creation of this user interface -->
<!-- user admin, class j-admin -->
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
    <capability>urn:ietf:params:netconf:base:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:candidate:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:confirmed-commit:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:validate:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:url:1.0?scheme=http,ftp,file</capability>
    <capability>urn:ietf:params:xml:ns:netconf:base:1.0</capability>
    <capability>http://xml.juniper.net/netconf/junos/1.0</capability>
    <capability>http://xml.juniper.net/dmi/system/1.0</capability>
  </capabilities>
  <session-id>27721</session-id>
</hello>
]]>]]>
`

	junosReplyOpen = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos" message-id="%ID%" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` + "\n"

	junosSystemInfo = `<system-information>
<host-name>switch1</host-name>
<hardware-model>ex4100-f-12p</hardware-model>
<os-name>junos-ex</os-name>
<os-version>25.4R1-S1.7</os-version>
<serial-number>XX0000000000</serial-number>
</system-information>
`

	junosSyntaxError = `<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>syntax error</error-message>
<error-info>
<bad-element>no-such-rpc</bad-element>
</error-info>
</rpc-error>
`

	junosSessionEnd = "<!-- session end at 2026-10-07 20:32:41 CEST -->\n"
)

func junosReply(msgID, body string) string {
	return strings.Replace(junosReplyOpen, "%ID%", msgID, 1) + body + "</rpc-reply>\n]]>]]>\n"
}

// serveJunos scripts dev like the captured Junos device until the client
// sends <close-session>, after which it hangs up the way Junos does.
func serveJunos(dev *netconftest.Device) {
	if _, err := dev.Recv(); err != nil { // client hello
		return
	}
	if err := dev.WriteRaw(junosHello); err != nil {
		return
	}

	for {
		req, err := dev.Recv()
		if err != nil {
			return
		}
		msgID := netconftest.ExtractMessageID(req)

		switch {
		case strings.Contains(req, "<close-session"):
			_ = dev.WriteRaw(junosReply(msgID, "<ok/>\n") + junosSessionEnd)
			dev.Hangup()
			return
		case strings.Contains(req, "<get-system-information"):
			err = dev.WriteRaw(junosReply(msgID, junosSystemInfo))
		default:
			err = dev.WriteRaw(junosReply(msgID, junosSyntaxError))
		}
		if err != nil {
			return
		}
	}
}

// logRecorder collects log records at warn level and above.
type logRecorder struct {
	mu      sync.Mutex
	records []string
}

func (r *logRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Message)
	return nil
}

func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

func (r *logRecorder) Records() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.records...)
}

func TestJunosSession(t *testing.T) {
	wire, dev := netconftest.NewWire(t)
	go serveJunos(dev)

	logs := &logRecorder{}
	sess, err := Open(context.Background(), wire, WithLogger(slog.New(logs)))
	be.NilErr(t, err)
	be.Equal(t, uint64(27721), sess.SessionID())
	be.True(t, sess.ServerCaps().Has(":candidate:1.0"))
	be.False(t, sess.ServerCaps().Has(CapNetConf11))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A Junos-native RPC given as literal XML.
	var info struct {
		Hostname string `xml:"system-information>host-name"`
		Model    string `xml:"system-information>hardware-model"`
	}
	be.NilErr(t, sess.Exec(ctx, "<get-system-information/>", &info))
	be.Equal(t, "switch1", info.Hostname)
	be.Equal(t, "ex4100-f-12p", info.Model)

	// The raw reply keeps the junos namespace declaration.
	reply, err := sess.ExecReply(ctx, "<get-system-information/>")
	be.NilErr(t, err)
	be.In(t, `xmlns:junos="http://xml.juniper.net/junos/25.4R1-S1.7/junos"`, string(reply.Raw))

	// An unknown RPC surfaces as an RPCError.
	err = sess.Exec(ctx, "<no-such-rpc/>", nil)
	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, ErrOperationFailed, rpcErr.Tag)
	be.Equal(t, "syntax error", rpcErr.Message)
	be.In(t, "no-such-rpc", string(rpcErr.Info))

	// Junos writes an unframed comment and hangs up after close-session; a
	// graceful close must neither fail nor log warnings.
	be.NilErr(t, sess.Close(ctx))
	be.Equal(t, "", strings.Join(logs.Records(), "; "))
}

// Text that is not ASCII as Junos 26.2 (vJunos-router) sends it: a character
// reference for every byte above 127.
const (
	junos262ReplyOpen = `<rpc-reply xmlns:junos="http://xml.juniper.net/junos/26.2R1.7/junos" message-id="%ID%" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` + "\n"

	// A statement that was loaded as "xåx" in UTF-8.
	junosTextConfig = `<configuration xmlns="http://xml.juniper.net/xnm/1.1/xnm" junos:changed-seconds="1791538182" junos:changed-localtime="2026-10-09 09:29:42 UTC">
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

	// The answer to a set command that gave "sjuende – 7" for a number.
	junosTextError = `<load-configuration-results>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>Invalid numeric value: 'sjuende &#226;&#128;&#147; 7'</error-message>
<error-info>
<bad-element>sjuende &#226;&#128;&#147; 7</bad-element>
</error-info>
</rpc-error>
<ok/>
</load-configuration-results>
`
)

// The session puts the text of a Junos device back together in what it
// buffers, and leaves what another device sends alone.
func TestJunosText(t *testing.T) {
	answer := func(body string) func(msgID, req string) string {
		return func(msgID, _ string) string {
			return strings.Replace(junos262ReplyOpen, "%ID%", msgID, 1) + body + "</rpc-reply>\n]]>]]>\n"
		}
	}

	t.Run("a reply", func(t *testing.T) {
		sess := wireSessionAfter(t, junosHello, answer(junosTextConfig))

		reply, err := sess.ExecReply(testCtx(t), "<get-configuration/>")
		be.NilErr(t, err)
		be.In(t, "<building>xåx</building>", string(reply.Raw))

		building, err := reply.Element("building")
		be.NilErr(t, err)
		be.Equal(t, "xåx", building.Text)
		be.Equal(t, "<building>xåx</building>", string(building.Raw))

		var config struct {
			Building string `xml:"configuration>groups>system>location>building"`
		}
		be.NilErr(t, sess.Exec(testCtx(t), "<get-configuration/>", &config))
		be.Equal(t, "xåx", config.Building)
	})

	t.Run("an error", func(t *testing.T) {
		sess := wireSessionAfter(t, junosHello, answer(junosTextError))

		reply, err := sess.ExecReply(testCtx(t), "<load-configuration/>")
		var rpcErr RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "Invalid numeric value: 'sjuende – 7'", rpcErr.Message)
		bad, _ := rpcErr.Detail("bad-element")
		be.Equal(t, "sjuende – 7", bad)
		be.In(t, "'sjuende – 7': Invalid numeric value", err.Error())
		be.In(t, "<bad-element>sjuende – 7</bad-element>", string(reply.Raw))
	})

	t.Run("a notification", func(t *testing.T) {
		got := make(chan string, 1)
		sess := wireSessionAfter(t, junosHello,
			func(msgID, _ string) string { return junosCommitNotification + "\n]]>]]>\n" + framed(msgID, "<ok/>") },
			WithNotifHandler(func(_ context.Context, msg *Message) {
				raw, _ := io.ReadAll(msg)
				got <- string(raw)
			}),
		)

		be.NilErr(t, sess.Exec(testCtx(t), "<create-subscription/>", nil))
		select {
		case raw := <-got:
			be.In(t, "<value>"+junosCommitComment+"</value>", raw)
		case <-testCtx(t).Done():
			t.Fatal("no notification")
		}
	})

	// nobody has read a streamed reply before it is handed over
	t.Run("a streamed reply is as sent", func(t *testing.T) {
		sess := wireSessionAfter(t, junosHello, answer(junosTextConfig))

		msg, err := sess.Stream(testCtx(t), "<get-configuration/>")
		be.NilErr(t, err)
		raw, err := io.ReadAll(msg)
		be.NilErr(t, err)
		be.NilErr(t, msg.Close())
		be.In(t, "<building>x&#195;&#165;x</building>", string(raw))
	})

	// To a device that keeps to XML the two references are two characters.
	t.Run("a device that is not Junos", func(t *testing.T) {
		sess := wireSession(t, answer(junosTextConfig))

		reply, err := sess.ExecReply(testCtx(t), "<get-configuration/>")
		be.NilErr(t, err)
		be.In(t, "<building>x&#195;&#165;x</building>", string(reply.Raw))

		building, err := reply.Element("building")
		be.NilErr(t, err)
		be.Equal(t, "xÃ¥x", building.Text)
	})
}

// A session with Junos 26.2 that has rfc-compliant and version-1.1 set, the
// one TestFramerJunosChunked of package transport reads message by message,
// played to a session.  Junos in that mode says hello only when the client
// has, speaks in chunks from then on, and puts a prefix on every element of
// the protocol.  Its warnings come as they do without the mode.
func TestJunosChunkedSession(t *testing.T) {
	raw, err := os.ReadFile("transport/testdata/junos-chunked-session.raw")
	be.NilErr(t, err)
	hello, rest, found := bytes.Cut(raw, []byte("]]>]]>"))
	be.True(t, found)
	replies := bytes.SplitAfter(rest, []byte("\n##\n"))
	replies = replies[:len(replies)-1] // nothing is behind the last one
	be.Equal(t, 11, len(replies))

	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		if err := dev.WriteRaw(string(hello) + "]]>]]>"); err != nil {
			return
		}
		dev.Upgrade()
		for _, reply := range replies {
			if _, err := dev.Recv(); err != nil {
				return
			}
			if err := dev.WriteRaw(string(reply)); err != nil {
				return
			}
		}
	}()

	sess, err := Open(t.Context(), wire, WithLogger(slog.New(slog.DiscardHandler)))
	be.NilErr(t, err)
	be.True(t, sess.ServerCaps().Has(CapNetConf11))
	be.Equal(t, uint64(46687), sess.SessionID())

	ctx := testCtx(t)
	ask := func(t *testing.T, op string) (*Reply, error) {
		t.Helper()
		return sess.ExecReply(ctx, op)
	}
	ok := func(t *testing.T, op string) *Reply {
		t.Helper()
		reply, err := ask(t, op)
		be.NilErr(t, err)
		return reply
	}

	// the order is that of the capture
	_, err = ok(t, `<lock><target><candidate/></target></lock>`).Element("ok")
	be.NilErr(t, err)
	_, err = ok(t, `<unlock><target><candidate/></target></unlock>`).Element("ok")
	be.NilErr(t, err)

	_, err = ask(t, `<no-such-rpc/>`)
	var rpcErr RPCError
	be.True(t, errors.As(err, &rpcErr))
	be.Equal(t, ErrOperationFailed, rpcErr.Tag)
	be.Equal(t, "syntax error", rpcErr.Message)
	bad, _ := rpcErr.Detail("bad-element")
	be.Equal(t, "no-such-rpc", bad)
	be.Equal(t, "no-such-rpc", rpcErr.Request())

	// a warning next to an <ok/>
	reply := ok(t, `<commit-configuration><check/><synchronize/></commit-configuration>`)
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "'synchronize' ignored in single routing engine chassis", reply.Warnings[0].Message)
	_, err = reply.Element("ok")
	be.NilErr(t, err)

	// a warning and nothing else
	reply = ok(t, `<open-configuration><private/></open-configuration>`)
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "changes cannot be committed while 'configure exclusive' is active", reply.Warnings[0].Message)
	ok(t, `<close-configuration/>`)
	be.Equal(t, 0, len(ok(t, `<open-configuration><private/></open-configuration>`).Warnings))

	// a warning inside a result
	reply = ok(t, `<load-configuration action="set" format="text"><configuration-set>delete groups netconf-lib-no-such-group</configuration-set></load-configuration>`)
	be.Equal(t, 1, len(reply.Warnings))
	be.Equal(t, "statement not found", reply.Warnings[0].Message)

	ok(t, `<load-configuration rollback="0"/>`)
	ok(t, `<close-configuration/>`)
	be.NilErr(t, sess.Close(ctx))
}
