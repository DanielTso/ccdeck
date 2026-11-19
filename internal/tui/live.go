package tui

import (
	"math"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

// liveView is a grid of panes, each following one live session.
//
// Pane assignment is stable: a pane keeps its session for as long as the
// session exists, live or ended, so panes never trade places as activity
// changes. A live session without a pane gets a new pane while there are
// fewer than wanted, else the pane of an ended session (never the focused
// one), waiting sessions before the others. Live sessions that still do not
// fit are listed on the strip under the grid, waiting ones first.
//
// The assignment is kept for every pane wanted, and the window shows as
// many as fit: a smaller window hides the last panes but keeps the focused
// one on screen, and a larger one brings them back where they were. The
// grid never has more panes than sessions to show.
type liveView struct {
	env     *env
	w, h    int
	visible bool

	want     int      // panes the user asked for
	assigned []string // session id per pane wanted, in pane order; no blanks
	slots    []string // the panes on screen: assigned, as many as fit
	focus    int      // index into slots
	focusID  string   // the focused session, which focus follows
	zoom     bool

	entries map[string][]core.Entry
	loadErr map[string]error
	seq     map[string]int // latest load asked per session, to drop stale results
	nextSeq int
	dirty   map[string]bool // entries changed while hidden

	// A pane scrolled up holds still: its newest entries wait in held
	// until it is back at the bottom.
	back map[string]int // lines scrolled up from the bottom
	held map[string][]core.Entry
}

type liveEntriesMsg struct {
	id      string
	seq     int
	entries []core.Entry
	err     error
}

const (
	liveTail  = 200 // entries fetched per pane
	minPaneW  = 46
	minPaneH  = 8
	maxPanes  = 16
	stripRows = 1
)

func newLiveView(e *env) *liveView {
	return &liveView{
		env:     e,
		want:    e.layout.LivePanes,
		entries: map[string][]core.Entry{},
		loadErr: map[string]error{},
		seq:     map[string]int{},
		dirty:   map[string]bool{},
		back:    map[string]int{},
		held:    map[string][]core.Entry{},
	}
}

func (v *liveView) Init() tea.Cmd {
	v.assign()
	return nil
}

func (v *liveView) SetSize(w, h int) {
	v.w, v.h = w, h
	v.assign()
}

func (v *liveView) Capturing() bool { return false }

func (v *liveView) Show(visible bool) tea.Cmd {
	v.visible = visible
	if !visible {
		return nil
	}
	var cmds []tea.Cmd
	for _, id := range v.slots {
		if v.dirty[id] {
			cmds = append(cmds, v.load(id))
		}
	}
	return tea.Batch(append(cmds, v.loadMissing())...)
}

func (v *liveView) Hints() []key.Binding {
	k := v.env.keys
	resume := withDesc(k.Resume, "focus")
	if s, ok := v.Selected(); ok && s.Live == nil {
		resume = withDesc(k.Resume, "resume")
	}
	zoom := k.Zoom
	if v.zoom {
		zoom = withDesc(k.Zoom, "unzoom")
	}
	return []key.Binding{resume, k.NextPane, zoom, k.MorePanes, k.FewerPanes, k.Open}
}

// Selected is the session in the focused pane.
func (v *liveView) Selected() (core.Session, bool) {
	if v.focus < len(v.slots) {
		return v.session(v.slots[v.focus])
	}
	return core.Session{}, false
}

func (v *liveView) session(id string) (core.Session, bool) {
	if id == "" {
		return core.Session{}, false
	}
	for _, s := range v.env.sessions {
		if s.ID == id {
			return s, true
		}
	}
	return core.Session{}, false
}

func (v *liveView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case sessionsMsg, resizedMsg:
		return v.reassign()
	case entriesChangedMsg:
		if !slices.Contains(v.slots, msg.id) {
			return nil
		}
		if !v.visible {
			v.dirty[msg.id] = true
			return nil
		}
		return v.load(msg.id)
	case liveEntriesMsg:
		if msg.seq != v.seq[msg.id] {
			return nil
		}
		v.loadErr[msg.id] = msg.err
		if msg.err == nil {
			if v.back[msg.id] > 0 {
				v.held[msg.id] = msg.entries
			} else {
				v.entries[msg.id] = msg.entries
			}
		}
	case tea.KeyPressMsg:
		return v.key(msg)
	case tea.MouseClickMsg:
		if i := v.paneAt(msg.X, msg.Y); i >= 0 {
			v.setFocus(i)
		}
	case tea.MouseWheelMsg:
		// The pane scrolled is the one the keys then act on, so its
		// "end to follow" works.
		if i := v.paneAt(msg.X, msg.Y); i >= 0 {
			v.setFocus(i)
			v.scroll(i, -wheelDelta(msg))
		}
	}
	return nil
}

