package tui

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

// usageView shows token use and cost: today and the last seven days, by
// model, and per session with context gauges and burn-rate sparklines.
//
// Daily totals come from the store's Daily, which can be costly, so they
// load in a command when the view is shown and at most every dailyEvery
// while it stays on screen and sessions keep changing.
type usageView struct {
	env     *env
	w, h    int
	visible bool

	days    []core.DayUsage // the last usageDays days, oldest first
	loaded  bool
	loading bool
	due     bool // a reload is scheduled

	rows   []*core.Session // live first, then most recently active
	cursor int
	offset int
	selID  string
	picked bool // the user chose a row; until then the top row stays selected
}

type (
	dailyMsg    struct{ days []core.DayUsage }
	dailyDueMsg struct{}
)

const (
	usageDays  = 7
	dailyEvery = 2 * time.Second
)

func newUsageView(e *env) *usageView { return &usageView{env: e} }

func (v *usageView) Init() tea.Cmd {
	v.rebuild()
	return nil
}

func (v *usageView) SetSize(w, h int) {
	v.w, v.h = w, h
	v.ensureVisible()
}

func (v *usageView) Capturing() bool { return false }

func (v *usageView) Show(visible bool) tea.Cmd {
	v.visible = visible
	if visible && !v.loading {
		return v.load()
	}
	return nil
}

func (v *usageView) Hints() []key.Binding {
	k := v.env.keys
	resume := withDesc(k.Resume, "resume")
	if s, ok := v.Selected(); ok && s.Live != nil {
		resume = withDesc(k.Resume, "focus")
	}
	return []key.Binding{resume, k.Open, withDesc(k.Down, "move")}
}

// Selected is the session under the cursor.
func (v *usageView) Selected() (core.Session, bool) {
	if v.cursor >= 0 && v.cursor < len(v.rows) {
		return *v.rows[v.cursor], true
	}
	return core.Session{}, false
}

func (v *usageView) load() tea.Cmd {
	v.loading = true
	store := v.env.store
	return func() tea.Msg { return dailyMsg{days: store.Daily(usageDays)} }
}

func (v *usageView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case sessionsMsg:
		v.rebuild()
		if v.visible && !v.due && !v.loading {
			v.due = true
			return tea.Tick(dailyEvery, func(time.Time) tea.Msg { return dailyDueMsg{} })
		}
	case dailyDueMsg:
		v.due = false
		if v.visible && !v.loading {
			return v.load()
		}
	case dailyMsg:
		v.loading, v.loaded = false, true
		v.days = msg.days
	case tea.KeyPressMsg:
		return v.key(msg)
	case tea.MouseWheelMsg:
		switch d := wheelDelta(msg); {
		case d < 0:
			v.move(-1)
		case d > 0:
			v.move(1)
		}
	case tea.MouseClickMsg:
		top, n := v.tableRows()
		if i := v.offset + msg.Y - top; msg.Y >= top && msg.Y < top+n && i < len(v.rows) {
			v.pick(i)
		}
	}
	return nil
}

func (v *usageView) key(msg tea.KeyPressMsg) tea.Cmd {
	k := v.env.keys
	_, n := v.tableRows()
	switch {
	case key.Matches(msg, k.Up):
		v.move(-1)
	case key.Matches(msg, k.Down):
		v.move(1)
	case key.Matches(msg, k.PageUp):
		v.move(-max(n-1, 1))
	case key.Matches(msg, k.PageDown):
		v.move(max(n-1, 1))
	case key.Matches(msg, k.Top):
		v.pick(0)
	case key.Matches(msg, k.Bottom):
		v.pick(len(v.rows) - 1)
	case key.Matches(msg, k.Resume):
		if s, ok := v.Selected(); ok {
			return resumeOrFocus(v.env, s)
		}
	case key.Matches(msg, k.Back):
		return goBack
	}
	return nil
}

