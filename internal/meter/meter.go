// Package meter is the status-line meter. Claude Code passes a status-line
// command a JSON document on stdin with three things its files do not hold:
// its own context-window figure, its own running cost, and the account's
// rate-limit windows. This package decodes that document, keeps the numbers
// ccdeck needs in a small versioned state file per session, and reads those
// files back.
//
// Nothing but numbers is stored: no prompt, title, path or model name. The
// state directory is ccdeck's own, never the Claude directory.
package meter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Version is the version of the state file format. A file with another
// version is ignored.
const Version = 1

// Limits on what is read, so a surprising input or file cannot exhaust
// memory.
const (
	// MaxInput is the most of stdin the helper reads. Claude Code sends a
	// few kilobytes.
	MaxInput = 8 << 20
	maxFile  = 64 << 10 // a state file is a few hundred bytes
	maxIDLen = 128
)

// Window names, as core.RateLimit.Window spells them.
const (
	Window5h    = "5h"
	Window7d    = "7d"
	WindowSpend = "spend"
)

// Errors.
var (
	// ErrEmpty is returned by Decode for input with nothing in it.
	ErrEmpty = errors.New("meter: empty input")
	// ErrBadID is returned by Write for a session id that is not a plain
	// id and so cannot name a file.
	ErrBadID = errors.New("meter: session id is not a plain id")
	// errFormat marks a state file that is not one this version wrote.
	errFormat = errors.New("meter: not a state file")
)

// Report is what one status-line run recorded about a session.
type Report struct {
	SessionID string
	Meter     core.Meter
	Limits    []core.RateLimit
}

// Input is the decoded status-line input: the report, plus what the
// printed line shows but the state file does not keep.
type Input struct {
	Report
	ModelID   string // e.g. "claude-opus-5-5"
	ModelName string // e.g. "Opus"; sanitised, at most 40 runes
	HasCost   bool   // cost.total_cost_usd was present
}

// ValidID reports whether id is a plain session id that is safe as a file
// name: 1 to 128 ASCII letters, digits, '-' and '_'.
func ValidID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	for i := range len(id) {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Dir is the state directory: $XDG_STATE_HOME/ccdeck/meter, else
// ~/.local/state/ccdeck/meter.
func Dir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "ccdeck", "meter"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "ccdeck", "meter"), nil
}

