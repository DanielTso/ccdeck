package index

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/meter"
)

// applyMeter on both sides of the boundary, and its precedence over and
// under the other cost kinds.
func TestApplyMeter(t *testing.T) {
	last := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	base := core.Session{ContextTokens: 50_000, ContextWindow: 200_000, CostUSD: 1.5, CostKind: core.CostEstimated}
	report := core.Meter{At: last, HasContext: true, ContextPercent: 40, ContextWindow: 1_000_000, ContextTokens: 400_000, CostUSD: 2.25}

	tests := []struct {
		name     string
		sum      func(s *core.Session)
		meter    func(m *core.Meter)
		at       time.Time // latest main-thread assistant message
		wantCtx  [2]int64  // tokens, window
		wantCost float64
		wantKind core.CostKind
	}{
		{"as new as the transcript: the report wins", nil, nil, last,
			[2]int64{400_000, 1_000_000}, 2.25, core.CostClaude},
		{"newer than the transcript: the report wins", nil, nil, last.Add(-time.Second),
			[2]int64{400_000, 1_000_000}, 2.25, core.CostClaude},
		{"a millisecond older: the transcript wins", nil, nil, last.Add(time.Millisecond),
			[2]int64{50_000, 200_000}, 1.5, core.CostEstimated},
		{"no transcript message yet: the report wins", nil, nil, time.Time{},
			[2]int64{400_000, 1_000_000}, 2.25, core.CostClaude},
		{"a reported end-of-session cost beats the report", func(s *core.Session) { s.CostUSD, s.CostKind = 3.18, core.CostReported }, nil, last,
			[2]int64{400_000, 1_000_000}, 3.18, core.CostReported},
		{"the report beats no price", func(s *core.Session) { s.CostUSD, s.CostKind = 0, core.CostNone }, nil, last,
			[2]int64{400_000, 1_000_000}, 2.25, core.CostClaude},
		{"an older report does not beat no price", func(s *core.Session) { s.CostUSD, s.CostKind = 0, core.CostNone }, nil, last.Add(time.Second),
			[2]int64{50_000, 200_000}, 0, core.CostNone},
		{"a zero cost is no cost", nil, func(m *core.Meter) { m.CostUSD = 0 }, last,
			[2]int64{400_000, 1_000_000}, 1.5, core.CostEstimated},
		{"no context figure keeps the transcript's", nil, func(m *core.Meter) { m.HasContext = false }, last,
			[2]int64{50_000, 200_000}, 2.25, core.CostClaude},
		{"no window size keeps the transcript's", nil, func(m *core.Meter) { m.ContextWindow = 0 }, last,
			[2]int64{50_000, 200_000}, 2.25, core.CostClaude},
		{"tokens from the percentage when the count is missing", nil, func(m *core.Meter) { m.ContextTokens = 0 }, last,
			[2]int64{400_000, 1_000_000}, 2.25, core.CostClaude},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum, m := base, report
			if tt.sum != nil {
				tt.sum(&sum)
			}
			if tt.meter != nil {
				tt.meter(&m)
			}
			applyMeter(&sum, m, tt.at)
			if sum.Meter == nil || *sum.Meter != m {
				t.Errorf("Meter = %+v, want the report kept for display", sum.Meter)
			}
			if got := [2]int64{sum.ContextTokens, sum.ContextWindow}; got != tt.wantCtx {
				t.Errorf("context = %v, want %v", got, tt.wantCtx)
			}
			if sum.CostUSD != tt.wantCost || sum.CostKind != tt.wantKind {
				t.Errorf("cost = %v %s, want %v %s", sum.CostUSD, sum.CostKind, tt.wantCost, tt.wantKind)
			}
		})
	}
}

// putReport writes a status-line report as the helper does.
func (e *env) putReport(id string, at time.Time, cost float64, limits ...core.RateLimit) {
	e.t.Helper()
	r := meter.Report{SessionID: id, Limits: limits, Meter: core.Meter{
		At: at, HasContext: true, ContextPercent: 30, ContextWindow: 1_000_000, ContextTokens: 300_000, CostUSD: cost,
	}}
	if err := meter.Write(e.meter, r); err != nil {
		e.t.Fatal(err)
	}
}

