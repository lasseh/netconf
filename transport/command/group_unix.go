//go:build unix

package command

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup gives the process a session of its own, and so a process group
// that Close kills with everything in it: what ssh starts, the ssh of a jump
// host or a ProxyCommand, outlives ssh otherwise.  It reports whether the
// process leads a group of its own, the one to kill.
//
// A process group the caller asked for in cmd.SysProcAttr stays as it is, as
// a process that leads a session cannot join one.  The caller's SysProcAttr
// is copied, and not written to.
func ownGroup(cmd *exec.Cmd) bool {
	attr := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		own := *cmd.SysProcAttr
		attr = &own
	}
	if !attr.Setpgid && !attr.Foreground {
		attr.Setsid = true
	}
	cmd.SysProcAttr = attr
	return attr.Setsid || attr.Pgid == 0
}

// end kills p, and with group the process group that p leads.  The group
// outlives p while anything in it is left, so it is killed after p is gone.
func end(p *os.Process, group bool) error {
	if !group {
		return p.Kill()
	}
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
