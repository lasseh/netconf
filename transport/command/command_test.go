package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/transport"
	"github.com/lasseh/netconf/transport/transporttest"
)

// helperEnv tells the test binary to be the process of a test, and what
// that process is to do.
const helperEnv = "NETCONF_COMMAND_HELPER"

// TestMain makes the test binary the command of the tests: with helperEnv
// set it is a process that a transport is run over.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helper(mode)
		return
	}
	os.Exit(m.Run())
}

// helper is the process of a test.
func helper(mode string) {
	what, arg, _ := strings.Cut(mode, " ")
	switch what {
	case "relay":
		// standard input and output, connected to the test: a device that
		// the test scripts
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		go func() {
			_, _ = io.Copy(conn, os.Stdin)
			_ = conn.(*net.TCPConn).CloseWrite()
		}()
		_, _ = io.Copy(os.Stdout, conn)
	case "refuse":
		// an ssh that could not log in
		fmt.Fprintln(os.Stderr, "** WARNING: connection is not using a post-quantum key exchange algorithm.")
		fmt.Fprintln(os.Stderr, "admin@router1: Permission denied (publickey,password).")
		os.Exit(255)
	case "silent":
		// takes what it is sent and says nothing
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "stay":
		// outlives its input: a process that only a kill ends
		_, _ = io.Copy(io.Discard, os.Stdin)
		time.Sleep(time.Minute)
	case "hello":
		// says hello, answers nothing and leaves when it is hung up on
		fmt.Print(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>7</session-id></hello>]]>]]>`)
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "leave":
		// says hello and exits without a word, with what arg says
		fmt.Print(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>7</session-id></hello>]]>]]>`)
		buf := make([]byte, 1<<10)
		_, _ = os.Stdin.Read(buf) // the hello of the client
		fmt.Fprintln(os.Stderr, "Connection to router1 closed by remote host.")
		os.Exit(3)
	case "cut":
		// says hello and exits inside a message that nobody reads: a
		// notification, to a session that has no handler for one
		fmt.Print(`<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities><session-id>7</session-id></hello>]]>]]>` +
			`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-10-10T11:52:01Z</eventTime><event>`)
		buf := make([]byte, 1<<10)
		_, _ = os.Stdin.Read(buf) // the hello of the client
		fmt.Fprintln(os.Stderr, "Connection to router1 closed by remote host.")
		os.Exit(255)
	case "mute":
		// ends its output and stays
		_ = os.Stdout.Close()
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "orphan":
		// leaves a child behind that holds what arg names, its standard
		// error or its standard output, and exits like an ssh that could
		// not log in
		child := exec.CommandContext(context.Background(), os.Args[0]) //nolint:gosec // the test binary itself
		child.Env = append(os.Environ(), helperEnv+"=linger")
		if arg == "stderr" {
			child.Stderr = os.Stderr
		} else {
			child.Stdout = os.Stdout
		}
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "admin@router1: Permission denied (publickey).")
		os.Exit(255)
	case "linger":
		// the child of an orphan: it outlives the test that waits for it
		time.Sleep(6 * time.Second)
	}
}

// helperCommand returns the command of a helper process.
func helperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0]) //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	return cmd
}

// gone reports whether the process of cmd has ended and was waited for.  It
// is for after the transport was closed, which is when that is known.
func gone(cmd *exec.Cmd) bool {
	return cmd.ProcessState != nil
}

