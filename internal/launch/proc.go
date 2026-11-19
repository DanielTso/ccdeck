package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Procs reads the process tree.
type Procs interface {
	// Parent returns the parent pid of pid.
	Parent(pid int) (int, error)
}

// procFS is Procs backed by a /proc-style directory.
type procFS struct{ root string }

// systemProcs reads the real /proc. On systems without one every lookup fails
// and Focus reports that the session cannot be reached.
func systemProcs() Procs { return procFS{root: "/proc"} }

func (p procFS) Parent(pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	return parentFromStat(string(data))
}

// parentFromStat extracts the parent pid, field 4, from the contents of
// /proc/<pid>/stat. The command name (field 2) is in parentheses and may
// itself contain spaces and parentheses, so fields are counted from the last
// closing parenthesis.
func parentFromStat(stat string) (int, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, fmt.Errorf("malformed stat line %q", stat)
	}
	fields := strings.Fields(stat[i+1:]) // state, ppid, ...
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed stat line %q", stat)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf("malformed stat line %q: %w", stat, err)
	}
	return ppid, nil
}
