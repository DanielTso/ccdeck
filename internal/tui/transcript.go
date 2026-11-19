package tui

import (
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

// transcriptView shows one session's whole conversation, scrollable, with
// tool calls folded to one line.
//
// Rendering is incremental: each item (an entry, or a tool call and its
// result) keeps its rendered lines until the width, the theme or its fold
// state changes, and View only copies the lines on screen. New entries
// are appended to the items already built.
type transcriptView struct {
	env     *env
	w, h    int
	visible bool

	id      string
	sess    core.Session // the latest snapshot of the open session
	entries []core.Entry
	loaded  bool
	loadErr error
	loading bool // a load is in flight
	dirty   bool // entries changed while loading or hidden
	gen     int  // counts opens, so a late load for another session is dropped

	items   []titem
	pending map[string]int // tool_use id → item, until its result arrives
	starts  []int          // starts[i] is item i's first line; the last is the total
	renderW int            // the width items were rendered at
	renders int            // items rendered so far, for tests

	offset int  // first line on screen
	follow bool // keep the newest line on screen
	unseen int  // lines added below while not following
	sel    int  // selected foldable item, -1 for none

	find    textinput.Model
	finding bool
	query   string // lower case
	matches []int  // items containing the query, in order
	match   int    // index into matches
	opened  int    // the item find unfolded to show a match, -1 for none
}

// titem is one displayed step: a single entry, or a tool call together
// with its result.
type titem struct {
	entry    int // index into entries
	result   int // index of the paired tool result, -1 for none
	fold     bool
	expanded bool
	lines    []string // rendered at renderW; nil when stale
	lower    string   // lower-cased text for search, built on first search
}

// transcriptMsg delivers a session's entries, loaded off the update loop.
type transcriptMsg struct {
	gen     int
	entries []core.Entry
	err     error
}

const gutter = 2 // cells left of the text for the selection and match marks

func newTranscriptView(e *env) *transcriptView {
	t := &transcriptView{env: e, sel: -1, opened: -1, follow: true, find: textinput.New()}
	t.find.Prompt = "find: "
	t.find.Placeholder = "text in this transcript"
	t.find.SetStyles(inputStyles(e.st))
	return t
}

func (t *transcriptView) Init() tea.Cmd { return nil }

func (t *transcriptView) SetSize(w, h int) {
	t.w, t.h = w, h
	t.find.SetWidth(max(1, w/2))
	t.relayout()
}

func (t *transcriptView) Capturing() bool { return t.finding }

func (t *transcriptView) Show(visible bool) tea.Cmd {
	t.visible = visible
	if visible && t.dirty && !t.loading {
		return t.load()
	}
	return nil
}

func (t *transcriptView) Hints() []key.Binding {
	k := t.env.keys
	if t.finding {
		return []key.Binding{hint("enter", "keep"), hint("esc", "clear")}
	}
	hints := []key.Binding{k.NextFold, k.Expand, k.ExpandAll, k.Find}
	if len(t.matches) > 0 {
		hints = append(hints, k.NextMatch, k.PrevMatch)
	}
	if !t.follow {
		hints = append(hints, withDesc(k.Bottom, "follow"))
	}
	return append(hints, k.Back)
}

func (t *transcriptView) bodyH() int { return max(t.h-1, 0) }

func (t *transcriptView) total() int {
	if len(t.starts) == 0 {
		return 0
	}
	return t.starts[len(t.starts)-1]
}

func (t *transcriptView) maxOffset() int { return max(t.total()-t.bodyH(), 0) }

func (t *transcriptView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case openMsg:
		return t.open(msg.id)
	case transcriptMsg:
		if msg.gen != t.gen {
			return nil
		}
		t.loading = false
		t.loaded = true
		t.loadErr = msg.err
		if msg.err == nil {
			t.setEntries(msg.entries)
		}
		if t.dirty && t.visible {
			return t.load()
		}
	case entriesChangedMsg:
		if msg.id == t.id && t.id != "" {
			if !t.visible || t.loading {
				t.dirty = true
				return nil
			}
			return t.load()
		}
	case sessionsMsg:
		if s, ok := t.lookup(); ok {
			t.sess = s
		}
	case themeMsg:
		t.find.SetStyles(inputStyles(t.env.st))
		t.renderW = 0 // re-render everything in the new colours
		t.relayout()
	case tea.KeyPressMsg:
		if t.finding {
			return t.findKey(msg)
		}
		return t.key(msg)
	case tea.MouseWheelMsg:
		t.scrollBy(wheelDelta(msg))
	case tea.MouseClickMsg:
		t.click(msg.Y)
	}
	return nil
}

