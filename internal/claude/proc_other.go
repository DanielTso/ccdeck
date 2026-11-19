//go:build !linux

package claude

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

// SystemProcesses returns the ProcessChecker for this machine. Without
// /proc it can only check that the pid exists; procStart is ignored.
func SystemProcesses() ProcessChecker { return pidChecker{} }

type pidChecker struct{}

func (pidChecker) Alive(pid int, _ string) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = p.Release() }()
	if runtime.GOOS == "windows" {
		return true // FindProcess opened the process, so it exists
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
