package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DanielTso/ccdeck/internal/core"
)

func headerLine(h *harness) string { return strings.SplitN(h.view(), "\n", 2)[0] }

// The demo reports a five-hour window at 84%: the top bar shows it with
// its warning mark next to the status counts, at every width from 80.
func TestTopBarRateLimit(t *testing.T) {
	for _, tt := range []struct {
		w, h        int
		view        string
		wantReset   bool
		wantCompact bool
	}{
		{140, 40, "1", true, false},
		{100, 30, "1", true, true},
		{80, 24, "1", true, true},
		{80, 24, "o", false, true}, // the transcript's tab takes room
	} {
		h := newHarness(t, tt.w, tt.h)
		if tt.view == "o" {
			h.keys("o")
		}
		top := headerLine(h)
		if w := ansi.StringWidth(top); w > tt.w {
			t.Errorf("%dx%d %s: header is %d cells", tt.w, tt.h, tt.view, w)
		}
		if !strings.Contains(top, " 5h ▇ 84%!") { // a space before it: the sides never touch
			t.Errorf("%dx%d %s: header lacks the window: %q", tt.w, tt.h, tt.view, top)
		}
		if got := strings.Contains(top, "↻1h11m"); got != tt.wantReset {
			t.Errorf("%dx%d %s: reset time shown = %v: %q", tt.w, tt.h, tt.view, got, top)
		}
		if got := !strings.Contains(top, "sessions"); got != tt.wantCompact {
			t.Errorf("%dx%d %s: compact counts = %v: %q", tt.w, tt.h, tt.view, got, top)
		}
		// The counts keep the right edge.
		if !strings.HasSuffix(strings.TrimRight(top, " "), "12") && !strings.HasSuffix(strings.TrimRight(top, " "), "12 sessions") {
			t.Errorf("%dx%d %s: counts moved from the right edge: %q", tt.w, tt.h, tt.view, top)
		}
	}
}

// The warning never rests on colour alone, and a window below 80% has no
// mark.
func TestLimitBadgeMarksOnlyHighWindows(t *testing.T) {
	store := bigStore(3, 0)
	store.limits = []core.RateLimit{{Window: "7d", UsedPercent: 79.9, ResetsAt: testNow.Add(50 * time.Hour)}}
	store.limitsAt = testNow
	h := newHarnessOn(t, store, 120, 30)
	if top := headerLine(h); !strings.Contains(top, "7d ▇ 79% ↻2d2h") || strings.Contains(top, "!") {
		t.Errorf("header %q", top)
	}
}

// Without limits, nothing about them is drawn: no badge, no block, no
// gap, and the Usage view lays out exactly as before.
func TestNoLimitsDrawsNothing(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {140, 40}} {
		store := bigStore(5, 0)
		h := newHarnessOn(t, store, size[0], size[1])
		top := headerLine(h)
		right := h.m.counts(false) + " "
		if ansi.StringWidth(top) != size[0] || !strings.HasSuffix(top, ansi.Strip(right)) || strings.Contains(top, "↻") {
			t.Errorf("%v: header %q", size, top)
		}
		h.keys("3")
		h.send(dailyMsg{})
		screen := h.view()
		if strings.Contains(screen, "Rate limits") {
			t.Errorf("%v: Usage shows a rate-limit block without limits:\n%s", size, screen)
		}
		withoutTop := h.usage().layout().tableTop

		// The same store with limits gains the block, and loses it again
		// when they go.
		store.limits = []core.RateLimit{{Window: "5h", UsedPercent: 20, ResetsAt: testNow.Add(time.Hour)}}
		h.send(clockMsg{})
		if size[1] >= 40 {
			if !strings.Contains(h.view(), "Rate limits") {
				t.Errorf("%v: no block with limits:\n%s", size, h.view())
			}
			if got := h.usage().layout().tableTop; got != withoutTop+3 {
				t.Errorf("%v: table top %d with one window, want %d", size, got, withoutTop+3)
			}
		}
		store.limits = nil
		h.send(clockMsg{})
		if got := h.usage().layout().tableTop; got != withoutTop || strings.Contains(h.view(), "Rate limits") {
			t.Errorf("%v: after the limits went the table starts at %d, want %d", size, got, withoutTop)
		}
	}
}