// setFocus focuses pane i, and so its session.
func (v *liveView) setFocus(i int) {
	if i < 0 || i >= len(v.slots) {
		return
	}
	v.focus, v.focusID = i, v.slots[i]
}

func (v *liveView) key(msg tea.KeyPressMsg) tea.Cmd {
	k := v.env.keys
	n := len(v.slots)
	f := v.focus
	page := max(v.paneBodyH(f)/2, 1)
	switch {
	case key.Matches(msg, k.NextPane):
		if n > 0 {
			v.setFocus((v.focus + 1) % n)
		}
	case key.Matches(msg, k.PrevPane):
		if n > 0 {
			v.setFocus((v.focus + n - 1) % n)
		}
	case key.Matches(msg, k.Zoom):
		v.zoom = !v.zoom && n > 0
	case key.Matches(msg, k.MorePanes):
		if v.want >= maxPanes {
			return nil
		}
		v.want++
		if n := v.fit(v.want); n < v.want {
			v.want = n
			return tea.Batch(v.reassign(), status("%d %s fit in this window", n, plural(n, "pane", "panes")))
		}
		cmd := v.reassign()
		if len(v.slots) < v.want {
			return tea.Batch(cmd, status("Up to %d panes: one opens for each live session", v.want))
		}
		return cmd
	case key.Matches(msg, k.FewerPanes):
		if len(v.slots) > 1 {
			v.want = len(v.slots) - 1
			return v.reassign()
		}
	case key.Matches(msg, k.Resume):
		if s, ok := v.Selected(); ok {
			return resumeOrFocus(v.env, s)
		}
	case key.Matches(msg, k.Up):
		v.scroll(f, 1)
	case key.Matches(msg, k.Down):
		v.scroll(f, -1)
	case key.Matches(msg, k.PageUp):
		v.scroll(f, page)
	case key.Matches(msg, k.PageDown):
		v.scroll(f, -page)
	case key.Matches(msg, k.Top):
		v.scroll(f, math.MaxInt/2)
	case key.Matches(msg, k.Bottom):
		v.scroll(f, math.MinInt/2)
	case key.Matches(msg, k.Back):
		if v.zoom {
			v.zoom = false
			return nil
		}
		return goBack
	}
	return nil
}

// scroll moves pane i's view d lines up (negative: down), clamped to what
// it holds. Reaching the bottom releases any held entries.
func (v *liveView) scroll(i, d int) {
	if i < 0 || i >= len(v.slots) || v.slots[i] == "" {
		return
	}
	id := v.slots[i]
	bodyH := v.paneBodyH(i)
	b := max(v.back[id]+d, 0)
	if b > 0 {
		b = min(b, liveTail*20) // more than any tail holds; the line below clamps exactly
		lines := convoTail(v.env.st, v.entries[id], nil, v.paneTextW(i), bodyH+b)
		b = min(b, max(len(lines)-bodyH, 0))
	}
	v.back[id] = b
	if b == 0 {
		if e, ok := v.held[id]; ok {
			v.entries[id] = e
			delete(v.held, id)
		}
	}
}

