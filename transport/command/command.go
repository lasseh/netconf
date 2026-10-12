// Package command is a NETCONF transport over a process: it speaks NETCONF
// on the standard input and output of a command that it starts.
//
// The command that matters is the ssh of the system, which does what the ssh
// client of package ssh does not: ProxyCommand, a jump host behind a jump
// host, connection sharing, Kerberos, a key on a hardware token.
//
//	tr, err := nccmd.Start(exec.Command("ssh", "-s", "-p", "830", "router1", "netconf"))
//	if err != nil { ... }
//	session, err := netconf.Open(ctx, tr)
//
// The command gets pipes and no terminal.  An ssh that has to ask for a
// password or a passphrase has nowhere to ask, so the login has to be one
// that needs no typing: a key without a passphrase, an agent, a certificate,
// a connection that is shared already.
//
// The package is usually imported under another name:
//
//	import nccmd "github.com/lasseh/netconf/transport/command"
package command

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lasseh/netconf/internal/tail"
	"github.com/lasseh/netconf/transport"
)

// alias it to a private type so we can make it private when embedding.
type framer = transport.Framer

// Transport is NETCONF over the standard input and output of a process.
type Transport struct {
	cmd *exec.Cmd

	// stdin and stdout are our ends of the pipes to the process
	stdin  *os.File
	stdout *os.File
	// stderr is the end of what the process wrote to standard error, nil
	// when that goes somewhere of the caller's choosing
	stderr *tail.Buffer

	// exited is closed when the process has been waited for, after which
	// exitErr is what Wait returned
	exited  chan struct{}
	exitErr error

	closing atomic.Bool
	// stayed says that a failure has waited for the exit of the process
	// and it did not come
	stayed    atomic.Bool
	closeOnce sync.Once
	// closeErr is what Close returns, written under closeOnce
	closeErr error

	*framer
}

// exitGrace is how long a failed read or write waits for the process to be
// found gone, so that the error can say so.  A pipe ends a moment before the
// process that held it is reaped.
const exitGrace = time.Second

// Start starts cmd and returns a transport that speaks NETCONF on its
// standard input and output.  cmd is one that was not started, with Stdin
// and Stdout not set.  Its standard error is kept for the error of a command
// that exits, unless cmd.Stderr is set, which is then left alone.
//
// The transport owns the process.  Close kills it and waits for it, and so
// the context given to netconf.Open, which closes the transport when it
// ends, kills it too.  Only the process itself is killed: use a command that
// is the program and not a shell around it.
//
// When the process exits by itself, reads and writes fail with an error
// that says so, with the exit status and what the process wrote to standard
// error last: "Permission denied (publickey)" from an ssh that could not
// log in.  errors.As finds the *exec.ExitError in it, which a process that
// exited with status 0 has none of.  A child that the process left behind
// delays that and does not prevent it.  The status is waited for as long as
// cmd.WaitDelay gives a child that holds the standard error, a second unless
// it is set.  With the standard output held, the output is over once the
// process has been waited for and nothing has come for a second, where a
// pipe takes a deadline, which is everywhere but on Windows; a child that
// keeps writing keeps it open.
func Start(cmd *exec.Cmd) (*Transport, error) {
	if cmd.Stdin != nil || cmd.Stdout != nil {
		return nil, errors.New("command: Stdin and Stdout of the command must not be set")
	}

	// Pipes of our own, and not those of StdinPipe and StdoutPipe: Wait
	// closes those, and the process is waited for while its output is still
	// being read.
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_, _ = inR.Close(), inW.Close()
		return nil, fmt.Errorf("command: %w", err)
	}
	cmd.Stdin, cmd.Stdout = inR, outW

	exited := make(chan struct{})
	t := &Transport{
		cmd:    cmd,
		stdin:  inW,
		stdout: outR,
		exited: exited,
		framer: transport.NewFramer(&output{f: outR, exited: exited}, inW),
	}
	if cmd.Stderr == nil {
		t.stderr = &tail.Buffer{}
		cmd.Stderr = t.stderr
	}
	// Wait is not held up by whoever else has the standard error of the
	// process open, a child it left behind.
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = exitGrace
	}

	err = cmd.Start()
	// the process has its ends of the pipes, or there is no process
	_, _ = inR.Close(), outW.Close()
	if err != nil {
		_, _ = inW.Close(), outR.Close()
		return nil, fmt.Errorf("command: %w", err)
	}

	go func() {
		t.exitErr = cmd.Wait()
		close(t.exited)
		// for a read that was waiting when the process went, see output
		_ = outR.SetReadDeadline(time.Now().Add(exitGrace))
	}()
	return t, nil
}

// output is the standard output of the process, as the framer reads it.  A
// child that the process left behind can hold the other end of it open, and
// then the output does not end with the process.  So once the process is
// gone, a read that gets nothing for exitGrace is the end of the output.
// What the process had written by then is still read.
type output struct {
	f      *os.File
	exited <-chan struct{}
	ended  atomic.Bool
}

