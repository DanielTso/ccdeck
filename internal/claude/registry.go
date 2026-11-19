package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Registration is one entry of the live session registry: a running Claude
// Code process and the session it owns.
type Registration struct {
	SessionID string
	CWD       string // the process's working directory
	Version   string // Claude Code version of the process
	ProcStart string // the process start time as Claude Code recorded it
	UpdatedAt time.Time
	Live      core.Live
}

// ProcessChecker decides whether a registry entry's process is still
// running. Tests substitute a fake.
type ProcessChecker interface {
	// Alive reports whether pid is running and is the process that started
	// at procStart (field 22 of /proc/<pid>/stat on Linux). An empty
	// procStart checks the pid alone.
	Alive(pid int, procStart string) bool
}

// registryName matches registry files. Anything else in the directory,
// in particular the <pid>.<hash>.key files, is never opened.
var registryName = regexp.MustCompile(`^[0-9]+\.json$`)

// ReadRegistry reads the registry files in dir (Paths.SessionsDir) and
// returns the entries whose process is alive, one per session (the most
// recently updated wins). Unreadable or malformed files are skipped, and a
// missing dir means no live sessions.
func ReadRegistry(dir string, pc ProcessChecker) ([]Registration, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	bySession := map[string]int{}
	var regs []Registration
	for _, e := range ents {
		if e.IsDir() || !registryName.MatchString(e.Name()) {
			continue
		}
		r, ok := readRegistration(filepath.Join(dir, e.Name()))
		if !ok || !pc.Alive(r.Live.PID, r.ProcStart) {
			continue
		}
		if i, dup := bySession[r.SessionID]; dup {
			if r.UpdatedAt.After(regs[i].UpdatedAt) {
				regs[i] = r
			}
			continue
		}
		bySession[r.SessionID] = len(regs)
		regs = append(regs, r)
	}
	return regs, nil
}

func readRegistration(path string) (Registration, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Registration{}, false
	}
	var w struct {
		PID             int    `json:"pid"`
		SessionID       string `json:"sessionId"`
		CWD             string `json:"cwd"`
		StartedAt       int64  `json:"startedAt"`
		ProcStart       any    `json:"procStart"`
		Version         string `json:"version"`
		Kind            string `json:"kind"`
		Name            string `json:"name"`
		Status          string `json:"status"`
		WaitingFor      string `json:"waitingFor"`
		UpdatedAt       int64  `json:"updatedAt"`
		StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	}
	if decodeTolerant(data, &w) != nil || w.PID <= 0 || w.SessionID == "" {
		return Registration{}, false
	}
	return Registration{
		SessionID: w.SessionID,
		CWD:       w.CWD,
		Version:   w.Version,
		ProcStart: procStartString(w.ProcStart),
		UpdatedAt: millis(w.UpdatedAt),
		Live: core.Live{
			PID:         w.PID,
			Name:        w.Name,
			Kind:        w.Kind,
			Status:      liveStatus(w.Status),
			WaitingFor:  w.WaitingFor,
			StartedAt:   millis(w.StartedAt),
			StatusSince: millis(w.StatusUpdatedAt),
		},
	}, true
}

// procStartString accepts procStart as a string (observed) or a number.
func procStartString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func liveStatus(s string) core.Status {
	switch core.Status(s) {
	case core.StatusBusy, core.StatusIdle, core.StatusWaiting:
		return core.Status(s)
	}
	return core.StatusUnknown
}

func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