func (t *transcriptView) lookup() (core.Session, bool) {
	for _, s := range t.env.sessions {
		if s.ID == t.id {
			return s, true
		}
	}
	return core.Session{}, false
}

// open shows a session. Reopening the session already shown keeps the
// scroll position and refreshes the entries.
func (t *transcriptView) open(id string) tea.Cmd {
	if id != t.id {
		t.gen++
		t.id = id
		t.entries, t.items, t.pending, t.starts = nil, nil, nil, nil
		t.opened = -1
		t.loaded, t.loadErr, t.loading, t.dirty = false, nil, false, false
		t.offset, t.follow, t.unseen, t.sel = 0, true, 0, -1
		t.clearFind()
	}
	t.sess, _ = t.env.store.Session(id)
	if t.loading {
		t.dirty = true
		return nil
	}
	return t.load()
}

func (t *transcriptView) load() tea.Cmd {
	t.loading, t.dirty = true, false
	store, id, gen := t.env.store, t.id, t.gen
	return func() tea.Msg {
		e, err := store.Entries(id, 0)
		return transcriptMsg{gen: gen, entries: e, err: err}
	}
}

// Building items.

// setEntries replaces the entries, appending to the items already built
// when the new entries extend the old ones.
func (t *transcriptView) setEntries(entries []core.Entry) {
	old := t.entries
	extends := len(entries) >= len(old) &&
		(len(old) == 0 || sameEntry(entries[len(old)-1], old[len(old)-1]))
	if !extends {
		t.items, t.pending, t.starts = nil, nil, nil
		t.opened = -1
		t.sel, t.offset = -1, 0
		old = nil
	}
	if t.pending == nil {
		t.pending = map[string]int{}
	}
	t.entries = entries
	before := t.total()
	for i := len(old); i < len(entries); i++ {
		t.add(i)
	}
	t.relayout()
	if added := t.total() - before; added > 0 && !t.follow && extends {
		t.unseen += added
	}
	if t.query != "" {
		t.search(false)
	}
}

func sameEntry(a, b core.Entry) bool {
	return a.Kind == b.Kind && a.Time.Equal(b.Time) && a.ToolID == b.ToolID && a.Text == b.Text
}

// add files entry i: a tool result joins its call, by tool id or, when
// there are no ids, by following it directly.
func (t *transcriptView) add(i int) {
	e := t.entries[i]
	if e.Kind == core.EntryToolResult {
		k, ok := t.pending[e.ToolID]
		if e.ToolID == "" || !ok {
			k, ok = len(t.items)-1, false
			if k >= 0 && e.ToolID == "" && t.items[k].result < 0 && t.items[k].entry == i-1 &&
				t.entries[i-1].Kind == core.EntryToolUse {
				ok = true
			}
		}
		if ok {
			it := &t.items[k]
			delete(t.pending, t.entries[it.entry].ToolID)
			it.result, it.lines, it.lower = i, nil, ""
			return
		}
	}
	it := titem{entry: i, result: -1}
	switch e.Kind {
	case core.EntryToolUse, core.EntryToolResult, core.EntryThinking:
		it.fold = true
	}
	if e.Kind == core.EntryToolUse && e.ToolID != "" {
		t.pending[e.ToolID] = len(t.items)
	}
	t.items = append(t.items, it)
}