// relayed returns a transport over a process that passes everything on to
// the test, and the test's end of that as the device.
func relayed(t *testing.T) (transport.Transport, transporttest.Peer) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	tr, err := Start(helperCommand(t, "relay "+ln.Addr().String()))
	be.NilErr(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	conn, err := ln.Accept()
	be.NilErr(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return tr, transporttest.NewStreamPeer(t, conn, nil)
}

func TestTransportConformance(t *testing.T) {
	transporttest.Run(t, relayed)
}

// Close kills the process and waits for it, whatever the process is doing.
func TestClose(t *testing.T) {
	cmd := helperCommand(t, "silent")
	tr, err := Start(cmd)
	be.NilErr(t, err)

	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	select {
	case err := <-closed:
		be.NilErr(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close waits for a process that it was to kill")
	}
	be.True(t, gone(cmd))

	// again, which does nothing
	be.NilErr(t, tr.Close())
}

// A process that is not the caller's to kill, one that runs as somebody
// else, is left with its input closed, and Close does not wait for it for
// good.
func TestCloseCannotKill(t *testing.T) {
	denied := errors.New("operation not permitted")
	kill = func(*os.Process) error { return denied }
	t.Cleanup(func() { kill = (*os.Process).Kill })

	cmd := helperCommand(t, "stay")
	tr, err := Start(cmd)
	be.NilErr(t, err)
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	select {
	case err := <-closed:
		be.True(t, errors.Is(err, denied))
		be.In(t, "was not killed and has not exited", err.Error())
	case <-time.After(10 * exitGrace):
		t.Fatal("Close waits for a process that it could not kill")
	}
	// and says the same again
	be.True(t, errors.Is(tr.Close(), denied))
}

// A session that is closed, and one that never came to be because its
// context ended, leave no process behind.
func TestSessionEndsTheProcess(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		cmd := helperCommand(t, "hello")
		tr, err := Start(cmd)
		be.NilErr(t, err)
		session, err := netconf.Open(t.Context(), tr)
		be.NilErr(t, err)
		be.Equal(t, uint64(7), session.SessionID())

		// the device answers nothing, close-session included
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		be.Nonzero(t, session.Close(ctx))
		be.True(t, gone(cmd))
	})

	t.Run("the context of Open ends", func(t *testing.T) {
		cmd := helperCommand(t, "silent")
		tr, err := Start(cmd)
		be.NilErr(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, err = netconf.Open(ctx, tr)
		be.True(t, errors.Is(err, netconf.ErrHello))
		be.True(t, errors.Is(err, context.DeadlineExceeded))
		be.True(t, gone(cmd))
	})
}

// A command that exits says why on its standard error, and that is in the
// error: an ssh that could not log in.
func TestProcessExits(t *testing.T) {
	t.Run("before a hello", func(t *testing.T) {
		cmd := helperCommand(t, "refuse")
		tr, err := Start(cmd)
		be.NilErr(t, err)

		_, err = netconf.Open(t.Context(), tr)
		be.True(t, errors.Is(err, netconf.ErrHello))
		be.In(t, "has exited: exit status 255: ", err.Error())
		be.In(t, "admin@router1: Permission denied (publickey,password).", err.Error())
		be.False(t, strings.Contains(err.Error(), "\n"))

		// the exit status is there for a program
		var exitErr *exec.ExitError
		be.True(t, errors.As(err, &exitErr))
		be.Equal(t, 255, exitErr.ExitCode())
		be.True(t, gone(cmd))
	})

	t.Run("under a session", func(t *testing.T) {
		tr, err := Start(helperCommand(t, "leave"))
		be.NilErr(t, err)
		session, err := netconf.Open(t.Context(), tr)
		be.NilErr(t, err)

		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the session outlives its process")
		}
		be.In(t, "has exited: exit status 3: Connection to router1 closed by remote host.", session.Err().Error())
	})

	// The process exits inside a message that the session only reads to
	// get past it.  That it has exited is still why the session is over.
	t.Run("inside a message nobody reads", func(t *testing.T) {
		tr, err := Start(helperCommand(t, "cut"))
		be.NilErr(t, err)
		session, err := netconf.Open(t.Context(), tr)
		be.NilErr(t, err)

		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the session outlives its process")
		}
		be.In(t, "has exited: exit status 255: Connection to router1 closed by remote host.", session.Err().Error())
		var exitErr *exec.ExitError
		be.True(t, errors.As(session.Err(), &exitErr))
	})

	// A child that the process left behind keeps the standard error open,
	// and Wait gives it WaitDelay to be done.  The exit status is known
	// only then, so it is waited for that long.
	t.Run("with a child that holds its standard error", func(t *testing.T) {
		cmd := helperCommand(t, "orphan stderr")
		cmd.WaitDelay = exitGrace + 200*time.Millisecond
		tr, err := Start(cmd)
		be.NilErr(t, err)

		_, err = netconf.Open(t.Context(), tr)
		be.True(t, errors.Is(err, netconf.ErrHello))
		be.In(t, "has exited: exit status 255: ", err.Error())
		be.In(t, "admin@router1: Permission denied (publickey).", err.Error())
		var exitErr *exec.ExitError
		be.True(t, errors.As(err, &exitErr))
	})

	// With the standard output held open, the output does not end with the
	// process.  The transport is over all the same.
	t.Run("with a child that holds its standard output", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a pipe takes no deadline on Windows, skipping test")
		}
		tr, err := Start(helperCommand(t, "orphan stdout"))
		be.NilErr(t, err)
		t.Cleanup(func() { _ = tr.Close() })

		// the child lives longer than this
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		_, err = netconf.Open(ctx, tr)
		be.True(t, errors.Is(err, netconf.ErrHello))
		be.False(t, errors.Is(err, context.DeadlineExceeded))
		be.In(t, "has exited: exit status 255: ", err.Error())
	})

	// what the caller wants the standard error for is the caller's
	t.Run("with a standard error of the caller's", func(t *testing.T) {
		var stderr strings.Builder
		cmd := helperCommand(t, "refuse")
		cmd.Stderr = &stderr
		tr, err := Start(cmd)
		be.NilErr(t, err)

		_, err = netconf.Open(t.Context(), tr)
		be.In(t, "has exited: exit status 255", err.Error())
		be.False(t, strings.Contains(err.Error(), "Permission denied"))
		be.In(t, "Permission denied", stderr.String())
	})
}

