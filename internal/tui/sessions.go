package tui

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/DanielTso/ccdeck/internal/core"
)

type sortMode int

const (
	sortRecent sortMode = iota
	sortProject
	sortContext
	sortCost
)

var sortNames = [...]string{"recent", "project", "context", "cost"}

type scopeMode int

const (
	scopeAll scopeMode = iota
	scopeLive
	scopeEnded
)

var scopeNames = [...]string{"all", "live", "ended"}

const (
	tailEntries   = 60  // entries fetched for the preview
	searchLimit   = 200 // prompt search hits
	entryCacheCap = 16  // sessions whose entries stay cached
	minPreviewH   = 16  // below this body height a narrow layout hides the preview
	wideLayout    = 100 // from this width the preview sits beside the list
)

// row is a line of the list: a session, or a group header.
type row struct {
	s     *core.Session // nil for a group header
	dir   string        // group header: the project directory
	count int           // group header: sessions in the group
}

// sessionsView is the session browser: a list and a preview.
type sessionsView struct {
	env  *env
	w, h int

	sort  sortMode
	scope scopeMode
	group bool

	filter    textinput.Model
	filtering bool // the filter input has focus

	search    textinput.Model
	searching bool
	hits      []core.PromptHit
	hit       int
	searchSeq int

	rows   []row
	cursor int // index into rows, -1 when nothing is selectable
	offset int // first visible row
	selID  string
	picked bool // the user chose a row; until then the top row stays selected

	entries  map[string][]core.Entry
	loadErr  map[string]error // why a session's entries could not be read
	cacheLRU []string         // entries keys, least recently used first
	loadSeq  int
}

type (
	previewDueMsg struct{ seq int }
	entriesMsg    struct {
		id      string
		entries []core.Entry
		err     error
	}
	searchMsg struct {
		seq  int
		hits []core.PromptHit
	}
)

func newSessionsView(e *env) *sessionsView {
	v := &sessionsView{
		env:     e,
		group:   e.layout.GroupByProject,
		cursor:  -1,
		entries: map[string][]core.Entry{},
		loadErr: map[string]error{},
		filter:  textinput.New(),
		search:  textinput.New(),
	}
	v.filter.Prompt = "/ "
	v.filter.Placeholder = "filter sessions"
	v.search.Prompt = "prompts: "
	v.search.Placeholder = "search everything you have typed"
	v.styleInputs()
	return v
}

func (v *sessionsView) styleInputs() {
	s := inputStyles(v.env.st)
	v.filter.SetStyles(s)
	v.search.SetStyles(s)
}

func (v *sessionsView) Init() tea.Cmd { return v.rebuild() }

func (v *sessionsView) SetSize(w, h int) {
	v.w, v.h = w, h
	lay := v.layout()
	v.filter.SetWidth(max(1, lay.listW-20))
	v.search.SetWidth(max(1, lay.listW-24))
	v.ensureVisible()
}

func (v *sessionsView) Capturing() bool { return v.filtering || v.searching }

func (v *sessionsView) Hints() []key.Binding {
	k := v.env.keys
	switch {
	case v.searching:
		return []key.Binding{hint("enter", "go to session"), hint("↑/↓", "move"), hint("esc", "close")}
	case v.filtering:
		return []key.Binding{hint("enter", "keep filter"), hint("↑/↓", "move"), hint("esc", "clear")}
	}
	resume := withDesc(k.Resume, "resume")
	if s, ok := v.Selected(); ok && s.Live != nil {
		resume = withDesc(k.Resume, "focus")
	}
	return []key.Binding{resume, k.Fork, k.New, k.Open, k.Filter, k.Search, k.Sort, k.Group, k.Scope}
}

// Selected is the session under the cursor, or under the selected search
// hit while searching.
func (v *sessionsView) Selected() (core.Session, bool) {
	if v.searching {
		if v.hit < len(v.hits) {
			return v.lookup(v.hits[v.hit].SessionID)
		}
		return core.Session{}, false
	}
	if v.cursor >= 0 && v.cursor < len(v.rows) && v.rows[v.cursor].s != nil {
		return *v.rows[v.cursor].s, true
	}
	return core.Session{}, false
}

func (v *sessionsView) lookup(id string) (core.Session, bool) {
	for _, s := range v.env.sessions {
		if s.ID == id {
			return s, true
		}
	}
	return core.Session{}, false
}

