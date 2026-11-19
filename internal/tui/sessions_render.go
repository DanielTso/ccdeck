package tui

import (
	"fmt"
	"strings"

	"github.com/DanielTso/ccdeck/internal/core"
)

func (v *sessionsView) View() string {
	if v.w <= 0 || v.h <= 0 {
		return ""
	}
	lay := v.layout()
	list := v.renderList(lay.listW, lay.listH)
	if lay.prevW == 0 {
		return list
	}
	preview := v.renderPreview(lay.prevW, lay.prevH)
	if lay.stacked {
		rule := v.env.st.faint.Render(strings.Repeat("─", v.w))
		return list + "\n" + rule + "\n" + preview
	}
	sep := v.env.st.faint.Render("│")
	ll := strings.Split(list, "\n")
	pl := strings.Split(preview, "\n")
	var b strings.Builder
	for i := range v.h {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fit(ll[i], lay.listW) + sep + pl[i])
	}
	return b.String()
}

// The list.

func (v *sessionsView) renderList(w, h int) string {
	lines := []string{v.listHeader(w)}
	n := h - 1
	switch {
	case v.searching:
		lines = append(lines, v.renderHits(w, n)...)
	case len(v.rows) == 0:
		lines = append(lines, "", " "+v.env.st.muted.Render(truncate(v.emptyText(), w-2)))
	default:
		cols := v.columns(w)
		for i := v.offset; i < len(v.rows) && i < v.offset+n; i++ {
			lines = append(lines, v.renderRow(v.rows[i], i == v.cursor, cols))
		}
	}
	return clip(strings.Join(lines, "\n"), w, h)
}

func (v *sessionsView) emptyText() string {
	switch {
	case v.filter.Value() != "":
		return "No sessions match “" + v.filter.Value() + "”. esc clears the filter."
	case len(v.env.sessions) == 0:
		return "No sessions yet."
	default:
		return "No " + scopeNames[v.scope] + " sessions."
	}
}

