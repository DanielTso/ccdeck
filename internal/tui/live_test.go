package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestLiveGrid(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("2")
	lv := h.live()
	if len(lv.slots) != 4 {
		t.Fatalf("%d panes at 120x35, want 4", len(lv.slots))
	}
	waiting := h.sessionTitled("flaky refund")
	if lv.slots[0] != waiting.ID {
		t.Fatalf("pane 1 holds %q; the waiting session should come first", lv.slots[0])
	}
	v := h.view()
	for _, want := range []string{
		"╔", "◆ WAITING", "needs you: approve Bash: go test", // waiting: double border, word, what for
		"╔═ ▶1",                                                  // focus: a marker, not colour alone
		"Refactor the rate limiter", "opus 5.5", "62%", "~$2.69", // header facts
		"The burst test is off by one", // the conversation tail
		"4 panes · 5 live · 1 without a pane", "Migrate the build",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("live view is missing %q", want)
		}
	}
	h.keys("tab")
	if v := h.view(); !strings.Contains(v, "┏━ ▶2") || !strings.Contains(v, "┃") {
		t.Error("a focused pane should have a heavy border and the marker")
	}
}

func TestLiveDropsPanesWhenNarrow(t *testing.T) {
	for _, tt := range []struct{ w, h, want int }{{80, 24, 2}, {120, 35, 4}, {200, 50, 4}, {40, 10, 1}} {
		h := newHarness(t, tt.w, tt.h)
		if got := len(h.live().slots); got != tt.want {
			t.Errorf("%dx%d: %d panes, want %d", tt.w, tt.h, got, tt.want)
		}
	}
}

// TestLivePanesStayPut checks the assignment rule: panes keep their
// sessions through activity, an ended session keeps its pane until a live
// session needs it, and the focused pane is never taken.
func TestLivePanesStayPut(t *testing.T) {
	h := newHarness(t, 120, 35, func(o *Options) { o.Layout.LivePanes = 5 })
	h.keys("2")
	lv := h.live()
	start := slices.Clone(lv.slots)
	if len(start) != 5 || slices.Contains(start, "") {
		t.Fatalf("five live sessions should fill five panes: %v", start)
	}

	defer func(t0 time.Time) { testNow = t0 }(testNow)
	for range 60 {
		testNow = testNow.Add(time.Second)
		h.store.Step()
		h.setSessions(func([]core.Session) []core.Session { return h.store.Sessions() })
	}
	if !slices.Equal(lv.slots, start) {
		t.Fatalf("panes reshuffled under activity:\n%v\n%v", start, lv.slots)
	}

	// The session in pane 3 ends: it stays, marked ended.
	ended := lv.slots[2]
	h.setSessions(func(ss []core.Session) []core.Session {
		for i := range ss {
			if ss[i].ID == ended {
				ss[i].Live = nil
			}
		}
		return ss
	})
	if lv.slots[2] != ended || !strings.Contains(h.view(), "· ended") {
		t.Fatalf("an ended session should keep its pane, marked ended:\n%s", h.view())
	}

	// A new session starts waiting: it takes the ended session's pane,
	// not a live one, and nothing else moves.
	h.setSessions(func(ss []core.Session) []core.Session {
		fresh := ss[0]
		fresh.ID, fresh.Title = "new-waiting", "A brand new session that needs approval"
		fresh.Live = &core.Live{PID: 9, Status: core.StatusWaiting, WaitingFor: "approve Edit: x.go", StatusSince: testNow}
		return append([]core.Session{fresh}, ss...)
	})
	want := slices.Clone(start)
	want[2] = "new-waiting"
	if !slices.Equal(lv.slots, want) {
		t.Fatalf("slots = %v, want %v", lv.slots, want)
	}

	// With no ended pane free, another newcomer waits on the strip, loud.
	h.setSessions(func(ss []core.Session) []core.Session {
		fresh := ss[0]
		fresh.ID, fresh.Title = "second-waiting", "Second session that needs you"
		fresh.Live = &core.Live{PID: 10, Status: core.StatusWaiting, WaitingFor: "approve Bash: rm -rf build", StatusSince: testNow}
		return append([]core.Session{fresh}, ss...)
	})
	if !slices.Equal(lv.slots, want) {
		t.Fatal("a newcomer displaced a live session")
	}
	strip := lastLines(h.view(), 2)[0]
	if !strings.Contains(strip, "◆ WAITING") || !strings.Contains(strip, "Second session that needs you") {
		t.Fatalf("a waiting session without a pane is not announced: %q", strip)
	}

	// The focused pane is never taken, even when its session has ended.
	h.keys("tab", "tab") // focus pane 3
	h.setSessions(func(ss []core.Session) []core.Session {
		for i := range ss {
			if ss[i].ID == "new-waiting" {
				ss[i].Live = nil
			}
		}
		return ss
	})
	if lv.slots[2] != "new-waiting" {
		t.Fatal("the focused pane was reassigned")
	}
	h.keys("tab") // focus moves away: now pane 3 may go to the waiting newcomer
	h.send(sessionsMsg{})
	if lv.slots[2] != "second-waiting" {
		t.Fatalf("pane 3 = %q once unfocused, want the waiting newcomer", lv.slots[2])
	}
}