// rebuild orders the sessions, live ones first and then by activity to
// the minute, keeping the selected session selected.
func (v *usageView) rebuild() {
	v.rows = v.rows[:0]
	for i := range v.env.sessions {
		v.rows = append(v.rows, &v.env.sessions[i])
	}
	slices.SortStableFunc(v.rows, func(a, b *core.Session) int {
		if al, bl := a.Live != nil, b.Live != nil; al != bl {
			if al {
				return -1
			}
			return 1
		}
		if c := b.LastActivity.Truncate(time.Minute).Compare(a.LastActivity.Truncate(time.Minute)); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	// As in Sessions: the top row until the scan is complete or the user
	// picks one, then that session.
	i := 0
	if v.picked {
		i = slices.IndexFunc(v.rows, func(s *core.Session) bool { return s.ID == v.selID })
		if i < 0 {
			i = min(v.cursor, len(v.rows)-1)
		}
	}
	v.setCursor(i)
	if v.env.ready && v.selID != "" {
		v.picked = true
	}
}

func (v *usageView) move(d int) { v.pick(min(max(v.cursor+d, 0), len(v.rows)-1)) }

// pick selects row i on the user's behalf.
func (v *usageView) pick(i int) {
	v.picked = true
	v.setCursor(i)
}

func (v *usageView) setCursor(i int) {
	v.cursor = max(i, 0)
	v.selID = ""
	if v.cursor < len(v.rows) {
		v.selID = v.rows[v.cursor].ID
	}
	v.ensureVisible()
}

func (v *usageView) ensureVisible() {
	_, n := v.tableRows()
	if n <= 0 {
		return
	}
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+n {
		v.offset = v.cursor - n + 1
	}
	v.offset = min(max(v.offset, 0), max(len(v.rows)-n, 0))
}

// Layout. From the top: the totals, the days, the models and the session
// table, which takes the rest. When the window is short the days and then
// the models give way, so the table keeps a few rows.

type usageLayout struct {
	days, models bool
	tableTop     int // line of the first session row
}

const (
	minTableRows = 4
	maxTitleW    = 64 // wider windows give the room to the sparkline
)

func (v *usageView) layout() usageLayout {
	models := min(len(v.modelNames()), 4)
	lay := usageLayout{days: true, models: true}
	height := func() int {
		n := 3 // totals and a blank line
		if lay.days {
			n += 4 // title, labels, bars, blank
		}
		if lay.models {
			n += 2 + models
		}
		return n + 2 // table title and column header
	}
	if v.h-height() < minTableRows {
		lay.days = false
	}
	if v.h-height() < minTableRows {
		lay.models = false
	}
	lay.tableTop = height()
	return lay
}

// tableRows is the first line of the session rows and how many fit.
func (v *usageView) tableRows() (int, int) {
	top := v.layout().tableTop
	return top, max(v.h-top, 0)
}

// Rendering.

func (v *usageView) View() string {
	if v.w <= 0 || v.h <= 0 {
		return ""
	}
	lay := v.layout()
	var lines []string
	lines = append(lines, v.totals()...)
	lines = append(lines, "")
	if lay.days {
		lines = append(lines, v.dayBars()...)
		lines = append(lines, "")
	}
	if lay.models {
		lines = append(lines, v.modelTable(min(len(v.modelNames()), 4))...)
		lines = append(lines, "")
	}
	for len(lines) < lay.tableTop-2 {
		lines = append(lines, "")
	}
	lines = append(lines, v.sessionTable(v.h-lay.tableTop)...)
	return clip(strings.Join(lines, "\n"), v.w, v.h)
}

// totals is a line each for today and the last seven days.
func (v *usageView) totals() []string {
	st := v.env.st
	if !v.loaded {
		return []string{" " + st.bold.Render("Usage") + st.muted.Render("  loading daily totals…"), ""}
	}
	var week core.DayUsage
	week.Usage = map[string]core.Usage{}
	ids := map[string]bool{}
	partial := false
	for _, d := range v.days {
		for m, u := range d.Usage {
			week.Usage[m] = week.Usage[m].Add(u)
		}
		// A day with tokens and no cost has a model without a price: the
		// week's sum then leaves that day out, and must say so.
		if d.CostUSD <= 0 && sumUsage(d.Usage).Total() > 0 {
			partial = true
		}
		week.CostUSD += d.CostUSD
		for _, id := range d.SessionIDs {
			ids[id] = true
		}
		// A store that does not name its sessions can only be summed.
		week.Sessions += max(d.Sessions-len(d.SessionIDs), 0)
	}
	week.Sessions += len(ids)
	today := core.DayUsage{}
	if len(v.days) > 0 {
		today = v.days[len(v.days)-1]
	}
	return []string{v.totalLine("Today", today, false), v.totalLine(itoa(len(v.days))+" days", week, partial)}
}

// totalLine is one line of totals. partial marks a cost that leaves out
// days with an unpriced model.
func (v *usageView) totalLine(label string, d core.DayUsage, partial bool) string {
	st := v.env.st
	u := sumUsage(d.Usage)
	sep := st.faint.Render(" · ")
	parts := []string{
		st.text.Render(fmtTokens(u.Total())) + st.muted.Render(" tokens"),
		st.text.Render(fmtTokens(u.Fresh())) + st.muted.Render(" fresh"),
		st.text.Render(fmtTokens(u.Output)) + st.muted.Render(" out"),
		v.dayCost(d.CostUSD, u.Total(), partial),
		st.text.Render(itoa(d.Sessions)) + st.muted.Render(plural(d.Sessions, " session", " sessions")),
	}
	line := " " + st.bold.Render(fit(label, 8)) + " "
	for i, p := range parts {
		next := p
		if i > 0 {
			next = sep + p
		}
		if width(line)+width(next) > v.w-1 {
			break
		}
		line += next
	}
	return line
}

// dayCost shows a daily figure, which is always an estimate. Zero dollars
// for days with tokens means no price was known, and says so. A partial
// figure covers only some of the tokens, so it is a lower bound.
func (v *usageView) dayCost(usd float64, tokens int64, partial bool) string {
	st := v.env.st
	switch {
	case tokens == 0:
		return st.muted.Render("no cost")
	case usd <= 0:
		return st.muted.Render("no price known")
	case partial:
		return st.text.Render("≥ "+fmtCost(core.CostEstimated, usd)) + st.muted.Render(" partial estimate")
	}
	return st.text.Render(fmtCost(core.CostEstimated, usd)) + st.muted.Render(" estimate")
}

func sumUsage(m map[string]core.Usage) core.Usage {
	var t core.Usage
	for _, u := range m {
		t = t.Add(u)
	}
	return t
}

// dayBars draws fresh tokens per day: a label line and a bar line, one
// column per day, today last.
func (v *usageView) dayBars() []string {
	st := v.env.st
	title := " " + st.bold.Render("Fresh tokens per day") + st.muted.Render(" · today last")
	if !v.loaded || len(v.days) == 0 {
		return []string{title, "", ""}
	}
	var top int64
	for _, d := range v.days {
		top = max(top, sumUsage(d.Usage).Fresh())
	}
	colW := max((v.w-1)/len(v.days), 4)
	var labels, bars strings.Builder
	labels.WriteString(" ")
	bars.WriteString(" ")
	for i, d := range v.days {
		fresh := sumUsage(d.Usage).Fresh()
		label := d.Date.Format("Mon")
		if n := fmtTokens(fresh); width(label)+1+width(n) <= colW-1 && fresh > 0 {
			label += " " + n
		}
		style := st.muted
		if i == len(v.days)-1 {
			style = st.bold
		}
		labels.WriteString(style.Render(fit(label, colW)))
		bar := strings.Repeat(sparkline([]int64{fresh}, top), colW-2)
		if fresh == 0 {
			bar = st.faint.Render(fit("·", colW-2))
		} else {
			bar = st.accent.Render(bar)
		}
		bars.WriteString(bar + "  ")
	}
	return []string{title, labels.String(), bars.String()}
}

// modelNames are the models used in the last seven days, most fresh
// tokens first.
func (v *usageView) modelNames() []string {
	week := map[string]core.Usage{}
	for _, d := range v.days {
		for m, u := range d.Usage {
			week[m] = week[m].Add(u)
		}
	}
	names := slices.Collect(maps.Keys(week))
	slices.SortFunc(names, func(a, b string) int {
		if c := cmp.Compare(week[b].Fresh(), week[a].Fresh()); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return names
}

// modelTable is the per-model breakdown: tokens today and over the week,
// and each model's share of the week's fresh tokens. Daily totals carry no
// per-model cost, so there is no cost column.
func (v *usageView) modelTable(n int) []string {
	st := v.env.st
	week := map[string]core.Usage{}
	var today map[string]core.Usage
	var all int64
	for i, d := range v.days {
		for m, u := range d.Usage {
			week[m] = week[m].Add(u)
			all += u.Fresh()
		}
		if i == len(v.days)-1 {
			today = d.Usage
		}
	}
	type col struct {
		head string
		w    int
		val  func(m string) string
	}
	cols := []col{
		{"fresh today", 12, func(m string) string { return fmtTokens(today[m].Fresh()) }},
		{"fresh 7d", 10, func(m string) string { return fmtTokens(week[m].Fresh()) }},
		{"output 7d", 11, func(m string) string { return fmtTokens(week[m].Output) }},
		{"cache read 7d", 15, func(m string) string { return fmtTokens(week[m].CacheRead) }},
	}
	nameW := min(max(v.w/5, 10), 22)
	shareW := 0
	for {
		used := 1 + nameW
		for _, c := range cols {
			used += c.w
		}
		shareW = min(v.w-used-3, 40)
		if shareW >= 12 || len(cols) == 1 {
			break
		}
		cols = cols[:len(cols)-1]
	}
	head := " " + st.bold.Render(fit("Model", nameW))
	for _, c := range cols {
		head += st.muted.Render(fitRight(c.head, c.w))
	}
	if shareW >= 12 {
		label := "share of 7d fresh"
		if width(label) > shareW {
			label = "share"
		}
		head += "  " + st.muted.Render(fit(label, shareW))
	}
	lines := []string{head}
	for _, m := range v.modelNames()[:n] {
		l := " " + st.text.Render(fit(shortModel(m), nameW))
		for _, c := range cols {
			l += st.text.Render(fitRight(c.val(m), c.w))
		}
		if shareW >= 12 {
			pct := 0.0
			if all > 0 {
				pct = 100 * float64(week[m].Fresh()) / float64(all)
			}
			l += "  " + v.shareBar(pct, shareW)
		}
		lines = append(lines, l)
	}
	return lines
}

func (v *usageView) shareBar(pct float64, w int) string {
	st := v.env.st
	bar := w - 5
	filled := int(pct/100*float64(bar) + 0.5)
	if filled == 0 && pct > 0 {
		filled = 1
	}
	return st.accent.Render(strings.Repeat("━", filled)) + st.faint.Render(strings.Repeat("─", bar-filled)) +
		st.muted.Render(fitRight(fmt.Sprintf("%.0f%%", pct), 5))
}

// Session table.

type usageCols struct {
	title, model, gauge, spark, hour, fresh, cost int
}

func (v *usageView) usageColumns() usageCols {
	w := v.w
	c := usageCols{gauge: 9, hour: 7, cost: 8}
	switch {
	case w >= 150:
		c.spark, c.gauge = 30, 13
	case w >= 110:
		c.spark, c.gauge = 20, 13
	case w >= 70:
		c.spark = 12
	case w >= 56:
		c.spark, c.gauge = 8, 4
	default:
		c.gauge, c.hour = 4, 0
	}
	if w >= 96 {
		c.model = 10
	}
	if w >= 120 {
		c.fresh = 8
	}
	fixed := 3 + 2 + c.gauge + 2 + c.cost + 1 // marker, mark and space; gauge and cost with their gaps; margin
	for _, n := range []int{c.model, c.spark, c.hour, c.fresh} {
		if n > 0 {
			fixed += 2 + n
		}
	}
	c.title = max(w-fixed, 8)
	if c.title > maxTitleW && c.spark > 0 {
		grow := min(c.title-maxTitleW, 60-c.spark) // at most a bar a minute
		c.spark += grow
		c.title -= grow
	}
	return c
}

func (v *usageView) sessionTable(n int) []string {
	st := v.env.st
	c := v.usageColumns()
	legend := st.muted.Render("cost: ~ estimate · — no price known ")
	title := " " + st.bold.Render("Sessions") + st.muted.Render("  live first")
	if gap := v.w - width(title) - width(legend); gap >= 2 {
		title += strings.Repeat(" ", gap) + legend
	}
	lines := []string{title, v.tableHeader(c)}
	if len(v.rows) == 0 {
		return append(lines, " "+st.muted.Render("No sessions yet."))
	}
	end := min(v.offset+n, len(v.rows))
	now := v.env.now()
	bins := make([][]int64, 0, end-v.offset)
	var top int64 // one scale for every sparkline on screen, so rows compare
	for _, s := range v.rows[v.offset:end] {
		b := burnBins(s.Recent, now, c.spark)
		for _, x := range b {
			top = max(top, x)
		}
		bins = append(bins, b)
	}
	for i, s := range v.rows[v.offset:end] {
		lines = append(lines, v.sessionRow(*s, v.offset+i == v.cursor, c, bins[i], top))
	}
	return lines
}

func (v *usageView) tableHeader(c usageCols) string {
	st := v.env.st
	gap := "  "
	h := "   " + fit("", c.title)
	if c.model > 0 {
		h += gap + fit("model", c.model)
	}
	h += gap + fitRight("context", c.gauge)
	if c.spark > 0 {
		h += gap + fit("last hour", c.spark)
	}
	if c.hour > 0 {
		h += gap + fitRight("tok/h", c.hour)
	}
	if c.fresh > 0 {
		h += gap + fitRight("fresh", c.fresh)
	}
	h += gap + fitRight("cost", c.cost)
	return st.muted.Render(h)
}

func (v *usageView) sessionRow(s core.Session, selected bool, c usageCols, bins []int64, top int64) string {
	st := v.env.st
	if selected {
		st = st.on(st.selected)
	}
	gap := st.text.Render("  ")
	marker := st.text.Render(" ")
	title := st.text
	if selected {
		marker, title = st.marker.Render("▌"), st.bold
	}
	var b strings.Builder
	b.WriteString(marker + st.mark(s.Status()) + st.text.Render(" "))
	b.WriteString(title.Render(fit(oneLine(s.Title), c.title)))
	if c.model > 0 {
		b.WriteString(gap + st.muted.Render(fit(shortModel(s.Model), c.model)))
	}
	b.WriteString(gap + st.gauge(s, c.gauge))
	var hour int64
	for _, x := range burnBins(s.Recent, v.env.now(), 1) {
		hour += x
	}
	if c.spark > 0 {
		if hour == 0 {
			b.WriteString(gap + st.faint.Render(fit("—", c.spark)))
		} else {
			b.WriteString(gap + st.accent.Render(sparkline(bins, top)))
		}
	}
	if c.hour > 0 {
		label := "—"
		if hour > 0 {
			label = fmtTokens(hour)
		}
		b.WriteString(gap + st.text.Render(fitRight(label, c.hour)))
	}
	if c.fresh > 0 {
		fresh := "—"
		if n := s.TotalUsage().Fresh(); n > 0 {
			fresh = fmtTokens(n)
		}
		b.WriteString(gap + st.muted.Render(fitRight(fresh, c.fresh)))
	}
	cost := st.text
	if s.CostKind == core.CostNone {
		cost = st.muted
	}
	b.WriteString(gap + cost.Render(fitRight(fmtCost(s.CostKind, s.CostUSD), c.cost)) + st.text.Render(" "))
	return b.String()
}
