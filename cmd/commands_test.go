package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanielTso/ccdeck/internal/config"
	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/demo"
	"github.com/DanielTso/ccdeck/internal/tui"
)

const (
	fixtureID    = "5f1e2d3c-0000-4000-8000-00000000abcd"
	fixtureTitle = "Tidy the invented widget factory"
)

// isolate points every directory ccdeck uses at temporary ones and returns
// a Claude directory holding one ended session.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", t.TempDir()) // doctor must not find tmux or claude
	dir := t.TempDir()
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-01-10T10:00:00.000Z","cwd":"/work/widgets","sessionId":"` + fixtureID + `","message":{"role":"user","content":"Make the widgets tidier"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-10T10:00:05.000Z","cwd":"/work/widgets","message":{"id":"m1","model":"claude-test-1","content":[{"type":"text","text":"Tidied."}],"usage":{"input_tokens":100,"output_tokens":20}}}`,
		`{"type":"ai-title","aiTitle":"` + fixtureTitle + `"}`,
		`not json`,
	}
	writeFile(t, filepath.Join(dir, "projects", "-work-widgets", fixtureID+".jsonl"), strings.Join(lines, "\n")+"\n")
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	dir := isolate(t)
	out, err := run(t, "list", "--claude-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", fixtureID[:8], "ended", "claude-test-1", fixtureTitle, "/work/widgets"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, "list", "--json", "--claude-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []core.Session
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		t.Fatalf("not JSON sessions: %v\n%s", err, out)
	}
	if len(sessions) != 1 || sessions[0].ID != fixtureID || sessions[0].Turns != 1 {
		t.Errorf("sessions = %+v", sessions)
	}

	out, err = run(t, "list", "--live", "--json", "--claude-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--live with nothing running = %q, want []", out)
	}
}

func TestListDemo(t *testing.T) {
	isolate(t)
	out, err := run(t, "list", "--demo", "--live", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var sessions []core.Session
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) == 0 {
		t.Fatal("the demo has live sessions")
	}
	for _, s := range sessions {
		if s.Live == nil {
			t.Errorf("--live listed ended session %s", s.ID)
		}
	}
}

func TestShow(t *testing.T) {
	dir := isolate(t)
	out, err := run(t, "show", fixtureID[:6], "--claude-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fixtureID, fixtureTitle, "1 turns, 1 messages", "Make the widgets tidier", "Tidied."} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, "show", fixtureID, "--json", "-n", "1", "--claude-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Session core.Session `json:"session"`
		Entries []core.Entry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session.ID != fixtureID || len(got.Entries) != 1 || got.Entries[0].Text != "Tidied." {
		t.Errorf("show --json = %+v", got)
	}

	if _, err := run(t, "show", "zzz", "--claude-dir", dir); err == nil || !strings.Contains(err.Error(), "no session id starts with") {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestFindSession(t *testing.T) {
	sessions := []core.Session{{ID: "abc1"}, {ID: "abc2"}, {ID: "abd"}, {ID: "ab"}}
	tests := []struct {
		prefix, want, err string
	}{
		{prefix: "abc1", want: "abc1"},
		{prefix: "abd", want: "abd"},
		{prefix: "ab", want: "ab"}, // an exact id wins over the prefix match
		{prefix: "abc", err: "matches 2 sessions"},
		{prefix: "x", err: "no session id"},
		{prefix: "", err: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			s, err := findSession(sessions, tt.prefix)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Errorf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || s.ID != tt.want {
				t.Errorf("got %q, %v; want %q", s.ID, err, tt.want)
			}
		})
	}
}

func TestDoctor(t *testing.T) {
	dir := isolate(t)
	out, err := run(t, "doctor", "--claude-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Claude directory", "1 directories, 1 session transcripts", "0 registry files",
		"1 files read", "4 read, 1 malformed and skipped", "not found: using defaults",
		"colour profile", "[layout]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	dir := isolate(t)
	if _, err := run(t, "list", "--claude-dir", dir, "--config", filepath.Join(dir, "missing.toml")); err == nil {
		t.Error("a missing --config file must be an error")
	}
	bad := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, bad, "[layout]\nlive_panes = 0\n")
	_, err := run(t, "list", "--claude-dir", dir, "--config", bad)
	if err == nil || !strings.Contains(err.Error(), "layout.live_panes") {
		t.Errorf("bad config: err = %v, want the key named", err)
	}
}

func TestDashboardRejectsUnknownTheme(t *testing.T) {
	isolate(t)
	_, err := run(t, "--demo", "--theme", "no-such-theme")
	if err == nil || !strings.Contains(err.Error(), "no-such-theme") {
		t.Errorf("err = %v, want the theme named", err)
	}
}

func TestTUIOptions(t *testing.T) {
	cfg := config.Default()
	cfg.Theme = "mine"
	cfg.Themes["mine"] = config.Theme{Base: "paper", Colors: map[string]string{"accent": "#ff8800"}}
	cfg.Keys["quit"] = []string{"x"}
	cfg.Layout = config.Layout{PreviewRatio: 0.4, LivePanes: 6, GroupByProject: true}
	st := demo.New(demo.Options{})
	l := newLauncher(cfg, settings{})

	o := tuiOptions(cfg, "", st, l)
	if o.Theme != "mine" || o.Themes["mine"].Base != "paper" || o.Themes["mine"].Colors["accent"] != "#ff8800" {
		t.Errorf("themes not carried over: %+v", o)
	}
	if o.Layout != (tui.Layout{PreviewRatio: 0.4, LivePanes: 6, GroupByProject: true}) || o.Keys["quit"][0] != "x" {
		t.Errorf("layout or keys not carried over: %+v", o)
	}
	if _, err := tui.New(o); err != nil {
		t.Errorf("the UI rejects a translated config: %v", err)
	}
	if o := tuiOptions(cfg, "paper", st, l); o.Theme != "paper" {
		t.Errorf("--theme does not override the config: %q", o.Theme)
	}
}

// Every layout the config accepts, the UI must accept too.
func TestConfigBoundsSuitTheUI(t *testing.T) {
	st := demo.New(demo.Options{})
	for _, lay := range []config.Layout{
		{PreviewRatio: config.MinPreviewRatio, LivePanes: config.MinLivePanes},
		{PreviewRatio: config.MaxPreviewRatio, LivePanes: config.MaxLivePanes},
	} {
		cfg := config.Default()
		cfg.Layout = lay
		if _, err := tui.New(tuiOptions(cfg, "", st, newLauncher(cfg, settings{}))); err != nil {
			t.Errorf("layout %+v: %v", lay, err)
		}
	}
}

func TestIndexPrices(t *testing.T) {
	got := indexPrices(map[string]config.Price{"m": {Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CacheWrite1h: 5}})
	p := got["m"]
	if p.Input != 1 || p.Output != 2 || p.CacheRead != 3 || p.CacheWrite5m != 4 || p.CacheWrite1h != 5 || p.WebSearch != 0 {
		t.Errorf("price = %+v", p)
	}
}

func TestLaunchClaudeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	def := filepath.Join(home, ".claude")
	alt := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		s    settings
		env  string
		want string
	}{
		{"default, variable unset", settings{}, "", ""},
		{"flag", settings{claudeDir: alt}, "", alt},
		{"flag naming the default", settings{claudeDir: def}, "", ""},
		{"relative flag becomes absolute", settings{claudeDir: "rel/dir"}, "", filepath.Join(wd, "rel/dir")},
		{"variable", settings{}, alt, alt},
		{"flag beats variable", settings{claudeDir: alt}, "/elsewhere", alt},
		{"demo", settings{claudeDir: alt, demo: true}, alt, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", tt.env)
			if got := launchClaudeDir(tt.s, tt.env); got != tt.want {
				t.Errorf("launchClaudeDir = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every string list, show and doctor print from a transcript is cleaned of
// escape sequences, which could otherwise retitle or reprogram the terminal.
func TestPrintedTranscriptFieldsAreCleaned(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	const evil = "\x1b]0;PWN\x07\x1b[31m"
	id := "6a1e2d3c-0000-4000-8000-00000000abcd"
	proj := filepath.Join(dir, "projects", "-work-evil")
	cwd := "/work/ev" + evil + "il"
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-01-10T10:00:00.000Z","cwd":` + jsonString(t, cwd) + `,"gitBranch":` + jsonString(t, "br"+evil) + `,"sessionId":"` + id + `","message":{"role":"user","content":"Do it"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-10T10:00:05.000Z","message":{"id":"m1","model":` + jsonString(t, "claude-x"+evil) + `,"content":[{"type":"tool_use","id":"t1","name":` + jsonString(t, "Bash"+evil) + `,"input":{"command":"ls"}}],"usage":{"input_tokens":100,"output_tokens":20}}}`,
	}
	writeFile(t, filepath.Join(proj, id+".jsonl"), strings.Join(lines, "\n")+"\n")
	sub := filepath.Join(proj, id, "subagents")
	writeFile(t, filepath.Join(sub, "agent-ag1.jsonl"),
		`{"type":"assistant","uuid":"s1","timestamp":"2026-01-10T10:00:06.000Z","isSidechain":true,"message":{"id":"m2","model":`+jsonString(t, "claude-y"+evil)+`,"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":1}}}`+"\n")
	writeFile(t, filepath.Join(sub, "agent-ag1.meta.json"), `{"agentType":`+jsonString(t, "Explore"+evil)+`,"description":"look"}`)

	// Make the session live, owned by this test process, so doctor lists it.
	if stat, err := os.ReadFile("/proc/self/stat"); err == nil {
		f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		writeFile(t, filepath.Join(dir, "sessions", "1.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"%s","cwd":%s,"procStart":"%s","status":"idle","name":%s}`,
			os.Getpid(), id, jsonString(t, cwd), f[19], jsonString(t, "n"+evil)))
	}

	for _, args := range [][]string{{"list"}, {"show", id[:6]}, {"doctor"}} {
		t.Run(args[0], func(t *testing.T) {
			out, err := run(t, append(args, "--claude-dir", dir)...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(out, "\x1b\x07") {
				t.Errorf("output carries escape sequences: %q", out)
			}
		})
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A wrong Claude directory is an error naming it, not an empty listing.
func TestMissingClaudeDir(t *testing.T) {
	isolate(t)
	missing := filepath.Join(t.TempDir(), "typo")
	for _, args := range [][]string{{"list"}, {"show", "abc"}} {
		_, err := run(t, append(args, "--claude-dir", missing)...)
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "--claude-dir") {
			t.Errorf("%s: err = %v, want the directory named", args[0], err)
		}
	}
	out, err := run(t, "doctor", "--claude-dir", missing)
	if err != nil {
		t.Fatalf("doctor fails instead of reporting: %v", err)
	}
	if !strings.Contains(out, "not run") || !strings.Contains(out, "colour profile") {
		t.Errorf("doctor output:\n%s", out)
	}
}

// A value the UI rejects is reported with the file and key it came from.
func TestUIErrorsNameTheConfigKey(t *testing.T) {
	isolate(t)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	for _, tt := range []struct{ file, want string }{
		{`theme = "nosuch"`, "key theme: unknown theme"},
		{"[keys]\nfork = [\"q\"]", "key keys.fork: key \"q\" is bound to both"},
	} {
		writeFile(t, cfgPath, tt.file+"\n")
		_, err := run(t, "--demo", "--config", cfgPath)
		if err == nil || !strings.Contains(err.Error(), cfgPath) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want the file and %q", tt.file, err, tt.want)
		}
	}
	_, err := run(t, "--demo", "--theme", "nosuch")
	if err == nil || !strings.HasPrefix(err.Error(), "--theme: ") {
		t.Errorf("--theme: err = %v", err)
	}
}
