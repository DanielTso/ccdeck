package index

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

// seed writes two sessions in two projects; A has a nested subagent.
func seed(t *testing.T, e *env) {
	t.Helper()
	e.write(transcript("-home-user-widgets", sessA),
		`{"type":"permission-mode","permissionMode":"default"}`+"\n"+
			userLine(t, t0, "Make the widget spin")+
			assistantLines(t, t0.Add(time.Second), "msg_a1", 100, 20, "Spinning now.")+
			"not json at all\n")
	e.write("projects/-home-user-widgets/"+sessA+"/subagents/workflows/run1/agent-x1.jsonl",
		subagentLine(t, t0.Add(2*time.Second), "msg_s1", 50, 5))
	e.write("projects/-home-user-widgets/"+sessA+"/subagents/workflows/run1/agent-x1.meta.json",
		`{"agentType":"Explore","description":"Look around","model":"haiku"}`)
	e.write(transcript("-home-user-gadgets", sessB),
		userLine(t, t0.Add(time.Hour), "Polish the gadget")+
			assistantLines(t, t0.Add(time.Hour+time.Second), "msg_b1", 10, 2, "Polished."))
}

func TestScan(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.open()

	got := s.Sessions()
	if len(got) != 2 || got[0].ID != sessB || got[1].ID != sessA {
		t.Fatalf("Sessions() = %v, want B then A", ids(got))
	}
	a := got[1]
	if a.Title != "Make the widget spin" || a.ProjectDir != projDir || a.Messages != 1 || a.Turns != 1 {
		t.Errorf("A = title %q dir %q messages %d turns %d", a.Title, a.ProjectDir, a.Messages, a.Turns)
	}
	// Usage is counted once per message, and the subagent's is added.
	if u := a.Usage[model1]; u.Input != 150 || u.Output != 25 {
		t.Errorf("A usage = %+v, want input 150 output 25", u)
	}
	if len(a.Subagents) != 1 || a.Subagents[0].Type != "Explore" {
		t.Errorf("A subagents = %+v", a.Subagents)
	}
	if a.ContextTokens != 100 || a.ContextWindow != 200000 {
		t.Errorf("A context = %d of %d", a.ContextTokens, a.ContextWindow)
	}
	if want := (150*3.0 + 25*15.0) / 1e6; a.CostKind != core.CostEstimated || !near(a.CostUSD, want) {
		t.Errorf("A cost = %v %v, want estimated %v", a.CostUSD, a.CostKind, want)
	}
	if a.Live != nil {
		t.Errorf("A live = %+v, want nil", a.Live)
	}

	st := s.Stats()
	if st.Sessions != 2 || st.Files != 3 || st.Skipped != 1 || st.Resumed != 0 || st.Watches == 0 {
		t.Errorf("Stats() = %+v", st)
	}
}