func TestMeterOverridesTranscript(t *testing.T) {
	e := newEnv(t)
	e.meter = filepath.Join(t.TempDir(), "meter")
	ts := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	e.write(transcript("-w", sessA), userLine(t, ts.Add(-time.Second), "hi")+assistantLines(t, ts, "m1", 1000, 10, "hello"))
	e.write(transcript("-w", sessB), userLine(t, ts, "hi")+assistantLines(t, ts, "m2", 1000, 10, "hello"))
	e.putReport(sessA, ts, 4.5)                   // as new as the last message
	e.putReport(sessB, ts.Add(-time.Second), 7.0) // older than it

	s := e.open()
	a := session(t, s, sessA)
	if a.CostKind != core.CostClaude || a.CostUSD != 4.5 || a.ContextTokens != 300_000 || a.ContextWindow != 1_000_000 || a.Meter == nil {
		t.Errorf("session A = %s %v, context %d/%d, meter %v; want the report's figures", a.CostKind, a.CostUSD, a.ContextTokens, a.ContextWindow, a.Meter)
	}
	b := session(t, s, sessB)
	if b.CostKind != core.CostEstimated || b.ContextTokens != 1000 || b.ContextWindow != 200_000 || b.Meter == nil || b.Meter.CostUSD != 7.0 {
		t.Errorf("session B = %s %v, context %d/%d, meter %+v; want the transcript's figures and the report kept", b.CostKind, b.CostUSD, b.ContextTokens, b.ContextWindow, b.Meter)
	}

	// The conversation moves on: the report no longer describes it.
	e.append(transcript("-w", sessA), assistantLines(t, ts.Add(time.Minute), "m3", 2000, 10, "more"))
	waitFor(t, "the transcript to win", func() bool { return session(t, s, sessA).CostKind == core.CostEstimated })
	if a := session(t, s, sessA); a.ContextTokens != 2000 || a.Meter == nil {
		t.Errorf("after a newer message: context %d, meter %v", a.ContextTokens, a.Meter)
	}
	// The helper reports again, through the watch.
	e.putReport(sessA, ts.Add(time.Minute+time.Second), 5.0)
	waitFor(t, "the new report to win", func() bool {
		a := session(t, s, sessA)
		return a.CostKind == core.CostClaude && a.CostUSD == 5.0
	})
	// A report removed is a report gone.
	if err := os.Remove(filepath.Join(e.meter, sessA+".json")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the report to go", func() bool { return session(t, s, sessA).Meter == nil })
}

func TestMeterForLiveSessionWithoutTranscript(t *testing.T) {
	e := newEnv(t)
	e.meter = filepath.Join(t.TempDir(), "meter")
	now := time.Now()
	e.write("sessions/4100.json", registryFile(t, 4100, sessA, "idle", "", now))
	e.putReport(sessA, now, 0.01)
	s := e.open()
	if a := session(t, s, sessA); a.CostKind != core.CostClaude || a.ContextWindow != 1_000_000 {
		t.Errorf("live session without transcript: %s, window %d", a.CostKind, a.ContextWindow)
	}
}

func TestLimits(t *testing.T) {
	now := time.Now()
	five := core.RateLimit{Window: "5h", UsedPercent: 84, ResetsAt: now.Add(time.Hour).Truncate(time.Second).UTC()}
	week := core.RateLimit{Window: "7d", UsedPercent: 40, ResetsAt: now.Add(72 * time.Hour).Truncate(time.Second).UTC()}
	gone := core.RateLimit{Window: "5h", UsedPercent: 99, ResetsAt: now.Add(-time.Minute).Truncate(time.Second).UTC()}

	t.Run("meter off", func(t *testing.T) {
		e := newEnv(t)
		if l, at := e.open().Limits(); l != nil || !at.IsZero() {
			t.Errorf("Limits = %v, %v", l, at)
		}
	})
	t.Run("helper never ran", func(t *testing.T) {
		e := newEnv(t)
		e.meter = filepath.Join(t.TempDir(), "meter")
		if l, at := e.open().Limits(); l != nil || !at.IsZero() {
			t.Errorf("Limits = %v, %v", l, at)
		}
	})
	t.Run("windows of the newest report with limits, expired ones dropped", func(t *testing.T) {
		e := newEnv(t)
		e.meter = filepath.Join(t.TempDir(), "meter")
		at := now.Add(-time.Minute).Truncate(time.Millisecond)
		third := "cccccccc-0000-4000-8000-000000000003"
		for _, id := range []string{sessA, sessB, third} { // this directory's sessions
			e.write(transcript("-w", id), userLine(t, at, "hi"))
		}
		e.putReport(sessA, at.Add(-time.Hour), 1, core.RateLimit{Window: "5h", UsedPercent: 10, ResetsAt: five.ResetsAt})
		e.putReport(sessB, at, 1, five, week, gone)
		e.putReport(third, at.Add(time.Second), 1) // newer, no limits
		s := e.open()
		l, got := s.Limits()
		if !reflect.DeepEqual(l, []core.RateLimit{five, week}) || !got.Equal(at) {
			t.Errorf("Limits = %+v, %v; want 5h and 7d from %v", l, got, at)
		}
		// A newer report changes them, through the watch.
		events, cancel := s.Subscribe()
		defer cancel()
		e.putReport(sessA, now, 1, week)
		waitEvent(t, events, core.Event{Kind: core.SessionsChanged})
		waitFor(t, "the newer limits", func() bool { l, _ := s.Limits(); return len(l) == 1 && l[0] == week })
	})
	t.Run("every window reset", func(t *testing.T) {
		e := newEnv(t)
		e.meter = filepath.Join(t.TempDir(), "meter")
		e.write(transcript("-w", sessA), userLine(t, now, "hi"))
		e.putReport(sessA, now, 1, gone)
		if l, at := e.open().Limits(); l != nil || !at.IsZero() {
			t.Errorf("Limits = %v, %v", l, at)
		}
	})
}

// Without a meter directory, and with one that does not exist or is
// empty, the sessions are exactly what they are with the meter off.
func TestNoMeterDirectoryChangesNothing(t *testing.T) {
	e := newEnv(t)
	ts := time.Now().Add(-time.Hour)
	e.write(transcript("-w", sessA), userLine(t, ts, "hi")+assistantLines(t, ts, "m1", 1000, 10, "hello"))
	e.write("sessions/4100.json", registryFile(t, 4100, sessB, "busy", "", ts))
	snapshot := func(meterDir string) []core.Session {
		e.meter = meterDir
		e.cache = t.TempDir()
		s := e.open()
		defer func() { _ = s.Close() }()
		return s.Sessions()
	}
	off := snapshot("")
	if len(off) != 2 {
		t.Fatalf("want 2 sessions, have %d", len(off))
	}
	empty := filepath.Join(t.TempDir(), "meter")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"missing": filepath.Join(t.TempDir(), "meter"), "empty": empty} {
		if got := snapshot(dir); !reflect.DeepEqual(got, off) {
			t.Errorf("%s meter directory changed the sessions:\n%+v\nwant\n%+v", name, got, off)
		}
	}
}

