package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestResolvePaths(t *testing.T) {
	home := t.TempDir()
	withConfig := t.TempDir()
	if err := os.WriteFile(filepath.Join(withConfig, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, dir, env string
		want           Paths
	}{
		{"default", "", "", Paths{Dir: filepath.Join(home, ".claude"), ConfigFile: filepath.Join(home, ".claude.json")}},
		{"environment", "", "/srv/cc", Paths{Dir: "/srv/cc", ConfigFile: filepath.Join(home, ".claude.json")}},
		{"flag beats environment", "/opt/cc/", "/srv/cc", Paths{Dir: "/opt/cc", ConfigFile: filepath.Join(home, ".claude.json")}},
		{"config file inside an explicit dir", withConfig, "", Paths{Dir: withConfig, ConfigFile: filepath.Join(withConfig, ".claude.json")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvePaths(tt.dir, tt.env, home); got != tt.want {
				t.Errorf("paths = %+v, want %+v", got, tt.want)
			}
		})
	}
	p := tests[0].want
	if p.ProjectsDir() != filepath.Join(home, ".claude", "projects") ||
		p.SessionsDir() != filepath.Join(home, ".claude", "sessions") ||
		p.HistoryFile() != filepath.Join(home, ".claude", "history.jsonl") ||
		p.CatalogDir() != filepath.Join(home, ".claude", "cache", "model-catalog") {
		t.Errorf("layout: %s %s %s %s", p.ProjectsDir(), p.SessionsDir(), p.HistoryFile(), p.CatalogDir())
	}
}

func TestTranscripts(t *testing.T) {
	got, err := fixturePaths(t).Transcripts()
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b TranscriptFile) int { return strings.Compare(a.SessionID, b.SessionID) })
	want := []TranscriptFile{
		{SessionID: sessionOne, Path: transcriptOne()},
		{SessionID: sessionTwo, Path: transcriptTwo()},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("transcripts = %+v, want %+v", got, want)
	}
	if none, err := resolvePaths(t.TempDir(), "", "").Transcripts(); err != nil || none != nil {
		t.Errorf("missing projects dir: %v %v", none, err)
	}
}