func TestUsageRateLimitBlock(t *testing.T) {
	store := bigStore(3, 0)
	store.limitsAt = testNow.Add(-2 * time.Minute)
	store.limits = []core.RateLimit{
		{Window: "5h", UsedPercent: 84.2, ResetsAt: testNow.Add(72 * time.Minute)},
		{Window: "7d", UsedPercent: 41, ResetsAt: testNow.Add(76 * time.Hour)},
		{Window: "spend", UsedPercent: 62.8, ResetsAt: testNow.Add(300 * time.Hour), UsedUSD: 314.12, LimitUSD: 500, Period: "monthly"},
		{Window: "5h", UsedPercent: 99, ResetsAt: testNow.Add(-time.Second)}, // reset already
	}
	h := newHarnessOn(t, store, 140, 40)
	h.keys("3")
	h.send(dailyMsg{})
	screen := h.view()
	for _, want := range []string{
		"Rate limits · from Claude Code, 2m ago",
		"5 hours", "84%!", "resets in 1h12m (16:16)",
		"7 days", "41%", "resets in 3d4h",
		"spend", "62%", "$314.12 of $500.00 monthly",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("Usage lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "99%") {
		t.Errorf("a window that has reset is shown:\n%s", screen)
	}
	// Short windows: the days, then the models give way before the
	// limits; the table keeps its rows.
	for _, height := range []int{24, 18, 14} {
		h.send(tea.WindowSizeMsg{Width: 80, Height: height})
		lay := h.usage().layout()
		if _, n := h.usage().tableRows(); n < minTableRows || !lay.limits && (lay.days || lay.models) {
			t.Errorf("80x%d: layout %+v with %d table rows", height, lay, n)
		}
	}
}

// A window disappears when its reset time passes, without a store event.
func TestLimitsExpireOnTheClock(t *testing.T) {
	store := bigStore(3, 0)
	store.limits = []core.RateLimit{{Window: "5h", UsedPercent: 90, ResetsAt: testNow.Add(time.Minute)}}
	now := testNow
	h := newHarnessOn(t, store, 120, 30, func(o *Options) { o.Now = func() time.Time { return now } })
	if !strings.Contains(headerLine(h), "5h") {
		t.Fatalf("header %q", headerLine(h))
	}
	now = now.Add(2 * time.Minute)
	h.send(clockMsg{})
	if top := headerLine(h); strings.Contains(top, "5h") {
		t.Errorf("an expired window is still shown: %q", top)
	}
}

// The preview says when context and cost are Claude Code's own figures.
func TestPreviewNamesTheMeterAsSource(t *testing.T) {
	store := bigStore(2, 0)
	s := &store.sessions[0]
	s.ContextTokens, s.ContextWindow = 300_000, 1_000_000
	s.CostUSD, s.CostKind = 4.5, core.CostClaude
	s.Meter = &core.Meter{At: testNow, HasContext: true, ContextPercent: 30, ContextWindow: 1_000_000, ContextTokens: 300_000, CostUSD: 4.5}
	h := newHarnessOn(t, store, 140, 40)
	screen := h.view()
	for _, want := range []string{"300k / 1.00M · Claude Code", "~$4.50  Claude Code's estimate"} {
		if !strings.Contains(screen, want) {
			t.Errorf("preview lacks %q:\n%s", want, screen)
		}
	}
	// An older report kept for display is not named as the source.
	s.Meter.ContextTokens, s.CostKind = 280_000, core.CostEstimated
	h.m.env.sessions = store.Sessions()
	h.send(sessionsMsg{})
	if strings.Contains(h.view(), "Claude Code") {
		t.Errorf("an older report is named as the source:\n%s", h.view())
	}
}

// Pasting goes into the field that has focus.
func TestPasteReachesTheFilter(t *testing.T) {
	h := newHarness(t, 140, 40)
	h.keys("/")
	h.send(tea.PasteMsg{Content: "bazel\n"})
	if got := h.sessions().filter.Value(); got != "bazel " {
		t.Errorf("filter = %q", got)
	}
	if !strings.Contains(h.view(), "/ bazel") {
		t.Errorf("view lacks the pasted filter:\n%s", h.view())
	}
	if s := h.selected(); !strings.Contains(s.Title, "Bazel") {
		t.Errorf("the list was not filtered: selected %q", s.Title)
	}
}
