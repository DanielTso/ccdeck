package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func TestKeyPressHelper(t *testing.T) {
	for _, name := range []string{"enter", "esc", "up", "down", "home", "end", "tab", "ctrl+c", "q", "T", "/", "?", "2"} {
		if got := keyPress(name).String(); got != name {
			t.Errorf("keyPress(%q).String() = %q", name, got)
		}
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	h := newHarness(t, 80, 24)
	base := Options{Store: h.store, Launcher: h.l}
	tests := []struct {
		name string
		mod  func(*Options)
		want string
	}{
		{"no store", func(o *Options) { o.Store = nil }, "no store"},
		{"no launcher", func(o *Options) { o.Launcher = nil }, "no launcher"},
		{"unknown theme", func(o *Options) { o.Theme = "neon" }, `unknown theme "neon"`},
		{"bad base", func(o *Options) { o.Themes = map[string]ThemeSpec{"x": {Base: "neon"}} }, `base "neon"`},
		{"builtin clash", func(o *Options) { o.Themes = map[string]ThemeSpec{"paper": {}} }, "built-in"},
		{"bad token", func(o *Options) {
			o.Themes = map[string]ThemeSpec{"x": {Colors: map[string]string{"acent": "#fff"}}}
		}, `unknown colour token "acent"`},
		{"bad colour", func(o *Options) {
			o.Themes = map[string]ThemeSpec{"x": {Colors: map[string]string{"accent": "orange"}}}
		}, `bad colour "orange"`},
		{"bad hex", func(o *Options) {
			o.Themes = map[string]ThemeSpec{"x": {Colors: map[string]string{"accent": "#ggg"}}}
		}, `bad colour "#ggg"`},
		{"unknown action", func(o *Options) { o.Keys = map[string][]string{"explode": {"x"}} }, `unknown key action "explode"`},
		{"empty keys", func(o *Options) { o.Keys = map[string][]string{"quit": {}} }, "no key given"},
		{"conflict", func(o *Options) { o.Keys = map[string][]string{"fork": {"q"}} }, `"q" is bound to both`},
		{"ratio", func(o *Options) { o.Layout.PreviewRatio = 0.95 }, "preview ratio"},
		{"panes", func(o *Options) { o.Layout.LivePanes = 40 }, "live panes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base
			tt.mod(&o)
			_, err := New(o)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %s", err, tt.want)
			}
		})
	}
}

func TestCustomThemeAndKeyOverrides(t *testing.T) {
	h := newHarness(t, 120, 35, func(o *Options) {
		o.Theme = "mine"
		o.Themes = map[string]ThemeSpec{"mine": {Base: "paper", Colors: map[string]string{"accent": "#ff8800", "busy": "33"}}}
		o.Keys = map[string][]string{"fork": {"F"}, "quit": {"x"}}
	})
	if got := h.m.env.st.theme; got.name != "mine" || got.dark {
		t.Fatalf("theme = %s dark=%v, want mine on the paper base", got.name, got.dark)
	}
	if !strings.Contains(h.view(), "F fork") {
		t.Error("footer does not show the overridden fork key")
	}
	ended := h.selectTitle("Tidy shell")
	h.keys("f")
	if len(h.l.Calls()) != 0 {
		t.Fatalf("old key still forks: %v", h.l.Calls())
	}
	h.keys("F")
	if calls := h.l.Calls(); len(calls) != 1 || calls[0] != "fork "+ended.ID {
		t.Fatalf("calls = %v", calls)
	}
	h.keys("q") // no longer quit: must not panic or quit
	h.keys("?")
	if !strings.Contains(h.view(), "x/ctrl+c") && !strings.Contains(h.view(), "x ") {
		t.Error("help does not show the overridden quit key")
	}
}