func TestSessionOf(t *testing.T) {
	p := resolvePaths("/c", "", "")
	tests := []struct {
		path, want string
		ok         bool
	}{
		{"/c/projects/-p/abc.jsonl", "abc", true},
		{"/c/projects/-p/abc/subagents/agent-1.jsonl", "abc", true},
		{"/c/projects/-p/abc/subagents/workflows/wf/agent-2.meta.json", "abc", true},
		{"/c/projects/-p", "", false},
		{"/c/projects/-p/notes.txt", "", false},
		{"/c/sessions/1.json", "", false},
		{"/elsewhere/projects/-p/abc.jsonl", "", false},
	}
	for _, tt := range tests {
		got, ok := p.SessionOf(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SessionOf(%q) = %q, %v; want %q, %v", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSubagentFiles(t *testing.T) {
	got, err := SubagentFiles(transcriptOne())
	if err != nil {
		t.Fatal(err)
	}
	dir := SubagentsDir(transcriptOne())
	want := []SubagentFile{
		{ID: "abc123", Path: filepath.Join(dir, "agent-abc123.jsonl"), MetaPath: filepath.Join(dir, "agent-abc123.meta.json")},
		{ID: "def456", Path: filepath.Join(dir, "workflows", "wf_run1", "agent-def456.jsonl"), MetaPath: filepath.Join(dir, "workflows", "wf_run1", "agent-def456.meta.json")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subagent files = %+v\nwant %+v", got, want)
	}
	if none, err := SubagentFiles(transcriptTwo()); err != nil || none != nil {
		t.Errorf("session without subagents: %v %v", none, err)
	}

	meta, err := ReadSubagentMeta(want[0].MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta != (SubagentMeta{AgentType: "Explore", Description: "Survey the widget package", Model: "haiku", ToolUseID: "toolu_agent1"}) {
		t.Errorf("meta = %+v", meta)
	}
	if _, err := ReadSubagentMeta(want[1].MetaPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing meta: err = %v", err)
	}
}

func TestReadRegistry(t *testing.T) {
	dir := fixturePaths(t).SessionsDir()
	procs := fakeProcs{
		101: "5000", // alive
		// 202 is dead: no such pid
		303: "7001", // pid reused: start time differs from the registry's 7000
		404: "8000", // alive, session has no transcript
	}
	regs, err := ReadRegistry(dir, procs)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range regs {
		ids = append(ids, r.SessionID)
	}
	slices.Sort(ids)
	if want := []string{sessionOne, "44444444-4444-4444-8444-444444444444"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("live sessions = %v, want %v (the .key file, dead and reused pids excluded)", ids, want)
	}
	for _, r := range regs {
		if r.SessionID == sessionOne && (r.ProcStart != "5000" || r.CWD != "/home/user/demo" || r.Live.Status != core.StatusWaiting) {
			t.Errorf("registration = %+v", r)
		}
	}
	if none, err := ReadRegistry(filepath.Join(t.TempDir(), "absent"), procs); err != nil || none != nil {
		t.Errorf("missing dir: %v %v", none, err)
	}
}

func TestReadRegistryKeepsNewestPerSession(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("1.json", `{"pid":1,"sessionId":"s","status":"idle","updatedAt":1000}`)
	write("2.json", `{"pid":2,"sessionId":"s","status":"busy","updatedAt":2000,"procStart":12345}`)
	write("3.json", `{"pid":0,"sessionId":"t"}`)
	regs, err := ReadRegistry(dir, fakeProcs{1: "", 2: "12345"})
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].Live.PID != 2 || regs[0].Live.Status != core.StatusBusy || regs[0].ProcStart != "12345" {
		t.Errorf("registrations = %+v", regs)
	}
}

func TestReadHistory(t *testing.T) {
	hits, err := ReadHistory(fixturePaths(t).HistoryFile())
	if err != nil {
		t.Fatal(err)
	}
	want := []core.PromptHit{
		{SessionID: sessionOne, Time: at(t, "2026-01-10T10:00:00Z"), Text: "Add a parser for the widget format"},
		{SessionID: sessionTwo, Time: at(t, "2026-01-09T08:00:00Z"), Text: "Tidy the README"},
		{SessionID: sessionOne, Time: at(t, "2026-01-10T10:02:00Z"), Text: "Now handle\nnested widgets"},
	}
	if !reflect.DeepEqual(hits, want) {
		t.Errorf("history = %+v\nwant %+v", hits, want)
	}
	if _, err := ReadHistory(filepath.Join(t.TempDir(), "none")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing history: err = %v", err)
	}
}

func TestCatalog(t *testing.T) {
	cat, err := ReadCatalog(fixturePaths(t).CatalogDir())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		model string
		want  int64
	}{
		{"claude-test-1", 200000},
		{"claude-test-2", 1000000},
		{"claude-test-2-20260101", 1000000}, // prefix at a boundary
		{"claude-test-2[1m]", 1000000},
		{"claude-test-20", 0}, // not a boundary
		{"claude-test-3", 0},  // listed without a window
		{"", 0},
	}
	for _, tt := range tests {
		if got := cat.Window(tt.model); got != tt.want {
			t.Errorf("Window(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
	empty, err := ReadCatalog(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(empty) != 0 {
		t.Errorf("missing catalog dir: %v %v", empty, err)
	}
}

func TestLookupPrefersLongestPrefix(t *testing.T) {
	cat := Catalog{"claude-x": 1, "claude-x-5": 2}
	if got := cat.Window("claude-x-5-20260101"); got != 2 {
		t.Errorf("Window = %d, want the longer prefix's 2", got)
	}
}

func TestPrices(t *testing.T) {
	prices, err := ReadPrices(fixturePaths(t).ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	want := Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite5m: 3.75, CacheWrite1h: 6, WebSearch: 0.01}
	if got, ok := prices.Lookup("claude-test-1-20260101"); !ok || got != want {
		t.Errorf("Lookup = %+v %v, want %+v", got, ok, want)
	}
	u := core.Usage{Input: 1e6, Output: 2e6, Thinking: 1e6, CacheRead: 1e6, CacheWrite5m: 1e6, CacheWrite1h: 1e6, WebSearches: 3}
	// 3 + 30 + 0.3 + 3.75 + 6 + 0.03; thinking is inside output.
	if got := want.Cost(u); got < 43.0799 || got > 43.0801 {
		t.Errorf("Cost = %v, want 43.08", got)
	}
	if _, err := ReadPrices(filepath.Join(t.TempDir(), "none.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing config: err = %v", err)
	}
}
