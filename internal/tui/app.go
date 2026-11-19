// Package tui is the Bubble Tea application: one root model that owns the
// window size, theme, key map and store subscription, and a set of views
// that render sessions into the rectangle they are given.
package tui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

const (
	clockEvery   = 20 * time.Second // re-render so relative times stay true
	statusFor    = 6 * time.Second  // how long a footer message stays
	previewDelay = 120 * time.Millisecond
)

// Model is the root model. Create it with New and run it with
// tea.NewProgram.
type Model struct {
	env    *env
	views  [4]view
	active viewID
	prev   viewID // where back goes from the transcript

	themes    []theme
	themeIdx  int
	autoTheme bool // follow the terminal background until the user picks

	width, height int
	help          bool

	status   statusMsg
	statusID int

	events <-chan core.Event
	cancel func()
}

// clockMsg is the periodic re-render tick.
type clockMsg struct{}

// storeEventMsg carries one store event; storeClosedMsg ends the stream.
type (
	storeEventMsg  core.Event
	storeClosedMsg struct{}
	clearStatusMsg struct{ id int }
)

// New builds the root model. It fails on an unknown theme, a bad custom
// theme or a bad key override, naming the offending value.
func New(opts Options) (*Model, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	keys, err := newKeyMap(opts.Keys)
	if err != nil {
		return nil, err
	}
	themes, err := resolveThemes(opts.Themes)
	if err != nil {
		return nil, err
	}
	m := &Model{themes: themes, autoTheme: opts.Theme == ""}
	name := opts.Theme
	if name == "" {
		name = defaultDarkTheme
	}
	if m.themeIdx = m.themeIndex(name); m.themeIdx < 0 {
		return nil, unknownTheme(name, themes)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	home, _ := os.UserHomeDir()
	m.env = &env{
		store:        opts.Store,
		launcher:     opts.Launcher,
		keys:         keys,
		st:           newStyles(themes[m.themeIdx]),
		now:          now,
		layout:       opts.Layout.withDefaults(),
		home:         home,
		sessions:     opts.Store.Sessions(),
		previewDelay: previewDelay,
	}
	m.views = [4]view{
		newSessionsView(m.env),
		newLiveView(m.env),
		newUsageView(m.env),
		newTranscriptView(m.env),
	}
	m.events, m.cancel = opts.Store.Subscribe()
	return m, nil
}

func unknownTheme(name string, themes []theme) error {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.name
	}
	return fmt.Errorf("tui: unknown theme %q (have %s)", name, strings.Join(names, ", "))
}

func (m *Model) themeIndex(name string) int {
	for i, t := range m.themes {
		if t.name == name {
			return i
		}
	}
	return -1
}

// Init starts the store subscription and the clock, and asks for the
// terminal background when the theme follows it.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitForEvent(), tickClock()}
	if m.autoTheme {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	for _, v := range m.views {
		cmds = append(cmds, v.Init())
	}
	return tea.Batch(cmds...)
}

// waitForEvent waits for the next store event. Each event re-arms it.
func (m *Model) waitForEvent() tea.Cmd {
	events := m.events
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return storeClosedMsg{}
		}
		return storeEventMsg(ev)
	}
}

func tickClock() tea.Cmd {
	return tea.Tick(clockEvery, func(time.Time) tea.Msg { return clockMsg{} })
}

