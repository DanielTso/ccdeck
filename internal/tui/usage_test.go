package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestUsageView(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("3")
	v := h.view()
	for _, want := range []string{
		"Today", "7 days", "tokens", "fresh", "~$", "estimate", "sessions",
		"Fresh tokens per day", "today last",
		"Model", "opus 5.5", "sonnet 5", "haiku 4.5", "share of 7d fresh",
		"Sessions", "cost: ~ estimate · — no price known",
		"context", "last hour", "tok/h",
		"Refactor the rate limiter", "~$2.69", // estimated
		"$3.18", // reported, no tilde
	} {
		if !strings.Contains(v, want) {
			t.Errorf("usage view is missing %q", want)
		}
	}
	if strings.Contains(v, "$0.00") {
		t.Error("a cost shows as $0.00")
	}
	// Live sessions come first, and the session with no price shows a dash.
	uv := h.usage()
	for i, s := range uv.rows {
		if s.Live == nil {
			for _, r := range uv.rows[i:] {
				if r.Live != nil {
					t.Fatal("a live session is listed after an ended one")
				}
			}
			break
		}
	}
	row := lineWith(t, v, "Draft the Q3 architecture")
	if !strings.HasSuffix(strings.TrimSpace(row), "—") {
		t.Errorf("a session with no price should end in a dash: %q", row)
	}
	// Burn-rate sparklines only for sessions active in the last hour.
	if row := lineWith(t, v, "Refactor the rate limiter"); !strings.ContainsAny(row, "▁▂▃▄▅▆▇█") {
		t.Errorf("busy session has no sparkline: %q", row)
	}
}

func lineWith(t *testing.T, s, sub string) string {
	t.Helper()
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	t.Fatalf("no line contains %q", sub)
	return ""
}

func TestUsageNoPriceIsHonest(t *testing.T) {
	f := bigStore(3, 0)
	f.days = []core.DayUsage{
		{Date: testNow.AddDate(0, 0, -1), Usage: map[string]core.Usage{"acme": {Input: 10, Output: 2000}}},
		{Date: testNow, Usage: map[string]core.Usage{"acme": {Input: 5, Output: 900}}, Sessions: 1},
	}
	f.sessions[1].CostKind, f.sessions[1].CostUSD = core.CostNone, 0
	h := newHarnessOn(t, f, 120, 35)
	h.keys("3")
	v := h.view()
	for _, label := range []string{"Today", "2 days"} {
		if l := lineWith(t, v, label); !strings.Contains(l, "no price known") || strings.Contains(l, "$") {
			t.Errorf("days with tokens and no price must not show dollars: %q", l)
		}
	}
	if row := lineWith(t, v, "number 1 "); !strings.HasSuffix(strings.TrimSpace(row), "—") {
		t.Errorf("a session with no price should show a dash: %q", row)
	}
}

func TestUsageLoadsDailyOffTheUpdateLoop(t *testing.T) {
	f := bigStore(20, 0)
	h := newHarnessOn(t, f, 120, 35)
	if f.dailyCalls.Load() != 0 {
		t.Fatal("Daily called before the Usage view was shown")
	}
	_, cmd := h.m.Update(keyPress("3"))
	if f.dailyCalls.Load() != 0 {
		t.Fatal("Daily called inside Update")
	}
	if !strings.Contains(h.view(), "loading daily totals") {
		t.Fatal("no loading state")
	}
	h.run(cmd)
	if f.dailyCalls.Load() != 1 || !h.usage().loaded {
		t.Fatal("showing Usage did not load the daily totals")
	}
	// A burst of changes schedules one reload, not one per change.
	var ticks int
	for range 10 {
		if _, cmd := h.m.Update(sessionsMsg{}); cmd != nil {
			ticks++
		}
	}
	if ticks != 1 || f.dailyCalls.Load() != 1 {
		t.Fatalf("%d reloads scheduled, %d Daily calls", ticks, f.dailyCalls.Load())
	}
	h.send(dailyDueMsg{})
	if f.dailyCalls.Load() != 2 {
		t.Fatal("the scheduled reload did not run")
	}
	h.keys("1")
	h.send(dailyDueMsg{})
	if f.dailyCalls.Load() != 2 {
		t.Fatal("a hidden Usage view reloaded")
	}
}

func TestUsageNavigation(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("3")
	uv := h.usage()
	h.keys("down", "down")
	if uv.cursor != 2 {
		t.Fatalf("cursor %d", uv.cursor)
	}
	h.send(tea.MouseWheelMsg{X: 5, Y: 20, Button: tea.MouseWheelUp})
	if uv.cursor != 1 {
		t.Fatalf("wheel up: cursor %d", uv.cursor)
	}
	top, _ := uv.tableRows()
	h.send(tea.MouseClickMsg{X: 5, Y: top + 1 + 6, Button: tea.MouseLeft}) // +1 for the app header
	if uv.cursor != 6 {
		t.Fatalf("click: cursor %d, want 6", uv.cursor)
	}
	s, _ := uv.Selected()
	if s.Live != nil {
		t.Fatalf("row 6 should be an ended session, got %q", s.Title)
	}
	h.keys("enter")
	if calls := h.l.Calls(); len(calls) != 1 || calls[0] != "resume "+s.ID {
		t.Fatalf("calls = %v", calls)
	}
	h.keys("o")
	if h.m.active != viewTranscript || h.transcript().id != s.ID {
		t.Fatal("o did not open the selected session")
	}
	h.keys("esc")
	if h.m.active != viewUsage {
		t.Fatal("esc from the transcript should return to Usage")
	}
	// The selection survives a refresh.
	h.send(sessionsMsg{})
	if got, _ := uv.Selected(); got.ID != s.ID {
		t.Fatal("refresh moved the selection")
	}
}