func (o *output) Read(p []byte) (int, error) {
	if o.ended.Load() {
		return 0, io.EOF
	}
	select {
	case <-o.exited:
		_ = o.f.SetReadDeadline(time.Now().Add(exitGrace))
	default:
	}
	n, err := o.f.Read(p)
	if n == 0 && errors.Is(err, os.ErrDeadlineExceeded) {
		o.ended.Store(true)
		err = io.EOF
	}
	return n, err
}

// kill is what ends the process, for the test of a process it cannot end.
var kill = (*os.Process).Kill

// Close kills the process, waits for it and releases everything that waits
// on the transport.  It is safe to call more than once.
//
// A process that cannot be killed, one that runs as another user, finds its
// input closed and is given a moment to leave.  Close returns an error when
// it has not.
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		t.closing.Store(true)
		// The process may be gone already, which is fine.  Closing our ends
		// of the pipes is what gets a read or a write off them.
		err := kill(t.cmd.Process)
		_ = t.stdin.Close()
		_ = t.stdout.Close()
		if err == nil || errors.Is(err, os.ErrProcessDone) {
			<-t.exited
			return
		}
		select {
		case <-t.exited:
		case <-time.After(exitGrace):
			t.closeErr = fmt.Errorf("command: %s was not killed and has not exited: %w", filepath.Base(t.cmd.Path), err)
		}
	})
	return t.closeErr
}

// MsgReader returns a reader for the next message, like the one of the
// Framer.  When the process has exited, what the reader fails with says so.
func (t *Transport) MsgReader() (io.ReadCloser, error) {
	r, err := t.framer.MsgReader()
	if err != nil {
		return nil, t.because(err)
	}
	return msgReader{r, t}, nil
}

// MsgWriter returns a writer for a new message, like the one of the Framer.
// When the process has exited, what the writer fails with says so.
func (t *Transport) MsgWriter() (io.WriteCloser, error) {
	w, err := t.framer.MsgWriter()
	if err != nil {
		return nil, t.because(err)
	}
	return msgWriter{w, t}, nil
}

// msgReader and msgWriter are the reader and the writer of a message, with
// errors that name the process when it has exited under them.
type (
	msgReader struct {
		io.ReadCloser
		t *Transport
	}
	msgWriter struct {
		io.WriteCloser
		t *Transport
	}
)

func (r msgReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	// the end of a message is not a failure
	if err != nil && err != io.EOF { //nolint:errorlint // what a reader ends with, never wrapped
		err = r.t.because(err)
	}
	return n, err
}

// Close reads the message to its end.  What that fails with is explained
// like the error of a Read: the session closes a message that nobody reads
// without reading it first.
func (r msgReader) Close() error {
	if err := r.ReadCloser.Close(); err != nil {
		return r.t.because(err)
	}
	return nil
}

func (w msgWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err != nil {
		err = w.t.because(err)
	}
	return n, err
}

func (w msgWriter) Close() error {
	if err := w.WriteCloser.Close(); err != nil {
		return w.t.because(err)
	}
	return nil
}

// because returns why IO on the transport failed with err: the process
// having exited, if it has, and err as it is otherwise.  The misuse of a
// reader or a writer is not the process's doing, and neither is a message
// that cannot be framed or was not.
func (t *Transport) because(err error) error {
	if t.closing.Load() {
		return err
	}
	for _, own := range []error{transport.ErrStreamBusy, transport.ErrInvalidIO, transport.ErrEndOfMsgInPayload, transport.ErrMalformedChunk} {
		if errors.Is(err, own) {
			return err
		}
	}

	// Wait gives a child that holds the standard error WaitDelay to be done
	// with it, and the exit status is known only then.  A process that is
	// still there after that is not waited for again by the next failure.
	select {
	case <-t.exited:
	default:
		if t.stayed.Load() {
			return err
		}
		select {
		case <-t.exited:
		case <-time.After(t.cmd.WaitDelay + exitGrace):
			t.stayed.Store(true)
			return err
		}
	}
	if t.closing.Load() {
		return err
	}
	return &exitError{name: filepath.Base(t.cmd.Path), err: t.exitErr, stderr: t.stderr.String()}
}

// exitError is the error of a transport whose process has exited by itself.
type exitError struct {
	name   string
	err    error // what Wait returned, nil for a process that exited with 0
	stderr string
}

func (e *exitError) Error() string {
	status := "exit status 0"
	if e.err != nil {
		status = e.err.Error()
	}
	if e.stderr == "" {
		return fmt.Sprintf("command: %s has exited: %s", e.name, status)
	}
	return fmt.Sprintf("command: %s has exited: %s: %s", e.name, status, e.stderr)
}

func (e *exitError) Unwrap() error { return e.err }
