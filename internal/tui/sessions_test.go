package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestPreviewShowsSelection(t *testing.T) {
	h := newHarness(t, 120, 35)
	s := h.selectTitle("rate limiter")
	v := h.view()
	for _, want := range []string{
		"busy for", "feat/tenant-rate-limit", "opus 5.5", "124k / 200k", "estimate",
		"agents   3", "Explore", // subagents
		"The burst test is off by one", // conversation tail, newest at the bottom
		"✗ --- FAIL: TestTenantBurst",  // failed tool result
	} {
		if !strings.Contains(v, want) {
			t.Errorf("preview of %q is missing %q", s.Title, want)
		}
	}

	h.selectTitle("flaky refund")
	if v := h.view(); !strings.Contains(v, "◆ waiting") || !strings.Contains(v, "approve Bash: go test") {
		t.Errorf("waiting session does not say what it waits for:\n%s", v)
	}
	h.selectTitle("Q3 architecture")
	if v := h.view(); !strings.Contains(v, "window unknown") || !strings.Contains(v, "no price known") {
		t.Errorf("unknown window and cost not explained:\n%s", v)
	}
	h.selectTitle("kiln-7")
	if !strings.Contains(h.view(), "Nothing said yet.") {
		t.Error("live session without transcript should say so")
	}
}

func TestPreviewFollowsNewEntries(t *testing.T) {
	h := newHarness(t, 120, 35)
	s := h.selectTitle("rate limiter")
	before := len(h.sessions().entries[s.ID])
	for range 30 {
		h.store.Step()
	}
	h.send(storeEventMsg{Kind: core.SessionsChanged}, storeEventMsg{Kind: core.EntriesChanged, SessionID: s.ID})
	if after := len(h.sessions().entries[s.ID]); after <= before {
		t.Fatalf("entries %d -> %d after EntriesChanged", before, after)
	}
	if h.selected().ID != s.ID {
		t.Fatal("selection moved on refresh")
	}
}

func TestPreviewIsDebounced(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.m.env.previewDelay = previewDelay
	sv := h.sessions()
	_, cmd := h.m.Update(keyPress("down"))
	if cmd == nil {
		t.Fatal("moving to an uncached session should schedule a load")
	}
	stale := previewDueMsg{seq: sv.loadSeq}
	h.m.Update(keyPress("down"))
	id := h.selected().ID
	h.send(stale)
	if _, ok := sv.entries[id]; ok {
		t.Fatal("a stale timer loaded entries")
	}
	h.send(previewDueMsg{seq: sv.loadSeq})
	if _, ok := sv.entries[id]; !ok {
		t.Fatal("the current timer did not load entries")
	}
}

func TestEntryCacheIsBounded(t *testing.T) {
	sv := newHarness(t, 120, 35).sessions()
	for i := range entryCacheCap + 5 {
		sv.remember(fmt.Sprint(i), nil)
	}
	if len(sv.entries) != entryCacheCap || len(sv.cacheLRU) != entryCacheCap {
		t.Fatalf("cache holds %d/%d", len(sv.entries), len(sv.cacheLRU))
	}
	if _, ok := sv.entries["0"]; ok {
		t.Fatal("oldest entry not evicted")
	}
}

func TestSortScopeGroup(t *testing.T) {
	h := newHarness(t, 120, 35)
	sv := h.sessions()
	titles := func() []string {
		var out []string
		for _, r := range sv.rows {
			if r.s != nil {
				out = append(out, r.s.Title)
			}
		}
		return out
	}

	h.keys("s")
	if !strings.Contains(h.view(), "all · project") {
		t.Fatal("sort not shown after s")
	}
	if got := titles()[0]; got != "Tidy shell aliases" { // dotfiles sorts first
		t.Errorf("project sort starts with %q", got)
	}
	h.keys("s")
	if got := titles()[0]; !strings.Contains(got, "Bazel") { // 95% full
		t.Errorf("context sort starts with %q", got)
	}
	h.keys("s")
	if got := sv.rows[0].s.CostUSD; got < sv.rows[1].s.CostUSD {
		t.Error("cost sort not descending")
	}
	h.keys("s")
	if !strings.Contains(h.view(), "all · recent") {
		t.Fatal("sort did not cycle back to recent")
	}

	h.keys("a")
	for _, r := range sv.rows {
		if r.s.Live == nil {
			t.Fatalf("live scope shows ended %q", r.s.Title)
		}
	}
	if len(sv.rows) != 5 || !strings.Contains(h.view(), "live · recent  5") {
		t.Fatalf("live scope: %d rows", len(sv.rows))
	}
	h.keys("a")
	for _, r := range sv.rows {
		if r.s.Live != nil {
			t.Fatalf("ended scope shows live %q", r.s.Title)
		}
	}
	h.keys("a")
	if len(sv.rows) != 12 {
		t.Fatalf("all scope: %d rows", len(sv.rows))
	}

	sel := h.selectTitle("Bazel")
	h.keys("g")
	var headers []string
	for _, r := range sv.rows {
		if r.s == nil {
			headers = append(headers, projectName(r.dir))
		}
	}
	if len(headers) != 7 || headers[0] != "orbit-api" {
		t.Fatalf("group headers = %v", headers)
	}
	if h.selected().ID != sel.ID {
		t.Fatal("grouping lost the selection")
	}
	if !strings.Contains(h.view(), "orbit-api 3") {
		t.Error("group header with count not rendered")
	}
	// Moving never lands on a header.
	h.keys("home")
	for range 20 {
		if sv.rows[sv.cursor].s == nil {
			t.Fatal("cursor on a group header")
		}
		h.keys("down")
	}
	h.keys("up")
	if sv.rows[sv.cursor].s == nil {
		t.Fatal("cursor on a group header")
	}
}