func (v *liveView) load(id string) tea.Cmd {
	v.nextSeq++
	v.seq[id] = v.nextSeq
	delete(v.dirty, id)
	store, seq := v.env.store, v.nextSeq
	return func() tea.Msg {
		e, err := store.Entries(id, liveTail)
		return liveEntriesMsg{id: id, seq: seq, entries: e, err: err}
	}
}

// Assignment.

// assign brings the assignment up to date with the sessions and the panes
// wanted, then works out which panes the window shows. It starts no loads,
// so SetSize can call it; reassign does both.
func (v *liveView) assign() {
	byID := make(map[string]*core.Session, len(v.env.sessions))
	for i := range v.env.sessions {
		byID[v.env.sessions[i].ID] = &v.env.sessions[i]
	}
	v.assigned = slices.DeleteFunc(v.assigned, func(id string) bool { return byID[id] == nil })
	v.trim(v.want)
	fit := v.fit(v.want)
	for _, s := range v.unassigned() {
		if !v.place(s.ID, fit, byID) {
			break
		}
	}

	v.slots = v.slots[:0]
	for _, i := range v.shown(fit) {
		v.slots = append(v.slots, v.assigned[i])
	}
	v.forgetUnassigned()
	f := slices.Index(v.slots, v.focusID)
	if f < 0 {
		f = min(v.focus, len(v.slots)-1)
	}
	v.focus, v.focusID = max(f, 0), ""
	if len(v.slots) > 0 {
		v.focusID = v.slots[v.focus]
	} else {
		v.zoom = false
	}
}

// trim cuts the assignment to n panes, keeping the focused session.
func (v *liveView) trim(n int) {
	if len(v.assigned) <= n {
		return
	}
	if f := slices.Index(v.assigned, v.focusID); f >= n && n > 0 {
		v.assigned[n-1] = v.focusID
	}
	v.assigned = v.assigned[:n]
}

// shown is which panes of the assignment are on screen when n fit: the
// first n, with the focused one in the last place when it would be off.
func (v *liveView) shown(n int) []int {
	n = min(n, len(v.assigned))
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	if f := slices.Index(v.assigned, v.focusID); f >= n && n > 0 {
		out[n-1] = f
	}
	return out
}

// place gives a live session a pane: a new one on screen, else an ended
// session's pane on screen, else a new one off screen, else an ended
// session's pane off screen. It reports false when there is none.
func (v *liveView) place(id string, fit int, byID map[string]*core.Session) bool {
	if len(v.assigned) < fit {
		v.assigned = append(v.assigned, id)
		return true
	}
	on := v.shown(fit)
	if i := v.endedPane(on, byID); i >= 0 {
		v.assigned[i] = id
		return true
	}
	if len(v.assigned) < v.want {
		v.assigned = append(v.assigned, id)
		return true
	}
	var off []int
	for i := range v.assigned {
		if !slices.Contains(on, i) {
			off = append(off, i)
		}
	}
	if i := v.endedPane(off, byID); i >= 0 {
		v.assigned[i] = id
		return true
	}
	return false
}

// endedPane is the pane among panes whose session ended longest ago, never
// the focused one, or -1.
func (v *liveView) endedPane(panes []int, byID map[string]*core.Session) int {
	best := -1
	for _, i := range panes {
		s := byID[v.assigned[i]]
		if s.ID == v.focusID || s.Live != nil {
			continue
		}
		if best < 0 || s.LastActivity.Before(byID[v.assigned[best]].LastActivity) {
			best = i
		}
	}
	return best
}

// reassign assigns panes and loads the ones that need it.
func (v *liveView) reassign() tea.Cmd {
	v.assign()
	return v.loadMissing()
}

// loadMissing loads every pane that has no entries and no load under way,
// once the view is on screen.
func (v *liveView) loadMissing() tea.Cmd {
	if !v.visible {
		return nil
	}
	var cmds []tea.Cmd
	for _, id := range v.slots {
		_, have := v.entries[id]
		_, asked := v.seq[id]
		if id != "" && !have && !asked {
			cmds = append(cmds, v.load(id))
		}
	}
	return tea.Batch(cmds...)
}

