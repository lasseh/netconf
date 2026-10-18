package netconf

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/internal/netconftest"
	"github.com/lasseh/netconf/transport"
)

// junosCapabilities is what Junos 26.2R1.7 advertises on a vJunos-router.
const junosCapabilities = `    <capability>urn:ietf:params:netconf:base:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:candidate:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:confirmed-commit:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:validate:1.0</capability>
    <capability>urn:ietf:params:netconf:capability:url:1.0?scheme=http,ftp,file</capability>
    <capability>urn:ietf:params:xml:ns:netconf:base:1.0</capability>
    <capability>urn:ietf:params:xml:ns:netconf:capability:candidate:1.0</capability>
    <capability>urn:ietf:params:xml:ns:netconf:capability:confirmed-commit:1.0</capability>
    <capability>urn:ietf:params:xml:ns:netconf:capability:validate:1.0</capability>
    <capability>urn:ietf:params:xml:ns:netconf:capability:url:1.0?scheme=http,ftp,file</capability>
    <capability>urn:ietf:params:xml:ns:yang:ietf-yang-metadata?module=ietf-yang-metadata&amp;revision=2016-08-05</capability>
    <capability>urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring</capability>
    <capability>http://xml.juniper.net/netconf/junos/1.0</capability>
    <capability>http://xml.juniper.net/dmi/system/1.0</capability>
    <capability>http://yang.juniper.net/junos/jcmd?module=junos-configuration-metadata&amp;revision=2021-09-01</capability>
`

// The first message of Junos 26.2R1.7 (vJunos-router) to a user whose login
// class is "unauthorized", captured in a lab: two lines of plain text, two
// replies that answer nothing, a warning in one and an error in the other,
// and then a hello like any other, all in one message.  The user was made
// for the capture.
const junosRestrictedHello = `warning: user "nclib-restricted" does not have access privileges.

error: Restricted user session.
<!-- No zombies were killed during the creation of this user interface -->
<rpc-reply>
<rpc-error>
<error-severity>warning</error-severity>
<error-message>
user "nclib-restricted" does not have access privileges.
</error-message>
</rpc-error>
</rpc-reply>
<rpc-reply>
<rpc-error>
<error-type>protocol</error-type>
<error-tag>operation-failed</error-tag>
<error-severity>error</error-severity>
<error-message>
Restricted user session.
</error-message>
</rpc-error>
</rpc-reply>
<!-- user nclib-restricted, class j-unauthorized -->
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
` + junosCapabilities + `  </capabilities>
  <session-id>31316</session-id>
</hello>
`

// helloFrom opens a session to a device that sends first where its hello
// belongs, and returns what Open made of it.
func helloFrom(t *testing.T, first string, opts ...SessionOption) (*Session, error) {
	t.Helper()
	wire, dev := netconftest.NewWire(t)
	go func() {
		if _, err := dev.Recv(); err != nil {
			return
		}
		_ = dev.WriteRaw(first)
	}()
	return Open(testCtx(t), wire, opts...)
}

// What a device writes behind its hello, in the same message, is not looked
// at, as it never was: a line that is no XML there does not keep a session
// from opening.
func TestHelloWithSomethingBehindIt(t *testing.T) {
	hello, found := strings.CutSuffix(wireHello, "]]>]]>")
	be.True(t, found)
	for name, behind := range map[string]string{
		"text that is no XML":   "\nready & waiting\n",
		"a control character":   "\x00",
		"an element left open":  "<motd>",
		"a comment and a break": "<!-- session 7 -->\n",
	} {
		t.Run(name, func(t *testing.T) {
			sess, err := helloFrom(t, hello+behind+"]]>]]>", WithLogger(slog.New(slog.DiscardHandler)))
			be.NilErr(t, err)
			be.Nonzero(t, sess.SessionID())
			be.True(t, sess.ServerCaps().Has(CapNetConf10))
		})
	}
}

// A hello exchange that fails says so in a way a program can ask for, with
// what the device sent.
// hungUp is a transport to a device that has said something and is gone:
// what it said is there to read, and nothing can be written to it.
type hungUp struct{ *transport.Framer }

func (hungUp) Close() error { return nil }

// stoppable is a transport whose Close ends what is being read from it.
type stoppable struct {
	hungUp
	r io.Closer
}

func (s stoppable) Close() error { return s.r.Close() }

// gone is the way to a device that has hung up.
type gone struct{}