func (v *sessionsView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case v.searching:
			return v.searchKey(msg)
		case v.filtering:
			return v.filterKey(msg)
		}
		return v.key(msg)
	case tea.MouseClickMsg:
		return v.click(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		switch d := wheelDelta(msg); {
		case d < 0:
			return v.move(-1)
		case d > 0:
			return v.move(1)
		}
	case sessionsMsg:
		return v.rebuild()
	case themeMsg:
		v.styleInputs()
	case entriesChangedMsg:
		if msg.id == v.previewID() {
			return v.loadEntries(msg.id)
		}
		v.forget(msg.id)
	case previewDueMsg:
		if msg.seq == v.loadSeq {
			if id := v.previewID(); id != "" {
				return v.loadEntries(id)
			}
		}
	case entriesMsg:
		if msg.err != nil {
			v.loadErr[msg.id] = msg.err
		} else {
			delete(v.loadErr, msg.id)
		}
		v.remember(msg.id, msg.entries)
	case searchMsg:
		if msg.seq == v.searchSeq {
			v.hits, v.hit = msg.hits, 0
			return v.previewChanged()
		}
	}
	return nil
}

func (v *sessionsView) key(msg tea.KeyPressMsg) tea.Cmd {
	k := v.env.keys
	switch {
	case key.Matches(msg, k.Up):
		return v.move(-1)
	case key.Matches(msg, k.Down):
		return v.move(1)
	case key.Matches(msg, k.PageUp):
		return v.move(-max(1, v.listRows()-1))
	case key.Matches(msg, k.PageDown):
		return v.move(max(1, v.listRows()-1))
	case key.Matches(msg, k.Top):
		return v.pick(v.selectable(0, 1))
	case key.Matches(msg, k.Bottom):
		return v.pick(v.selectable(len(v.rows)-1, -1))
	case key.Matches(msg, k.Resume):
		return v.resume()
	case key.Matches(msg, k.Fork):
		return v.fork()
	case key.Matches(msg, k.New):
		return v.newSession()
	case key.Matches(msg, k.Filter):
		v.filtering = true
		return v.filter.Focus()
	case key.Matches(msg, k.Search):
		v.searching = true
		v.search.Reset()
		v.hits, v.hit = nil, 0
		return tea.Batch(v.search.Focus(), v.previewChanged())
	case key.Matches(msg, k.Sort):
		v.sort = (v.sort + 1) % sortMode(len(sortNames))
		return v.rebuild()
	case key.Matches(msg, k.Group):
		v.group = !v.group
		return v.rebuild()
	case key.Matches(msg, k.Scope):
		v.scope = (v.scope + 1) % scopeMode(len(scopeNames))
		return v.rebuild()
	case key.Matches(msg, k.Back):
		if v.filter.Value() != "" {
			v.filter.Reset()
			return v.rebuild()
		}
	}
	return nil
}

func (v *sessionsView) filterKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.filtering = false
		v.filter.Blur()
		v.filter.Reset()
		return v.rebuild()
	case "enter":
		v.filtering = false
		v.filter.Blur()
		return nil
	case "up":
		return v.move(-1)
	case "down":
		return v.move(1)
	}
	before := v.filter.Value()
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(msg)
	if v.filter.Value() != before {
		return tea.Batch(cmd, v.rebuild())
	}
	return cmd
}

func (v *sessionsView) searchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.closeSearch()
		return v.previewChanged()
	case "enter":
		if v.hit >= len(v.hits) {
			return nil
		}
		id := v.hits[v.hit].SessionID
		v.closeSearch()
		return v.jumpTo(id)
	case "up":
		if v.hit > 0 {
			v.hit--
			return v.previewChanged()
		}
		return nil
	case "down":
		if v.hit < len(v.hits)-1 {
			v.hit++
			return v.previewChanged()
		}
		return nil
	}
	before := v.search.Value()
	var cmd tea.Cmd
	v.search, cmd = v.search.Update(msg)
	q := v.search.Value()
	if q == before {
		return cmd
	}
	v.searchSeq++
	if strings.TrimSpace(q) == "" {
		v.hits, v.hit = nil, 0
		return tea.Batch(cmd, v.previewChanged())
	}
	seq, store := v.searchSeq, v.env.store
	return tea.Batch(cmd, func() tea.Msg {
		return searchMsg{seq: seq, hits: store.SearchPrompts(q, searchLimit)}
	})
}

func (v *sessionsView) closeSearch() {
	v.searching = false
	v.search.Blur()
	v.hits, v.hit = nil, 0
}

// jumpTo selects a session, widening the scope and dropping the filter if
// they hide it.
func (v *sessionsView) jumpTo(id string) tea.Cmd {
	if _, ok := v.lookup(id); !ok {
		return status("That session is no longer in the list")
	}
	v.selID, v.picked = id, true
	cmd := v.rebuild()
	if s, _ := v.Selected(); s.ID != id {
		v.scope = scopeAll
		v.filter.Reset()
		v.selID = id
		cmd = v.rebuild()
	}
	return tea.Batch(cmd, v.previewChanged())
}

// Actions. A live session is never resumed a second time: enter focuses
// it, and fork waits until it has ended.