// unassigned lists live sessions without a pane in the assignment:
// waiting first, then most recently active first.
func (v *liveView) unassigned() []core.Session { return v.liveExcept(v.assigned) }

// offscreen lists live sessions with no pane on screen, in the same order.
func (v *liveView) offscreen() []core.Session { return v.liveExcept(v.slots) }

func (v *liveView) liveExcept(ids []string) []core.Session {
	var waiting, rest []core.Session
	for _, s := range v.env.sessions {
		if s.Live == nil || slices.Contains(ids, s.ID) {
			continue
		}
		if s.Status() == core.StatusWaiting {
			waiting = append(waiting, s)
		} else {
			rest = append(rest, s)
		}
	}
	return append(waiting, rest...)
}

func (v *liveView) forgetUnassigned() {
	for _, m := range []map[string][]core.Entry{v.entries, v.held} {
		for id := range m {
			if !slices.Contains(v.slots, id) {
				delete(m, id)
			}
		}
	}
	for _, m := range []map[string]int{v.back, v.seq} {
		for id := range m {
			if !slices.Contains(v.slots, id) {
				delete(m, id)
			}
		}
	}
	for id := range v.loadErr {
		if !slices.Contains(v.slots, id) {
			delete(v.loadErr, id)
		}
	}
	for id := range v.dirty {
		if !slices.Contains(v.slots, id) {
			delete(v.dirty, id)
		}
	}
}

// Layout.

// gridShape is how n panes are arranged: rows of cols, the last row possibly
// shorter.
type gridShape struct{ cols, rows int }

func (v *liveView) gridH() int { return max(v.h-stripRows, 0) }

// arrange picks the arrangement of n panes that keeps panes closest to
// square on screen (a cell is about twice as tall as it is wide), and
// reports false when n panes of the minimum size do not fit.
func arrange(n, w, h int) (gridShape, bool) {
	best, bestScore := gridShape{}, -1
	for c := 1; c <= n; c++ {
		r := (n + c - 1) / c
		pw, ph := w/c, h/r
		if pw < minPaneW || ph < minPaneH {
			continue
		}
		if score := min(pw*10/22, ph); score > bestScore {
			best, bestScore = gridShape{c, r}, score
		}
	}
	return best, bestScore >= 0
}

// fit is how many of want panes fit, at least one.
func (v *liveView) fit(want int) int {
	for n := want; n > 1; n-- {
		if _, ok := arrange(n, v.w, v.gridH()); ok {
			return n
		}
	}
	return 1
}

// rect is a pane's place in the view.
type rect struct{ x, y, w, h int }

// rects places the panes, or the focused pane alone when zoomed.
func (v *liveView) rects() []rect {
	n := len(v.slots)
	if n == 0 {
		return nil
	}
	if v.zoom {
		out := make([]rect, n)
		out[v.focus] = rect{0, 0, v.w, v.gridH()}
		return out
	}
	g, ok := arrange(n, v.w, v.gridH())
	if !ok {
		g = gridShape{1, n}
	}
	out := make([]rect, 0, n)
	for r := range g.rows {
		inRow := min(g.cols, n-r*g.cols)
		y0, y1 := v.gridH()*r/g.rows, v.gridH()*(r+1)/g.rows
		for c := range inRow {
			x0, x1 := v.w*c/inRow, v.w*(c+1)/inRow
			out = append(out, rect{x0, y0, x1 - x0, y1 - y0})
		}
	}
	return out
}

// paneAt is the pane under a point, or -1.
func (v *liveView) paneAt(x, y int) int {
	for i, r := range v.rects() {
		if r.w > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h {
			return i
		}
	}
	return -1
}

// paneBodyH and paneTextW are the conversation area of pane i: inside
// the border and margins, under the pane's header.
func (v *liveView) paneBodyH(i int) int {
	rs := v.rects()
	if i >= len(rs) {
		return 1
	}
	s, _ := v.session(v.slots[i])
	return max(rs[i].h-2-len(v.paneHead(s, rs[i].w-4)), 1)
}