// TestFitsEverySize renders every view, with and without help, in every
// theme at several sizes and checks nothing overflows or wraps.
func TestFitsEverySize(t *testing.T) {
	sizes := [][2]int{{80, 24}, {100, 30}, {120, 35}, {200, 50}, {60, 14}, {40, 10}}
	for _, th := range BuiltinThemes() {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", th, sz[0], sz[1]), func(t *testing.T) {
				h := newHarness(t, sz[0], sz[1], func(o *Options) { o.Theme = th })
				check := func(state string) {
					t.Helper()
					lines := strings.Split(h.view(), "\n")
					if len(lines) != sz[1] {
						t.Errorf("%s: %d lines, want %d", state, len(lines), sz[1])
					}
					for i, l := range lines {
						if w := lipgloss.Width(l); w > sz[0] {
							t.Errorf("%s: line %d is %d wide: %q", state, i, w, l)
						}
					}
				}
				check("sessions")
				h.keys("down", "down", "down")
				check("waiting selected")
				h.keys("g")
				check("grouped")
				h.keys("p")
				h.typeText("the")
				check("searching")
				h.keys("esc", "/")
				h.typeText("zzzz")
				check("empty filter")
				h.keys("esc", "?")
				check("help")
				h.keys("esc", "2")
				check("live")
				h.keys("tab", "k", "k")
				check("live scrolled")
				h.keys("z")
				check("live zoomed")
				h.keys("z", "+", "+")
				check("live more panes")
				h.keys("3")
				check("usage")
				h.keys("end")
				check("usage at the end")
				h.keys("1", "home", "o")
				check("transcript")
				h.keys("e")
				check("transcript expanded")
				h.keys("/")
				h.typeText("test")
				check("transcript finding")
				h.keys("enter", "?")
				check("transcript help")
			})
		}
	}
}

func TestNavigation(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("2")
	if h.m.active != viewLive || !strings.Contains(h.view(), "panes · 5 live") {
		t.Fatalf("2 did not open Live:\n%s", h.view())
	}
	h.keys("3")
	if h.m.active != viewUsage || !strings.Contains(h.view(), "Fresh tokens per day") {
		t.Fatal("3 did not open Usage")
	}
	h.keys("esc")
	if h.m.active != viewSessions {
		t.Fatalf("esc from Usage went to %v", h.m.active)
	}
	s := h.selectTitle("flaky refund")
	h.keys("o")
	if h.m.active != viewTranscript || !strings.Contains(h.view(), s.Title) {
		t.Fatalf("o did not open the transcript:\n%s", h.view())
	}
	if tv := h.m.views[viewTranscript].(*transcriptView); tv.id != s.ID {
		t.Fatalf("transcript got %q, want %q", tv.id, s.ID)
	}
	h.keys("esc")
	if h.m.active != viewSessions {
		t.Fatal("esc from the transcript did not go back")
	}
	h.keys("2", "1")
	if h.m.active != viewSessions {
		t.Fatal("1 did not return to Sessions")
	}
}

func TestThemeCyclesEverywhere(t *testing.T) {
	h := newHarness(t, 120, 35)
	for _, view := range []string{"1", "2", "3", "o", "?"} {
		before := h.m.env.st.theme.name
		h.keys(view, "T")
		if h.m.env.st.theme.name == before {
			t.Errorf("T did not change the theme after %q", view)
		}
	}
	tv := h.transcript()
	if tv.renderW == 0 || tv.renders < 2*len(tv.items) {
		t.Error("the transcript was not re-rendered in the new theme")
	}
}

func TestHelpOverlay(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("?")
	v := h.view()
	for _, want := range []string{"Global", "Move", "Sessions", "search past prompts", "? or esc to close"} {
		if !strings.Contains(v, want) {
			t.Errorf("help is missing %q", want)
		}
	}
	if strings.Contains(v, "zoom pane") {
		t.Error("help in Sessions lists the Live view's keys")
	}
	h.keys("2") // ignored while help is open
	if h.m.active != viewSessions || !h.m.help {
		t.Fatal("keys leaked through the help overlay")
	}
	h.keys("esc")
	if h.m.help {
		t.Fatal("esc did not close help")
	}
	h.keys("2", "?")
	if v := h.view(); !strings.Contains(v, "zoom pane") || strings.Contains(v, "search past prompts") {
		t.Errorf("help in Live should list the Live keys only:\n%s", v)
	}
}