// Update handles a message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		for _, v := range m.views {
			v.SetSize(m.width, max(0, m.height-2))
		}
		return m, m.broadcast(resizedMsg{})
	case tea.BackgroundColorMsg:
		if m.autoTheme {
			name := defaultDarkTheme
			if !msg.IsDark() {
				name = defaultLightTheme
			}
			return m, m.setTheme(m.themeIndex(name))
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.MouseClickMsg:
		msg.Y--
		return m, m.views[m.active].Update(msg)
	case tea.MouseWheelMsg:
		msg.Y--
		return m, m.views[m.active].Update(msg)
	case storeEventMsg:
		return m, tea.Batch(m.storeEvent(core.Event(msg)), m.waitForEvent())
	case storeClosedMsg:
		return m, nil
	case clockMsg:
		return m, tickClock()
	case statusMsg:
		m.statusID++
		m.status = msg
		id := m.statusID
		return m, tea.Tick(statusFor, func(time.Time) tea.Msg { return clearStatusMsg{id} })
	case clearStatusMsg:
		if msg.id == m.statusID {
			m.status = statusMsg{}
		}
		return m, nil
	case navigateMsg:
		if msg.back {
			return m, m.switchTo(m.prev)
		}
		return m, m.switchTo(msg.to)
	case launchMsg:
		return m, runLaunch(msg.launch, msg.what)
	case launchDoneMsg:
		cmd := m.refresh()
		if msg.err != nil {
			return m, tea.Batch(cmd, statusErr(msg.err))
		}
		return m, tea.Batch(cmd, status("%s", msg.what))
	}
	return m, m.broadcast(msg)
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.env.keys
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if m.help {
		switch {
		case key.Matches(msg, k.Quit):
			return m.quit()
		case key.Matches(msg, k.Help, k.Back):
			m.help = false
		case key.Matches(msg, k.Theme):
			return m.nextTheme()
		}
		return nil
	}
	v := m.views[m.active]
	if v.Capturing() {
		return v.Update(msg)
	}
	switch {
	case key.Matches(msg, k.Quit):
		return m.quit()
	case key.Matches(msg, k.Help):
		m.help = true
	case key.Matches(msg, k.Theme):
		return m.nextTheme()
	case key.Matches(msg, k.Sessions):
		return m.switchTo(viewSessions)
	case key.Matches(msg, k.Live):
		return m.switchTo(viewLive)
	case key.Matches(msg, k.Usage):
		return m.switchTo(viewUsage)
	case key.Matches(msg, k.Open):
		sel, ok := v.(selector)
		if !ok {
			return v.Update(msg)
		}
		s, ok := sel.Selected()
		if !ok {
			return nil
		}
		open := m.views[viewTranscript].Update(openMsg{id: s.ID})
		return tea.Batch(open, m.switchTo(viewTranscript))
	default:
		return v.Update(msg)
	}
	return nil
}

func (m *Model) nextTheme() tea.Cmd {
	m.autoTheme = false
	next := (m.themeIdx + 1) % len(m.themes)
	return tea.Batch(m.setTheme(next), status("Theme: %s", m.themes[next].name))
}

// switchTo makes a view active and tells the views that care that they
// were hidden or shown.
func (m *Model) switchTo(id viewID) tea.Cmd {
	if id == m.active {
		return nil
	}
	m.prev = viewSessions // back from a tab goes to Sessions
	if id == viewTranscript {
		m.prev = m.active // back from the transcript goes where it was opened
	}
	var cmds []tea.Cmd
	if v, ok := m.views[m.active].(shower); ok {
		cmds = append(cmds, v.Show(false))
	}
	m.active = id
	if v, ok := m.views[id].(shower); ok {
		cmds = append(cmds, v.Show(true))
	}
	return tea.Batch(cmds...)
}

func (m *Model) quit() tea.Cmd {
	m.cancel()
	return tea.Quit
}

func (m *Model) setTheme(i int) tea.Cmd {
	if i < 0 || i == m.themeIdx {
		return nil
	}
	m.themeIdx = i
	m.env.st = newStyles(m.themes[i])
	return m.broadcast(themeMsg{})
}

func (m *Model) storeEvent(ev core.Event) tea.Cmd {
	switch ev.Kind {
	case core.SessionsChanged:
		return m.refresh()
	case core.EntriesChanged:
		return m.broadcast(entriesChangedMsg{id: ev.SessionID})
	}
	return nil
}