func (v *liveView) paneTextW(i int) int {
	rs := v.rects()
	if i >= len(rs) {
		return 1
	}
	return max(rs[i].w-4, 1)
}

// Rendering.

func (v *liveView) View() string {
	if v.w <= 0 || v.h <= 0 {
		return ""
	}
	canvas := make([][]string, v.gridH()) // per line, the pane segments left to right
	for i, r := range v.rects() {
		if r.w == 0 {
			continue
		}
		for j, l := range v.pane(i, r.w, r.h) {
			canvas[r.y+j] = append(canvas[r.y+j], l)
		}
	}
	lines := make([]string, 0, v.h)
	for _, segs := range canvas {
		lines = append(lines, strings.Join(segs, ""))
	}
	if len(v.slots) == 0 && len(lines) > 0 {
		lines[len(lines)/2] = v.centred(v.env.st.muted.Render("No live sessions right now."))
		if j := len(lines)/2 + 1; j < len(lines) {
			lines[j] = v.centred(v.env.st.faint.Render("A pane opens for each claude session as it starts."))
		}
	}
	lines = append(lines, v.strip())
	return clip(strings.Join(lines, "\n"), v.w, v.h)
}

// centred places s in the middle of a line of the view.
func (v *liveView) centred(s string) string {
	s = truncate(s, v.w)
	return strings.Repeat(" ", max((v.w-width(s))/2, 0)) + s
}

// border is a box-drawing set. The shape carries meaning on its own, so
// focus and waiting do not depend on colour: heavy for the focused pane,
// double for a session waiting on the user.
type border struct{ tl, tr, bl, br, h, v string }

var (
	borderRound  = border{"╭", "╮", "╰", "╯", "─", "│"}
	borderHeavy  = border{"┏", "┓", "┗", "┛", "━", "┃"}
	borderDouble = border{"╔", "╗", "╚", "╝", "═", "║"}
)

