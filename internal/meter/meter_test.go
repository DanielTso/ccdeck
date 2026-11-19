package meter

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const id = "0a1b2c3d-0000-4000-8000-000000000001"

// sample is a status-line input as Claude Code documents it, with every
// field this package reads and several it ignores.
const sample = `{
  "cwd": "/invented/project",
  "session_id": "` + id + `",
  "session_name": "invented-name",
  "transcript_path": "/invented/transcript.jsonl",
  "model": {"id": "claude-opus-5-5", "display_name": "Opus"},
  "workspace": {"current_dir": "/invented/project", "project_dir": "/invented/project", "added_dirs": []},
  "version": "2.1.90",
  "output_style": {"name": "default"},
  "cost": {"total_cost_usd": 1.2345, "total_duration_ms": 45000, "total_api_duration_ms": 2300,
           "total_lines_added": 156, "total_lines_removed": 23},
  "context_window": {"total_input_tokens": 15500, "total_output_tokens": 1200, "context_window_size": 200000,
                     "used_percentage": 8, "remaining_percentage": 92,
                     "current_usage": {"input_tokens": 8500, "output_tokens": 1200,
                                       "cache_creation_input_tokens": 5000, "cache_read_input_tokens": 2000}},
  "exceeds_200k_tokens": false,
  "rate_limits": {
    "five_hour": {"used_percentage": 23.5, "resets_at": 1772377200},
    "seven_day": {"used_percentage": 41.2, "resets_at": 1772809200},
    "spend_limit": {"used_percentage": 62.8, "resets_at": 1774990800, "used_usd": 314.12, "limit_usd": 500, "period": "monthly"}
  },
  "vim": {"mode": "NORMAL"}
}`

func TestDecodeDocumentedSample(t *testing.T) {
	in, err := Decode([]byte(sample), now)
	if err != nil {
		t.Fatal(err)
	}
	want := Input{
		Report: Report{
			SessionID: id,
			Meter: core.Meter{
				At: now, HasContext: true, ContextPercent: 8, ContextWindow: 200_000, ContextTokens: 15_500,
				CostUSD: 1.2345, LinesAdded: 156, LinesRemoved: 23,
			},
			Limits: []core.RateLimit{
				{Window: "5h", UsedPercent: 23.5, ResetsAt: time.Unix(1772377200, 0).UTC()},
				{Window: "7d", UsedPercent: 41.2, ResetsAt: time.Unix(1772809200, 0).UTC()},
				{Window: "spend", UsedPercent: 62.8, ResetsAt: time.Unix(1774990800, 0).UTC(), UsedUSD: 314.12, LimitUSD: 500, Period: "monthly"},
			},
		},
		ModelID: "claude-opus-5-5", ModelName: "Opus", HasCost: true,
	}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("Decode =\n%+v\nwant\n%+v", in, want)
	}
}

func TestDecodeTolerance(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, in Input)
	}{
		{"empty object", `{}`, func(t *testing.T, in Input) {
			if in.SessionID != "" || in.Meter.HasContext || in.HasCost || len(in.Limits) != 0 || !in.Meter.At.Equal(now) {
				t.Errorf("%+v", in)
			}
		}},
		{"null document", `null`, func(t *testing.T, in Input) {
			if in.SessionID != "" || in.Meter.HasContext {
				t.Errorf("%+v", in)
			}
		}},
		{"nulls everywhere", `{"session_id":null,"model":null,"cost":null,"context_window":{"used_percentage":null,"context_window_size":null},"rate_limits":{"five_hour":null,"seven_day":{"used_percentage":null,"resets_at":5}}}`,
			func(t *testing.T, in Input) {
				if in.Meter.HasContext || in.HasCost || len(in.Limits) != 0 {
					t.Errorf("%+v", in)
				}
			}},
		{"null used percentage keeps the window size", `{"session_id":"` + id + `","context_window":{"used_percentage":null,"context_window_size":200000,"total_input_tokens":0}}`,
			func(t *testing.T, in Input) {
				if in.Meter.HasContext || in.Meter.ContextWindow != 200_000 || in.SessionID != id {
					t.Errorf("%+v", in)
				}
			}},
		{"wrong types are skipped, the rest kept", `{"session_id":42,"model":"opus","cost":{"total_cost_usd":"lots","total_lines_added":3},"context_window":{"used_percentage":50},"rate_limits":[1,2]}`,
			func(t *testing.T, in Input) {
				if in.SessionID != "" || in.HasCost || in.Meter.LinesAdded != 3 || !in.Meter.HasContext || in.Meter.ContextPercent != 50 {
					t.Errorf("%+v", in)
				}
			}},
		{"out of range numbers are clamped", `{"cost":{"total_cost_usd":-4},"context_window":{"used_percentage":250,"context_window_size":-1,"total_input_tokens":1e300},"rate_limits":{"five_hour":{"used_percentage":-3,"resets_at":1e300},"spend_limit":{"used_percentage":130,"period":"\u001b[31mhourly"}}}`,
			func(t *testing.T, in Input) {
				m := in.Meter
				if m.CostUSD != 0 || m.ContextPercent != 100 || m.ContextWindow != 0 || m.ContextTokens != 1e10 {
					t.Errorf("meter %+v", m)
				}
				if len(in.Limits) != 2 || in.Limits[0].UsedPercent != 0 || !in.Limits[0].ResetsAt.Equal(time.Unix(1e11, 0)) ||
					in.Limits[1].UsedPercent != 130 || in.Limits[1].Period != "" {
					t.Errorf("limits %+v", in.Limits)
				}
			}},
		{"control characters leave the model name", `{"model":{"display_name":"Op\u001b[2Jus\n\u0007 5"}}`,
			func(t *testing.T, in Input) {
				if in.ModelName != "Op[2Jus 5" {
					t.Errorf("ModelName = %q", in.ModelName)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := Decode([]byte(tt.input), now)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, in)
		})
	}

	for name, input := range map[string]string{"empty": "", "blank": " \n\t", "garbage": "not json at all", "truncated": sample[:200]} {
		if _, err := Decode([]byte(input), now); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := Decode(nil, now); !errors.Is(err, ErrEmpty) {
		t.Errorf("Decode(nil) = %v, want ErrEmpty", err)
	}
}

