package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/meter"
)

// asMain makes the test binary run as ccdeck itself, so that the tests
// below execute the real command in its own process: real stdin and
// stdout, a real exit status, real signals.
const asMain = "CCDECK_TEST_RUN_AS_MAIN"

// childEnv is the environment of a helper process. Under the race detector
// a process otherwise sleeps a second on exit.
func childEnv(stateHome string, extra ...string) []string {
	env := append(os.Environ(), asMain+"=1", "GORACE=atexit_sleep_ms=0", "XDG_STATE_HOME="+stateHome,
		"HOME="+filepath.Join(stateHome, "home"), "NO_COLOR=1", "COLUMNS=200")
	return append(env, extra...)
}

func TestMain(m *testing.M) {
	if os.Getenv(asMain) == "1" {
		if err := Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const helperID = "0a1b2c3d-0000-4000-8000-000000000001"

// helperInput is a status-line input with every field the helper reads,
// shaped like Claude Code's documented example.
func helperInput(t *testing.T, id string, cost float64, now time.Time) []byte {
	t.Helper()
	in := map[string]any{
		"cwd": "/invented/project", "session_id": id, "session_name": "invented-name",
		"transcript_path": "/invented/transcript.jsonl", "version": "2.1.90",
		"model":     map[string]any{"id": "claude-opus-5-5", "display_name": "Opus"},
		"workspace": map[string]any{"current_dir": "/invented/project", "project_dir": "/invented/project"},
		"cost": map[string]any{"total_cost_usd": cost, "total_duration_ms": 45000, "total_api_duration_ms": 2300,
			"total_lines_added": 156, "total_lines_removed": 23},
		"context_window": map[string]any{"total_input_tokens": 15500, "total_output_tokens": 1200,
			"context_window_size": 200000, "used_percentage": 8, "remaining_percentage": 92,
			"current_usage": map[string]any{"input_tokens": 8500, "output_tokens": 1200,
				"cache_creation_input_tokens": 5000, "cache_read_input_tokens": 2000}},
		"rate_limits": map[string]any{
			"five_hour":   map[string]any{"used_percentage": 84.5, "resets_at": now.Add(72 * time.Minute).Unix()},
			"seven_day":   map[string]any{"used_percentage": 41.2, "resets_at": now.Add(76 * time.Hour).Unix()},
			"spend_limit": map[string]any{"used_percentage": 62.8, "resets_at": now.Add(300 * time.Hour).Unix(), "used_usd": 314.12, "limit_usd": 500, "period": "monthly"},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type helperResult struct {
	stdout string
	code   int
	took   time.Duration
}

// runHelper runs `ccdeck statusline args...` in its own process with
// stdin, the state directory under stateHome, and extra environment.
func runHelper(t *testing.T, stdin []byte, stateHome string, env []string, args ...string) helperResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, os.Args[0], append([]string{"statusline"}, args...)...)
	c.Env = childEnv(stateHome, env...)
	c.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	c.Stdout = &out
	start := time.Now()
	err := c.Run()
	r := helperResult{stdout: out.String(), took: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		t.Fatalf("run helper: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the helper did not finish")
	}
	return r
}

// oneLine checks the helper's contract for one run: exit 0, and one
// non-empty line.
func (r helperResult) oneLine(t *testing.T) string {
	t.Helper()
	if r.code != 0 {
		t.Errorf("exit status %d, want 0", r.code)
	}
	line, ok := strings.CutSuffix(r.stdout, "\n")
	if !ok || line == "" || strings.Contains(line, "\n") {
		t.Errorf("output %q is not one non-empty line", r.stdout)
	}
	return line
}

func meterFiles(t *testing.T, stateHome string) []string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(stateHome, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	return files
}

func TestStatuslineRecordsAndPrints(t *testing.T) {
	state := t.TempDir()
	now := time.Now()
	r := runHelper(t, helperInput(t, helperID, 1.2345, now), state, nil)
	line := r.oneLine(t)
	for _, want := range []string{"Opus", "ctx 8%", "$1.23", "5h 84%!", "resets in 1h1"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	dir := filepath.Join(state, "ccdeck", "meter")
	rep, err := meter.Read(filepath.Join(dir, helperID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Meter.CostUSD != 1.2345 || rep.Meter.ContextTokens != 15500 || len(rep.Limits) != 3 || rep.Meter.At.Before(now.Add(-time.Second)) {
		t.Errorf("report = %+v", rep)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory mode: %v, %v", fi.Mode(), err)
	}
	if files := meterFiles(t, state); len(files) != 1 {
		t.Errorf("files written: %v", files)
	}
	t.Logf("one run took %v (test binary, race detector when enabled)", r.took)
}

// Every way the input or the environment can be wrong still exits 0 with
// one line, promptly.
func TestStatuslineNeverDisturbsTheSession(t *testing.T) {
	now := time.Now()
	big := map[string]any{"session_id": helperID, "pad": strings.Repeat("x", 1<<20), "cost": map[string]any{"total_cost_usd": 2}}
	bigJSON, _ := json.Marshal(big)
	garbage := bytes.Repeat([]byte("\x00\xff{[\"garbage"), 1<<20/12)
	deep := strings.Repeat("[", 100_000)

	readOnly := func(t *testing.T, state string) {
		dir := filepath.Join(state, "ccdeck", "meter")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}
	isFile := func(t *testing.T, state string) {
		if err := os.MkdirAll(filepath.Join(state, "ccdeck"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "ccdeck", "meter"), []byte("not a dir"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		stdin []byte
		setup func(t *testing.T, state string)
		want  string // in the line
	}{
		{"empty stdin", nil, nil, "ccdeck"},
		{"blank stdin", []byte("\n\n  \n"), nil, "ccdeck"},
		{"garbage", []byte("this is not json {"), nil, "ccdeck"},
		{"1 MB of garbage", garbage, nil, "ccdeck"},
		{"1 MB of JSON", bigJSON, nil, "$2.00"},
		{"deeply nested", []byte(deep), nil, "ccdeck"},
		{"null document", []byte("null"), nil, "ccdeck"},
		{"empty object", []byte("{}"), nil, "ccdeck"},
		{"null fields", []byte(`{"session_id":null,"model":null,"cost":null,"context_window":{"used_percentage":null},"rate_limits":null}`), nil, "ccdeck"},
		{"missing fields", []byte(`{"session_id":"` + helperID + `","model":{"display_name":"Opus"}}`), nil, "Opus"},
		{"wrong types", []byte(`{"session_id":7,"model":"x","cost":{"total_cost_usd":"lots"},"context_window":[],"rate_limits":{"five_hour":"full"}}`), nil, "ccdeck"},
		{"state directory is a file", helperInput(t, helperID, 1, now), isFile, "Opus"},
		{"state directory is read-only", helperInput(t, helperID, 1, now), readOnly, "Opus"},
	}
	if os.Geteuid() == 0 {
		tests = tests[:len(tests)-1] // root writes anywhere
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, state)
			}
			r := runHelper(t, tt.stdin, state, nil)
			if line := r.oneLine(t); !strings.Contains(line, tt.want) {
				t.Errorf("line %q lacks %q", line, tt.want)
			}
			if r.took > 5*time.Second {
				t.Errorf("took %v", r.took)
			}
		})
	}
}

// A session id that is not a plain id never becomes a path.
func TestStatuslineHostileSessionIDs(t *testing.T) {
	now := time.Now()
	for _, id := range []string{"../../escape", "../meter2/x", "a/b", "..", ".", "/abs/path", "x\x00y", "line\nbreak", "esc\x1b[2J", "ok id", strings.Repeat("a", 300)} {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		r := runHelper(t, helperInput(t, id, 1, now), state, nil)
		if line := r.oneLine(t); !strings.Contains(line, "Opus") {
			t.Errorf("%q: line %q", id, line)
		}
		if files := meterFiles(t, root); len(files) != 0 {
			t.Errorf("%q: files written: %v", id, files)
		}
	}
}

func TestStatuslineWrap(t *testing.T) {
	now := time.Now()
	input := helperInput(t, helperID, 1.5, now)
	tests := []struct {
		name    string
		command string
		want    string // the whole output, or "" for ccdeck's own line
		maxTook time.Duration
	}{
		{"passes stdin and prints the output", "wc -c", fmt.Sprintf("%d\n", len(input)), 0},
		{"keeps several lines", `printf 'one\ntwo\nthree\n'`, "one\ntwo\nthree\n", 0},
		{"adds a missing newline", `printf 'no newline'`, "no newline\n", 0},
		{"keeps the environment", `printf '%s' "$COLUMNS"`, "200\n", 0},
		{"fails", "exit 3", "", 0},
		{"fails after printing", "echo partial; exit 1", "", 0},
		{"prints nothing", "true", "", 0},
		{"prints only blanks", `printf '\n  \n'`, "", 0},
		{"is not found", "/nonexistent/command", "", 0},
		{"prints too much", "head -c 200000 /dev/zero | tr '\\0' x", "", 0},
		{"hangs", "sleep 30", "", 5 * time.Second},
		{"hangs after printing", "echo partial; sleep 30", "", 5 * time.Second},
		{"hangs in a child holding stdout", "sleep 30 & echo started; wait", "", 5 * time.Second},
		{"exits leaving a job holding stdout", "echo mine; sleep 1 &", "mine\n", 900 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := t.TempDir()
			r := runHelper(t, input, state, nil, "--wrap", tt.command, "--wrap-timeout=300ms")
			if r.code != 0 {
				t.Errorf("exit status %d", r.code)
			}
			if tt.want != "" {
				if r.stdout != tt.want {
					t.Errorf("output %q, want %q", r.stdout, tt.want)
				}
			} else if line := r.oneLine(t); !strings.Contains(line, "Opus") || !strings.Contains(line, "$1.50") {
				t.Errorf("output %q, want ccdeck's own line", r.stdout)
			}
			if tt.maxTook > 0 && r.took > tt.maxTook {
				t.Errorf("took %v, want the wrapped command cut off", r.took)
			}
			// The report is written whatever the wrapped command does.
			if _, err := meter.Read(filepath.Join(state, "ccdeck", "meter", helperID+".json")); err != nil {
				t.Errorf("no report: %v", err)
			}
		})
	}
}

// A wrapped command cut off by the timeout is killed with everything it
// started: nothing it left behind runs on.
func TestStatuslineWrapKillsTheWholeCommand(t *testing.T) {
	state := t.TempDir()
	marker := filepath.Join(state, "marker")
	cmd := fmt.Sprintf("(sleep 1; touch %s) & sleep 30", marker)
	r := runHelper(t, helperInput(t, helperID, 1, time.Now()), state, nil, "--wrap", cmd, "--wrap-timeout", "200ms")
	r.oneLine(t)
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a child of the wrapped command survived the timeout")
	}
}

// A wrapped command that fails is stopped with everything it started, even
// when it exits at once; one that succeeds keeps its background jobs, such
// as a cache refresh, though its output after it exits is not read.
func TestStatuslineWrapBackgroundJobs(t *testing.T) {
	tests := []struct {
		name     string
		command  string // %[1]s is the marker the job creates
		want     string // "" for ccdeck's own line
		survives bool
	}{
		{"failure stops a job", "(sleep 0.5; touch %[1]s) >/dev/null & exit 1", "", false},
		{"failure stops a job holding stdout", "(sleep 0.5; touch %[1]s) & exit 1", "", false},
		{"no output stops a job", "(sleep 0.5; touch %[1]s) >/dev/null &", "", false},
		{"success keeps a job", "(sleep 0.5; touch %[1]s) >/dev/null & echo mine", "mine\n", true},
		{"success keeps a job holding stdout", "(sleep 0.5; touch %[1]s) & echo mine", "mine\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := t.TempDir()
			marker := filepath.Join(state, "marker")
			r := runHelper(t, helperInput(t, helperID, 1, time.Now()), state, nil, "--wrap", fmt.Sprintf(tt.command, marker))
			if tt.want != "" {
				if r.stdout != tt.want {
					t.Errorf("output %q, want %q", r.stdout, tt.want)
				}
			} else if line := r.oneLine(t); !strings.Contains(line, "Opus") {
				t.Errorf("output %q, want ccdeck's own line", r.stdout)
			}
			time.Sleep(time.Second)
			if _, err := os.Stat(marker); (err == nil) != tt.survives {
				t.Errorf("the job ran to the end: %v, want %v", err == nil, tt.survives)
			}
		})
	}
}

// Arguments the helper does not understand never cost the status line.
func TestStatuslineArguments(t *testing.T) {
	input := helperInput(t, helperID, 1, time.Now())
	for _, args := range [][]string{
		{"--bogus"}, {"--wrap"}, {"--wrap-timeout"}, {"--wrap-timeout=forever"}, {"--wrap-timeout", "-5s"},
		{"extra", "args"}, {"--claude-dir", "/nonexistent"}, {"--wrap="},
	} {
		r := runHelper(t, input, t.TempDir(), nil, args...)
		if line := r.oneLine(t); !strings.Contains(line, "Opus") {
			t.Errorf("%v: line %q", args, line)
		}
	}
	r := runHelper(t, nil, t.TempDir(), nil, "--help")
	if r.code != 0 || !strings.Contains(r.stdout, `"command": "ccdeck statusline"`) || !strings.Contains(r.stdout, "--wrap") {
		t.Errorf("--help: %d %q", r.code, r.stdout)
	}
	if got := parseStatuslineArgs([]string{"--wrap=a b", "--wrap-timeout", "50ms"}); got.wrap != "a b" || got.wrapTimeout != 50*time.Millisecond {
		t.Errorf("parse = %+v", got)
	}
}

// Killing the helper at random points while it writes leaves the old state
// file or the new one, whole, never a partial one.
func TestStatuslineKilledMidWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the helper many times")
	}
	state := t.TempDir()
	path := filepath.Join(state, "ccdeck", "meter", helperID+".json")
	now := time.Now()
	runHelper(t, helperInput(t, helperID, 1, now), state, nil).oneLine(t)
	// Pad the input so that each run spends a while reading and writing.
	padded := func(cost float64) []byte {
		var in map[string]any
		_ = json.Unmarshal(helperInput(t, helperID, cost, now), &in)
		in["pad"] = strings.Repeat("y", 1<<20)
		b, _ := json.Marshal(in)
		return b
	}
	inputs := [][]byte{padded(1), padded(2)}
	// How long a whole run takes here; kills land anywhere in it.
	start := time.Now()
	runHelper(t, inputs[1], state, nil).oneLine(t)
	whole := time.Since(start)
	const runs = 30
	kills, completed := 0, 0
	for i := range runs {
		c := exec.Command(os.Args[0], "statusline")
		c.Env = childEnv(state)
		c.Stdin = bytes.NewReader(inputs[i%2])
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(rand.Int64N(int64(whole))))
		if c.Process.Signal(syscall.SIGKILL) == nil {
			kills++
		}
		if err := c.Wait(); err == nil {
			completed++
		}
		rep, err := meter.Read(path)
		if err != nil {
			t.Fatalf("run %d: state file unreadable after a kill: %v", i, err)
		}
		if rep.Meter.CostUSD != 1 && rep.Meter.CostUSD != 2 {
			t.Fatalf("run %d: state file holds %+v", i, rep.Meter)
		}
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	t.Logf("a whole run takes %v; %d temporary files left by kills mid-write", whole, len(ents)-1)
	t.Logf("%d runs signalled, %d completed before the kill", kills, completed)
	if completed == 0 || completed == runs {
		t.Logf("every kill landed on the same side of the write; the stage test in package meter covers each step")
	}
}