// pane renders pane i in exactly h lines of w cells.
func (v *liveView) pane(i, w, h int) []string {
	st := v.env.st
	s, ok := v.session(v.slots[i])
	focused := i == v.focus
	b, bs := borderRound, st.faint
	switch {
	case ok && s.Status() == core.StatusWaiting:
		b, bs = borderDouble, st.statusStyle(core.StatusWaiting)
	case focused:
		b, bs = borderHeavy, lipgloss.NewStyle().Foreground(st.theme.borderFocused)
	}
	iw, ih := max(w-2, 0), max(h-2, 0)

	label := " " + itoa(i+1) + " "
	if focused {
		label = " ▶" + itoa(i+1) + " "
	}
	top := bs.Render(label)
	if ok {
		top += v.paneBadge(s) + " "
	}
	var bottom string
	if ok {
		if n := v.back[s.ID]; n > 0 {
			bottom = st.accent.Render(" ↑" + itoa(n) + " paused · " + keyLabel(v.env.keys.Bottom, 1) + " to follow ")
		} else if s.Live != nil && s.Live.Name != "" {
			bottom = st.muted.Render(" " + s.Live.Name + " ")
		}
	}

	var body []string
	if !ok {
		body = []string{"", st.muted.Render(truncate("No live session for this pane.", iw-1))}
	} else {
		head := v.paneHead(s, iw-2)
		n := max(ih-len(head), 0)
		convo := convoTail(st, v.entries[s.ID], v.loadErr[s.ID], iw-2, n+v.back[s.ID])
		if _, loaded := v.entries[s.ID]; !loaded && v.loadErr[s.ID] == nil {
			convo = []string{st.muted.Render("Loading…")}
		}
		if len(convo) > n {
			convo = convo[:n]
		}
		body = append(head, convo...)
	}

	out := make([]string, 0, h)
	out = append(out, bs.Render(b.tl+b.h)+edge(top, b.h, bs, iw-1)+bs.Render(b.tr))
	for j := range ih {
		l := ""
		if j < len(body) {
			l = body[j]
		}
		out = append(out, bs.Render(b.v)+" "+fit(l, iw-1)+bs.Render(b.v))
	}
	if h >= 2 {
		out = append(out, bs.Render(b.bl)+edgeRight(bottom, b.h, bs, iw)+bs.Render(b.br))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out[:h]
}

// edge is a border run w cells wide starting with label.
func edge(label, h string, bs lipgloss.Style, w int) string {
	label = truncate(label, w)
	return label + bs.Render(strings.Repeat(h, max(w-width(label), 0)))
}

// edgeRight is a border run w cells wide ending with label and one rule.
func edgeRight(label, h string, bs lipgloss.Style, w int) string {
	label = truncate(label, max(w-1, 0))
	return bs.Render(strings.Repeat(h, max(w-width(label)-1, 0))) + label + bs.Render(strings.Repeat(h, min(1, w)))
}

// paneBadge is the status for a pane's top edge. Waiting is shown in
// reverse video with the word in capitals, so it stands out without
// colour.
func (v *liveView) paneBadge(s core.Session) string {
	st := v.env.st
	now := v.env.now()
	switch s.Status() {
	case core.StatusWaiting:
		return st.statusStyle(core.StatusWaiting).Reverse(true).Render(" ◆ WAITING ") +
			st.statusStyle(core.StatusWaiting).Render(" "+since(now, s.Live.StatusSince))
	case core.StatusEnded:
		return st.badge(core.StatusEnded) + st.muted.Render(" "+ago(now, s.LastActivity))
	}
	return st.badge(s.Status()) + st.muted.Render(" "+since(now, s.Live.StatusSince))
}

// paneHead is the title, the facts line and, for a waiting session, what
// it waits for.
func (v *liveView) paneHead(s core.Session, w int) []string {
	st := v.env.st
	if s.ID == "" {
		return nil
	}
	sep := st.faint.Render(" · ")
	facts := st.text.Render(projectName(s.ProjectDir))
	if s.Model != "" {
		facts += sep + st.muted.Render(shortModel(s.Model))
	}
	facts += sep + st.gaugeInline(s) + sep + st.muted.Render(fmtCost(s.CostKind, s.CostUSD))
	head := []string{st.bold.Render(truncate(oneLine(s.Title), w)), truncate(facts, w)}
	if s.Status() == core.StatusWaiting {
		what := oneLine(s.Live.WaitingFor)
		if what == "" {
			what = "your input"
		}
		head = append(head, st.statusStyle(core.StatusWaiting).Render(truncate("▶ needs you: "+what, w)))
	}
	return append(head, st.faint.Render(strings.Repeat("─", max(w, 0))))
}

// strip is the line under the grid: the pane count and the live sessions
// that have no pane, waiting ones first and loud.
func (v *liveView) strip() string {
	st := v.env.st
	live := 0
	for _, s := range v.env.sessions {
		if s.Live != nil {
			live++
		}
	}
	left := " " + st.muted.Render(itoa(len(v.slots))+plural(len(v.slots), " pane", " panes")+" · "+itoa(live)+" live")
	if v.zoom {
		left += st.muted.Render(" · zoomed")
	}
	if live == 0 {
		return left + st.muted.Render(" · live sessions appear here when claude runs")
	}
	off := v.offscreen()
	if len(off) == 0 {
		return left
	}
	parts := []string{st.muted.Render(itoa(len(off)) + " without a pane:")}
	for _, s := range off {
		if s.Status() == core.StatusWaiting {
			parts = append(parts, st.statusStyle(core.StatusWaiting).Reverse(true).Render(" ◆ WAITING ")+" "+
				st.statusStyle(core.StatusWaiting).Render(truncate(oneLine(s.Title), 32)))
		} else {
			parts = append(parts, st.mark(s.Status())+" "+st.text.Render(truncate(oneLine(s.Title), 24)))
		}
	}
	more := st.muted.Render("  " + keyLabel(v.env.keys.MorePanes, 1) + " adds a pane")
	line := left + st.muted.Render(" · ") + strings.Join(parts, "  ")
	if width(line)+width(more) <= v.w {
		line += more
	}
	return line
}