func TestHostileSessionIDsAreDropped(t *testing.T) {
	for _, bad := range []string{"", "../escape", "a/b", `a\b`, "..", ".", "id\x00", "id\n", "id\x1b[0m", "ïd", "a b", strings.Repeat("a", 129)} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
		input := `{"session_id":` + quote(bad) + `}`
		in, err := Decode([]byte(input), now)
		if err != nil {
			t.Fatalf("%q: %v", bad, err)
		}
		if in.SessionID != "" {
			t.Errorf("Decode kept session id %q", bad)
		}
		if err := Write(t.TempDir(), Report{SessionID: bad}); !errors.Is(err, ErrBadID) {
			t.Errorf("Write(%q) = %v, want ErrBadID", bad, err)
		}
	}
	for _, good := range []string{id, "abc", "A_b-9", strings.Repeat("a", 128)} {
		if !ValidID(good) {
			t.Errorf("ValidID(%q) = false", good)
		}
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteString(`\` + string(r))
		case r < 0x20:
			b.WriteString(`\u00` + "0123456789abcdef"[r>>4:r>>4+1] + "0123456789abcdef"[r&15:r&15+1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String() + `"`
}

func report(t *testing.T) Report {
	t.Helper()
	in, err := Decode([]byte(sample), now)
	if err != nil {
		t.Fatal(err)
	}
	return in.Report
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "ccdeck", "meter")
	r := report(t)
	if err := Write(dir, r); err != nil {
		t.Fatal(err)
	}
	got, err := Read(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("Read =\n%+v\nwant\n%+v", got, r)
	}
	for path, want := range map[string]fs.FileMode{dir: 0o700, filepath.Join(dir, id+".json"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&^want != 0 {
			t.Errorf("%s has mode %v, want at most %v", path, fi.Mode().Perm(), want)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"invented", "Opus", "claude-opus", "transcript", "2.1.90"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("state file holds %q:\n%s", leak, data)
		}
	}
}

// A run killed at any step of a write leaves the previous file whole, and
// readers skip what it left behind.
func TestInterruptedWriteKeepsOldFile(t *testing.T) {
	for _, at := range []stage{stageCreated, stagePartial, stageWritten, stageClosed} {
		dir := t.TempDir()
		old := report(t)
		if err := Write(dir, old); err != nil {
			t.Fatal(err)
		}
		next := old
		next.Meter.At = now.Add(time.Minute)
		next.Meter.CostUSD = 9.99
		next.Limits = nil
		if err := write(dir, next, func(s stage) bool { return s == at }); !errors.Is(err, errStopped) {
			t.Fatalf("stage %d: write = %v", at, err)
		}
		got, err := Read(filepath.Join(dir, id+".json"))
		if err != nil || !reflect.DeepEqual(got, old) {
			t.Errorf("stage %d: Read = %+v, %v; want the old report", at, got, err)
		}
		all, err := ReadDir(dir)
		if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], old) {
			t.Errorf("stage %d: ReadDir = %+v, %v; want the old report alone", at, all, err)
		}
		ents, _ := os.ReadDir(dir)
		if len(ents) != 2 {
			t.Errorf("stage %d: want the state file and one temporary file, have %d entries", at, len(ents))
		}
		// The next run completes and replaces the file in one step.
		if err := Write(dir, next); err != nil {
			t.Fatal(err)
		}
		if got, err := Read(filepath.Join(dir, id+".json")); err != nil || !reflect.DeepEqual(got, next) {
			t.Errorf("stage %d: after a complete write Read = %+v, %v", at, got, err)
		}
	}
}