// refresh replaces the session snapshot and tells the views.
func (m *Model) refresh() tea.Cmd {
	m.env.sessions = m.env.store.Sessions()
	return m.broadcast(sessionsMsg{})
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.views))
	for _, v := range m.views {
		cmds = append(cmds, v.Update(msg))
	}
	return tea.Batch(cmds...)
}

// View renders the whole screen.
func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor = m.env.st.theme.background
	v.ForegroundColor = m.env.st.theme.text
	v.WindowTitle = m.windowTitle()
	if m.width <= 0 || m.height <= 0 {
		return v
	}
	body := m.views[m.active].View()
	screen := m.header() + "\n" + clip(body, m.width, m.height-2) + "\n" + m.footer()
	if m.help {
		screen = m.overlayHelp(screen)
	}
	v.SetContent(screen)
	return v
}

func (m *Model) windowTitle() string {
	n := 0
	for _, s := range m.env.sessions {
		if s.Status() == core.StatusWaiting {
			n++
		}
	}
	if n > 0 {
		return "ccdeck · " + itoa(n) + " waiting"
	}
	return "ccdeck"
}

// header is the tab bar on the left and live counts on the right.
func (m *Model) header() string {
	st := m.env.st
	k := m.env.keys
	left := " " + st.brand.Render("ccdeck") + "  "
	tabs := []struct {
		id viewID
		b  key.Binding
	}{{viewSessions, k.Sessions}, {viewLive, k.Live}, {viewUsage, k.Usage}}
	for _, t := range tabs {
		style := st.tab
		if t.id == m.active {
			style = st.tabActive
		}
		left += st.muted.Render(keyLabel(t.b, 1)) + " " + style.Render(viewTitles[t.id]) + "  "
	}
	if m.active == viewTranscript {
		left += st.faint.Render("›") + " " + st.tabActive.Render(viewTitles[viewTranscript])
	}

	right := m.counts(false) + " "
	if width(left)+width(right) > m.width {
		right = m.counts(true) + " "
	}
	if width(left)+width(right) > m.width {
		right = ""
	}
	gap := max(0, m.width-width(left)-width(right))
	return truncate(left+strings.Repeat(" ", gap)+right, m.width)
}

// counts summarises live sessions: waiting first, because it needs you.
// Compact drops the words and keeps the glyphs.
func (m *Model) counts(compact bool) string {
	st := m.env.st
	n := map[core.Status]int{}
	for _, s := range m.env.sessions {
		n[s.Status()]++
	}
	var parts []string
	for _, s := range []core.Status{core.StatusWaiting, core.StatusBusy, core.StatusIdle, core.StatusUnknown} {
		switch {
		case n[s] == 0:
		case compact:
			parts = append(parts, st.statusStyle(s).Render(statusGlyph(s)+itoa(n[s])))
		default:
			parts = append(parts, st.statusStyle(s).Render(statusGlyph(s)+" "+itoa(n[s])+" "+statusWord(s)))
		}
	}
	total := itoa(len(m.env.sessions))
	if !compact {
		total += " sessions"
	}
	parts = append(parts, st.muted.Render(total))
	return strings.Join(parts, st.faint.Render("  "))
}

// footer is a transient message, or the active view's key hints.
func (m *Model) footer() string {
	st := m.env.st
	if m.status.text != "" {
		style := st.text
		if m.status.isErr {
			style = st.err
		}
		return " " + style.Render(truncate(oneLine(m.status.text), m.width-2))
	}
	global := renderHints(st, []key.Binding{m.env.keys.Help, m.env.keys.Quit}, m.width-2)
	local := renderHints(st, m.views[m.active].Hints(), m.width-width(global)-5)
	gap := max(1, m.width-2-width(local)-width(global))
	return truncate(" "+local+strings.Repeat(" ", gap)+global+" ", m.width)
}