func (v *sessionsView) current() (core.Session, bool) {
	s, ok := v.Selected()
	if !ok {
		return s, false
	}
	if fresh, ok := v.env.store.Session(s.ID); ok {
		return fresh, true
	}
	return s, true
}

func (v *sessionsView) resume() tea.Cmd {
	s, ok := v.Selected()
	if !ok {
		return nil
	}
	return resumeOrFocus(v.env, s)
}

func (v *sessionsView) fork() tea.Cmd {
	s, ok := v.current()
	if !ok {
		return nil
	}
	if s.Live != nil {
		return status("%s is running%s; fork it once it has ended", quote(s.Title), where(s))
	}
	l := v.env.launcher
	return buildLaunch("Forked "+quote(s.Title), func() (core.Launch, error) { return l.Resume(s, true) })
}

func (v *sessionsView) newSession() tea.Cmd {
	s, ok := v.current()
	if !ok || s.ProjectDir == "" {
		return nil
	}
	l, dir := v.env.launcher, s.ProjectDir
	return buildLaunch("New session in "+projectName(dir), func() (core.Launch, error) { return l.New(dir) })
}

// Rows and selection.

// rebuild derives the rows from the snapshot, scope, filter, sort and
// grouping, keeping the selected session selected.
func (v *sessionsView) rebuild() tea.Cmd {
	list := make([]*core.Session, 0, len(v.env.sessions))
	for i := range v.env.sessions {
		s := &v.env.sessions[i]
		live := s.Live != nil
		if v.scope == scopeAll || (v.scope == scopeLive) == live {
			list = append(list, s)
		}
	}
	if q := strings.TrimSpace(v.filter.Value()); q != "" {
		src := make([]string, len(list))
		for i, s := range list {
			src[i] = oneLine(s.Title) + "  " + projectName(s.ProjectDir) + "  " + s.GitBranch
		}
		matches := fuzzy.FindNoSort(q, src)
		kept := make([]*core.Session, len(matches))
		for i, m := range matches {
			kept[i] = list[m.Index]
		}
		list = kept
	}
	slices.SortStableFunc(list, v.compare)

	v.rows = v.rows[:0]
	if v.group {
		var dirs []string
		byDir := map[string][]*core.Session{}
		for _, s := range list {
			if _, seen := byDir[s.ProjectDir]; !seen {
				dirs = append(dirs, s.ProjectDir)
			}
			byDir[s.ProjectDir] = append(byDir[s.ProjectDir], s)
		}
		for _, d := range dirs {
			v.rows = append(v.rows, row{dir: d, count: len(byDir[d])})
			for _, s := range byDir[d] {
				v.rows = append(v.rows, row{s: s})
			}
		}
	} else {
		for _, s := range list {
			v.rows = append(v.rows, row{s: s})
		}
	}

	// Sessions arrive one by one at start-up: until the scan is complete
	// or the user picks one, keep the top row selected rather than
	// whichever arrived first. After that the selection stays on its
	// session, so a key acts on the session that was read.
	i := v.selectable(0, 1)
	if v.picked {
		i = slices.IndexFunc(v.rows, func(r row) bool { return r.s != nil && r.s.ID == v.selID })
		if i < 0 {
			i = v.selectable(min(max(v.cursor, 0), len(v.rows)-1), 1)
		}
	}
	cmd := v.setCursor(i)
	if v.env.ready && v.cursor >= 0 {
		v.picked = true
	}
	return cmd
}

