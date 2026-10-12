package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
)

// junosCommand is what starts NETCONF on the ssh service of a Junos device
// that has no NETCONF service configured.
const junosCommand = "xml-mode netconf need-trailer"

// A device that refuses the netconf subsystem may start NETCONF with a
// command.  WithCommand sends that as an exec request in place of the
// subsystem request, and the rest of the dial is the same.
func TestDialHost_Command(t *testing.T) {
	t.Run("the command is what is asked for", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithCommand(junosCommand))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
		be.NilErr(t, srv.Wait(t))
		be.AllEqual(t, []string{"exec " + junosCommand}, srv.Asked())
	})

	// The first byte of the device is looked at before there is a
	// transport, and is still there for who captures the traffic.
	t.Run("captured from the first byte", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithCommand(junosCommand))
		be.NilErr(t, err)
		var received bytes.Buffer
		tr.DebugCapture(&received, nil)
		readGreeting(t, tr)
		be.Equal(t, "hi]]>]]>", received.String())
		be.NilErr(t, tr.Close())
		be.NilErr(t, srv.Wait(t))
	})

	t.Run("without it the subsystem is", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(greet)
		host, port := hostPort(t, srv.Addr())

		tr, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""), WithInsecureIgnoreHostKey())
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())
		be.NilErr(t, srv.Wait(t))
		be.AllEqual(t, []string{"subsystem netconf"}, srv.Asked())
	})

	// A device says no to the request itself: Junos does on its NETCONF
	// port, which takes the subsystem and nothing else.
	t.Run("a device that takes no command", func(t *testing.T) {
		srv := newTestServer(t)
		srv.RejectExec = true
		srv.Serve(func(ch ssh.Channel) error {
			_, err := io.ReadAll(ch)
			return err
		})
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithCommand(junosCommand))
		be.Nonzero(t, err)
		be.In(t, `refused to run "xml-mode netconf need-trailer"`, err.Error())
		be.False(t, errors.Is(err, ErrSubsystemRefused))
		be.NilErr(t, srv.Wait(t))
	})

	// A device that runs the command, which then cannot do what it was
	// told: it says why on its standard error and ends.  That is in the
	// error of the dial, and not left for the hello exchange to trip over.
	t.Run("a command that fails", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(func(ch ssh.Channel) error {
			// the command is taken, and then it runs
			for len(srv.Asked()) == 0 {
				time.Sleep(time.Millisecond)
			}
			if _, err := io.WriteString(ch.Stderr(), "\nerror: unknown command.\n  xml-mode netconf need-trailer\n"); err != nil {
				return err
			}
			if _, err := ch.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{1})); err != nil {
				return err
			}
			return ch.Close()
		})
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithCommand(junosCommand))
		be.Nonzero(t, err)
		be.In(t, `"xml-mode netconf need-trailer"`, err.Error())
		be.In(t, "ended before it said anything: error: unknown command. xml-mode netconf need-trailer", err.Error())
		be.NilErr(t, srv.Wait(t))
	})

	t.Run("a command that ends without a word", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Serve(func(ch ssh.Channel) error {
			for len(srv.Asked()) == 0 {
				time.Sleep(time.Millisecond)
			}
			return ch.Close()
		})
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(t.Context(), host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithCommand(junosCommand))
		be.Nonzero(t, err)
		be.In(t, `"xml-mode netconf need-trailer" ended before it said anything: EOF`, err.Error())
		be.NilErr(t, srv.Wait(t))
	})

	t.Run("a command that says nothing", func(t *testing.T) {
		// the dial is given up once the device has taken the command
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		srv := newTestServer(t)
		srv.Serve(func(ch ssh.Channel) error {
			for len(srv.Asked()) == 0 {
				time.Sleep(time.Millisecond)
			}
			cancel()
			_, err := io.Copy(io.Discard, ch)
			return err
		})
		host, port := hostPort(t, srv.Addr())

		_, err := DialHost(ctx, host, WithPort(port), WithUser("admin"), WithPassword(""),
			WithInsecureIgnoreHostKey(), WithCommand(junosCommand))
		be.True(t, errors.Is(err, context.Canceled))
		be.In(t, `running "xml-mode netconf need-trailer" on `+srv.Addr(), err.Error())
	})
}