func TestWriteFailures(t *testing.T) {
	r := report(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(file, r); err == nil {
		t.Error("state directory is a file: want an error")
	}
	if err := Write(filepath.Join(file, "below"), r); err == nil {
		t.Error("state directory below a file: want an error")
	}
	if os.Geteuid() != 0 {
		ro := t.TempDir()
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		if err := Write(ro, r); err == nil {
			t.Error("read-only state directory: want an error")
		}
		if ents, _ := os.ReadDir(ro); len(ents) != 0 {
			t.Errorf("read-only directory gained %d entries", len(ents))
		}
	}
}

func TestReadDirSkipsWhatIsNotAStateFile(t *testing.T) {
	dir := t.TempDir()
	r := report(t)
	if err := Write(dir, r); err != nil {
		t.Fatal(err)
	}
	other := r
	other.SessionID = "other-session"
	otherData, err := encode(other)
	if err != nil {
		t.Fatal(err)
	}
	future, _ := encode(r)
	future = []byte(strings.Replace(string(future), `"v":1`, `"v":99`, 1))
	files := map[string][]byte{
		"garbage.json":        []byte("{not json"),
		"empty.json":          nil,
		"renamed.json":        otherData, // names another session
		"future.json":         future,
		"huge.json":           []byte(`{"v":1,"pad":"` + strings.Repeat("x", maxFile) + `"}`),
		"not-a-state-file.md": otherData,
		"bad name!.json":      otherData,
		tempPrefix + "x-123":  otherData,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != id {
		t.Errorf("ReadDir = %+v, want the one good report", got)
	}
	if _, err := ReadDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing directory: err = %v, want ErrNotExist", err)
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	put := func(sid string, at time.Time) {
		t.Helper()
		r := report(t)
		r.SessionID, r.Meter.At = sid, at
		if err := Write(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	put("old-ended", now.Add(-31*24*time.Hour))
	put("old-live", now.Add(-40*24*time.Hour))
	put("recent-ended", now.Add(-29*24*time.Hour))
	oldTemp := filepath.Join(dir, tempPrefix+"x-1")
	newTemp := filepath.Join(dir, tempPrefix+"x-2")
	unreadable := filepath.Join(dir, "unreadable.json")
	foreign := filepath.Join(dir, "notes.txt")
	for _, p := range []string{oldTemp, newTemp, unreadable, foreign} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long := now.Add(-60 * 24 * time.Hour)
	for _, p := range []string{oldTemp, unreadable, foreign} {
		if err := os.Chtimes(p, long, long); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(newTemp, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	n := Cleanup(dir, now, func(sid string) bool { return sid == "old-live" })
	if n != 3 {
		t.Errorf("Cleanup removed %d files, want 3", n)
	}
	for name, kept := range map[string]bool{
		"old-ended.json": false, "old-live.json": true, "recent-ended.json": true,
		tempPrefix + "x-1": false, tempPrefix + "x-2": true, "unreadable.json": false, "notes.txt": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if exists := err == nil; exists != kept {
			t.Errorf("%s: exists = %v, want %v", name, exists, kept)
		}
	}
	if n := Cleanup(filepath.Join(dir, "missing"), now, nil); n != 0 {
		t.Errorf("missing directory: removed %d", n)
	}
}

func TestCurrentAndTightest(t *testing.T) {
	l := func(w string, used float64, resets time.Duration) core.RateLimit {
		r := core.RateLimit{Window: w, UsedPercent: used}
		if resets != 0 {
			r.ResetsAt = now.Add(resets)
		}
		return r
	}
	limits := []core.RateLimit{l("5h", 95, -time.Minute), l("7d", 40, 48*time.Hour), l("spend", 40, time.Hour), l("x", 10, 0)}
	cur := Current(limits, now)
	if len(cur) != 3 || cur[0].Window != "7d" {
		t.Errorf("Current = %+v; want the expired 5h window dropped", cur)
	}
	got, ok := Tightest(limits, now)
	if !ok || got.Window != "spend" {
		t.Errorf("Tightest = %+v, %v; want spend, which ties 7d and resets sooner", got, ok)
	}
	if _, ok := Tightest(limits[:1], now); ok {
		t.Error("only an expired window: want none")
	}
	if _, ok := Tightest(nil, now); ok {
		t.Error("no windows: want none")
	}
	// The boundary: a window resetting exactly now has reset.
	if cur := Current([]core.RateLimit{l("5h", 50, 0)}, now); len(cur) != 1 {
		t.Error("a window without a reset time is current")
	}
	at := core.RateLimit{Window: "5h", UsedPercent: 50, ResetsAt: now}
	if cur := Current([]core.RateLimit{at}, now); len(cur) != 0 {
		t.Error("a window resetting now is gone")
	}
}

// Merge takes the windows of the newest report with limits and, for each,
// the freshest figure any report has: a later reset time is a later
// window, and within one window use only rises.
func TestMerge(t *testing.T) {
	h1, h2 := now.Add(time.Hour), now.Add(2*time.Hour)
	rep := func(sid string, age time.Duration, limits ...core.RateLimit) Report {
		return Report{SessionID: sid, Meter: core.Meter{At: now.Add(-age)}, Limits: limits}
	}
	five := func(used float64, resets time.Time) core.RateLimit {
		return core.RateLimit{Window: Window5h, UsedPercent: used, ResetsAt: resets}
	}
	spend := func(used, usd, limit float64) core.RateLimit {
		return core.RateLimit{Window: WindowSpend, UsedPercent: used, ResetsAt: h2, UsedUSD: usd, LimitUSD: limit, Period: "monthly"}
	}
	week := core.RateLimit{Window: Window7d, UsedPercent: 40, ResetsAt: now.Add(72 * time.Hour)}
	tests := []struct {
		name    string
		reports []Report
		want    []core.RateLimit
		wantAge time.Duration // of the newest report the figures came from
	}{
		{"none", nil, nil, -1},
		{"reports without limits", []Report{rep("a", 0), rep("b", time.Second)}, nil, -1},
		{"one report", []Report{rep("a", time.Minute, five(30, h1), week)}, []core.RateLimit{five(30, h1), week}, time.Minute},
		{"a newer report without limits says nothing", []Report{rep("a", time.Minute, five(30, h1)), rep("b", 0)},
			[]core.RateLimit{five(30, h1)}, time.Minute},
		{"same reset: the highest use, from an older report", []Report{rep("idle", 0, five(20, h1)), rep("active", time.Second, five(85, h1))},
			[]core.RateLimit{five(85, h1)}, time.Second},
		{"same reset and use: the newer report", []Report{rep("a", time.Second, five(50, h1)), rep("b", 0, five(50, h1))},
			[]core.RateLimit{five(50, h1)}, 0},
		{"a later reset is a later window", []Report{rep("stale", 0, five(90, h1)), rep("fresh", time.Second, five(5, h2))},
			[]core.RateLimit{five(5, h2)}, time.Second},
		{"an unknown reset: the newer report", []Report{rep("a", 0, five(20, time.Time{})), rep("b", time.Second, five(85, h1))},
			[]core.RateLimit{five(20, time.Time{})}, 0},
		{"windows come from the newest report", []Report{rep("old", time.Hour, five(10, h1), week), rep("new", 0, spend(10, 50, 500))},
			[]core.RateLimit{spend(10, 50, 500)}, 0},
		{"each window on its own", []Report{rep("a", 0, five(20, h1), week), rep("b", time.Second, five(85, h1), core.RateLimit{Window: Window7d, UsedPercent: 10, ResetsAt: week.ResetsAt})},
			[]core.RateLimit{five(85, h1), week}, 0},
		{"a window named twice is merged once", []Report{rep("a", 0, five(99, now.Add(-time.Minute)), week, five(84, h1))},
			[]core.RateLimit{five(84, h1), week}, 0},
		{"spend: the most dollars, though a raised limit lowers the percentage",
			[]Report{rep("old limit", 0, spend(80, 400, 500)), rep("raised", time.Second, spend(41, 410, 1000))},
			[]core.RateLimit{spend(41, 410, 1000)}, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, at := Merge(tt.reports)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Merge = %+v, want %+v", got, tt.want)
			}
			wantAt := time.Time{}
			if tt.wantAge >= 0 {
				wantAt = now.Add(-tt.wantAge)
			}
			if !at.Equal(wantAt) {
				t.Errorf("at = %v, want %v", at, wantAt)
			}
		})
	}
	// The input is not reordered.
	in := []Report{rep("a", time.Hour, five(1, h1)), rep("b", 0, five(2, h1))}
	_, _ = Merge(in)
	if in[0].SessionID != "a" {
		t.Error("Merge reordered its input")
	}
}

func TestDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if got, err := Dir(); err != nil || got != filepath.Join(state, "ccdeck", "meter") {
		t.Errorf("Dir = %q, %v", got, err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "relative/ignored")
	if got, err := Dir(); err != nil || got != filepath.Join(home, ".local", "state", "ccdeck", "meter") {
		t.Errorf("Dir = %q, %v", got, err)
	}
}