func TestThemes(t *testing.T) {
	h := newHarness(t, 120, 35, func(o *Options) { o.Theme = "" })
	if !h.m.autoTheme || h.m.env.st.theme.name != "ember" {
		t.Fatal("empty theme should start on ember and follow the terminal")
	}
	h.send(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if got := h.m.env.st.theme.name; got != "paper" {
		t.Fatalf("light terminal picked %s", got)
	}
	h.keys("T")
	if got := h.m.env.st.theme.name; got != "contrast" {
		t.Fatalf("T after paper gave %s", got)
	}
	if !strings.Contains(h.view(), "Theme: contrast") {
		t.Error("theme change not announced")
	}
	h.send(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	if got := h.m.env.st.theme.name; got != "contrast" {
		t.Fatal("a chosen theme must not follow the terminal any more")
	}
	if v := h.m.View(); v.BackgroundColor == nil || !v.AltScreen {
		t.Error("view should paint the theme background on the alt screen")
	}
}

func TestQuitCancelsSubscription(t *testing.T) {
	h := newHarness(t, 80, 24)
	_, cmd := h.m.Update(keyPress("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
	if _, ok := <-h.m.events; ok {
		t.Fatal("subscription still open after quit")
	}
}

func TestStoreEventsRefresh(t *testing.T) {
	h := newHarness(t, 120, 35)
	before := h.m.env.sessions[0].LastActivity
	testNow = testNow.Add(10 * 1e9)
	defer func() { testNow = testNow.Add(-10 * 1e9) }()
	for range 20 {
		h.store.Step()
	}
	msg := h.m.waitForEvent()()
	if _, ok := msg.(storeEventMsg); !ok {
		t.Fatalf("got %T, want a store event", msg)
	}
	h.send(msg)
	var moved bool
	for _, s := range h.m.env.sessions {
		moved = moved || s.LastActivity.After(before)
	}
	if !moved {
		t.Fatal("snapshot not refreshed after a store event")
	}
	_ = h.store.Close()
	if _, ok := h.m.waitForEvent()().(storeClosedMsg); !ok {
		t.Fatal("closed store should end the stream")
	}
}

func TestStatusMessages(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.send(statusMsg{text: "boom", isErr: true})
	if !strings.Contains(h.view(), "boom") {
		t.Fatal("status not shown")
	}
	h.send(clearStatusMsg{id: h.m.statusID - 1})
	if !strings.Contains(h.view(), "boom") {
		t.Fatal("a stale clear removed a newer status")
	}
	h.send(clearStatusMsg{id: h.m.statusID})
	if strings.Contains(h.view(), "boom") {
		t.Fatal("status not cleared")
	}
	h.send(launchDoneMsg{what: "x", err: errors.New("claude not found")})
	if !strings.Contains(h.view(), "claude not found") {
		t.Fatal("launch error not shown")
	}
}

func TestWindowTitleCountsWaiting(t *testing.T) {
	h := newHarness(t, 80, 24)
	if got := h.m.View().WindowTitle; got != "ccdeck · 1 waiting" {
		t.Fatalf("title = %q", got)
	}
}

// A terminal without colour, as with NO_COLOR, keeps its own default
// colours: the theme does not repaint them.
func TestNoColorLeavesTerminalColours(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.send(tea.ColorProfileMsg{Profile: colorprofile.ASCII})
	if v := h.m.View(); v.BackgroundColor != nil || v.ForegroundColor != nil {
		t.Error("an ASCII profile still repaints the terminal's default colours")
	}
	h.send(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if v := h.m.View(); v.BackgroundColor == nil || v.ForegroundColor == nil {
		t.Error("a colour profile does not paint the theme colours")
	}
	t.Setenv("NO_COLOR", "1")
	h = newHarness(t, 120, 35)
	if v := h.m.View(); v.BackgroundColor != nil {
		t.Error("NO_COLOR repaints the background before the profile arrives")
	}
}

// The help overlay lists the Usage view's own binding.
func TestHelpInUsageListsItsKeys(t *testing.T) {
	h := newHarness(t, 120, 35)
	h.keys("3", "?")
	if v := h.view(); !strings.Contains(v, "resume, or focus if live") {
		t.Errorf("help in Usage omits enter:\n%s", v)
	}
}
