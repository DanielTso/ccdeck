package demo

import (
	"reflect"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// clock is a manual clock for tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(t *testing.T, seed uint64) (*Store, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 18, 15, 4, 5, 0, time.Local)}
	st := New(Options{Seed: seed, Now: c.now})
	t.Cleanup(func() { _ = st.Close() })
	return st, c
}

func steps(st *Store, c *clock, n int) {
	for range n {
		c.t = c.t.Add(time.Second)
		st.Step()
	}
}

func TestDeterministic(t *testing.T) {
	a, ca := newTest(t, 7)
	b, cb := newTest(t, 7)
	steps(a, ca, 50)
	steps(b, cb, 50)
	if !reflect.DeepEqual(a.Sessions(), b.Sessions()) {
		t.Fatal("same seed and clock gave different sessions")
	}
	ea, _ := a.Entries(specs[0].id, 0)
	eb, _ := b.Entries(specs[0].id, 0)
	if !reflect.DeepEqual(ea, eb) {
		t.Fatal("same seed and clock gave different entries")
	}

	other, co := newTest(t, 8)
	steps(other, co, 50)
	if reflect.DeepEqual(a.Sessions(), other.Sessions()) {
		t.Fatal("different seeds gave identical sessions")
	}
}

func TestDataCoversAwkwardCases(t *testing.T) {
	st, _ := newTest(t, 1)
	ss := st.Sessions()
	if len(ss) < 12 {
		t.Fatalf("got %d sessions, want at least 12", len(ss))
	}
	var waiting, nearlyFull, unknownWindow, noCost, wide, long, manyAgents, noTranscript, reported bool
	for _, s := range ss {
		if s.Status() == core.StatusWaiting && s.Live.WaitingFor != "" {
			waiting = true
		}
		if p, ok := s.ContextPercent(); ok && p > 90 {
			nearlyFull = true
		}
		if s.ContextWindow == 0 && s.ContextTokens > 0 {
			unknownWindow = true
		}
		if s.CostKind == core.CostNone {
			noCost = true
		}
		if s.CostKind == core.CostReported {
			reported = true
		}
		if hasWide(s.Title) {
			wide = true
		}
		if len(s.Title) > 80 {
			long = true
		}
		if len(s.Subagents) >= 3 {
			manyAgents = true
		}
		if s.Live != nil && s.Model == "" {
			noTranscript = true
		}
	}
	for name, ok := range map[string]bool{
		"waiting": waiting, "nearly full": nearlyFull, "unknown window": unknownWindow,
		"no cost": noCost, "reported cost": reported, "wide characters": wide, "long title": long,
		"several subagents": manyAgents, "live without transcript": noTranscript,
	} {
		if !ok {
			t.Errorf("no session with %s", name)
		}
	}
	for i := 1; i < len(ss); i++ {
		if ss[i].LastActivity.After(ss[i-1].LastActivity) {
			t.Fatal("sessions not most recent first")
		}
	}
}

func hasWide(s string) bool {
	for _, r := range s {
		if r >= 0x1100 {
			return true
		}
	}
	return false
}

func TestStepMovesAndNotifies(t *testing.T) {
	st, c := newTest(t, 3)
	events, cancel := st.Subscribe()
	defer cancel()
	before, _ := st.Session(specs[0].id)
	beforeEntries, _ := st.Entries(specs[0].id, 0)
	steps(st, c, 40)

	after, _ := st.Session(specs[0].id)
	afterEntries, _ := st.Entries(specs[0].id, 0)
	if len(afterEntries) <= len(beforeEntries) {
		t.Error("busy session gained no entries in 40 steps")
	}
	if after.TotalUsage().Total() <= before.TotalUsage().Total() {
		t.Error("busy session used no tokens in 40 steps")
	}
	if !after.LastActivity.After(before.LastActivity) {
		t.Error("last activity did not move")
	}

	var sawSessions, sawEntries bool
	for len(events) > 0 {
		ev := <-events
		switch ev.Kind {
		case core.SessionsChanged:
			sawSessions = true
		case core.EntriesChanged:
			sawEntries = sawEntries || ev.SessionID != ""
		}
	}
	if !sawSessions || !sawEntries {
		t.Errorf("events: sessions %v, entries %v", sawSessions, sawEntries)
	}
}