func (v *sessionsView) compare(a, b *core.Session) int {
	var c int
	switch v.sort {
	case sortProject:
		c = cmp.Compare(projectName(a.ProjectDir), projectName(b.ProjectDir))
	case sortContext:
		c = cmp.Compare(contextKey(*b), contextKey(*a))
	case sortCost:
		c = cmp.Compare(b.CostUSD, a.CostUSD)
	}
	if c != 0 {
		return c
	}
	// Recency counts in whole minutes, the list's resolution, so sessions
	// working at the same time do not trade places on every update.
	if c = b.LastActivity.Truncate(time.Minute).Compare(a.LastActivity.Truncate(time.Minute)); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// contextKey orders by window fill, sessions with an unknown window last.
func contextKey(s core.Session) float64 {
	if p, ok := s.ContextPercent(); ok {
		return p
	}
	return -1
}

// selectable finds the nearest session row from i, looking in direction
// dir first and then the other way. -1 when there is none.
func (v *sessionsView) selectable(i, dir int) int {
	for _, d := range []int{dir, -dir} {
		for j := i; j >= 0 && j < len(v.rows); j += d {
			if v.rows[j].s != nil {
				return j
			}
		}
	}
	return -1
}

func (v *sessionsView) move(delta int) tea.Cmd {
	if v.searching {
		v.hit = min(max(v.hit+delta, 0), max(len(v.hits)-1, 0))
		return v.previewChanged()
	}
	if v.cursor < 0 {
		return nil
	}
	dir := 1
	if delta < 0 {
		dir = -1
	}
	target := min(max(v.cursor+delta, 0), len(v.rows)-1)
	i := v.selectable(target, dir)
	if (i-v.cursor)*dir < 0 { // only headers that way: stay put
		i = v.cursor
	}
	return v.pick(i)
}

// pick selects row i on the user's behalf: from now on the selection
// follows that session as the list changes.
func (v *sessionsView) pick(i int) tea.Cmd {
	v.picked = true
	return v.setCursor(i)
}

func (v *sessionsView) setCursor(i int) tea.Cmd {
	v.cursor = i
	v.ensureVisible()
	id := ""
	if i >= 0 {
		id = v.rows[i].s.ID
	}
	if id == v.selID {
		return nil
	}
	v.selID = id
	return v.previewChanged()
}

func (v *sessionsView) ensureVisible() {
	n := v.listRows()
	if n <= 0 {
		return
	}
	if v.cursor >= 0 {
		top := v.cursor
		if top > 0 && v.rows[top-1].s == nil {
			top-- // keep the group header in view
		}
		if top < v.offset {
			v.offset = top
		}
		if v.cursor >= v.offset+n {
			v.offset = v.cursor - n + 1
		}
	}
	v.offset = min(max(v.offset, 0), max(len(v.rows)-n, 0))
}

func (v *sessionsView) click(x, y int) tea.Cmd {
	lay := v.layout()
	if x >= lay.listW || y < 1 || y >= lay.listH {
		return nil
	}
	if v.searching {
		if i := v.hitStart(lay.listH-1) + y - 1; i < len(v.hits) {
			v.hit = i
			return v.previewChanged()
		}
		return nil
	}
	i := v.offset + y - 1
	if i < len(v.rows) && v.rows[i].s != nil {
		return v.pick(i)
	}
	return nil
}

// hitStart is the first search hit shown when n fit.
func (v *sessionsView) hitStart(n int) int {
	return min(max(0, v.hit-n+1), max(0, len(v.hits)-n))
}

// Preview entries: loaded off the update loop, debounced while the
// selection moves, and cached for a few sessions.

// previewID is the session the preview shows.
func (v *sessionsView) previewID() string {
	s, ok := v.Selected()
	if !ok {
		return ""
	}
	return s.ID
}

// previewChanged schedules loading the preview's entries unless cached.
func (v *sessionsView) previewChanged() tea.Cmd {
	id := v.previewID()
	if id == "" {
		return nil
	}
	if _, ok := v.entries[id]; ok {
		v.touch(id)
		return nil
	}
	v.loadSeq++
	if v.env.previewDelay <= 0 {
		return v.loadEntries(id)
	}
	seq := v.loadSeq
	return tea.Tick(v.env.previewDelay, func(time.Time) tea.Msg { return previewDueMsg{seq} })
}

func (v *sessionsView) loadEntries(id string) tea.Cmd {
	store := v.env.store
	return func() tea.Msg {
		e, err := store.Entries(id, tailEntries)
		return entriesMsg{id: id, entries: e, err: err}
	}
}

func (v *sessionsView) remember(id string, e []core.Entry) {
	v.entries[id] = e
	v.touch(id)
	for len(v.cacheLRU) > entryCacheCap {
		delete(v.entries, v.cacheLRU[0])
		delete(v.loadErr, v.cacheLRU[0])
		v.cacheLRU = v.cacheLRU[1:]
	}
}

func (v *sessionsView) touch(id string) {
	v.cacheLRU = append(slices.DeleteFunc(v.cacheLRU, func(s string) bool { return s == id }), id)
}

func (v *sessionsView) forget(id string) {
	delete(v.entries, id)
	delete(v.loadErr, id)
	v.cacheLRU = slices.DeleteFunc(v.cacheLRU, func(s string) bool { return s == id })
}

// Layout.

type sessionsLayout struct {
	stacked      bool // preview under the list rather than beside it
	listW, listH int
	prevW, prevH int // zero when the preview is hidden
}

func (v *sessionsView) layout() sessionsLayout {
	if v.w >= wideLayout {
		pw := int(math.Round(float64(v.w) * v.env.layout.PreviewRatio))
		lw := v.w - pw - 1
		if lw < 44 {
			lw = 44
			pw = v.w - lw - 1
		}
		return sessionsLayout{listW: lw, listH: v.h, prevW: pw, prevH: v.h}
	}
	if v.h < minPreviewH {
		return sessionsLayout{listW: v.w, listH: v.h}
	}
	lh := (v.h - 1) * 9 / 20
	return sessionsLayout{stacked: true, listW: v.w, listH: lh, prevW: v.w, prevH: v.h - 1 - lh}
}

// listRows is how many rows fit under the list header.
func (v *sessionsView) listRows() int { return v.layout().listH - 1 }