// Layout and rendering.

func (t *transcriptView) textW() int { return max(t.w-gutter-1, 8) }

// relayout renders stale items and recomputes line positions, keeping the
// line at the top of the screen in place unless following.
func (t *transcriptView) relayout() {
	if t.w <= 0 {
		return
	}
	anchor, within := t.itemAt(t.offset), 0
	if anchor >= 0 {
		within = t.offset - t.starts[anchor]
	}
	if w := t.textW(); w != t.renderW {
		t.renderW = w
		for i := range t.items {
			t.items[i].lines = nil
		}
	}
	t.starts = t.starts[:0]
	n := 0
	for i := range t.items {
		if t.items[i].lines == nil {
			t.items[i].lines = t.render(i)
			t.renders++
		}
		t.starts = append(t.starts, n)
		n += len(t.items[i].lines)
	}
	t.starts = append(t.starts, n)
	switch {
	case t.follow:
		t.offset = t.maxOffset()
	case anchor >= 0 && anchor < len(t.items):
		t.offset = t.starts[anchor] + min(within, max(len(t.items[anchor].lines)-1, 0))
	}
	t.scrollTo(t.offset)
}

// itemAt is the item showing line l in the last layout, or -1.
func (t *transcriptView) itemAt(l int) int {
	n := len(t.starts) - 1 // items laid out
	if n <= 0 || l < 0 || l >= t.total() {
		return -1
	}
	return sort.Search(n, func(i int) bool { return t.starts[i+1] > l })
}

// render draws one item at renderW.
func (t *transcriptView) render(i int) []string {
	st := t.env.st
	w := t.renderW
	it := t.items[i]
	e := t.entries[it.entry]
	var lines []string
	if e.Kind == core.EntryPrompt && i > 0 {
		lines = append(lines, "")
	}
	indent := func(pad string, style func(...string) string, ls []string) {
		for _, l := range ls {
			lines = append(lines, pad+style(l))
		}
	}
	switch {
	case e.Kind == core.EntryPrompt:
		ls := paragraphs(e.Text, w-2)
		for j, l := range ls {
			lead := "  "
			if j == 0 {
				lead = st.accent.Render("› ")
			}
			lines = append(lines, lead+st.bold.Render(l))
		}
	case e.Kind == core.EntryText:
		indent("  ", st.text.Render, paragraphs(e.Text, w-2))
	case !it.fold:
		lines = append(lines, entryLine(st, e, w))
	case !it.expanded:
		lines = append(lines, t.folded(it, w))
	case e.Kind == core.EntryThinking:
		lines = append(lines, "  "+st.muted.Italic(true).Render("▾ ∴ thinking"))
		body := paragraphs(e.Text, w-4)
		if len(body) == 0 {
			body = []string{"(no text recorded)"}
		}
		indent("    ", st.muted.Italic(true).Render, body)
	case e.Kind == core.EntryToolUse:
		lines = append(lines, "  "+st.accent.Render("▾ ")+st.bold.Render(toolName(e)))
		indent("    ", st.text.Render, paragraphs(e.Text, w-4))
		if it.result >= 0 {
			lines = append(lines, t.resultBlock(t.entries[it.result], w)...)
		} else {
			lines = append(lines, "    "+st.muted.Render("… no result yet"))
		}
	default: // a tool result on its own
		lines = append(lines, t.resultBlock(e, w)...)
	}
	return lines
}

// folded is the one-line form of a foldable item: a tool call with the
// first line of its result, a thinking marker, or a lone result.
func (t *transcriptView) folded(it titem, w int) string {
	st := t.env.st
	e := t.entries[it.entry]
	if e.Kind != core.EntryToolUse {
		return entryLine(st, e, w)
	}
	tool := toolName(e)
	head := "  " + st.muted.Render("▸ ") + st.text.Render(tool) + " "
	rest := w - width(head)
	if it.result < 0 {
		return head + st.muted.Render(truncate(oneLine(e.Text), rest-2)+" …")
	}
	input := oneLine(e.Text)
	inW := min(width(input), max(rest*3/5, rest-40))
	return head + st.muted.Render(fit(input, inW)) + "  " + resultLine(st, t.entries[it.result], rest-inW-2)
}