func TestFormatLine(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	full := meter.Input{
		Report: meter.Report{
			Meter: core.Meter{HasContext: true, ContextPercent: 81.7, CostUSD: 12.345},
			Limits: []core.RateLimit{
				{Window: "7d", UsedPercent: 41, ResetsAt: now.Add(76 * time.Hour)},
				{Window: "5h", UsedPercent: 63.9, ResetsAt: now.Add(130 * time.Minute)},
				{Window: "spend", UsedPercent: 99, ResetsAt: now.Add(-time.Minute)}, // reset already
			},
		},
		ModelID: "claude-opus-5-5", ModelName: "Opus", HasCost: true,
	}
	tests := []struct {
		name  string
		in    meter.Input
		width int
		color bool
		want  string
	}{
		{"everything", full, 0, false, "Opus · ctx 81%! · $12.35 · 5h 63% resets in 2h10m"},
		{"drops the reset time first", full, 40, false, "Opus · ctx 81%! · $12.35 · 5h 63%"},
		{"then the cost", full, 30, false, "Opus · ctx 81%! · 5h 63%"},
		{"then the model", full, 20, false, "ctx 81%! · 5h 63%"},
		{"then cuts", full, 10, false, "ctx 81%! …"},
		{"colour marks the warning", full, 0, true, "Opus · \x1b[1;31mctx 81%!\x1b[0m · $12.35 · 5h 63% resets in 2h10m"},
		{"model id when there is no name", meter.Input{ModelID: "claude-x"}, 0, false, "claude-x"},
		{"nothing known", meter.Input{}, 0, false, "ccdeck"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatLine(tt.in, now, tt.width, tt.color); got != tt.want {
				t.Errorf("formatLine = %q, want %q", got, tt.want)
			}
		})
	}
	if useColor(func(k string) string { return map[string]string{"NO_COLOR": "1", "TERM": "xterm"}[k] }) ||
		useColor(func(k string) string { return map[string]string{"TERM": "dumb"}[k] }) ||
		!useColor(func(k string) string { return map[string]string{"TERM": "xterm-256color"}[k] }) {
		t.Error("useColor does not follow NO_COLOR and TERM")
	}
	if columns(func(string) string { return "80" }) != 80 || columns(func(string) string { return "x" }) != 0 {
		t.Error("columns does not read COLUMNS")
	}
}