func TestGroupByProjectOption(t *testing.T) {
	h := newHarness(t, 120, 35, func(o *Options) { o.Layout.GroupByProject = true })
	if !h.sessions().group || h.sessions().rows[0].s != nil {
		t.Fatal("Layout.GroupByProject did not start grouped")
	}
}

func TestFilter(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("/")
	if !h.m.views[viewSessions].Capturing() {
		t.Fatal("filter input not focused")
	}
	h.typeText("bazel")
	sv := h.sessions()
	if len(sv.rows) != 1 || !strings.Contains(sv.rows[0].s.Title, "Bazel") {
		t.Fatalf("filter bazel kept %d rows", len(sv.rows))
	}
	h.keys("q") // typed into the filter, not quit
	if len(sv.rows) != 0 || !strings.Contains(h.view(), "No sessions match") {
		t.Fatal("filter bazelq should match nothing")
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	h.keys("enter")
	if h.m.views[viewSessions].Capturing() || len(sv.rows) != 1 {
		t.Fatal("enter should keep the filter and release the keyboard")
	}
	if !strings.Contains(h.view(), "/ bazel") {
		t.Error("kept filter not shown")
	}
	h.keys("esc")
	if len(sv.rows) != 12 {
		t.Fatal("esc did not clear the kept filter")
	}
	h.keys("/")
	h.typeText("orbit")
	h.keys("esc")
	if len(sv.rows) != 12 || sv.filtering {
		t.Fatal("esc while typing should clear and close the filter")
	}
}

func TestPromptSearch(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("p")
	if !strings.Contains(h.view(), "Type to search") {
		t.Fatal("empty search has no hint")
	}
	h.typeText("refund")
	sv := h.sessions()
	if len(sv.hits) != 1 {
		t.Fatalf("hits = %d", len(sv.hits))
	}
	if v := h.view(); !strings.Contains(v, "1 found") || !strings.Contains(v, "TestRefund fails") {
		t.Fatalf("hit not listed:\n%s", v)
	}
	// The preview follows the hit while searching.
	if s, _ := sv.Selected(); !strings.Contains(s.Title, "flaky refund") {
		t.Fatalf("preview shows %q", s.Title)
	}
	stale := searchMsg{seq: sv.searchSeq - 1}
	h.send(stale)
	if len(sv.hits) != 1 {
		t.Fatal("stale search result replaced newer hits")
	}
	h.keys("a", "a", "a") // search, not scope
	if sv.scope != scopeAll {
		t.Fatal("typing in search changed the scope")
	}
	for range 3 {
		h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}

	h.keys("s", "esc") // hide the target with the scope, then search again
	h.keys("a", "a")   // scope ended: the waiting session is hidden
	h.keys("p")
	h.typeText("refund")
	h.keys("enter")
	if sv.searching {
		t.Fatal("enter did not close search")
	}
	if s := h.selected(); !strings.Contains(s.Title, "flaky refund") || sv.scope != scopeAll {
		t.Fatalf("jumped to %q with scope %v", s.Title, sv.scope)
	}
}

func TestResumeForkNew(t *testing.T) {
	h := newHarness(t, 120, 35)
	ended := h.selectTitle("Tidy shell")
	h.keys("enter")
	h.keys("f")
	h.keys("n")
	want := []string{"resume " + ended.ID, "fork " + ended.ID, "new " + ended.ProjectDir}
	if got := h.l.Calls(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(h.view(), "New session in dotfiles") {
		t.Errorf("detached launch not reported:\n%s", h.view())
	}
}

func TestLiveSessionIsNeverResumed(t *testing.T) {
	h := newHarness(t, 120, 35)
	live := h.selectTitle("rate limiter")
	if !strings.Contains(h.view(), "enter focus") {
		t.Error("footer should offer focus for a live session")
	}
	h.keys("enter")
	if !strings.Contains(h.view(), "already running as orbit-api-3 (pid 41207)") {
		t.Errorf("unreachable live session not explained:\n%s", h.view())
	}
	h.keys("f")
	if !strings.Contains(h.view(), "fork it once it has ended") {
		t.Error("fork of a live session not refused")
	}
	h.l.focusOK = true
	h.keys("enter")
	want := []string{"focus " + live.ID, "focus " + live.ID}
	if got := h.l.Calls(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want only focus", got)
	}
	if !strings.Contains(h.view(), "Focused") {
		t.Error("focus not reported")
	}
}

func TestLauncherErrorsAreShown(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.l.err = fmt.Errorf("claude: executable not found")
	h.selectTitle("Tidy shell")
	h.keys("enter")
	if !strings.Contains(h.view(), "claude: executable not found") {
		t.Fatal("launcher error not shown")
	}
}

func TestRunLaunch(t *testing.T) {
	l := &fakeLauncher{}
	launch, _ := l.New("/x")
	if msg := runLaunch(launch, "x")(); fmt.Sprintf("%T", msg) == "tui.launchDoneMsg" {
		t.Fatal("a non-detached launch must hand the terminal over, not run in the background")
	}
	launch.Cmd = nil
	if _, ok := runLaunch(launch, "x")().(statusMsg); !ok {
		t.Fatal("a launch without a command should report an error")
	}
	l.detached = true
	launch, _ = l.New("/x")
	launch.Cmd.Path, launch.Cmd.Args = "/bin/sh", []string{"sh", "-c", "echo nope >&2; exit 3"}
	msg, _ := runLaunch(launch, "x")().(launchDoneMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "nope") {
		t.Fatalf("detached failure = %v, want its output", msg.err)
	}
}

func TestMouse(t *testing.T) {
	h := newHarness(t, 120, 35)
	sv := h.sessions()
	// Header line 0, list header line 1, rows from line 2.
	h.send(tea.MouseClickMsg{X: 5, Y: 2 + 4, Button: tea.MouseLeft})
	if sv.cursor != 4 {
		t.Fatalf("click selected row %d, want 4", sv.cursor)
	}
	h.send(tea.MouseClickMsg{X: 100, Y: 2, Button: tea.MouseLeft})
	if sv.cursor != 4 {
		t.Fatal("a click in the preview changed the selection")
	}
	h.send(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	if sv.cursor != 5 {
		t.Fatalf("wheel down: cursor %d", sv.cursor)
	}
	h.send(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	if sv.cursor != 4 {
		t.Fatalf("wheel up: cursor %d", sv.cursor)
	}
}

func TestScrollingKeepsCursorVisible(t *testing.T) {
	h := newHarness(t, 80, 12) // too short for a preview: list only
	sv := h.sessions()
	if sv.layout().prevW != 0 {
		t.Fatal("preview should hide in a short narrow window")
	}
	h.keys("end")
	if sv.cursor != len(sv.rows)-1 || !strings.Contains(h.view(), "Tidy shell aliases") {
		t.Fatal("end did not scroll to the last session")
	}
	h.keys("home")
	if sv.offset != 0 || sv.cursor != 0 {
		t.Fatal("home did not scroll back")
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if sv.cursor == 0 || sv.cursor < sv.offset || sv.cursor >= sv.offset+sv.listRows() {
		t.Fatalf("page down: cursor %d offset %d", sv.cursor, sv.offset)
	}
}

func TestNarrowLayoutStacksPreview(t *testing.T) {
	h := newHarness(t, 80, 24)
	lay := h.sessions().layout()
	if !lay.stacked || lay.listW != 80 || lay.prevW != 80 {
		t.Fatalf("layout at 80x24 = %+v", lay)
	}
	if !strings.Contains(h.view(), strings.Repeat("─", 80)) {
		t.Error("no rule between list and preview")
	}
	wide := newHarness(t, 120, 35).sessions().layout()
	if wide.stacked || wide.listW+wide.prevW+1 != 120 {
		t.Fatalf("layout at 120 = %+v", wide)
	}
}

func TestWideCharactersAlign(t *testing.T) {
	h := newHarness(t, 120, 35)
	var rowWidths []int
	for _, l := range strings.Split(h.view(), "\n")[2:14] {
		left, _, _ := strings.Cut(l, "│")
		rowWidths = append(rowWidths, width(left))
	}
	for i, w := range rowWidths {
		if w != rowWidths[0] {
			t.Fatalf("row %d is %d wide, row 0 is %d: %v", i, w, rowWidths[0], rowWidths)
		}
	}
	if !strings.Contains(h.view(), "認証フロー") {
		t.Fatal("wide-character title not shown")
	}
}

func TestEntriesErrorIsShown(t *testing.T) {
	h := newHarness(t, 120, 35)
	id := h.selected().ID
	h.send(entriesMsg{id: id, err: fmt.Errorf("transcript vanished")})
	if !strings.Contains(h.view(), "Cannot read the conversation: transcript vanished") {
		t.Fatalf("load error not shown:\n%s", h.view())
	}
	h.send(entriesMsg{id: id, entries: []core.Entry{{Kind: core.EntryText, Text: "back again"}}})
	if !strings.Contains(h.view(), "back again") {
		t.Fatal("a later successful load did not replace the error")
	}
}

func TestPreviewWording(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.selectTitle("Port the config loader")
	v := h.view()
	if !strings.Contains(v, "1 prompt · ") || strings.Contains(v, "1 prompts") {
		t.Errorf("counts not pluralised properly:\n%s", v)
	}
	h.setSessions(func(ss []core.Session) []core.Session {
		for i := range ss {
			if ss[i].Title == "kiln-7" {
				ss[i].Live.StatusSince = testNow.Add(-20 * time.Second)
			}
		}
		return ss
	})
	h.selectTitle("kiln-7") // idle for 20s: "for <1m", never "for now"
	if v := h.view(); !strings.Contains(v, "idle for <1m") || strings.Contains(v, "for now") {
		t.Errorf("a status younger than a minute reads oddly:\n%s", v)
	}
	h.setSessions(func(ss []core.Session) []core.Session {
		for i := range ss {
			if ss[i].Title == "Tidy shell aliases" {
				ss[i].LastActivity, ss[i].StartedAt = testNow, time.Time{}
			}
		}
		return ss
	})
	h.selectTitle("Tidy shell")
	v = h.view()
	if !strings.Contains(v, "· ended just now") || strings.Contains(v, "now ago") {
		t.Errorf("an ended session active this minute reads oddly:\n%s", v)
	}
	if strings.Contains(v, "started") {
		t.Error("an unknown start time is shown")
	}
}

func TestSearchHitClickWhenScrolled(t *testing.T) {
	h := newHarness(t, 120, 12) // room for a few hits only
	h.keys("p")
	h.typeText("e")
	sv := h.sessions()
	if len(sv.hits) < sv.listRows()+3 {
		t.Fatalf("only %d hits", len(sv.hits))
	}
	for range sv.listRows() + 2 {
		h.keys("down")
	}
	start := sv.hitStart(sv.listRows())
	if start == 0 {
		t.Fatal("the hit list did not scroll")
	}
	// Screen line 0 is the app header, 1 the list header, 2 the first hit.
	h.send(tea.MouseClickMsg{X: 5, Y: 2, Button: tea.MouseLeft})
	if sv.hit != start {
		t.Fatalf("clicking the top hit selected %d, want %d", sv.hit, start)
	}
}

func TestEscapeSequencesAreRemoved(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.setSessions(func(ss []core.Session) []core.Session {
		ss[0].Title = "evil\x1b]0;pwned\x07 title\x1b[2J with\rcontrols"
		return ss
	})
	raw := h.m.View().Content
	if strings.Contains(raw, "pwned") || strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\r") {
		t.Fatal("escape sequences from session text reached the screen")
	}
	if !strings.Contains(h.view(), "evil title") {
		t.Fatalf("sanitised title not shown:\n%s", h.view())
	}
}

// Focus may query tmux, so Update must leave it to a command.
func TestFocusRunsOutsideUpdate(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.selectTitle("rate limiter")
	h.l.focusOK = true
	_, cmd := h.m.Update(keyPress("enter"))
	if got := h.l.Calls(); len(got) != 0 {
		t.Fatalf("Update called the launcher: %v", got)
	}
	h.run(cmd)
	if got := h.l.Calls(); len(got) != 1 || !strings.HasPrefix(got[0], "focus ") {
		t.Fatalf("calls after running the command = %v, want one focus", got)
	}
}

// At start-up the store publishes sessions one by one. The top row stays
// selected until the user picks a row; after that the pick is kept.
func TestSelectionWhileSessionsArrive(t *testing.T) {
	scanning := make(chan struct{}) // never closed: the scan is running
	h := newHarness(t, 120, 35, func(o *Options) { o.Ready = scanning })
	all := slices.Clone(h.m.env.sessions)
	oldest := all[len(all)-1]
	h.setSessions(func([]core.Session) []core.Session { return []core.Session{oldest} })
	h.setSessions(func([]core.Session) []core.Session { return all[len(all)-3:] })
	if got := h.selected().ID; got != all[len(all)-3].ID {
		t.Fatalf("selected %q, want the top row %q", got, all[len(all)-3].ID)
	}
	h.keys("down")
	picked := h.selected()
	h.setSessions(func([]core.Session) []core.Session { return all })
	if got := h.selected().ID; got != picked.ID {
		t.Errorf("selected %q after more sessions arrived, want the pick %q", got, picked.ID)
	}
}

// Resume, fork and new stat directories and search PATH, so Update must
// leave them to a command too.
func TestLaunchesAreBuiltOutsideUpdate(t *testing.T) {
	for _, k := range []string{"enter", "f", "n"} {
		t.Run(k, func(t *testing.T) {
			h := newHarness(t, 120, 35)
			h.selectTitle("Tidy shell")
			_, cmd := h.m.Update(keyPress(k))
			if got := h.l.Calls(); len(got) != 0 {
				t.Fatalf("Update called the launcher: %v", got)
			}
			h.run(cmd)
			if got := h.l.Calls(); len(got) != 1 {
				t.Fatalf("calls after running the command = %v, want one", got)
			}
		})
	}
}

// A second launch key pressed before the first launch has taken over the
// terminal must not start the session again when the first one ends.
func TestOneLaunchAtATime(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.l.detached = false
	h.selectTitle("Tidy shell")
	_, first := h.m.Update(keyPress("enter"))
	_, second := h.m.Update(keyPress("enter"))
	runs := 0
	for _, cmd := range []tea.Cmd{first, second} {
		msg := cmd()
		if lm, ok := msg.(launchMsg); ok {
			if _, next := h.m.Update(lm); next != nil {
				runs++
			}
		}
	}
	if runs != 1 {
		t.Fatalf("%d launches run, want 1", runs)
	}
	h.send(launchDoneMsg{what: "Resumed"})
	_, again := h.m.Update(keyPress("enter"))
	lm, ok := again().(launchMsg)
	if !ok {
		t.Fatal("no launch after the first one ended")
	}
	if _, next := h.m.Update(lm); next == nil {
		t.Error("a launch after the first one ended was ignored")
	}
}

// Once the start-up scan is done, the selection stays on its session when
// recency reorders the list, even before the user presses a key: enter must
// act on the session that was read.
func TestSelectionSticksOnceReady(t *testing.T) {
	ready := make(chan struct{})
	h := newHarness(t, 120, 35, func(o *Options) { o.Ready = ready })
	all := slices.Clone(h.m.env.sessions)
	top, second := all[0], all[1]
	reorder := func(first core.Session) {
		h.setSessions(func(ss []core.Session) []core.Session {
			out := []core.Session{first}
			for _, s := range ss {
				if s.ID != first.ID {
					out = append(out, s)
				}
			}
			out[0].LastActivity = testNow.Add(time.Duration(len(h.l.Calls())+1) * time.Hour)
			return out
		})
	}
	// Still scanning: the top row is selected, whichever it is.
	reorder(second)
	if got := h.selected().ID; got != second.ID {
		t.Fatalf("while scanning selected %q, want the top row %q", got, second.ID)
	}
	close(ready)
	h.run(h.m.Init()) // delivers the ready message
	if !h.m.env.ready {
		t.Fatal("ready not delivered")
	}
	sel := h.selected().ID
	reorder(top)
	if got := h.selected().ID; got != sel {
		t.Errorf("after the scan the selection moved to %q, want it to stay on %q", got, sel)
	}
	h.keys("3")
	usel, _ := h.usage().Selected()
	reorder(second)
	if got, _ := h.usage().Selected(); got.ID != usel.ID {
		t.Errorf("usage selection moved to %q, want %q", got.ID, usel.ID)
	}
}
