package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

// view is one screen of the application. The root model owns the header,
// footer and help overlay; a view owns the rectangle between them.
//
// Views share one *env, which the root keeps current: the latest session
// snapshot, styles and key map. When those change the root sends
// sessionsMsg or themeMsg to every view.
type view interface {
	// Init returns commands to run once at start-up.
	Init() tea.Cmd
	// SetSize gives the view its rectangle.
	SetSize(width, height int)
	// Update handles a message. Key and mouse messages reach only the
	// active view (mouse coordinates relative to the view); everything else
	// reaches every view.
	Update(msg tea.Msg) tea.Cmd
	// View renders exactly the rectangle last given to SetSize.
	View() string
	// Capturing reports whether a text input has focus, in which case the
	// view receives every key and global shortcuts are off.
	Capturing() bool
	// Hints are the bindings the footer shows for the view's current state.
	Hints() []key.Binding
}

// selector is implemented by views that have a selected session, which
// the open action shows in the transcript.
type selector interface {
	Selected() (core.Session, bool)
}

// shower is implemented by views that work only while on screen, such as
// following conversations or polling daily totals. The root tells them
// when they are shown and hidden.
type shower interface {
	Show(visible bool) tea.Cmd
}

// viewID names the views; the order is the order of the tabs.
type viewID int

const (
	viewSessions viewID = iota
	viewLive
	viewUsage
	viewTranscript
)

var viewTitles = [...]string{"Sessions", "Live", "Usage", "Transcript"}

// env is the state every view reads.
type env struct {
	store    core.Store
	launcher core.Launcher
	keys     keyMap
	st       styles
	now      func() time.Time
	layout   Layout
	home     string // replaced by "~" in paths

	// sessions is the latest snapshot, most recently active first. Views
	// must not modify it.
	sessions []core.Session

	// previewDelay debounces loading entries while the selection moves.
	previewDelay time.Duration
}

// homePath shortens a path under the home directory to start with "~".
func (e *env) homePath(p string) string {
	if e.home != "" && (p == e.home || strings.HasPrefix(p, e.home+string(os.PathSeparator))) {
		return "~" + p[len(e.home):]
	}
	return p
}

// Messages shared between the root and the views.
type (
	// sessionsMsg tells views that env.sessions was replaced.
	sessionsMsg struct{}
	// entriesChangedMsg tells views a session's conversation grew.
	entriesChangedMsg struct{ id string }
	// themeMsg tells views that env.st was replaced.
	themeMsg struct{}
	// resizedMsg follows SetSize, so a view can start work its new size
	// needs, which SetSize cannot return.
	resizedMsg struct{}
	// openMsg asks a view to show one session (sent to the transcript).
	openMsg struct{ id string }
	// navigateMsg asks the root to switch views, or to go back to the
	// previous one.
	navigateMsg struct {
		to   viewID
		back bool
	}
	// statusMsg puts a transient message in the footer.
	statusMsg struct {
		text  string
		isErr bool
	}
	// launchMsg asks the root to run a launch built in a command.
	launchMsg struct {
		launch core.Launch
		what   string
	}
	// launchDoneMsg reports the end of a launched command.
	launchDoneMsg struct {
		what string
		err  error
	}
)

func goBack() tea.Msg { return navigateMsg{back: true} }

func status(format string, args ...any) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: fmt.Sprintf(format, args...)} }
}

func statusErr(err error) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: err.Error(), isErr: true} }
}

// runLaunch runs a launch: in the background when it is detached,
// otherwise by suspending the UI and handing the terminal over.
func runLaunch(l core.Launch, what string) tea.Cmd {
	if l.Cmd == nil {
		return statusErr(fmt.Errorf("%s: the launcher returned no command", what))
	}
	if !l.Detached {
		return tea.ExecProcess(l.Cmd, func(err error) tea.Msg { return launchDoneMsg{what: what, err: err} })
	}
	c := l.Cmd
	return func() tea.Msg {
		if c.Stdout != nil || c.Stderr != nil {
			return launchDoneMsg{what: what, err: c.Run()}
		}
		out, err := c.CombinedOutput()
		if msg := strings.TrimSpace(string(out)); err != nil && msg != "" {
			err = fmt.Errorf("%w: %s", err, oneLine(msg))
		}
		return launchDoneMsg{what: what, err: err}
	}
}

// Actions every view offers on a session. A live session is never resumed
// a second time: it is focused, or the user is told where it runs.

// resumeOrFocus focuses a live session and resumes an ended one. It reads
// the session afresh so a session that just started or ended is handled
// as it is now.
func resumeOrFocus(e *env, s core.Session) tea.Cmd {
	if fresh, ok := e.store.Session(s.ID); ok {
		s = fresh
	}
	if s.Live != nil {
		// Focus queries tmux, so it runs in a command, not in Update.
		return func() tea.Msg {
			l, ok := e.launcher.Focus(s)
			if !ok {
				return statusMsg{text: fmt.Sprintf("%s is already running%s; it cannot be reached from here", quote(s.Title), where(s))}
			}
			return launchMsg{launch: l, what: "Focused " + quote(s.Title)}
		}
	}
	l, err := e.launcher.Resume(s, false)
	if err != nil {
		return statusErr(err)
	}
	return runLaunch(l, "Resumed "+quote(s.Title))
}

func quote(title string) string { return "“" + truncate(oneLine(title), 40) + "”" }

func where(s core.Session) string {
	if s.Live == nil {
		return ""
	}
	if s.Live.Name != "" {
		return fmt.Sprintf(" as %s (pid %d)", s.Live.Name, s.Live.PID)
	}
	return fmt.Sprintf(" (pid %d)", s.Live.PID)
}

// inputStyles styles a text input from the theme.
func inputStyles(st styles) textinput.Styles {
	s := textinput.DefaultStyles(st.theme.dark)
	for _, state := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		state.Prompt = st.accent
		state.Text = st.text
		state.Placeholder = st.muted
	}
	s.Cursor.Color = st.theme.accent
	s.Cursor.Blink = false
	return s
}

// hint is a footer-only binding for keys that are not configurable, such
// as those of a focused text input.
func hint(keys, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys), key.WithHelp("", desc))
}

// withDesc is a copy of b described as desc in the footer.
func withDesc(b key.Binding, desc string) key.Binding {
	b.SetHelp(b.Help().Key, desc)
	return b
}

// wheelDelta is how many lines one wheel notch scrolls, signed: negative
// is up. Zero for other buttons.
func wheelDelta(m tea.MouseWheelMsg) int {
	switch m.Button {
	case tea.MouseWheelUp:
		return -wheelLines
	case tea.MouseWheelDown:
		return wheelLines
	}
	return 0
}

const wheelLines = 3