// renderHints lays out "key desc" pairs, dropping whole hints that do not
// fit rather than cutting one in half.
func renderHints(st styles, hints []key.Binding, w int) string {
	var b strings.Builder
	used := 0
	for _, h := range hints {
		k, d := keyLabel(h, 1), h.Help().Desc
		n := width(k) + 1 + width(d)
		if used > 0 {
			n += 2
		}
		if used+n > w {
			break
		}
		if used > 0 {
			b.WriteString("  ")
		}
		b.WriteString(st.key.Render(k) + " " + st.muted.Render(d))
		used += n
	}
	return b.String()
}

// overlayHelp draws the key help box over the centre of the screen.
func (m *Model) overlayHelp(screen string) string {
	box := m.helpBox()
	x := max(0, (m.width-lipgloss.Width(box))/2)
	y := max(0, (m.height-lipgloss.Height(box))/2)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(screen),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}

// helpBox lists every binding, grouped, in as many columns as fit, using
// the short descriptions when the long ones do not fit.
func (m *Model) helpBox() string {
	st := m.env.st
	inner := m.width - 8
	var body string
	for _, long := range []bool{true, false} {
		blocks := m.helpBlocks(long)
		body = strings.Join(blocks, "\n\n")
		fits := false
		for _, cols := range []int{len(blocks), 2} {
			if g := grid(blocks, cols); lipgloss.Width(g) <= inner && lipgloss.Height(g)+4 <= m.height {
				body, fits = g, true
				break
			}
		}
		if fits {
			break
		}
	}
	body += "\n\n" + st.muted.Render(keyLabel(m.env.keys.Help, 1)+" or "+keyLabel(m.env.keys.Back, 1)+" to close")
	box := st.overlay.Render(body)
	if lipgloss.Height(box) > m.height {
		box = clip(box, m.width, m.height) // too tall: cut, never pad
	}
	return box
}

// viewGroups is the key group of each view's own bindings.
var viewGroups = map[viewID]string{
	viewSessions: "sessions", viewLive: "live", viewTranscript: "transcript",
}

// helpBlocks renders one block per key group active in the current view:
// the shared groups and the view's own.
func (m *Model) helpBlocks(long bool) []string {
	st := m.env.st
	acts := m.env.keys.actions()
	var blocks []string
	for _, g := range keyGroups {
		if !slices.Contains(sharedGroups, g.title) && g.title != viewGroups[m.active] {
			continue
		}
		lines := []string{st.bold.Render(strings.ToUpper(g.title[:1]) + g.title[1:])}
		kw := 0
		for _, a := range g.actions {
			kw = max(kw, width(keyLabel(*acts[a], 2)))
		}
		for _, a := range g.actions {
			b := *acts[a]
			desc := b.Help().Desc
			if l, ok := longHelp[a]; ok && long {
				desc = l
			}
			lines = append(lines, st.key.Render(fit(keyLabel(b, 2), kw))+"  "+st.text.Render(desc))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return blocks
}

// grid lays blocks out in rows of cols, every column as wide as its widest
// block, with a blank line between rows.
func grid(blocks []string, cols int) string {
	widths := make([]int, cols)
	for i, b := range blocks {
		widths[i%cols] = max(widths[i%cols], lipgloss.Width(b))
	}
	var rows []string
	for start := 0; start < len(blocks); start += cols {
		var cells []string
		for i := start; i < min(start+cols, len(blocks)); i++ {
			cell := lipgloss.NewStyle().Width(widths[i%cols]).Render(blocks[i])
			if i%cols < cols-1 {
				cell = lipgloss.NewStyle().PaddingRight(4).Render(cell)
			}
			cells = append(cells, cell)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	return strings.Join(rows, "\n\n")
}

// clip makes s exactly h lines, each at most w cells.
func clip(s string, w, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if width(l) > w {
			lines[i] = truncate(l, w)
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string { return strconv.Itoa(n) }