func (gone) Write([]byte) (int, error) { return 0, io.EOF }

func TestHelloError(t *testing.T) {
	failed := func(t *testing.T, err error) *HelloError {
		t.Helper()
		be.True(t, errors.Is(err, ErrHello))
		var helloErr *HelloError
		if !errors.As(err, &helloErr) {
			t.Fatalf("no HelloError in: %v", err)
		}
		be.True(t, strings.HasPrefix(err.Error(), "netconf: hello exchange failed: "))
		return helloErr
	}

	t.Run("no common base version", func(t *testing.T) {
		const hello = `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.1</capability><capability>urn:example:x</capability></capabilities><session-id>7</session-id></hello>]]>]]>`
		_, err := helloFrom(t, hello, WithCapability(CapNetConf10))
		helloErr := failed(t, err)
		be.AllEqual(t, []string{CapNetConf11, "urn:example:x"}, helloErr.Capabilities)
		be.In(t, "no common netconf base capability", err.Error())
	})

	t.Run("no session-id", func(t *testing.T) {
		_, err := helloFrom(t, helloNoSessID+"]]>]]>")
		helloErr := failed(t, err)
		be.Nonzero(t, len(helloErr.Capabilities))
		be.In(t, "did not return a session-id", err.Error())
	})

	t.Run("no capabilities", func(t *testing.T) {
		_, err := helloFrom(t, helloNoCaps+"]]>]]>")
		be.Equal(t, 0, len(failed(t, err).Capabilities))
	})

	t.Run("made by hand without a cause", func(t *testing.T) {
		err := &HelloError{Text: "login:"}
		be.Equal(t, `netconf: hello exchange failed; the device said: "login:"`, err.Error())
		be.True(t, errors.Is(err, ErrHello))
	})

	// What a wrong port and a device without NETCONF look like: somebody
	// took the connection and hung up.
	t.Run("the connection ends before a hello", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			_, _ = dev.Recv()
			dev.Hangup()
		}()
		_, err := Open(testCtx(t), wire)
		_ = failed(t, err)
		be.True(t, errors.Is(err, io.ErrUnexpectedEOF))
		be.In(t, "wrong port", err.Error())
		be.In(t, "NETCONF", err.Error())
	})

	// A device that prints and says no hello: a shell, a login that went
	// somewhere else.
	t.Run("text and no hello", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() {
			if _, err := dev.Recv(); err == nil {
				_ = dev.WriteRaw("Last login: Sun Oct 11 09:20:11 2026\nerror: unknown command & more <")
				dev.Hangup()
			}
		}()
		_, err := Open(testCtx(t), wire)
		helloErr := failed(t, err)
		be.Equal(t, "Last login: Sun Oct 11 09:20:11 2026\nerror: unknown command & more <", helloErr.Text)
		be.In(t, `the device said: "Last login`, err.Error())
		be.False(t, strings.Contains(err.Error(), "\n"))
	})

	// The same device, one that waits at its prompt: the context ends the
	// exchange, and what the device printed until then is still what it
	// printed.
	t.Run("text, no hello and no end", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		printed := make(chan struct{})
		go func() {
			if _, err := dev.Recv(); err == nil {
				_ = dev.WriteRaw("Last login: Sun Oct 11 09:20:11 2026\nadmin@device> ")
			}
			close(printed)
		}()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-printed
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		_, err := Open(ctx, wire)
		helloErr := failed(t, err)
		be.True(t, errors.Is(err, context.Canceled))
		be.Equal(t, "Last login: Sun Oct 11 09:20:11 2026\nadmin@device>", helloErr.Text)
	})

	// A device that refuses at once says why and hangs up, without waiting
	// for the hello of the client.  That the hello could not be written is
	// then not all there is to say.
	t.Run("a refusal and a hangup before the hello is written", func(t *testing.T) {
		tr := hungUp{transport.NewFramer(strings.NewReader("error: too many sessions, try again later\n"), gone{})}

		_, err := Open(testCtx(t), tr)
		helloErr := failed(t, err)
		be.In(t, "failed to write hello message", err.Error())
		be.Equal(t, "error: too many sessions, try again later", helloErr.Text)
	})

	// The hello cannot be written and the device says nothing: that is not
	// waited for until the context ends, and the write is why Open failed.
	t.Run("a hello that cannot be written, to a device that says nothing", func(t *testing.T) {
		silent, never := io.Pipe()
		t.Cleanup(func() { _ = never.Close() })

		began := time.Now()
		_, err := Open(testCtx(t), stoppable{hungUp{transport.NewFramer(silent, gone{})}, silent})
		_ = failed(t, err)
		be.In(t, "failed to write hello message", err.Error())
		be.True(t, errors.Is(err, io.EOF))
		be.False(t, errors.Is(err, context.DeadlineExceeded))
		if took := time.Since(began); took > 3*time.Second {
			t.Errorf("Open took %s", took)
		}
	})

	t.Run("a message that is not a hello", func(t *testing.T) {
		_, err := helloFrom(t, `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/></rpc-reply>]]>]]>`)
		helloErr := failed(t, err)
		be.Equal(t, 0, len(helloErr.RPCErrors))
		be.In(t, "sent no hello", err.Error())
	})

	t.Run("errors in place of a hello", func(t *testing.T) {
		_, err := helloFrom(t, `<rpc-reply><rpc-error><error-type>protocol</error-type><error-tag>access-denied</error-tag><error-severity>error</error-severity><error-message>too many sessions</error-message></rpc-error></rpc-reply>]]>]]>`)
		helloErr := failed(t, err)
		be.Equal(t, 1, len(helloErr.RPCErrors))
		be.True(t, errors.Is(err, ErrAccessDenied))
		be.In(t, "too many sessions", err.Error())
	})

	t.Run("the hello of junos to a restricted user", func(t *testing.T) {
		_, err := helloFrom(t, junosRestrictedHello+"]]>]]>")
		helloErr := failed(t, err)

		// the errors, in the order Junos sent them
		be.Equal(t, 2, len(helloErr.RPCErrors))
		be.Equal(t, SevWarning, helloErr.RPCErrors[0].Severity)
		be.Equal(t, `user "nclib-restricted" does not have access privileges.`, helloErr.RPCErrors[0].Message)
		be.Equal(t, SevError, helloErr.RPCErrors[1].Severity)
		be.Equal(t, ErrOperationFailed, helloErr.RPCErrors[1].Tag)
		be.Equal(t, "Restricted user session.", helloErr.RPCErrors[1].Message)

		// they are within reach of errors.Is and errors.As like those of a reply
		be.True(t, errors.Is(err, ErrOperationFailed))
		var rpcErrs RPCErrors
		be.True(t, errors.As(err, &rpcErrs))
		be.Equal(t, 2, len(rpcErrs))

		// what Junos printed, and the hello it sent all the same
		be.Equal(t, "warning: user \"nclib-restricted\" does not have access privileges.\n\nerror: Restricted user session.", helloErr.Text)
		be.Equal(t, 15, len(helloErr.Capabilities))
		be.Equal(t, junosCapability, helloErr.Capabilities[12])

		be.In(t, "the device sent errors with its hello", err.Error())
		be.In(t, "netconf error: protocol operation-failed: Restricted user session.", err.Error())
		be.False(t, strings.Contains(err.Error(), "\n"))
	})

	// A warning by itself is no refusal: the session opens, and the log has
	// the warning.  Made up, no device was seen to do this.
	t.Run("a warning with the hello", func(t *testing.T) {
		logs := &logRecorder{}
		warned := `<rpc-reply><rpc-error><error-severity>warning</error-severity><error-message>the routing engine is the backup</error-message></rpc-error></rpc-reply>` + helloGood
		sess, err := helloFrom(t, warned+"]]>]]>", WithLogger(slog.New(logs)))
		be.NilErr(t, err)
		be.Equal(t, uint64(42), sess.SessionID())
		be.AllEqual(t, []string{"the device warned with its hello"}, logs.Records())
	})

	t.Run("a device that never says hello", func(t *testing.T) {
		wire, dev := netconftest.NewWire(t)
		go func() { _, _ = dev.Recv() }()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := Open(ctx, wire)
		_ = failed(t, err)
		be.True(t, errors.Is(err, context.DeadlineExceeded))
	})

	// a hello with a prefix, as Junos writes it in rfc-compliant mode
	t.Run("a hello with a prefix is a hello", func(t *testing.T) {
		const hello = `<nc:hello xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0"><nc:capabilities><nc:capability>urn:ietf:params:netconf:base:1.0</nc:capability></nc:capabilities><nc:session-id>9</nc:session-id></nc:hello>]]>]]>`
		sess, err := helloFrom(t, hello)
		be.NilErr(t, err)
		be.Equal(t, uint64(9), sess.SessionID())
	})
}