func TestAppendWhileWatched(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.open()
	events, cancel := s.Subscribe()
	defer cancel()

	// A partial line first: it must be held back until its newline arrives.
	more := userLine(t, t0.Add(time.Minute), "Faster") + assistantLines(t, t0.Add(time.Minute+time.Second), "msg_a2", 300, 40, "Faster now.")
	e.append(transcript("-home-user-widgets", sessA), more[:len(more)-10])
	time.Sleep(100 * time.Millisecond)
	e.append(transcript("-home-user-widgets", sessA), more[len(more)-10:])

	waitEvent(t, events, core.Event{Kind: core.EntriesChanged, SessionID: sessA})
	waitFor(t, "updated numbers", func() bool { return session(t, s, sessA).Messages == 2 })
	a := session(t, s, sessA)
	if a.Turns != 2 || a.ContextTokens != 300 || a.Usage[model1].Input != 450 || a.LastPrompt != "Faster" {
		t.Errorf("after append: turns %d context %d input %d last prompt %q", a.Turns, a.ContextTokens, a.Usage[model1].Input, a.LastPrompt)
	}
	es, err := s.Entries(sessA, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[1].Text != "Faster now." {
		t.Errorf("Entries = %+v", es)
	}
}

func TestNewSessionInNewProject(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	if n := len(s.Sessions()); n != 0 {
		t.Fatalf("%d sessions, want 0", n)
	}
	e.write(transcript("-home-user-fresh", sessB), userLine(t, t0, "Hello there"))
	waitFor(t, "the new session", func() bool { _, ok := s.Session(sessB); return ok })

	// A subagent in a nested directory created later is found too.
	e.write("projects/-home-user-fresh/"+sessB+"/subagents/workflows/r/agent-y.jsonl", subagentLine(t, t0, "msg_y", 7, 1))
	waitFor(t, "the new subagent", func() bool { return len(session(t, s, sessB).Subagents) == 1 })

	// Removing the transcript removes the session.
	if err := os.RemoveAll(e.path("projects/-home-user-fresh")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to go", func() bool { _, ok := s.Session(sessB); return !ok })
}

func TestProjectsDirCreatedLater(t *testing.T) {
	e := newEnv(t)
	if err := os.RemoveAll(e.path("projects")); err != nil {
		t.Fatal(err)
	}
	s := e.open()
	e.write(transcript("-home-user-late", sessA), userLine(t, t0, "Late start"))
	waitFor(t, "the session", func() bool { _, ok := s.Session(sessA); return ok })
}

// TestSweepWithoutWatching checks the safety net on its own: with no file
// events at all, the poll and the sweep still find every change.
func TestSweepWithoutWatching(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.openWith(func(tn *tuning) { tn.noWatch, tn.sweep = true, 100*time.Millisecond })
	events, cancel := s.Subscribe()
	defer cancel()

	e.append(transcript("-home-user-gadgets", sessB), assistantLines(t, t0.Add(2*time.Hour), "msg_b2", 1, 1, "More."))
	waitEvent(t, events, core.Event{Kind: core.EntriesChanged, SessionID: sessB})
	if got := session(t, s, sessB).Messages; got != 2 {
		t.Errorf("messages = %d, want 2", got)
	}
	e.write(transcript("-home-user-new", sessA[:8]+"-1111-4111-8111-111111111111"), userLine(t, t0, "New one"))
	waitFor(t, "the new session", func() bool { return len(s.Sessions()) == 3 })
	if err := os.Remove(e.path(transcript("-home-user-gadgets", sessB))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removed session to go", func() bool { _, ok := s.Session(sessB); return !ok })
	e.write("sessions/9.json", registryFile(t, 9, sessA, "idle", "", time.Now()))
	waitFor(t, "live status", func() bool { return session(t, s, sessA).Status() == core.StatusIdle })
}

func TestRegistry(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.open()
	events, cancel := s.Subscribe()
	defer cancel()
	now := time.Now()

	e.write("sessions/701.json", registryFile(t, 701, sessA, "busy", "", now))
	waitEvent(t, events, core.Event{Kind: core.SessionsChanged, SessionID: sessA})
	waitFor(t, "busy", func() bool { return session(t, s, sessA).Status() == core.StatusBusy })

	start := time.Now()
	e.write("sessions/701.json", registryFile(t, 701, sessA, "waiting", "approve Bash: make", now.Add(time.Second)))
	waitFor(t, "waiting", func() bool { return session(t, s, sessA).Status() == core.StatusWaiting })
	if d := time.Since(start); d > time.Second {
		t.Errorf("status change took %v", d)
	}
	if l := session(t, s, sessA).Live; l.WaitingFor != "approve Bash: make" || l.PID != 701 {
		t.Errorf("live = %+v", l)
	}

	// A live session with no transcript yet is listed, and goes when its
	// process is gone although its registry file stays (stale).
	const lonely = "cccccccc-0000-4000-8000-000000000003"
	e.write("sessions/702.json", registryFile(t, 702, lonely, "idle", "", now))
	waitFor(t, "the transcript-less session", func() bool { _, ok := s.Session(lonely); return ok })
	if got := session(t, s, lonely); got.Title != "widgets-702" || got.Path != "" {
		t.Errorf("transcript-less session = %q %q", got.Title, got.Path)
	}
	if es, err := s.Entries(lonely, 0); err != nil || es != nil {
		t.Errorf("Entries(transcript-less) = %v, %v", es, err)
	}

	e.procs.kill(701)
	e.procs.kill(702)
	waitFor(t, "stale registry entries to be dropped", func() bool {
		_, ok := s.Session(lonely)
		return !ok && session(t, s, sessA).Live == nil
	})
}

func TestCacheResume(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	first := e.open()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	cacheFiles, err := filepath.Glob(filepath.Join(e.cache, "index-*.json"))
	if err != nil || len(cacheFiles) != 1 {
		t.Fatalf("cache files = %v, %v", cacheFiles, err)
	}
	fi, err := os.Stat(cacheFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(cacheFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"widget spin", "Spinning", "Look around", "Polish"} {
		if strings.Contains(string(data), text) {
			t.Errorf("cache holds conversation text %q", text)
		}
	}

	// The transcript grows while nothing runs; the next start continues
	// from the cached offset and must agree with a full parse.
	e.append(transcript("-home-user-widgets", sessA),
		userLine(t, t0.Add(time.Minute), "Faster")+assistantLines(t, t0.Add(time.Minute), "msg_a2", 300, 40, "Faster now."))
	warm := e.open()
	if st := warm.Stats(); st.Resumed != 3 {
		t.Errorf("Resumed = %d, want 3", st.Resumed)
	}
	e.cache = t.TempDir()
	cold := e.open()
	if st := cold.Stats(); st.Resumed != 0 {
		t.Errorf("cold Resumed = %d", st.Resumed)
	}
	if w, c := warm.Sessions(), cold.Sessions(); !reflect.DeepEqual(w, c) {
		t.Errorf("resumed sessions differ from a full parse:\n%+v\n%+v", w, c)
	}
	if w, c := warm.Daily(400), cold.Daily(400); !reflect.DeepEqual(w, c) {
		t.Error("resumed daily usage differs from a full parse")
	}
}

func TestCacheInvalidation(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	_ = e.open().Close()

	t.Run("rewritten transcript", func(t *testing.T) {
		// Same lines, other text: the cached title offset no longer
		// points at what it said, so the file is parsed again.
		e.write(transcript("-home-user-gadgets", sessB),
			userLine(t, t0.Add(time.Hour), "Shine the gadget")+
				assistantLines(t, t0.Add(time.Hour+time.Second), "msg_b1", 10, 2, "Shiny."))
		s := e.open()
		if got := session(t, s, sessB).Title; got != "Shine the gadget" {
			t.Errorf("title = %q", got)
		}
		_ = s.Close()
	})
	t.Run("shrunken transcript", func(t *testing.T) {
		e.write(transcript("-home-user-gadgets", sessB), userLine(t, t0, "Tiny"))
		s := e.open()
		if got := session(t, s, sessB); got.Title != "Tiny" || got.Messages != 0 {
			t.Errorf("session = %q, %d messages", got.Title, got.Messages)
		}
		_ = s.Close()
	})
	t.Run("other version", func(t *testing.T) {
		files, _ := filepath.Glob(filepath.Join(e.cache, "index-*.json"))
		data, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"version":1`, `"version":0`, 1))
		if err := os.WriteFile(files[0], data, 0o600); err != nil {
			t.Fatal(err)
		}
		if st := e.open().Stats(); st.Resumed != 0 {
			t.Errorf("Resumed = %d with an outdated cache", st.Resumed)
		}
	})
}

func TestTranscriptReplacedWhileWatched(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.open()
	if _, err := s.Entries(sessB, 0); err != nil {
		t.Fatal(err)
	}
	e.write(transcript("-home-user-gadgets", sessB), userLine(t, t0, "Start over"))
	waitFor(t, "the reset", func() bool { return session(t, s, sessB).Messages == 0 })
	es, err := s.Entries(sessB, 0)
	if err != nil || len(es) != 1 || es[0].Text != "Start over" {
		t.Errorf("Entries = %+v, %v", es, err)
	}
}

func TestEntries(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.openWith(func(tn *tuning) { tn.entries = 1 })

	if _, err := s.Entries("nope", 0); !errors.Is(err, ErrUnknownSession) {
		t.Errorf("Entries(unknown) err = %v", err)
	}
	all, err := s.Entries(sessA, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []core.EntryKind{core.EntryPrompt, core.EntryThinking, core.EntryText}
	if len(all) != len(kinds) {
		t.Fatalf("Entries = %+v", all)
	}
	for i, k := range kinds {
		if all[i].Kind != k {
			t.Errorf("entry %d kind = %s, want %s", i, all[i].Kind, k)
		}
	}
	if last, _ := s.Entries(sessA, 1); len(last) != 1 || last[0].Text != "Spinning now." {
		t.Errorf("Entries(limit 1) = %+v", last)
	}
	// With room for one conversation, reading B evicts A; A still reads.
	if _, err := s.Entries(sessB, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.conv.items[sessA]; ok {
		t.Error("A was not evicted")
	}
	if again, err := s.Entries(sessA, 0); err != nil || !reflect.DeepEqual(again, all) {
		t.Errorf("Entries after eviction = %+v, %v", again, err)
	}
}

func TestSearchPrompts(t *testing.T) {
	e := newEnv(t)
	hist := func(ms int64, text, id string) string {
		return line(t, map[string]any{"display": text, "timestamp": ms, "project": projDir, "sessionId": id})
	}
	e.write("history.jsonl", hist(1000, "Fix the Widget", sessA)+hist(2000, "add a widget test", sessB)+hist(3000, "unrelated", sessB))
	s := e.open()

	got := s.SearchPrompts("WIDGET", 0)
	if len(got) != 2 || got[0].Text != "add a widget test" || got[1].SessionID != sessA {
		t.Errorf("SearchPrompts = %+v", got)
	}
	if got := s.SearchPrompts("widget", 1); len(got) != 1 {
		t.Errorf("limit 1 gave %d", len(got))
	}
	e.append("history.jsonl", hist(4000, "widget again", sessA))
	if got := s.SearchPrompts("widget", 0); len(got) != 3 || got[0].Text != "widget again" {
		t.Errorf("after append = %+v", got)
	}
}

func TestDaily(t *testing.T) {
	e := newEnv(t)
	y, m, d := time.Now().Date()
	today := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)
	e.write(transcript("-home-user-widgets", sessA),
		assistantLines(t, yesterday, "m1", 1000, 100, "one")+assistantLines(t, today, "m2", 2000, 200, "two"))
	e.write(transcript("-home-user-gadgets", sessB), assistantLines(t, today, "m3", 10, 1, "three"))
	s := e.open()

	days := s.Daily(3)
	if len(days) != 3 {
		t.Fatalf("%d days", len(days))
	}
	if want := time.Date(y, m, d, 0, 0, 0, 0, time.Local).AddDate(0, 0, -2); !days[0].Date.Equal(want) {
		t.Errorf("first day = %v, want %v", days[0].Date, want)
	}
	if days[0].Sessions != 0 || len(days[0].Usage) != 0 {
		t.Errorf("empty day = %+v", days[0])
	}
	if u := days[1].Usage[model1]; days[1].Sessions != 1 || u.Input != 1000 || u.Output != 100 {
		t.Errorf("yesterday = %+v", days[1])
	}
	if u := days[2].Usage[model1]; days[2].Sessions != 2 || u.Input != 2010 || u.Output != 201 {
		t.Errorf("today = %+v", days[2])
	}
	if want := (2010*3.0 + 201*15.0) / 1e6; !near(days[2].CostUSD, want) {
		t.Errorf("today cost = %v, want %v", days[2].CostUSD, want)
	}
	if s.Daily(0) != nil {
		t.Error("Daily(0) is not nil")
	}
}

func TestCloseStopsEverything(t *testing.T) {
	before := runtime.NumGoroutine()
	e := newEnv(t)
	seed(t, e)
	s := e.open()
	events, _ := s.Subscribe()
	_, cancel := s.Subscribe()
	cancel()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	for range events {
		// drain until closed
	}
	if late, _ := s.Subscribe(); late != nil {
		if _, ok := <-late; ok {
			t.Error("Subscribe after Close delivered an event")
		}
	}
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= before })
}

func TestCloseDuringScan(t *testing.T) {
	before := runtime.NumGoroutine()
	e := newEnv(t)
	for i := range 50 {
		id := strings.Replace(sessA, "0001", string(rune('a'+i%26))+string(rune('a'+i/26))+"01", 1)
		e.write(transcript("-home-user-many", id), userLine(t, t0, "hi")+assistantLines(t, t0, "m", 1, 1, "x"))
	}
	s, err := newStore(Options{ClaudeDir: e.dir, CacheDir: e.cache}, tuning{procs: e.procs, debounce: time.Millisecond, poll: time.Hour, sweep: time.Hour, workers: 2, entries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	<-s.Ready()
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= before })
}

func TestCoalesce(t *testing.T) {
	sc := func(id string) core.Event { return core.Event{Kind: core.SessionsChanged, SessionID: id} }
	ec := func(id string) core.Event { return core.Event{Kind: core.EntriesChanged, SessionID: id} }
	var q []core.Event
	for _, ev := range []core.Event{sc("a"), ec("a"), sc("a"), ec("a"), ec("b"), sc("b"), sc("c")} {
		q = coalesce(q, ev)
	}
	if want := []core.Event{sc(""), ec("a"), ec("b")}; !reflect.DeepEqual(q, want) {
		t.Errorf("coalesce = %+v, want %+v", q, want)
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	var ss subscribers
	events, cancel := ss.subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := range 10000 {
			ss.notify(core.Event{Kind: core.EntriesChanged, SessionID: string(rune('a' + i%3))})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("notify blocked on a subscriber that does not read")
	}
	seen := map[string]bool{}
	timeout := time.After(time.Second)
	for len(seen) < 3 {
		select {
		case ev := <-events:
			seen[ev.SessionID] = true
		case <-timeout:
			t.Fatalf("got only %v", seen)
		}
	}
	ss.closeAll()
}

func ids(ss []core.Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func near(a, b float64) bool { d := a - b; return d < 1e-12 && d > -1e-12 }

// A registry file caught half-written does not end its session: the last
// good reading stands until the file reads again, or the process or the
// file goes away.
func TestRegistryHalfWritten(t *testing.T) {
	e := newEnv(t)
	seed(t, e)
	s := e.openWith(func(tn *tuning) { tn.poll = 20 * time.Millisecond })
	now := time.Now()
	e.write("sessions/701.json", registryFile(t, 701, sessA, "busy", "", now))
	waitFor(t, "busy", func() bool { return session(t, s, sessA).Status() == core.StatusBusy })

	e.write("sessions/701.json", `{"pid":701,"sessionId":"`)
	time.Sleep(200 * time.Millisecond) // several polls and a debounce
	if got := session(t, s, sessA).Status(); got != core.StatusBusy {
		t.Fatalf("status while the file is half-written = %s, want busy", got)
	}

	e.write("sessions/701.json", registryFile(t, 701, sessA, "idle", "", now.Add(time.Second)))
	waitFor(t, "idle", func() bool { return session(t, s, sessA).Status() == core.StatusIdle })

	e.write("sessions/701.json", `{"pid":701,`)
	e.procs.kill(701)
	waitFor(t, "a dead process to end the session", func() bool { return session(t, s, sessA).Live == nil })

	e.write("sessions/702.json", registryFile(t, 702, sessA, "busy", "", now))
	waitFor(t, "busy again", func() bool { return session(t, s, sessA).Status() == core.StatusBusy })
	if err := os.Remove(e.path("sessions/702.json")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a removed file to end the session", func() bool { return session(t, s, sessA).Live == nil })
}