// The sweep deletes the reports of ended sessions after 30 days, and
// keeps those of live sessions and recent ones.
func TestMeterCleanup(t *testing.T) {
	e := newEnv(t)
	e.meter = filepath.Join(t.TempDir(), "meter")
	now := time.Now()
	ts := now.Add(-40 * 24 * time.Hour)
	e.write(transcript("-w", sessA), userLine(t, ts, "hi")+assistantLines(t, ts, "m1", 1000, 10, "hello"))
	e.write("sessions/4100.json", registryFile(t, 4100, sessB, "busy", "", now))
	recent := "cccccccc-0000-4000-8000-000000000003"
	e.putReport(sessA, ts, 1)
	e.putReport(sessB, ts, 1)
	e.putReport(recent, now.Add(-29*24*time.Hour), 1)

	s := e.openWith(func(tn *tuning) { tn.sweep = 50 * time.Millisecond })
	exists := func(id string) bool { _, err := os.Stat(filepath.Join(e.meter, id+".json")); return err == nil }
	waitFor(t, "the old report to be deleted", func() bool { return !exists(sessA) })
	waitFor(t, "the session to lose it", func() bool { return session(t, s, sessA).Meter == nil })
	if !exists(sessB) || !exists(recent) {
		t.Errorf("live or recent report deleted: live %v, recent %v", exists(sessB), exists(recent))
	}
}

