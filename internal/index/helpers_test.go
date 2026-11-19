package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// The fixtures are invented: a synthetic Claude directory built in a temp
// dir by each test.
const (
	sessA   = "aaaaaaaa-0000-4000-8000-000000000001"
	sessB   = "bbbbbbbb-0000-4000-8000-000000000002"
	model1  = "claude-test-1"
	projDir = "/home/user/widgets"
)

// fakeProcs is a ProcessChecker whose live pids the test controls.
type fakeProcs struct {
	mu   sync.Mutex
	dead map[int]bool
}

func (f *fakeProcs) Alive(pid int, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead[pid]
}

func (f *fakeProcs) kill(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead == nil {
		f.dead = map[int]bool{}
	}
	f.dead[pid] = true
}

// env is one test's Claude directory and cache directory.
type env struct {
	t     *testing.T
	dir   string
	cache string
	meter string // the meter's state directory; empty means the meter is off
	procs *fakeProcs
	// readyWait bounds how long open waits for the start-up scan; zero
	// means 5 seconds.
	readyWait time.Duration
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg"))
	e := &env{t: t, dir: filepath.Join(root, "claude"), cache: filepath.Join(root, "cache"), procs: &fakeProcs{}}
	e.write(".claude.json", `{"additionalModelCostsCache":{"claude-test-1":{"inputTokens":3,"outputTokens":15,"promptCacheWriteTokens":3.75,"promptCacheWrite1hTokens":6,"promptCacheReadTokens":0.3,"webSearchRequests":0.01}}}`)
	e.write("cache/model-catalog/catalog.json", `{"catalog":{"config":{"models":[{"id":"claude-test-1","runtime":{"max_input_tokens":200000}}]}}}`)
	e.mkdir("projects")
	e.mkdir("sessions")
	return e
}

func (e *env) path(rel string) string { return filepath.Join(e.dir, filepath.FromSlash(rel)) }

func (e *env) mkdir(rel string) {
	e.t.Helper()
	if err := os.MkdirAll(e.path(rel), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// write replaces a file atomically, as Claude Code does with registry files.
func (e *env) write(rel, content string) {
	e.t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) append(rel, content string) {
	e.t.Helper()
	f, err := os.OpenFile(e.path(rel), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		e.t.Fatal(err)
	}
}

func transcript(slug, id string) string { return "projects/" + slug + "/" + id + ".jsonl" }

// open starts a store over e with fast timers and no sweep, so that only
// file events (and the poll, for live sessions) can bring changes in, and
// waits for the start-up scan.
func (e *env) open() *Store {
	e.t.Helper()
	return e.openWith(func(*tuning) {})
}

func (e *env) openWith(adjust func(*tuning)) *Store {
	e.t.Helper()
	tn := tuning{procs: e.procs, debounce: 20 * time.Millisecond, poll: 50 * time.Millisecond, sweep: time.Hour, workers: 3, entries: 4}
	adjust(&tn)
	s, err := newStore(Options{ClaudeDir: e.dir, CacheDir: e.cache, MeterDir: e.meter, Prices: map[string]Price{"claude-test-2": {Input: 1, Output: 2}}}, tn)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = s.Close() })
	wait := e.readyWait
	if wait == 0 {
		wait = 5 * time.Second
	}
	select {
	case <-s.Ready():
	case <-time.After(wait):
		e.t.Fatal("start-up scan did not finish")
	}
	return s
}

func line(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func stamp(ts time.Time) string { return ts.UTC().Format("2006-01-02T15:04:05.000Z") }

func userLine(t *testing.T, ts time.Time, text string) string {
	return line(t, map[string]any{
		"type": "user", "timestamp": stamp(ts), "cwd": projDir, "gitBranch": "main", "version": "2.1.0",
		"isSidechain": false, "message": map[string]any{"role": "user", "content": text},
	})
}

// assistantLines is one API response split over a thinking and a text line,
// both repeating the same usage, as transcripts do.
func assistantLines(t *testing.T, ts time.Time, msgID string, in, out int64, text string) string {
	usage := map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
	mk := func(block map[string]any) string {
		return line(t, map[string]any{
			"type": "assistant", "timestamp": stamp(ts), "cwd": projDir, "isSidechain": false,
			"message": map[string]any{"id": msgID, "model": model1, "role": "assistant", "content": []any{block}, "usage": usage},
		})
	}
	return mk(map[string]any{"type": "thinking", "thinking": ""}) + mk(map[string]any{"type": "text", "text": text})
}

func subagentLine(t *testing.T, ts time.Time, msgID string, in, out int64) string {
	return line(t, map[string]any{
		"type": "assistant", "timestamp": stamp(ts), "cwd": projDir, "isSidechain": true,
		"message": map[string]any{"id": msgID, "model": model1, "role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": "done"}},
			"usage":   map[string]any{"input_tokens": in, "output_tokens": out}},
	})
}

func registryFile(t *testing.T, pid int, id, status, waitingFor string, updated time.Time) string {
	return line(t, map[string]any{
		"pid": pid, "sessionId": id, "cwd": projDir, "startedAt": updated.Add(-time.Minute).UnixMilli(),
		"procStart": "4242", "version": "2.1.0", "kind": "interactive", "name": fmt.Sprintf("widgets-%d", pid),
		"status": status, "waitingFor": waitingFor, "updatedAt": updated.UnixMilli(), "statusUpdatedAt": updated.UnixMilli(),
	})
}

// waitFor polls cond until it holds or fails the test after a while.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitEvent reads events until want arrives or fails the test.
func waitEvent(t *testing.T, events <-chan core.Event, want core.Event) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("events closed while waiting for %+v", want)
			}
			if ev == want || (ev.Kind == want.Kind && ev.SessionID == "" && want.Kind == core.SessionsChanged) {
				return
			}
		case <-timeout:
			t.Fatalf("no event %+v", want)
		}
	}
}

func session(t *testing.T, s *Store, id string) core.Session {
	t.Helper()
	sum, ok := s.Session(id)
	if !ok {
		t.Fatalf("session %s not found", id)
	}
	return sum
}