func lastLines(s string, n int) []string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-n:]
}

func TestLiveKeys(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("2")
	lv := h.live()
	h.keys("tab")
	if lv.focus != 1 {
		t.Fatalf("tab: focus %d", lv.focus)
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	h.send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if lv.focus != len(lv.slots)-1 {
		t.Fatalf("shift+tab should wrap: focus %d", lv.focus)
	}

	h.keys("z")
	if v := h.view(); !lv.zoom || strings.Count(v, "┏") != 1 || strings.Count(v, "╭") != 0 {
		t.Fatalf("zoom should show the focused pane alone:\n%s", v)
	}
	h.keys("esc")
	if lv.zoom || h.m.active != viewLive {
		t.Fatal("esc should unzoom first")
	}

	h.keys("+")
	if len(lv.slots) != 5 {
		t.Fatalf("+: %d panes", len(lv.slots))
	}
	if !strings.Contains(h.view(), "5 panes · 5 live") || strings.Contains(h.view(), "without a pane") {
		t.Fatal("the fifth pane should take the session that had none")
	}
	for range 12 {
		h.keys("+")
	}
	if !strings.Contains(h.view(), "fit in this window") {
		t.Error("asking for more panes than fit is not explained")
	}
	for range 20 {
		h.keys("-")
	}
	if len(lv.slots) != 1 {
		t.Fatalf("-: %d panes, want 1", len(lv.slots))
	}

	// enter focuses a live session; it never resumes one.
	h.keys("enter")
	if calls := h.l.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "focus ") {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.Contains(h.view(), "cannot be reached from here") {
		t.Error("unreachable session not explained")
	}

	// o opens the focused pane's transcript.
	id := lv.slots[lv.focus]
	h.keys("o")
	if h.m.active != viewTranscript || h.transcript().id != id {
		t.Fatal("o did not open the focused session's transcript")
	}
	h.keys("esc")
	if h.m.active != viewLive {
		t.Fatal("esc from the transcript should return to Live")
	}
	h.keys("esc")
	if h.m.active != viewSessions {
		t.Fatal("esc from Live should go back to Sessions")
	}
}

func TestLiveMouseAndHold(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("2")
	lv := h.live()
	rs := lv.rects()
	// Screen line 0 is the header, so view line y is screen line y+1.
	h.send(tea.MouseClickMsg{X: rs[3].x + 5, Y: rs[3].y + 3, Button: tea.MouseLeft})
	if lv.focus != 3 {
		t.Fatalf("click focused pane %d, want 4", lv.focus+1)
	}
	busy := h.sessionTitled("rate limiter").ID
	p := slices.Index(lv.slots, busy)
	h.send(tea.MouseWheelMsg{X: rs[p].x + 5, Y: rs[p].y + 5, Button: tea.MouseWheelUp})
	if lv.back[busy] == 0 || !strings.Contains(h.view(), "paused") {
		t.Fatal("wheel up did not scroll the pane back")
	}
	shown := len(lv.entries[busy])
	defer func(t0 time.Time) { testNow = t0 }(testNow)
	for len(lv.held[busy]) == 0 {
		testNow = testNow.Add(time.Second)
		h.store.Step()
		h.send(entriesChangedMsg{id: busy})
	}
	if len(lv.entries[busy]) != shown {
		t.Fatal("a scrolled-back pane changed under the reader")
	}
	for range 20 {
		h.send(tea.MouseWheelMsg{X: rs[p].x + 5, Y: rs[p].y + 5, Button: tea.MouseWheelDown})
	}
	if lv.back[busy] != 0 || len(lv.entries[busy]) <= shown || len(lv.held) != 0 {
		t.Fatal("back at the bottom, the held entries should show")
	}
}

func TestLiveLoadsOnlyWhenShown(t *testing.T) {
	h := newHarness(t, 120, 35)
	lv := h.live()
	if len(lv.entries) != 0 {
		t.Fatal("hidden Live view loaded entries at start")
	}
	h.keys("2")
	for _, id := range lv.slots {
		if _, ok := lv.entries[id]; id != "" && !ok {
			t.Fatalf("pane for %s not loaded when shown", id)
		}
	}
	h.keys("1")
	id := lv.slots[0]
	seq := lv.seq[id]
	h.send(entriesChangedMsg{id: id})
	if lv.seq[id] != seq || !lv.dirty[id] {
		t.Fatal("hidden Live view should mark changes, not load")
	}
	h.keys("2")
	if lv.seq[id] == seq || lv.dirty[id] {
		t.Fatal("showing Live did not reload the changed pane")
	}
	stale := liveEntriesMsg{id: id, seq: seq, entries: nil}
	h.send(stale)
	if len(lv.entries[id]) == 0 {
		t.Fatal("a stale load replaced newer entries")
	}
}

// TestLiveLoadsAfterResize: panes that come back when the window grows
// load their conversations, though SetSize cannot return commands.
func TestLiveLoadsAfterResize(t *testing.T) {
	h := newHarness(t, 200, 50)
	h.keys("2", "+")
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.send(tea.WindowSizeMsg{Width: 120, Height: 35})
	lv := h.live()
	for _, id := range lv.slots {
		if _, ok := lv.entries[id]; id != "" && !ok {
			t.Fatalf("pane for %s still loading after the window grew", id)
		}
	}
	if strings.Contains(h.view(), "Loading…") {
		t.Fatal("a pane is stuck loading")
	}
}

func TestLiveWithoutLiveSessions(t *testing.T) {
	f := bigStore(5, 0)
	f.sessions[0].Live = nil
	h := newHarnessOn(t, f, 100, 30)
	h.keys("2")
	v := h.view()
	if !strings.Contains(v, "No live sessions") || !strings.Contains(v, "0 live") {
		t.Fatalf("empty grid not explained:\n%s", v)
	}
	if strings.Count(v, "╭") != 0 || len(h.live().slots) != 0 {
		t.Fatalf("with nothing live there should be one message, not empty panes:\n%s", v)
	}
	h.keys("enter", "z", "tab")
	if len(h.l.Calls()) != 0 {
		t.Fatal("keys on an empty pane did something")
	}
}

func TestArrange(t *testing.T) {
	for _, tt := range []struct{ n, w, h, cols, rows int }{
		{4, 120, 32, 2, 2}, {4, 200, 47, 2, 2}, {2, 80, 21, 1, 2}, {6, 200, 47, 3, 2}, {1, 50, 10, 1, 1},
	} {
		g, ok := arrange(tt.n, tt.w, tt.h)
		if !ok || g.cols != tt.cols || g.rows != tt.rows {
			t.Errorf("arrange(%d, %d, %d) = %+v %v, want %dx%d", tt.n, tt.w, tt.h, g, ok, tt.cols, tt.rows)
		}
	}
	if _, ok := arrange(4, 80, 21); ok {
		t.Error("four panes should not fit in 80x21")
	}
	// Panes tile the grid with no gaps or overlaps.
	h := newHarness(t, 121, 36)
	h.keys("2", "+")
	lv := h.live()
	covered := map[[2]int]int{}
	for _, r := range lv.rects() {
		for y := r.y; y < r.y+r.h; y++ {
			for x := r.x; x < r.x+r.w; x++ {
				covered[[2]int{x, y}]++
			}
		}
	}
	if len(covered) != lv.w*lv.gridH() {
		t.Fatalf("panes cover %d cells of %d", len(covered), lv.w*lv.gridH())
	}
	for c, n := range covered {
		if n != 1 {
			t.Fatal(fmt.Sprint("cell covered twice: ", c))
		}
	}
}

func TestLiveScrollKeys(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("2", "tab", "tab") // pane 3: the busy session with the longest tail
	lv := h.live()
	id := lv.slots[lv.focus]
	h.keys("home")
	top := lv.back[id]
	if top == 0 || !strings.Contains(h.view(), "The rate limiter is global right now") {
		t.Fatalf("home did not scroll to the start of the tail (back %d)", top)
	}
	h.keys("k")
	if lv.back[id] != top {
		t.Fatal("scrolled past the oldest line")
	}
	h.keys("j")
	if lv.back[id] != top-1 {
		t.Fatalf("j: back %d, want %d", lv.back[id], top-1)
	}
	h.keys("end")
	if lv.back[id] != 0 || strings.Contains(h.view(), "paused") {
		t.Fatal("end did not return to following")
	}
}

// A transient resize keeps the focused session on screen and focused, and
// growing back restores every pane where it was.
func TestLiveResizeKeepsFocusAndLayout(t *testing.T) {
	h := newHarness(t, 140, 40)
	h.keys("2", "tab", "tab")
	lv := h.live()
	start, focused := slices.Clone(lv.slots), lv.slots[2]
	h.send(tea.WindowSizeMsg{Width: 40, Height: 10})
	if len(lv.slots) != 1 || lv.slots[0] != focused || lv.focus != 0 {
		t.Fatalf("small: slots %v focus %d, want only the focused %s", lv.slots, lv.focus, focused)
	}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	if !slices.Equal(lv.slots, start) || lv.focus != 2 {
		t.Fatalf("grown back: slots %v focus %d, want %v focus 2", lv.slots, lv.focus, start)
	}
}

// - drops a pane, never the focused session.
func TestLiveFewerPanesKeepsFocus(t *testing.T) {
	h := newHarness(t, 140, 40)
	h.keys("2")
	lv := h.live()
	h.send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // the last pane
	focused := lv.slots[lv.focus]
	h.keys("-")
	if got, _ := lv.Selected(); got.ID != focused {
		t.Fatalf("after -: focused %q, want %q (slots %v)", got.ID, focused, lv.slots)
	}
}

// Scrolling a pane with the wheel focuses it, so end resumes following it.
func TestLiveWheelFocusesThePane(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("2", "tab", "tab")
	lv := h.live()
	rs := lv.rects()
	h.send(tea.MouseWheelMsg{X: rs[0].x + 5, Y: rs[0].y + 5, Button: tea.MouseWheelUp})
	id := lv.slots[0]
	if lv.focus != 0 || lv.back[id] == 0 {
		t.Fatalf("focus %d back %d; the scrolled pane should be focused", lv.focus, lv.back[id])
	}
	h.keys("end")
	if lv.back[id] != 0 {
		t.Error("end did not resume following the scrolled pane")
	}
}

// The grid has no more panes than sessions to show.
func TestLiveNoEmptyPanes(t *testing.T) {
	f := bigStore(6, 0)
	for i := 1; i < 3; i++ {
		f.sessions[i].Live = &core.Live{PID: 100 + i, Status: core.StatusIdle, StatusSince: testNow}
	}
	h := newHarnessOn(t, f, 140, 40)
	h.keys("2")
	if got := len(h.live().slots); got != 3 {
		t.Fatalf("%d panes for 3 live sessions, want 3", got)
	}
	if strings.Contains(h.view(), "No live session for this pane") {
		t.Error("an empty pane is drawn")
	}
}

// Emoji built from joined code points (ZWJ sequences, skin tones,
// variation selectors) measure differently by grapheme and by wcwidth.
// The renderer uses wcwidth unless the terminal reports mode 2027, so text
// is normalised until both agree and no row overflows.
func TestLiveRowsFitWithJoinedEmoji(t *testing.T) {
	f := bigStore(4, 0)
	for i := range 2 {
		f.sessions[i].Live = &core.Live{PID: 100 + i, Status: core.StatusBusy, StatusSince: testNow}
		f.sessions[i].Title = "Add 👩‍💻 dev mode toggle 🌙 and ❤️ favorites ✨ for 👍🏽 people 🏳️‍🌈"
	}
	f.entries["s-0000"] = []core.Entry{{Kind: core.EntryPrompt, Text: "emoji test 👍🏽 please 👨‍👩‍👧"}}
	h := newHarnessOn(t, f, 140, 40)
	h.keys("2")
	for i, l := range strings.Split(h.m.View().Content, "\n") {
		if g, w := ansi.StringWidth(l), ansi.StringWidthWc(l); g != w || w > 140 {
			t.Errorf("line %d: %d cells by grapheme, %d by wcwidth: %q", i, g, w, ansi.Strip(l))
		}
	}
}