// One meter directory is shared by every Claude directory on the machine,
// so the limits of a store are those reported by its own sessions: a
// report for a session of another Claude directory (another account) is
// not this account's, until a session of that id turns up here.
func TestLimitsOnlyFromThisClaudeDirectory(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour).Truncate(time.Second).UTC()
	mine := core.RateLimit{Window: "5h", UsedPercent: 30, ResetsAt: reset}
	other := core.RateLimit{Window: "5h", UsedPercent: 97, ResetsAt: reset.Add(time.Minute)}
	elsewhere := "eeeeeeee-0000-4000-8000-000000000005"
	ts := now.Add(-time.Hour).Truncate(time.Millisecond)

	t.Run("only another directory's session reported", func(t *testing.T) {
		e := newEnv(t)
		e.meter = filepath.Join(t.TempDir(), "meter")
		e.write(transcript("-w", sessA), userLine(t, ts, "hi")+assistantLines(t, ts, "m1", 1000, 10, "hello"))
		e.putReport(elsewhere, now, 1, other)
		if l, at := e.open().Limits(); l != nil || !at.IsZero() {
			t.Errorf("Limits = %+v, %v; want none: the report belongs to another Claude directory", l, at)
		}
	})
	t.Run("a newer report from another directory does not win", func(t *testing.T) {
		e := newEnv(t)
		e.meter = filepath.Join(t.TempDir(), "meter")
		e.write(transcript("-w", sessA), userLine(t, ts, "hi")+assistantLines(t, ts, "m1", 1000, 10, "hello"))
		at := now.Add(-time.Minute).Truncate(time.Millisecond)
		e.putReport(sessA, at, 1, mine)
		e.putReport(elsewhere, now, 1, other)
		s := e.open()
		if l, got := s.Limits(); !reflect.DeepEqual(l, []core.RateLimit{mine}) || !got.Equal(at) {
			t.Errorf("Limits = %+v, %v; want this directory's %+v from %v", l, got, mine, at)
		}
		// A session of that id turns up here: its report is this account's.
		e.write(transcript("-w", elsewhere), userLine(t, ts, "hi")+assistantLines(t, ts, "m2", 1000, 10, "hello"))
		waitFor(t, "the session's limits", func() bool { l, _ := s.Limits(); return len(l) == 1 && l[0] == other })
	})
	t.Run("a live session without a transcript counts", func(t *testing.T) {
		e := newEnv(t)
		e.meter = filepath.Join(t.TempDir(), "meter")
		e.write("sessions/4100.json", registryFile(t, 4100, sessB, "idle", "", now))
		e.putReport(sessB, now, 1, mine)
		s := e.open()
		if l, _ := s.Limits(); !reflect.DeepEqual(l, []core.RateLimit{mine}) {
			t.Errorf("Limits = %+v, want %+v", l, mine)
		}
		// Its process ends: the session is gone from here, and so is what
		// it reported.
		e.procs.kill(4100)
		waitFor(t, "the limits to go", func() bool { l, _ := s.Limits(); return l == nil })
	})
}

// Claude Code takes its rate-limit figures from API responses, and the
// status line of an idle process re-stamps its old figures on every
// refresh. A newer report is therefore not a fresher figure: within one
// window (the same reset time) use only rises, so the highest wins, and a
// later reset time is a later window.
func TestLimitsFreshestFigures(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour).Truncate(time.Second).UTC()
	ts := now.Add(-time.Hour).Truncate(time.Millisecond)
	e := newEnv(t)
	e.meter = filepath.Join(t.TempDir(), "meter")
	e.write(transcript("-w", sessA), userLine(t, ts, "hi")+assistantLines(t, ts, "m1", 1000, 10, "hello"))
	e.write(transcript("-w", sessB), userLine(t, ts, "hi")+assistantLines(t, ts, "m2", 1000, 10, "hello"))
	active := now.Add(-2 * time.Second).Truncate(time.Millisecond)
	e.putReport(sessA, active, 1, core.RateLimit{Window: "5h", UsedPercent: 85, ResetsAt: reset})
	e.putReport(sessB, active.Add(time.Second), 1, core.RateLimit{Window: "5h", UsedPercent: 20, ResetsAt: reset})
	s := e.open()
	l, at := s.Limits()
	if want := []core.RateLimit{{Window: "5h", UsedPercent: 85, ResetsAt: reset}}; !reflect.DeepEqual(l, want) || !at.Equal(active) {
		t.Errorf("Limits = %+v, %v; want %+v reported at %v", l, at, want, active)
	}
}
