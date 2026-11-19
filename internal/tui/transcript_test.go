package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

// openTranscript selects a session by title and presses o.
func (h *harness) openTranscript(title string) *transcriptView {
	h.t.Helper()
	h.selectTitle(title)
	h.keys("o")
	if h.m.active != viewTranscript {
		h.t.Fatal("o did not open the transcript")
	}
	return h.transcript()
}

func TestTranscriptFoldsToolCalls(t *testing.T) {
	h := newHarness(t, 120, 35)
	tv := h.openTranscript("rate limiter")
	v := h.view()
	for _, want := range []string{
		"Refactor the rate limiter", "15 entries · following", // header
		"› The rate limiter is global right now",
		"▸ Bash go test ./internal/ratelimit/...  ✗ --- FAIL: TestTenantBurst", // call and result on one line
	} {
		if !strings.Contains(v, want) {
			t.Errorf("transcript is missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "tenant_test.go:31") {
		t.Fatal("folded result shows more than its first line")
	}
	// tab walks the tool calls; walk to the failing one.
	for range 20 {
		if it := tv.items[max(tv.sel, 0)]; tv.sel >= 0 && it.result >= 0 && tv.entries[it.result].IsError {
			break
		}
		h.keys("tab")
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	h.keys("tab") // shift+tab and back again lands on the same call
	h.keys("enter")
	v = h.view()
	if !strings.Contains(v, "▾ Bash") || !strings.Contains(v, "tenant_test.go:31: got 11 allowed, want 10") {
		t.Fatalf("expanded tool call does not show its whole result:\n%s", v)
	}
	h.keys("enter")
	if strings.Contains(h.view(), "tenant_test.go:31") {
		t.Fatal("enter did not fold the tool call again")
	}
	h.keys("e")
	if !strings.Contains(h.view(), "tenant_test.go:31") {
		t.Fatal("e did not expand everything")
	}
	h.keys("e")
	if strings.Contains(h.view(), "tenant_test.go:31") {
		t.Fatal("second e did not fold everything")
	}
}

func TestTranscriptPairsResults(t *testing.T) {
	tv := newHarness(t, 100, 30).transcript()
	tv.id, tv.loaded = "x", true
	tv.setEntries([]core.Entry{
		{Kind: core.EntryToolUse, Tool: "Read", Text: "a.go", ToolID: "1"},
		{Kind: core.EntryToolUse, Tool: "Read", Text: "b.go", ToolID: "2"},
		{Kind: core.EntryToolResult, Text: "B", ToolID: "2"},
		{Kind: core.EntryToolResult, Text: "A", ToolID: "1"},
		{Kind: core.EntryToolUse, Tool: "Bash", Text: "ls"}, // no ids: pair by order
		{Kind: core.EntryToolResult, Text: "out"},
		{Kind: core.EntryToolResult, Text: "orphan"},
	})
	got := make([][2]int, len(tv.items))
	for i, it := range tv.items {
		got[i] = [2]int{it.entry, it.result}
	}
	want := [][2]int{{0, 3}, {1, 2}, {4, 5}, {6, -1}}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
	// A result arriving later joins its call without rebuilding the rest.
	tv.setEntries(append(tv.entries, core.Entry{Kind: core.EntryToolUse, Tool: "Grep", Text: "x", ToolID: "9"}))
	before := tv.renders
	tv.setEntries(append(tv.entries, core.Entry{Kind: core.EntryToolResult, Text: "found", ToolID: "9"}))
	if last := tv.items[len(tv.items)-1]; last.result != len(tv.entries)-1 {
		t.Fatalf("late result not paired: %+v", last)
	}
	if n := tv.renders - before; n != 1 {
		t.Fatalf("pairing a late result re-rendered %d items, want 1", n)
	}
}

func TestTranscriptFollowsAndHolds(t *testing.T) {
	h := newHarness(t, 100, 12)
	tv := h.openTranscript("rate limiter")
	if !tv.follow || tv.offset != tv.maxOffset() {
		t.Fatal("a transcript should open at the end, following")
	}
	grow := func() {
		t.Helper()
		before := len(tv.entries)
		for len(tv.entries) == before {
			testNow = testNow.Add(time.Second)
			h.store.Step()
			h.send(entriesChangedMsg{id: tv.id})
		}
	}
	defer func(t0 time.Time) { testNow = t0 }(testNow)

	grow()
	if !tv.follow || tv.offset != tv.maxOffset() {
		t.Fatal("following did not keep the newest line on screen")
	}
	h.keys("k", "k", "k")
	held, top := tv.offset, tv.itemAt(tv.offset)
	if tv.follow {
		t.Fatal("scrolling up should stop following")
	}
	grow()
	if tv.offset != held || tv.itemAt(tv.offset) != top {
		t.Fatalf("position moved from %d to %d while scrolled up", held, tv.offset)
	}
	if tv.unseen == 0 || !strings.Contains(h.view(), "new") {
		t.Fatalf("new lines below not announced:\n%s", h.view())
	}
	h.keys("end")
	if !tv.follow || tv.unseen != 0 {
		t.Fatal("end should follow again")
	}
}

func TestTranscriptFind(t *testing.T) {
	h := newHarness(t, 120, 35)
	tv := h.openTranscript("memory growth")
	h.keys("/")
	if !tv.Capturing() {
		t.Fatal("/ did not open the find input")
	}
	h.typeText("retained")
	n := len(tv.matches)
	if n < 10 {
		t.Fatalf("found %d matches for retained", n)
	}
	h.keys("enter")
	if tv.Capturing() {
		t.Fatal("enter should keep the matches and release the keyboard")
	}
	cur := tv.matches[tv.match]
	if !tv.items[cur].expanded {
		t.Error("the current match is folded, hiding the text that matched")
	}
	if v := h.view(); !strings.Contains(v, "objects retained by") || !strings.Contains(v, "/"+itoa(n)+" matches") {
		t.Fatalf("match not shown:\n%s", v)
	}
	h.keys("n")
	if tv.matches[tv.match] == cur && n > 1 {
		t.Fatal("n did not move to the next match")
	}
	h.keys("N")
	if tv.matches[tv.match] != cur {
		t.Fatal("N did not move back")
	}
	h.keys("esc")
	if tv.query != "" || len(tv.matches) != 0 {
		t.Fatal("esc did not clear the search")
	}
	h.keys("/")
	h.typeText("zzzz")
	if !strings.Contains(h.view(), "no match") {
		t.Fatal("no match not reported")
	}
	h.keys("esc", "esc")
	if h.m.active != viewSessions {
		t.Fatal("esc with no search should go back")
	}
}

func TestTranscriptMouse(t *testing.T) {
	h := newHarness(t, 120, 20)
	tv := h.openTranscript("memory growth")
	off := tv.offset
	h.send(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelUp})
	if tv.offset != off-wheelLines || tv.follow {
		t.Fatalf("wheel up: offset %d -> %d", off, tv.offset)
	}
	h.send(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelDown})
	if tv.offset != off {
		t.Fatal("wheel down did not scroll back")
	}
	// Click the first folded line on screen: header is screen line 0 and
	// the view's own header line 1, so body line j is screen line j+2.
	for j := range tv.bodyH() {
		i := tv.itemAt(tv.offset + j)
		if i >= 0 && tv.items[i].fold && tv.starts[i] == tv.offset+j {
			h.send(tea.MouseClickMsg{X: 10, Y: j + 2, Button: tea.MouseLeft})
			if !tv.items[i].expanded || tv.sel != i {
				t.Fatalf("click did not expand item %d", i)
			}
			return
		}
	}
	t.Fatal("no folded line on screen")
}

func TestTranscriptLoadsOffTheUpdateLoop(t *testing.T) {
	f := bigStore(3, 50)
	h := newHarnessOn(t, f, 100, 30)
	before := f.entriesCalls.Load()
	_, cmd := h.m.Update(keyPress("o"))
	if f.entriesCalls.Load() != before {
		t.Fatal("Entries was called inside Update")
	}
	if !strings.Contains(h.view(), "Loading…") {
		t.Fatal("no loading state before the entries arrive")
	}
	h.run(cmd)
	if f.entriesCalls.Load() != before+1 || len(h.transcript().items) == 0 {
		t.Fatal("the load command did not fill the transcript")
	}
	// While hidden, changes are remembered rather than loaded.
	tv := h.transcript()
	h.keys("esc")
	h.send(entriesChangedMsg{id: "s-0000"})
	if tv.loading || !tv.dirty {
		t.Fatal("a hidden transcript should remember the change, not reload")
	}
	n := f.entriesCalls.Load()
	h.keys("o")
	if f.entriesCalls.Load() == n || tv.dirty {
		t.Fatal("showing the transcript again did not reload it")
	}
}

// TestTranscriptRendersIncrementally proves View does not re-render the
// transcript: after the first layout, frames and scrolling render nothing,
// and new entries render only themselves.
func TestTranscriptRendersIncrementally(t *testing.T) {
	f := bigStore(500, 5000)
	h := newHarnessOn(t, f, 120, 40)
	h.keys("o")
	tv := h.transcript()
	if len(tv.items) < 3000 || tv.renders != len(tv.items) {
		t.Fatalf("%d items, %d renders after opening", len(tv.items), tv.renders)
	}
	before := tv.renders
	for range 50 {
		_ = h.m.View()
		h.keys("k")
	}
	h.keys("home")
	_ = h.m.View()
	if tv.renders != before {
		t.Fatalf("frames and scrolling rendered %d items", tv.renders-before)
	}
	f.appendEntries("s-0000", makeEntries(5000, 5003)...)
	h.keys("end")
	h.send(entriesChangedMsg{id: "s-0000"})
	if n := tv.renders - before; n < 1 || n > 3 {
		t.Fatalf("three new entries rendered %d items", n)
	}
	if !strings.Contains(h.view(), "Prompt 5000") {
		t.Fatal("new entries not shown while following")
	}
}

func BenchmarkTranscriptView(b *testing.B) {
	f := bigStore(500, 5000)
	h := newHarnessOn(b, f, 160, 50)
	h.keys("o")
	b.ResetTimer()
	for i := range b.N {
		if i%2 == 0 {
			h.m.Update(keyPress("k"))
		}
		_ = h.m.View()
	}
}

func BenchmarkSessionsView500(b *testing.B) {
	f := bigStore(500, 10)
	h := newHarnessOn(b, f, 160, 50)
	b.ResetTimer()
	for range b.N {
		h.m.Update(sessionsMsg{})
		_ = h.m.View()
	}
}

func BenchmarkUsageView500(b *testing.B) {
	f := bigStore(500, 10)
	h := newHarnessOn(b, f, 160, 50)
	h.keys("3")
	b.ResetTimer()
	for range b.N {
		h.m.Update(sessionsMsg{})
		_ = h.m.View()
	}
}