func (t *transcriptView) resultBlock(e core.Entry, w int) []string {
	st := t.env.st
	style, mark := st.muted, "↳ "
	if e.IsError {
		style, mark = st.err, "✗ "
	}
	body := paragraphs(e.Text, w-6)
	if len(body) == 0 {
		body = []string{"(empty)"}
	}
	out := make([]string, len(body))
	for i, l := range body {
		lead := "      "
		if i == 0 {
			lead = "    " + style.Render(mark)
		}
		out[i] = lead + style.Render(l)
	}
	return out
}

// Scrolling and selection.

func (t *transcriptView) scrollBy(d int) { t.scrollTo(t.offset + d) }

func (t *transcriptView) scrollTo(off int) {
	t.offset = min(max(off, 0), t.maxOffset())
	t.follow = t.offset == t.maxOffset()
	if t.follow {
		t.unseen = 0
	}
	t.keepSelectionVisible()
}

// visibleItems is the range of items on screen, [first, last].
func (t *transcriptView) visibleItems() (int, int) {
	first := t.itemAt(t.offset)
	if first < 0 {
		return 0, -1
	}
	last := t.itemAt(min(t.offset+t.bodyH(), t.total()) - 1)
	return first, max(last, first)
}

// keepSelectionVisible moves the selection to the first foldable item on
// screen when the selected one scrolls away.
func (t *transcriptView) keepSelectionVisible() {
	first, last := t.visibleItems()
	if t.sel >= first && t.sel <= last {
		return
	}
	t.sel = -1
	for i := first; i <= last; i++ {
		if t.items[i].fold {
			t.sel = i
			return
		}
	}
}

// reveal scrolls so item i's first line is on screen, a third of the way
// down when it was not.
func (t *transcriptView) reveal(i int) {
	if top := t.starts[i]; top < t.offset || top >= t.offset+t.bodyH() {
		t.scrollTo(top - t.bodyH()/3)
	}
}

func (t *transcriptView) selectFold(dir int) {
	start := t.sel
	if start < 0 {
		first, _ := t.visibleItems()
		start = first - dir
	}
	for i := start + dir; i >= 0 && i < len(t.items); i += dir {
		if t.items[i].fold {
			t.sel = i
			t.reveal(i)
			return
		}
	}
}

// toggle expands or folds item i, keeping its first line where it is on
// screen, also while following: the body opens downwards.
func (t *transcriptView) toggle(i int) {
	if i < 0 || i >= len(t.items) || !t.items[i].fold {
		return
	}
	if i == t.opened {
		t.opened = -1 // the user's choice now, not find's
	}
	row := t.starts[i] - t.offset
	t.setExpanded(i, !t.items[i].expanded)
	t.follow = false
	t.relayout()
	t.scrollTo(t.starts[i] - row)
	t.reveal(i)
}

func (t *transcriptView) setExpanded(i int, on bool) {
	if it := &t.items[i]; it.expanded != on {
		it.expanded, it.lines = on, nil
	}
}

// toggleAll expands every fold, or folds them all when none is folded.
func (t *transcriptView) toggleAll() {
	expand := false
	for _, it := range t.items {
		if it.fold && !it.expanded {
			expand = true
			break
		}
	}
	for i := range t.items {
		if t.items[i].fold {
			t.setExpanded(i, expand)
		}
	}
	t.relayout()
	if t.sel >= 0 {
		t.reveal(t.sel)
	}
}