// listHeader shows the filter or search input on the left and the list's
// scope, sort and size on the right.
func (v *sessionsView) listHeader(w int) string {
	st := v.env.st
	var left, right string
	switch {
	case v.searching:
		left = v.search.View()
		right = st.muted.Render(itoa(len(v.hits)) + " found")
		if strings.TrimSpace(v.search.Value()) == "" {
			right = ""
		}
	case v.filtering:
		left = v.filter.View()
	case v.filter.Value() != "":
		left = st.accent.Render("/ ") + st.text.Render(truncate(v.filter.Value(), w/2))
	default:
		left = st.bold.Render("Sessions")
	}
	if !v.searching {
		state := scopeNames[v.scope] + " · " + sortNames[v.sort]
		if v.group {
			state += " · by project"
		}
		right = st.muted.Render(state + "  " + itoa(v.sessionCount()))
	}
	left = " " + left
	gap := w - width(left) - width(right) - 1
	if gap < 1 {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

func (v *sessionsView) sessionCount() int {
	n := 0
	for _, r := range v.rows {
		if r.s != nil {
			n++
		}
	}
	return n
}

// columns decides which columns fit in a list w cells wide.
type columns struct {
	w                     int
	gap                   int
	title, project, model int
	gauge, age            int
}

func (v *sessionsView) columns(w int) columns {
	c := columns{w: w, gap: 1, age: 4}
	if w >= 70 {
		c.gap = 2
	}
	fixed := 3 + c.age + c.gap // marker, glyph, space; age and its gap
	switch {
	case w >= 110:
		c.gauge = 13
	case w >= 64:
		c.gauge = 9
	case w >= 40:
		c.gauge = 4 // the percentage alone
	}
	if c.gauge > 0 {
		fixed += c.gauge + c.gap
	}
	if w >= 78 {
		c.model = 10
		fixed += c.model + c.gap
	}
	if !v.group && w >= 52 {
		c.project = min(max(w/5, 10), 20)
		fixed += c.project + c.gap
	}
	c.title = max(w-fixed-1, 8)
	return c
}

func (v *sessionsView) renderRow(r row, selected bool, c columns) string {
	st := v.env.st
	if r.s == nil {
		name := projectName(r.dir)
		dir := v.env.homePath(r.dir)
		head := " " + st.bold.Render(name) + " " + st.muted.Render(itoa(r.count))
		rest := c.w - width(head) - 1
		if rest > width(dir)+4 {
			head += "  " + st.muted.Render(truncateLeft(dir, rest-2))
		}
		return head
	}
	s := *r.s
	if selected {
		st = st.on(st.selected)
	}
	gap := st.text.Render(strings.Repeat(" ", c.gap))
	marker := st.text.Render(" ")
	if selected {
		marker = st.marker.Render("▌")
	}
	title := st.text
	if selected {
		title = st.bold
	}
	var b strings.Builder
	b.WriteString(marker + st.mark(s.Status()) + st.text.Render(" "))
	b.WriteString(title.Render(fit(oneLine(s.Title), c.title)))
	if c.project > 0 {
		b.WriteString(gap + st.muted.Render(fit(projectName(s.ProjectDir), c.project)))
	}
	if c.model > 0 {
		b.WriteString(gap + st.muted.Render(fit(shortModel(s.Model), c.model)))
	}
	if c.gauge > 0 {
		b.WriteString(gap + st.gauge(s, c.gauge))
	}
	b.WriteString(gap + st.muted.Render(fitRight(relTime(v.env.now(), s.LastActivity), c.age)))
	b.WriteString(st.text.Render(" "))
	return b.String()
}

func (v *sessionsView) renderHits(w, n int) []string {
	st := v.env.st
	if strings.TrimSpace(v.search.Value()) == "" {
		return []string{"", " " + st.muted.Render(truncate("Type to search every prompt you have sent, in every project.", w-2))}
	}
	if len(v.hits) == 0 {
		return []string{"", " " + st.muted.Render("No prompts match.")}
	}
	start := v.hitStart(n)
	var lines []string
	for i := start; i < len(v.hits) && i < start+n; i++ {
		h := v.hits[i]
		rs := st
		marker := " "
		if i == v.hit {
			rs = st.on(st.selected)
			marker = "▌"
		}
		project := "—"
		if s, ok := v.lookup(h.SessionID); ok {
			project = projectName(s.ProjectDir)
		}
		pw := min(max(w/5, 8), 16)
		tw := max(w-4-4-pw-4, 4)
		lines = append(lines, rs.marker.Render(marker)+
			rs.muted.Render(fitRight(relTime(v.env.now(), h.Time), 4)+"  ")+
			rs.muted.Render(fit(project, pw)+"  ")+
			rs.text.Render(fit(oneLine(h.Text), tw))+rs.text.Render(" "))
	}
	return lines
}

// The preview.

func (v *sessionsView) renderPreview(w, h int) string {
	s, ok := v.Selected()
	if !ok {
		return clip("", w, h)
	}
	iw := w - 3 // one cell of margin on the left, two on the right
	var lines []string
	if h < 18 {
		lines = v.previewCompact(s, iw)
	} else {
		lines = v.previewFull(s, iw)
	}
	if rest := h - len(lines); rest >= 3 {
		lines = append(lines, v.env.st.faint.Render(strings.Repeat("─", iw)))
		lines = append(lines, v.conversation(s.ID, iw, rest-1)...)
	}
	for i, l := range lines {
		lines[i] = " " + l
	}
	return clip(strings.Join(lines, "\n"), w, h)
}

// statusLines is the status badge, how long it has held, and what a
// waiting session waits for.
func (v *sessionsView) statusLines(s core.Session, w int) []string {
	st := v.env.st
	now := v.env.now()
	state := st.badge(s.Status())
	if s.Live != nil {
		state += st.muted.Render(" for " + since(now, s.Live.StatusSince))
		if s.Live.Name != "" {
			state += st.muted.Render(" · " + s.Live.Name)
		}
	} else {
		state += st.muted.Render(" " + ago(now, s.LastActivity))
	}
	lines := []string{truncate(state, w)}
	if s.Status() == core.StatusWaiting && s.Live.WaitingFor != "" {
		lines = append(lines, st.statusStyle(core.StatusWaiting).Render(truncate("  "+oneLine(s.Live.WaitingFor), w)))
	}
	return lines
}

// previewCompact fits the essentials in a few lines for short panes.
func (v *sessionsView) previewCompact(s core.Session, w int) []string {
	st := v.env.st
	lines := []string{st.bold.Render(truncate(oneLine(s.Title), w))}
	lines = append(lines, v.statusLines(s, w)...)
	sep := st.faint.Render(" · ")
	meta := st.text.Render(projectName(s.ProjectDir))
	if s.Model != "" {
		meta += sep + st.muted.Render(shortModel(s.Model))
	}
	meta += sep + st.gaugeInline(s) + sep + st.muted.Render(fmtCost(s.CostKind, s.CostUSD))
	return append(lines, truncate(meta, w))
}

func (v *sessionsView) previewFull(s core.Session, w int) []string {
	st := v.env.st
	now := v.env.now()
	var lines []string

	titleLines := wrap(oneLine(s.Title), w)
	if len(titleLines) > 2 {
		titleLines = titleLines[:2]
		titleLines[1] = truncate(titleLines[1]+" …", w)
	}
	for _, l := range titleLines {
		lines = append(lines, st.bold.Render(l))
	}
	lines = append(lines, v.statusLines(s, w)...)
	lines = append(lines, "")

	field := func(label, value string) {
		lines = append(lines, st.muted.Render(fit(label, 9))+truncate(value, w-9))
	}
	name := projectName(s.ProjectDir)
	field("project", st.text.Render(name)+"  "+
		st.muted.Render(truncateLeft(v.env.homePath(s.ProjectDir), max(w-9-width(name)-2, 4))))
	if s.GitBranch != "" {
		field("branch", st.text.Render(s.GitBranch))
	}
	if s.Model != "" {
		model := st.text.Render(shortModel(s.Model))
		if s.Version != "" {
			model += st.muted.Render("  v" + s.Version)
		}
		field("model", model)
	}
	if !s.StartedAt.IsZero() {
		field("started", st.text.Render(ago(now, s.StartedAt))+st.muted.Render(" · "+s.StartedAt.Local().Format("Jan 2 15:04")))
	}
	field("context", v.contextLine(s, w-9))
	if u := s.TotalUsage(); u.Total() > 0 {
		field("tokens", st.text.Render(fmtTokens(u.Total()))+st.muted.Render(" total · ")+
			st.text.Render(fmtTokens(u.Fresh()))+st.muted.Render(" fresh · ")+
			st.text.Render(fmtTokens(u.Output))+st.muted.Render(" out"))
	}
	switch s.CostKind {
	case core.CostEstimated:
		field("cost", st.text.Render(fmtCost(s.CostKind, s.CostUSD))+st.muted.Render("  estimate"))
	case core.CostReported:
		field("cost", st.text.Render(fmtCost(s.CostKind, s.CostUSD))+st.muted.Render("  reported"))
	default:
		field("cost", st.muted.Render("— no price known"))
	}
	if s.Turns+s.Messages > 0 {
		field("turns", st.text.Render(itoa(s.Turns))+st.muted.Render(plural(s.Turns, " prompt", " prompts")+" · ")+
			st.text.Render(itoa(s.Messages))+st.muted.Render(plural(s.Messages, " reply", " replies")+" · ")+
			st.text.Render(itoa(s.ToolCalls))+st.muted.Render(plural(s.ToolCalls, " tool call", " tool calls")))
	}
	if len(s.Subagents) > 0 {
		lines = append(lines, "")
		lines = append(lines, v.agentLines(s, w)...)
	}
	return append(lines, "")
}

// agentLines lists up to three subagents, most recently active first.
func (v *sessionsView) agentLines(s core.Session, w int) []string {
	st := v.env.st
	now := v.env.now()
	indent := strings.Repeat(" ", 9)
	lines := []string{st.muted.Render(fit("agents", 9)) + st.text.Render(itoa(len(s.Subagents)))}
	for i, a := range s.Subagents {
		if i == 3 {
			lines = append(lines, indent+st.muted.Render(fmt.Sprintf("+%d more", len(s.Subagents)-3)))
			break
		}
		tail := fitRight(fmtTokens(a.Usage.Fresh()), 5) + "  " + fitRight(relTime(now, a.LastActivity), 3)
		typ := fit(a.Type, min(16, max(w/5, 6)))
		desc := max(w-9-width(typ)-2-width(tail)-2, 4)
		lines = append(lines, indent+st.text.Render(typ)+"  "+
			st.muted.Render(fit(oneLine(a.Description), desc))+"  "+st.muted.Render(tail))
	}
	return lines
}

func (v *sessionsView) contextLine(s core.Session, w int) string {
	st := v.env.st
	if _, ok := s.ContextPercent(); !ok {
		if s.ContextTokens == 0 {
			return st.muted.Render("—")
		}
		return st.text.Render(fmtTokens(s.ContextTokens)) + st.muted.Render(" tokens · window unknown")
	}
	detail := "  " + fmtTokens(s.ContextTokens) + " / " + fmtTokens(s.ContextWindow)
	g := min(max(w-width(detail), 9), 30)
	return st.gauge(s, g) + st.muted.Render(detail)
}

// conversation renders the end of a session's conversation in at most n
// lines, newest at the bottom.
func (v *sessionsView) conversation(id string, w, n int) []string {
	entries, ok := v.entries[id]
	if !ok {
		return nil
	}
	return convoTail(v.env.st, entries, v.loadErr[id], w, n)
}