// wireInput is the part of Claude Code's status-line input that is read.
// Every field is optional; numbers are num so that null, absent and a
// value of another type can be told from zero.
type wireInput struct {
	SessionID string `json:"session_id"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Cost struct {
		TotalCostUSD      num `json:"total_cost_usd"`
		TotalLinesAdded   num `json:"total_lines_added"`
		TotalLinesRemoved num `json:"total_lines_removed"`
	} `json:"cost"`
	ContextWindow struct {
		Size             num `json:"context_window_size"`
		UsedPercentage   num `json:"used_percentage"`
		TotalInputTokens num `json:"total_input_tokens"`
	} `json:"context_window"`
	RateLimits struct {
		FiveHour   *wireWindow `json:"five_hour"`
		SevenDay   *wireWindow `json:"seven_day"`
		SpendLimit *wireWindow `json:"spend_limit"`
	} `json:"rate_limits"`
}

type wireWindow struct {
	UsedPercentage num    `json:"used_percentage"`
	ResetsAt       num    `json:"resets_at"` // epoch seconds
	UsedUSD        num    `json:"used_usd"`
	LimitUSD       num    `json:"limit_usd"`
	Period         string `json:"period"`
}

// Decode reads Claude Code's status-line input, stamping the report with
// now. It is tolerant: unknown fields are ignored, a field of the wrong
// type is skipped, out-of-range numbers are clamped or dropped, and a
// session id that is not a plain id is dropped. Only input that is empty
// or not JSON at all is an error.
func Decode(data []byte, now time.Time) (Input, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Input{}, ErrEmpty
	}
	var w wireInput
	if err := json.Unmarshal(data, &w); err != nil {
		// A value of the wrong type leaves that field zero and the rest
		// decoded; anything else means the input is not usable.
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return Input{}, fmt.Errorf("meter: decode input: %w", err)
		}
	}
	in := Input{
		ModelID:   clean(w.Model.ID, 80),
		ModelName: clean(w.Model.DisplayName, 40),
	}
	if ValidID(w.SessionID) {
		in.SessionID = w.SessionID
	}
	m := &in.Meter
	m.At = now
	cw := w.ContextWindow
	if n, ok := number(cw.Size, 0, 1e9); ok {
		m.ContextWindow = int64(n)
	}
	if n, ok := number(cw.TotalInputTokens, 0, 1e10); ok {
		m.ContextTokens = int64(n)
	}
	// Claude Code reports a used percentage of 0 with no tokens, not null,
	// after a compaction until the next API response. A context is never
	// empty, so that is no figure.
	if p, ok := number(cw.UsedPercentage, 0, 100); ok && (p > 0 || m.ContextTokens > 0) {
		m.ContextPercent, m.HasContext = p, true
	}
	if c, ok := number(w.Cost.TotalCostUSD, 0, 1e7); ok {
		m.CostUSD, in.HasCost = c, true
	}
	if n, ok := number(w.Cost.TotalLinesAdded, 0, 1e9); ok {
		m.LinesAdded = int(n)
	}
	if n, ok := number(w.Cost.TotalLinesRemoved, 0, 1e9); ok {
		m.LinesRemoved = int(n)
	}
	rl := w.RateLimits
	for _, x := range []struct {
		name string
		w    *wireWindow
	}{{Window5h, rl.FiveHour}, {Window7d, rl.SevenDay}, {WindowSpend, rl.SpendLimit}} {
		if l, ok := x.w.limit(x.name); ok {
			in.Limits = append(in.Limits, l)
		}
	}
	return in, nil
}

// limit turns one input window into a RateLimit. A window without a used
// percentage is no window.
func (w *wireWindow) limit(name string) (core.RateLimit, bool) {
	if w == nil {
		return core.RateLimit{}, false
	}
	used, ok := number(w.UsedPercentage, 0, 1000)
	if !ok {
		return core.RateLimit{}, false
	}
	l := core.RateLimit{Window: name, UsedPercent: used}
	if s, ok := number(w.ResetsAt, 1, 1e11); ok {
		l.ResetsAt = time.Unix(int64(s), 0).UTC()
	}
	if name == WindowSpend {
		l.UsedUSD, _ = number(w.UsedUSD, 0, 1e9)
		l.LimitUSD, _ = number(w.LimitUSD, 0, 1e9)
		l.Period = period(w.Period)
	}
	return l, true
}

// num is a JSON number that may be missing. Null, a value of another type
// and a number out of float64 range all leave it unset, without failing
// the document.
type num struct {
	v  float64
	ok bool
}

// UnmarshalJSON implements json.Unmarshaler. It never fails.
func (n *num) UnmarshalJSON(b []byte) error {
	var f float64
	if b[0] != 'n' && json.Unmarshal(b, &f) == nil {
		n.v, n.ok = f, true
	}
	return nil
}

// number returns n clamped to [lo, hi]; false when n is unset.
func number(n num, lo, hi float64) (float64, bool) {
	if !n.ok {
		return 0, false
	}
	return min(max(n.v, lo), hi), true
}

// period keeps the spend periods Claude Code documents and drops anything
// else.
func period(s string) string {
	switch s {
	case "daily", "weekly", "monthly":
		return s
	}
	return ""
}

// clean removes control characters (escape sequences lose their ESC and
// show as harmless text) and cuts s to n runes.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r >= 0x7f && r < 0xa0 || r == 0xfffd {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// Current returns the windows of limits whose reset time has not passed at
// now. A window with no reset time is kept.
func Current(limits []core.RateLimit, now time.Time) []core.RateLimit {
	var out []core.RateLimit
	for _, l := range limits {
		if l.ResetsAt.IsZero() || l.ResetsAt.After(now) {
			out = append(out, l)
		}
	}
	return out
}

// Tightest is the current window with the highest use, the earlier reset
// breaking ties; false when there is none.
func Tightest(limits []core.RateLimit, now time.Time) (core.RateLimit, bool) {
	var best core.RateLimit
	found := false
	for _, l := range Current(limits, now) {
		switch {
		case !found, l.UsedPercent > best.UsedPercent:
		case l.UsedPercent == best.UsedPercent && !l.ResetsAt.IsZero() && (best.ResetsAt.IsZero() || l.ResetsAt.Before(best.ResetsAt)):
		default:
			continue
		}
		best, found = l, true
	}
	return best, found
}

// Merge combines the rate limits that several reports of one account
// hold, and says when the newest report its figures came from was
// written; nothing when no report has limits.
//
// A newer report is not necessarily a fresher figure: Claude Code takes
// its figures from API responses, and an idle session reports the ones it
// last saw on every refresh. So the windows are those of the newest report
// that has limits (a report without them says nothing about the account),
// and each window's figure is the freshest any report has for it: a later
// reset time is a later window, within one window use only rises (dollars
// for a spend limit, whose percentage falls when the limit is raised), and
// otherwise, such as when a reset time is unknown, the newer report wins.
// Expired windows are not dropped here; see Current.
func Merge(reports []Report) (limits []core.RateLimit, at time.Time) {
	var withLimits []Report
	for _, r := range reports {
		if len(r.Limits) > 0 {
			withLimits = append(withLimits, r)
		}
	}
	if len(withLimits) == 0 {
		return nil, time.Time{}
	}
	slices.SortStableFunc(withLimits, func(a, b Report) int {
		if c := b.Meter.At.Compare(a.Meter.At); c != 0 {
			return c
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	for _, first := range withLimits[0].Limits {
		if slices.ContainsFunc(limits, func(l core.RateLimit) bool { return l.Window == first.Window }) {
			continue // a window named twice is merged once
		}
		best, from := first, withLimits[0].Meter.At
		for _, r := range withLimits {
			for _, l := range r.Limits {
				if l.Window == best.Window && fresher(l, best) {
					best, from = l, r.Meter.At
				}
			}
		}
		limits = append(limits, best)
		if from.After(at) {
			at = from
		}
	}
	return limits, at
}

// fresher reports whether l, from an older report, is a fresher figure for
// its window than cur: a later window, or more of the same one.
func fresher(l, cur core.RateLimit) bool {
	if l.ResetsAt.IsZero() || cur.ResetsAt.IsZero() {
		return false
	}
	if !l.ResetsAt.Equal(cur.ResetsAt) {
		return l.ResetsAt.After(cur.ResetsAt)
	}
	if l.Window == WindowSpend && l.UsedUSD > 0 && cur.UsedUSD > 0 {
		return l.UsedUSD > cur.UsedUSD
	}
	return l.UsedPercent > cur.UsedPercent
}

// The state file.

// wireFile is the state file. Field names are short and stable; Version
// changes when their meaning does.
type wireFile struct {
	V       int         `json:"v"`
	Session string      `json:"session"`
	At      time.Time   `json:"at"`
	Context wireContext `json:"context"`
	Cost    wireCost    `json:"cost"`
	Limits  []wireLimit `json:"limits,omitempty"`
}

type wireContext struct {
	Has     bool    `json:"has"`
	Percent float64 `json:"percent"`
	Window  int64   `json:"window"`
	Tokens  int64   `json:"tokens"`
}

type wireCost struct {
	USD          float64 `json:"usd"`
	LinesAdded   int     `json:"linesAdded"`
	LinesRemoved int     `json:"linesRemoved"`
}

type wireLimit struct {
	Window   string    `json:"window"`
	Used     float64   `json:"used"`
	ResetsAt time.Time `json:"resetsAt,omitzero"`
	UsedUSD  float64   `json:"usedUSD,omitempty"`
	LimitUSD float64   `json:"limitUSD,omitempty"`
	Period   string    `json:"period,omitempty"`
}

func encode(r Report) ([]byte, error) {
	m := r.Meter
	f := wireFile{
		V:       Version,
		Session: r.SessionID,
		At:      m.At.UTC(),
		Context: wireContext{Has: m.HasContext, Percent: m.ContextPercent, Window: m.ContextWindow, Tokens: m.ContextTokens},
		Cost:    wireCost{USD: m.CostUSD, LinesAdded: m.LinesAdded, LinesRemoved: m.LinesRemoved},
	}
	for _, l := range r.Limits {
		f.Limits = append(f.Limits, wireLimit{
			Window: l.Window, Used: l.UsedPercent, ResetsAt: l.ResetsAt.UTC(),
			UsedUSD: l.UsedUSD, LimitUSD: l.LimitUSD, Period: l.Period,
		})
	}
	data, err := json.Marshal(f)
	return append(data, '\n'), err
}

// decodeFile parses a state file, re-checking every value as Decode does,
// since the file is outside ccdeck's control once written.
func decodeFile(data []byte) (Report, error) {
	var f wireFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Report{}, fmt.Errorf("%w: %w", errFormat, err)
	}
	if f.V != Version || !ValidID(f.Session) || f.At.IsZero() {
		return Report{}, errFormat
	}
	clamp := func(v, lo, hi float64) float64 { return min(max(v, lo), hi) }
	r := Report{SessionID: f.Session, Meter: core.Meter{
		At:             f.At,
		HasContext:     f.Context.Has,
		ContextPercent: clamp(f.Context.Percent, 0, 100),
		ContextWindow:  int64(clamp(float64(f.Context.Window), 0, 1e9)),
		ContextTokens:  int64(clamp(float64(f.Context.Tokens), 0, 1e10)),
		CostUSD:        clamp(f.Cost.USD, 0, 1e7),
		LinesAdded:     int(clamp(float64(f.Cost.LinesAdded), 0, 1e9)),
		LinesRemoved:   int(clamp(float64(f.Cost.LinesRemoved), 0, 1e9)),
	}}
	for _, l := range f.Limits {
		switch l.Window {
		case Window5h, Window7d, WindowSpend:
		default:
			continue
		}
		r.Limits = append(r.Limits, core.RateLimit{
			Window: l.Window, UsedPercent: clamp(l.Used, 0, 1000), ResetsAt: l.ResetsAt,
			UsedUSD: clamp(l.UsedUSD, 0, 1e9), LimitUSD: clamp(l.LimitUSD, 0, 1e9), Period: period(l.Period),
		})
	}
	return r, nil
}

// fileName is the state file of a session.
func fileName(id string) string { return id + ".json" }

// tempPrefix starts every temporary file, so readers can skip them and
// Cleanup can find the ones a killed run left behind.
const tempPrefix = ".tmp-"

// Write records r as the state file of r.SessionID in dir, creating dir
// with mode 0700 when needed. The file has mode 0600 and is replaced by a
// rename, so a reader, or a run killed at any point, sees the old file or
// the new one and never a partial one. A killed run can leave a temporary
// file behind, which readers ignore and Cleanup removes.
func Write(dir string, r Report) error { return write(dir, r, nil) }

// stage names a point in write where a test can stop it, as a kill would.
type stage int

const (
	stageCreated stage = iota // the temporary file exists, empty
	stagePartial              // half the data is written
	stageWritten              // all data is written, the file is open
	stageClosed               // the file is closed, not yet renamed
)

var errStopped = errors.New("meter: stopped")

// write is Write with a test hook: when stop returns true at a stage,
// write returns at once, leaving everything as a kill would.
func write(dir string, r Report, stop func(stage) bool) error {
	if !ValidID(r.SessionID) {
		return ErrBadID
	}
	data, err := encode(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tempPrefix+r.SessionID+"-*") // mode 0600
	if err != nil {
		return err
	}
	at := func(s stage) bool { return stop != nil && stop(s) }
	if at(stageCreated) {
		return errStopped
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	half := len(data) / 2
	if _, err := f.Write(data[:half]); err != nil {
		return fail(err)
	}
	if at(stagePartial) {
		return errStopped
	}
	if _, err := f.Write(data[half:]); err != nil {
		return fail(err)
	}
	if at(stageWritten) {
		return errStopped
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if at(stageClosed) {
		return errStopped
	}
	if err := os.Rename(tmp, filepath.Join(dir, fileName(r.SessionID))); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Read reads one state file.
func Read(path string) (Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = f.Close() }() // read-only
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return Report{}, err
	}
	if len(data) > maxFile {
		return Report{}, fmt.Errorf("%w: larger than %d bytes", errFormat, maxFile)
	}
	return decodeFile(data)
}

// stateID returns the session id a directory entry is the state file of.
func stateID(e os.DirEntry) (string, bool) {
	id, ok := strings.CutSuffix(e.Name(), ".json")
	return id, ok && e.Type().IsRegular() && ValidID(id)
}

// ReadDir reads every state file in dir. Files that cannot be read, are
// not state files, or name another session than their own are skipped.
// The error is that of reading the directory, such as fs.ErrNotExist when
// the helper has never run.
func ReadDir(dir string) ([]Report, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Report
	for _, e := range ents {
		id, ok := stateID(e)
		if !ok {
			continue
		}
		r, err := Read(filepath.Join(dir, e.Name()))
		if err != nil || r.SessionID != id {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Retention.
const (
	// KeepFor is how long the state file of a session that is no longer
	// live is kept.
	KeepFor = 30 * 24 * time.Hour
	// tempKeepFor is how long a temporary file is left alone: longer than
	// any run, so a write in progress is never removed under it.
	tempKeepFor = time.Hour
)

// Cleanup removes from dir the state files older than KeepFor whose
// session live does not claim, and temporary files left by killed runs. A
// file's age is its report's time, or its modification time when it cannot
// be read. Only files this package writes are touched. It returns how many
// files it removed.
func Cleanup(dir string, now time.Time, live func(id string) bool) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if strings.HasPrefix(e.Name(), tempPrefix) {
			if now.Sub(info.ModTime()) > tempKeepFor && os.Remove(path) == nil {
				n++
			}
			continue
		}
		id, ok := stateID(e)
		if !ok || live != nil && live(id) {
			continue
		}
		at := info.ModTime()
		if r, err := Read(path); err == nil {
			at = r.Meter.At
		}
		if now.Sub(at) > KeepFor && os.Remove(path) == nil {
			n++
		}
	}
	return n
}