func TestSnapshotsAreIsolated(t *testing.T) {
	st, c := newTest(t, 3)
	snap, _ := st.Session(specs[0].id)
	usage := snap.Usage[opus]
	recent := len(snap.Recent)
	snap.Usage[opus] = core.Usage{}
	snap.Live.Status = core.StatusEnded
	steps(st, c, 40)
	if len(snap.Recent) != recent {
		t.Error("snapshot slice changed under the reader")
	}
	again, _ := st.Session(specs[0].id)
	if again.Usage[opus].Output < usage.Output || again.Live.Status == core.StatusEnded {
		t.Error("mutating a snapshot reached the store")
	}
}

func TestEntriesAndSearch(t *testing.T) {
	st, _ := newTest(t, 1)
	all, err := st.Entries(specs[0].id, 0)
	if err != nil || len(all) != len(specs[0].convo) {
		t.Fatalf("Entries all: %d, %v", len(all), err)
	}
	last3, _ := st.Entries(specs[0].id, 3)
	if !reflect.DeepEqual(last3, all[len(all)-3:]) {
		t.Error("limit did not return the most recent entries")
	}
	if _, err := st.Entries("nope", 1); err == nil {
		t.Error("unknown id: want error")
	}

	hits := st.SearchPrompts("REFUND", 0)
	if len(hits) != 1 || hits[0].SessionID != specs[1].id {
		t.Fatalf("SearchPrompts(REFUND) = %+v", hits)
	}
	if got := st.SearchPrompts("the", 2); len(got) != 2 || got[0].Time.Before(got[1].Time) {
		t.Errorf("limit or order wrong: %+v", got)
	}
	if st.SearchPrompts("  ", 0) != nil {
		t.Error("blank query should find nothing")
	}
}

func TestDaily(t *testing.T) {
	st, c := newTest(t, 1)
	days := st.Daily(7)
	if len(days) != 7 {
		t.Fatalf("got %d days", len(days))
	}
	if !days[6].Date.Equal(midnight(c.t)) {
		t.Errorf("last day %v, want today", days[6].Date)
	}
	for i := 1; i < len(days); i++ {
		if !days[i].Date.After(days[i-1].Date) {
			t.Fatal("days not oldest first")
		}
	}
	var today int64
	for _, u := range days[6].Usage {
		today += u.Total()
	}
	if today == 0 {
		t.Error("today has no usage")
	}
	if len(st.Daily(90)) != 90 {
		t.Error("days beyond history should still be returned")
	}
}

func TestCloseAndCancel(t *testing.T) {
	st, _ := newTest(t, 1)
	ch, cancel := st.Subscribe()
	cancel()
	cancel() // idempotent
	if _, ok := <-ch; ok {
		t.Error("cancelled channel still open")
	}
	ch2, _ := st.Subscribe()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch2; ok {
		t.Error("channel open after Close")
	}
	st.Step() // no panic after Close
	if _, ok := <-mustSubscribe(st); ok {
		t.Error("subscribe after Close should return a closed channel")
	}
}

func mustSubscribe(st *Store) <-chan core.Event {
	ch, _ := st.Subscribe()
	return ch
}

func TestBackgroundTicker(t *testing.T) {
	st := New(Options{Seed: 1, Interval: time.Millisecond})
	ch, cancel := st.Subscribe()
	defer cancel()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no event from the background ticker")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestToolCallsCarryIDs(t *testing.T) {
	st, c := newTest(t, 2)
	steps(st, c, 80)
	var long bool
	for _, s := range st.Sessions() {
		entries, _ := st.Entries(s.ID, 0)
		long = long || len(entries) > 200
		open := ""
		for i, e := range entries {
			switch e.Kind {
			case core.EntryToolUse:
				if e.ToolID == "" {
					t.Fatalf("%s: tool call %d has no id", s.Title, i)
				}
				open = e.ToolID
			case core.EntryToolResult:
				if open != "" && e.ToolID != open {
					t.Fatalf("%s: result %d has id %q, want %q", s.Title, i, e.ToolID, open)
				}
				open = ""
			}
		}
	}
	if !long {
		t.Error("no session has a conversation long enough to scroll")
	}
}

func TestCountsCoverConversation(t *testing.T) {
	st, _ := newTest(t, 1)
	for _, s := range st.Sessions() {
		entries, err := st.Entries(s.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		var prompts, uses int
		for _, e := range entries {
			switch e.Kind {
			case core.EntryPrompt:
				prompts++
			case core.EntryToolUse:
				uses++
			}
		}
		if s.Turns < prompts || s.ToolCalls < uses {
			t.Errorf("%q: turns %d, tool calls %d; conversation has %d prompts, %d tool calls",
				s.Title, s.Turns, s.ToolCalls, prompts, uses)
		}
	}
}