// Doctor says whether the meter is feeding, and how to enable it when not.
func TestDoctorMeter(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	started := time.Now().Add(-time.Hour)
	live := core.Session{ID: helperID, Live: &core.Live{PID: 1, StartedAt: started}}
	limits := []core.RateLimit{{Window: "5h", UsedPercent: 84, ResetsAt: time.Now().Add(time.Hour)}}

	var out bytes.Buffer
	doctorMeter(&out, []core.Session{live}, nil, time.Time{})
	for _, want := range []string{"Status-line meter", "never run", "no: none of the 1 live session", statuslineSnippet} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("not feeding: output lacks %q:\n%s", want, out.String())
		}
	}

	if err := meter.Write(filepath.Join(state, "ccdeck", "meter"), meter.Report{SessionID: helperID, Meter: core.Meter{At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	live.Meter = &core.Meter{At: time.Now()}
	stale := core.Session{ID: "other", Live: &core.Live{PID: 2, StartedAt: time.Now()}, Meter: &core.Meter{At: started}}
	out.Reset()
	doctorMeter(&out, []core.Session{live, stale}, limits, time.Now())
	for _, want := range []string{"1 report", "yes: 1 of 2 live sessions", "5h 84%"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("feeding: output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), statuslineSnippet) {
		t.Errorf("feeding: the snippet is shown:\n%s", out.String())
	}
}

// show prints Claude Code's report, which is kept even when it is older
// than the transcript.
func TestMeterSummary(t *testing.T) {
	now := time.Now()
	m := core.Meter{At: now.Add(-5 * time.Minute), HasContext: true, ContextPercent: 30, ContextWindow: 1_000_000, CostUSD: 4.5, LinesAdded: 156, LinesRemoved: 23}
	if got, want := meterSummary(m, now), "Claude Code reported context 30% of 1M, ~$4.50, +156 -23 lines 5m ago"; got != want {
		t.Errorf("meterSummary = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(cost(1.234, core.CostClaude), "~$1.23") {
		t.Errorf("cost = %q", cost(1.234, core.CostClaude))
	}
}
