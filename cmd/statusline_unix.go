//go:build unix

package cmd

import (
	"os/exec"
	"syscall"
)

// ownGroup starts c in a process group of its own and makes cancelling it
// kill the whole group, so a wrapped command that started children of its
// own leaves nothing running when it is cut off.
func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
}

// killGroup kills what is left of c's process group once c has been
// waited for, so that a command that failed leaves nothing it started
// running. While any member is left the group keeps its id; when none is,
// the id could only name another group after the kernel's pid counter
// wrapped around in the moment since Wait returned.
func killGroup(c *exec.Cmd) { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
