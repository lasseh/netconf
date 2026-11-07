//go:build !unix

package command

import (
	"os"
	"os/exec"
)

// ownGroup does nothing here: there is no process group to give the process.
func ownGroup(*exec.Cmd) bool { return false }

// end kills p alone.
func end(p *os.Process, _ bool) error { return p.Kill() }