func (t *transcriptView) key(msg tea.KeyPressMsg) tea.Cmd {
	k := t.env.keys
	page := max(t.bodyH()-1, 1)
	switch {
	case key.Matches(msg, k.Up):
		t.scrollBy(-1)
	case key.Matches(msg, k.Down):
		t.scrollBy(1)
	case key.Matches(msg, k.PageUp):
		t.scrollBy(-page)
	case key.Matches(msg, k.PageDown):
		t.scrollBy(page)
	case key.Matches(msg, k.Top):
		t.scrollTo(0)
	case key.Matches(msg, k.Bottom):
		t.scrollTo(t.maxOffset())
	case key.Matches(msg, k.NextFold):
		t.selectFold(1)
	case key.Matches(msg, k.PrevFold):
		t.selectFold(-1)
	case key.Matches(msg, k.Expand):
		t.toggle(t.sel)
	case key.Matches(msg, k.ExpandAll):
		t.toggleAll()
	case key.Matches(msg, k.Find):
		t.finding = true
		return t.find.Focus()
	case key.Matches(msg, k.NextMatch):
		t.step(1)
	case key.Matches(msg, k.PrevMatch):
		t.step(-1)
	case key.Matches(msg, k.Back):
		if t.query != "" {
			t.clearFind()
			return nil
		}
		return goBack
	}
	return nil
}

func (t *transcriptView) click(y int) {
	i := t.itemAt(t.offset + y - 1)
	if y < 1 || i < 0 || !t.items[i].fold {
		return
	}
	t.sel = i
	t.toggle(i)
}

// Search within the transcript.

func (t *transcriptView) findKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		t.clearFind()
		return nil
	case "enter":
		t.finding = false
		t.find.Blur()
		if t.query == "" {
			t.find.Reset()
		}
		return nil
	}
	var cmd tea.Cmd
	t.find, cmd = t.find.Update(msg)
	if q := strings.ToLower(strings.TrimSpace(t.find.Value())); q != t.query {
		t.query = q
		t.search(true)
	}
	return cmd
}

func (t *transcriptView) clearFind() {
	t.finding = false
	t.find.Blur()
	t.find.Reset()
	t.query, t.matches, t.match = "", nil, 0
	t.refold(-1)
}

// refold folds again the item find unfolded, unless it is keep.
func (t *transcriptView) refold(keep int) {
	i := t.opened
	if i < 0 || i == keep || i >= len(t.items) {
		return
	}
	t.opened = -1
	t.setExpanded(i, false)
	t.relayout()
}

// search finds the items containing the query. With jump it moves to the
// first match at or below the top of the screen; otherwise it keeps the
// current match when it still matches.
func (t *transcriptView) search(jump bool) {
	prev := -1
	if t.match < len(t.matches) {
		prev = t.matches[t.match]
	}
	t.matches, t.match = t.matches[:0], 0
	if t.query == "" {
		return
	}
	for i := range t.items {
		if strings.Contains(t.lowerText(i), t.query) {
			t.matches = append(t.matches, i)
		}
	}
	if !jump {
		if j, ok := slices.BinarySearch(t.matches, prev); ok {
			t.match = j
		}
		return
	}
	if len(t.matches) > 0 {
		top, _ := t.visibleItems()
		if j := sort.SearchInts(t.matches, top); j < len(t.matches) {
			t.match = j
		}
		t.goToMatch()
	}
}

func (t *transcriptView) lowerText(i int) string {
	it := &t.items[i]
	if it.lower == "" {
		e := t.entries[it.entry]
		s := e.Tool + " " + e.Text
		if it.result >= 0 {
			s += "\n" + t.entries[it.result].Text
		}
		it.lower = strings.ToLower(s)
	}
	return it.lower
}

func (t *transcriptView) step(d int) {
	if len(t.matches) == 0 {
		return
	}
	t.match = (t.match + d + len(t.matches)) % len(t.matches)
	t.goToMatch()
}