func TestUsageScrollsManySessions(t *testing.T) {
	h := newHarnessOn(t, bigStore(500, 0), 100, 30)
	h.keys("3", "end")
	uv := h.usage()
	if uv.cursor != 499 || !strings.Contains(h.view(), "number 499 ") {
		t.Fatal("end did not reach the last of 500 sessions")
	}
	h.keys("home")
	if uv.cursor != 0 || uv.offset != 0 {
		t.Fatal("home did not return to the top")
	}
}

func TestBurnBinsAndSparkline(t *testing.T) {
	now := testNow
	samples := []core.Sample{
		{Time: now.Add(-2 * time.Hour), Tokens: 999}, // too old
		{Time: now.Add(-59 * time.Minute), Tokens: 10},
		{Time: now.Add(-30 * time.Minute), Tokens: 40},
		{Time: now.Add(-29 * time.Minute), Tokens: 40},
		{Time: now, Tokens: 5},
	}
	bins := burnBins(samples, now, 4)
	want := []int64{10, 0, 80, 5}
	for i := range want {
		if bins[i] != want[i] {
			t.Fatalf("bins = %v, want %v", bins, want)
		}
	}
	if got := sparkline(bins, 80); got != "▁ █▁" {
		t.Fatalf("sparkline = %q", got)
	}
	if got := sparkline([]int64{0, 0}, 0); got != "  " {
		t.Fatalf("empty sparkline = %q", got)
	}
}

func TestUsageSelectionWhileSessionsArrive(t *testing.T) {
	scanning := make(chan struct{}) // never closed: the scan is running
	h := newHarness(t, 120, 35, func(o *Options) { o.Ready = scanning })
	h.keys("3")
	all := slices.Clone(h.m.env.sessions)
	h.setSessions(func([]core.Session) []core.Session { return all[len(all)-1:] })
	h.setSessions(func([]core.Session) []core.Session { return all })
	u := h.usage()
	if s, _ := u.Selected(); s.ID != u.rows[0].ID {
		t.Fatalf("selected %q, want the top row %q", s.Title, u.rows[0].Title)
	}
	h.keys("down")
	picked, _ := u.Selected()
	h.setSessions(func(ss []core.Session) []core.Session {
		return slices.DeleteFunc(ss, func(s core.Session) bool { return s.ID == u.rows[0].ID })
	})
	if s, _ := u.Selected(); s.ID != picked.ID {
		t.Errorf("selected %q, want the pick %q", s.Title, picked.Title)
	}
}

// A session active on several days counts once in the week's total.
func TestUsageWeekCountsDistinctSessions(t *testing.T) {
	f := bigStore(3, 0)
	u := map[string]core.Usage{"acme": {Input: 10, Output: 20}}
	f.days = []core.DayUsage{
		{Date: testNow.AddDate(0, 0, -1), Usage: u, Sessions: 2, SessionIDs: []string{"s-a", "s-b"}},
		{Date: testNow, Usage: u, Sessions: 2, SessionIDs: []string{"s-b", "s-c"}},
	}
	h := newHarnessOn(t, f, 120, 35)
	h.keys("3")
	l := lineWith(t, h.view(), "2 days")
	if !strings.Contains(l, "3 sessions") {
		t.Errorf("week line should count 3 distinct sessions: %q", l)
	}
}

// A day whose cost is unknown makes the week's figure partial, never a
// complete-looking estimate.
func TestUsageWeekCostWithAnUnpricedDayIsPartial(t *testing.T) {
	f := bigStore(3, 0)
	f.days = []core.DayUsage{
		{Date: testNow.AddDate(0, 0, -1), Usage: map[string]core.Usage{"acme": {Input: 1_000_000}}, CostUSD: 4.04,
			Sessions: 1, SessionIDs: []string{"s-a"}},
		{Date: testNow, Usage: map[string]core.Usage{"acme": {Input: 1_000_000}, "mystery": {Input: 50}},
			Sessions: 2, SessionIDs: []string{"s-a", "s-b"}},
	}
	h := newHarnessOn(t, f, 140, 35)
	h.keys("3")
	v := h.view()
	if l := lineWith(t, v, "Today"); !strings.Contains(l, "no price known") {
		t.Errorf("today: %q", l)
	}
	l := lineWith(t, v, "2 days")
	if !strings.Contains(l, "partial") || strings.Contains(l, "~$4.04 estimate") {
		t.Errorf("week with an unpriced day must say it is partial: %q", l)
	}
}
