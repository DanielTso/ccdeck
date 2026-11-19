//go:build linux

package claude

import (
	"bytes"
	"os"
	"strconv"
)

// SystemProcesses returns the ProcessChecker for this machine. On Linux it
// reads /proc/<pid>/stat and compares the process start time, so a registry
// file whose pid has been reused by another process is not live.
func SystemProcesses() ProcessChecker { return procChecker{root: "/proc"} }

type procChecker struct{ root string }

func (p procChecker) Alive(pid int, procStart string) bool {
	stat, err := os.ReadFile(p.root + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	if procStart == "" {
		return true
	}
	start, ok := statStartTime(stat)
	return ok && start == procStart
}

// statStartTime returns field 22 (starttime) of a /proc/<pid>/stat line.
// The command name (field 2) is in parentheses and may itself contain
// spaces and parentheses, so fields are counted from the last ')'.
func statStartTime(stat []byte) (string, bool) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return "", false
	}
	fields := bytes.Fields(stat[i+1:]) // fields[0] is field 3
	const idx = 22 - 3
	if len(fields) <= idx {
		return "", false
	}
	return string(fields[idx]), true
}