// A process that ended its output and has not exited is waited for once,
// by the read that found the output over, and not again by every call after
// it.
func TestExitIsWaitedForOnce(t *testing.T) {
	tr, err := Start(helperCommand(t, "mute"))
	be.NilErr(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	began := time.Now()
	r, err := tr.MsgReader()
	be.NilErr(t, err)
	_, err = io.ReadAll(r)
	be.True(t, errors.Is(err, io.ErrUnexpectedEOF))
	// whatever comes after it fails or not, it does not wait again
	_ = r.Close()
	if again, err := tr.MsgReader(); err == nil {
		_, _ = io.ReadAll(again)
		_ = again.Close()
	}

	// one wait of WaitDelay and exitGrace, a second each
	if took := time.Since(began); took > 3*exitGrace {
		t.Errorf("the failures took %s", took)
	}
}

// A message that cannot be framed is the caller's mistake and no sign of a
// process that is gone: it is not waited for.
func TestErrorOfTheMessage(t *testing.T) {
	tr, err := Start(helperCommand(t, "silent"))
	be.NilErr(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	began := time.Now()
	w, err := tr.MsgWriter()
	be.NilErr(t, err)
	_, err = io.WriteString(w, "<rpc>]]>]]></rpc>")
	if err == nil {
		err = w.Close()
	}
	be.True(t, errors.Is(err, transport.ErrEndOfMsgInPayload))
	if took := time.Since(began); took > exitGrace/2 {
		t.Errorf("the error took %s", took)
	}
}

func TestStart(t *testing.T) {
	t.Run("a command that is not there", func(t *testing.T) {
		_, err := Start(exec.CommandContext(t.Context(), "/no/such/netconf-command"))
		be.True(t, errors.Is(err, os.ErrNotExist))
	})

	t.Run("a command whose input or output is taken", func(t *testing.T) {
		cmd := helperCommand(t, "silent")
		cmd.Stdin = strings.NewReader("")
		_, err := Start(cmd)
		be.Nonzero(t, err)
		be.Zero(t, cmd.Process)
	})
}
