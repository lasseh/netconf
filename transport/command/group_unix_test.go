// syscall has no Getpgid on AIX, Solaris and illumos.
//go:build unix && !aix && !solaris

package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
)

// childOf starts a transport over a process that starts a child of its own,
// and returns the transport and the pid of the child.
func childOf(t *testing.T) (tr *Transport, child int) {
	t.Helper()
	pidfile := filepath.Join(t.TempDir(), "pid")
	tr, err := Start(helperCommand(t, "parent "+pidfile))
	be.NilErr(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, err := os.ReadFile(pidfile)
		if err != nil || !strings.HasSuffix(string(b), "\n") {
			continue
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(b)))
		be.NilErr(t, err)
		return tr, child
	}
	t.Fatal("the process did not say what its child is")
	return nil, 0
}

// ended reports whether the process pid is gone within a while: killed,
// and reaped by whoever it was left to.
func ended(pid int) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
	}
	return false
}

// A failed Open leaves nothing behind that the process started, the ssh -W
// of a jump host, however many times it fails.  The child outlives the test
// unless it is killed.
func TestCloseEndsWhatTheProcessStarted(t *testing.T) {
	children := make([]int, 0, 3)
	for range 3 {
		tr, child := childOf(t)
		children = append(children, child)

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		_, err := netconf.Open(ctx, tr)
		cancel()
		be.True(t, errors.Is(err, netconf.ErrHello))
	}
	for _, child := range children {
		be.True(t, ended(child))
	}
}

// The child goes with the process when the process has gone first.
func TestCloseAfterTheProcessExited(t *testing.T) {
	tr, child := childOf(t)
	be.NilErr(t, tr.cmd.Process.Kill())
	<-tr.exited

	be.NilErr(t, tr.Close())
	be.True(t, ended(child))
}

// Start gives the process a group of its own, which it leads, and keeps what
// the caller set in SysProcAttr.  A group the caller chose is not replaced,
// and one that the process does not lead is not killed.
func TestStartSysProcAttr(t *testing.T) {
	ours, err := syscall.Getpgid(os.Getpid())
	be.NilErr(t, err)

	for _, tc := range []struct {
		name   string
		attr   *syscall.SysProcAttr
		leads  bool
		killed bool // the group is what Close kills
	}{
		{"none", nil, true, true},
		{"a group of its own", &syscall.SysProcAttr{Setpgid: true}, true, true},
		{"the group of the test", &syscall.SysProcAttr{Setpgid: true, Pgid: ours}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := helperCommand(t, "silent")
			cmd.SysProcAttr = tc.attr
			tr, err := Start(cmd)
			be.NilErr(t, err)
			t.Cleanup(func() { _ = tr.Close() })

			if tc.attr != nil {
				// the caller's is kept, and not written to
				be.False(t, tc.attr.Setsid)
				be.True(t, cmd.SysProcAttr.Setpgid)
				be.Equal(t, tc.attr.Pgid, cmd.SysProcAttr.Pgid)
			}
			pgid, err := syscall.Getpgid(cmd.Process.Pid)
			be.NilErr(t, err)
			be.Equal(t, tc.leads, pgid == cmd.Process.Pid)
			be.Equal(t, tc.killed, tr.group)
			be.NilErr(t, tr.Close())
			be.True(t, gone(cmd))
		})
	}
}
