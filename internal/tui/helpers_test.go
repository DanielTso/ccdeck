package tui

import (
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/demo"
)

var testNow = time.Date(2026, 11, 18, 15, 4, 5, 0, time.Local)

// fakeLauncher records what it was asked to do and returns "true".
type fakeLauncher struct {
	mu       sync.Mutex
	calls    []string
	detached bool
	focusOK  bool
	err      error
}

func (f *fakeLauncher) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeLauncher) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeLauncher) launch() core.Launch {
	return core.Launch{Cmd: exec.Command("true"), Detached: f.detached}
}

func (f *fakeLauncher) Resume(s core.Session, fork bool) (core.Launch, error) {
	if fork {
		f.record("fork " + s.ID)
	} else {
		f.record("resume " + s.ID)
	}
	return f.launch(), f.err
}

func (f *fakeLauncher) New(dir string) (core.Launch, error) {
	f.record("new " + dir)
	return f.launch(), f.err
}

func (f *fakeLauncher) Focus(s core.Session) (core.Launch, bool) {
	f.record("focus " + s.ID)
	return f.launch(), f.focusOK
}

type harness struct {
	t     testing.TB
	m     *Model
	store *demo.Store
	l     *fakeLauncher
}

// newHarness builds a model on a still demo store at a fixed clock, sized
// w x h, with the preview loading synchronously.
func newHarness(t *testing.T, w, h int, mod ...func(*Options)) *harness {
	t.Helper()
	store := demo.New(demo.Options{Seed: 1, Now: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = store.Close() })
	hs := newHarnessOn(t, store, w, h, mod...)
	hs.store = store
	return hs
}

// newHarnessOn is newHarness on any store.
func newHarnessOn(t testing.TB, store core.Store, w, h int, mod ...func(*Options)) *harness {
	t.Helper()
	l := &fakeLauncher{detached: true}
	opts := Options{Store: store, Launcher: l, Theme: "ember", Now: func() time.Time { return testNow }}
	for _, f := range mod {
		f(&opts)
	}
	m, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	m.env.previewDelay = 0
	m.env.waiting = func(tea.Cmd) tea.Cmd { return func() tea.Msg { return waitingMsg{} } }
	hs := &harness{t: t, m: m, l: l}
	for _, v := range m.views {
		hs.run(v.Init())
	}
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	return hs
}

// send delivers messages and runs the resulting commands to completion.
func (h *harness) send(msgs ...tea.Msg) {
	h.t.Helper()
	for _, msg := range msgs {
		_, cmd := h.m.Update(msg)
		h.run(cmd)
	}
}

// keys presses keys in order, by their Bubble Tea names.
func (h *harness) keys(names ...string) {
	h.t.Helper()
	for _, n := range names {
		h.send(keyPress(n))
	}
}

// typeText types each rune as a key press.
func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// waitingMsg stands for a command that waits for a timer or for the store
// (env.wait): the harness does not run it, and tests that need its message
// send it themselves.
type waitingMsg struct{}

// run executes a command and feeds back what it produces. Every command
// that does work is waited for, however slow the machine; one that waits
// for a timer or the store is marked as such and returns a waitingMsg at
// once.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(time.Minute):
		h.t.Fatal("a command did not finish: one that waits for a timer or the store must go through env.wait")
	}
	switch msg := msg.(type) {
	case nil, waitingMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case tea.QuitMsg:
		// Leave it: a test that quits checks the command itself.
	default:
		h.send(msg)
	}
}

// view is the screen as plain text.
func (h *harness) view() string { return ansi.Strip(h.m.View().Content) }

func (h *harness) sessions() *sessionsView { return h.m.views[viewSessions].(*sessionsView) }

func (h *harness) live() *liveView { return h.m.views[viewLive].(*liveView) }

func (h *harness) usage() *usageView { return h.m.views[viewUsage].(*usageView) }

func (h *harness) transcript() *transcriptView {
	return h.m.views[viewTranscript].(*transcriptView)
}

// setSessions replaces the snapshot as a store event would, with edit
// applied to a copy of the current sessions.
func (h *harness) setSessions(edit func([]core.Session) []core.Session) {
	h.t.Helper()
	ss := edit(slices.Clone(h.m.env.sessions))
	h.m.env.sessions = ss
	h.send(sessionsMsg{})
}

// sessionTitled finds a session in the snapshot by part of its title.
func (h *harness) sessionTitled(sub string) core.Session {
	h.t.Helper()
	for _, s := range h.m.env.sessions {
		if strings.Contains(s.Title, sub) {
			return s
		}
	}
	h.t.Fatalf("no session titled %q", sub)
	return core.Session{}
}

func (h *harness) selected() core.Session {
	s, _ := h.sessions().Selected()
	return s
}

// selectTitle moves the cursor until the selected title contains sub.
func (h *harness) selectTitle(sub string) core.Session {
	h.t.Helper()
	h.keys("home")
	for range 40 {
		if s := h.selected(); strings.Contains(s.Title, sub) {
			return s
		}
		h.keys("down")
	}
	h.t.Fatalf("no session titled %q", sub)
	return core.Session{}
}

func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}