// goToMatch shows the current match, unfolding it so the text is visible.
// A fold find opened for an earlier match closes again.
func (t *transcriptView) goToMatch() {
	i := t.matches[t.match]
	t.refold(i)
	if t.items[i].fold {
		if !t.items[i].expanded {
			t.opened = i
		}
		t.setExpanded(i, true)
		t.relayout()
		t.sel = i
	}
	t.reveal(i)
}

// View.

func (t *transcriptView) View() string {
	if t.w <= 0 || t.h <= 0 {
		return ""
	}
	st := t.env.st
	lines := make([]string, 0, t.h)
	lines = append(lines, t.header())
	switch {
	case t.id == "":
		lines = append(lines, "", " "+st.muted.Render(truncate("No transcript open. Press o on a session.", t.w-2)))
	case t.loadErr != nil:
		lines = append(lines, "", " "+st.err.Render(truncate("Cannot read the conversation: "+oneLine(t.loadErr.Error()), t.w-2)))
	case !t.loaded:
		lines = append(lines, "", " "+st.muted.Render("Loading…"))
	case len(t.items) == 0:
		lines = append(lines, "", " "+st.muted.Render("Nothing said yet."))
	default:
		lines = append(lines, t.body()...)
	}
	return clip(strings.Join(lines, "\n"), t.w, t.h)
}

// body copies the lines on screen and adds the selection and match marks.
func (t *transcriptView) body() []string {
	st := t.env.st
	n := min(t.bodyH(), t.total()-t.offset)
	out := make([]string, 0, n)
	i := t.itemAt(t.offset)
	current := -1
	if t.match < len(t.matches) {
		current = t.matches[t.match]
	}
	mi := sort.SearchInts(t.matches, i)
	for l := t.offset; l < t.offset+n; l++ {
		for t.starts[i+1] <= l {
			i++
		}
		for mi < len(t.matches) && t.matches[mi] < i {
			mi++
		}
		first := l == t.starts[i]
		mark := "  "
		switch {
		case i == current && first:
			mark = st.marker.Render("▶ ")
		case i == current || i == t.sel:
			mark = st.marker.Render("▌ ")
		case first && mi < len(t.matches) && t.matches[mi] == i:
			mark = st.accent.Render("• ")
		}
		out = append(out, mark+t.items[i].lines[l-t.starts[i]])
	}
	return out
}

// header is the session title on the left and the scroll state, or the
// find input and match count, on the right.
func (t *transcriptView) header() string {
	st := t.env.st
	var left, right string
	switch {
	case t.finding:
		left = t.find.View()
	case t.query != "":
		left = st.accent.Render("find: ") + st.text.Render(truncate(t.query, t.w/3))
	default:
		title := t.sess.Title
		if title == "" {
			title = "Transcript"
		}
		left = st.mark(t.sess.Status()) + " " + st.bold.Render(oneLine(title))
	}
	if t.query != "" {
		if len(t.matches) == 0 {
			right = st.err.Render("no match")
		} else {
			right = st.text.Render(itoa(t.match+1)+"/"+itoa(len(t.matches))) + st.muted.Render(" matches")
		}
	} else if t.loaded && len(t.items) > 0 {
		right = t.position()
	}
	left = " " + left
	room := t.w - width(right) - 3
	if room < 12 {
		right, room = "", t.w-2
	}
	left = truncate(left, room)
	return left + strings.Repeat(" ", max(1, t.w-width(left)-width(right)-1)) + right
}

func (t *transcriptView) position() string {
	st := t.env.st
	count := itoa(len(t.entries)) + plural(len(t.entries), " entry", " entries")
	switch {
	case t.follow && t.sess.Live != nil:
		return st.muted.Render(count+" · ") + st.accent.Render("following")
	case t.follow || t.total() <= t.bodyH():
		return st.muted.Render(count + " · end")
	}
	pct := 100 * (t.offset + t.bodyH()) / max(t.total(), 1)
	s := st.muted.Render(count + " · " + itoa(pct) + "%")
	if t.unseen > 0 {
		s += st.accent.Render(" · ↓ " + itoa(t.unseen) + " new")
	}
	return s
}
