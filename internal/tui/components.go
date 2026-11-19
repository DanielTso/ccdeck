package tui

import (
	"fmt"
	"image/color"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Text measurement and fitting. Everything works in terminal cells, never
// bytes or runes, so wide characters and emoji line up.

// oneLine collapses all whitespace, newlines included, to single spaces,
// after removing escape sequences and control characters.
func oneLine(s string) string { return strings.Join(strings.Fields(sanitize(s)), " ") }

// sanitize removes terminal escape sequences and control characters other
// than newline and tab, so text from a transcript cannot move the cursor,
// recolour the screen or throw off width measurement.
func sanitize(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r < 0xa0)
}

// paragraphs wraps text to w cells keeping its line breaks. Runs of blank
// lines collapse to one and tabs become four spaces.
func paragraphs(text string, w int) []string {
	var out []string
	blank := false
	for _, l := range strings.Split(strings.TrimSpace(sanitize(text)), "\n") {
		l = strings.TrimRight(strings.ReplaceAll(l, "\t", "    "), " ")
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, wrap(l, w)...)
	}
	return out
}

// width is the display width of s in cells.
func width(s string) int { return ansi.StringWidth(s) }

// truncate cuts s to at most w cells, ending in an ellipsis when it cuts.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// truncateLeft cuts s from the left to at most w cells, starting with an
// ellipsis when it cuts. For paths, whose end matters most.
func truncateLeft(s string, w int) string {
	n := width(s)
	if w <= 0 {
		return ""
	}
	if n <= w {
		return s
	}
	return ansi.TruncateLeft(s, n-w+1, "…")
}

// fit truncates or pads s to exactly w cells, left-aligned.
func fit(s string, w int) string {
	s = truncate(s, w)
	return s + strings.Repeat(" ", max(0, w-width(s)))
}

// fitRight truncates or pads s to exactly w cells, right-aligned.
func fitRight(s string, w int) string {
	s = truncate(s, w)
	return strings.Repeat(" ", max(0, w-width(s))) + s
}

// wrap word-wraps s to w cells, breaking long words when it must.
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	return strings.Split(ansi.Wrap(s, w, "-/"), "\n")
}

// Formatting.

// relTime is a compact age: "now", "12m", "3h", "2d", "5w", "4mo", "2y".
func relTime(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	day := 24 * time.Hour
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < day:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 14*day:
		return fmt.Sprintf("%dd", int(d/day))
	case d < 60*day:
		return fmt.Sprintf("%dw", int(d/(7*day)))
	case d < 365*day:
		return fmt.Sprintf("%dmo", int(d/(30*day)))
	default:
		return fmt.Sprintf("%dy", int(d/(365*day)))
	}
}

// ago says how long ago t was: "just now", "12m ago".
func ago(now, t time.Time) string {
	switch r := relTime(now, t); r {
	case "—":
		return r
	case "now":
		return "just now"
	default:
		return r + " ago"
	}
}

// since says how long a state has held: "<1m", "12m".
func since(now, t time.Time) string {
	if r := relTime(now, t); r != "now" {
		return r
	}
	return "<1m"
}

// plural picks the singular or plural word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// fmtTokens is a compact token count: "950", "9.5k", "124k", "1.24M".
func fmtTokens(n int64) string {
	f := float64(n)
	switch {
	case n < 1_000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", f/1e3)
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", f/1e3)
	case n < 10_000_000:
		return fmt.Sprintf("%.2fM", f/1e6)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", f/1e6)
	default:
		return fmt.Sprintf("%.2fB", f/1e9)
	}
}

// fmtCost shows a cost, marking estimates with "~" and unknowns with "—".
func fmtCost(kind core.CostKind, usd float64) string {
	var s string
	switch {
	case kind == core.CostNone:
		return "—"
	case usd < 0.01:
		s = "<$0.01"
	default:
		s = fmt.Sprintf("$%.2f", usd)
	}
	if kind == core.CostEstimated {
		s = "~" + s
	}
	return s
}

// shortModel turns "claude-opus-5-5" into "opus 5.5" and drops date
// suffixes. Ids it does not recognise come back unchanged.
func shortModel(id string) string {
	s, ok := strings.CutPrefix(id, "claude-")
	if !ok {
		return id
	}
	if i := strings.LastIndexByte(s, '-'); i >= 0 && len(s)-i-1 == 8 && isDigits(s[i+1:]) {
		s = s[:i]
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '-' && unicode.IsDigit(rune(s[i+1])) {
			return s[:i] + " " + strings.ReplaceAll(s[i+1:], "-", ".")
		}
	}
	return s
}

func isDigits(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}

// projectName is the last element of a project directory.
func projectName(dir string) string {
	if dir == "" {
		return "—"
	}
	return filepath.Base(dir)
}

// Status, gauge and badge widgets.

func statusGlyph(s core.Status) string {
	switch s {
	case core.StatusBusy:
		return "●"
	case core.StatusIdle:
		return "○"
	case core.StatusWaiting:
		return "◆"
	case core.StatusEnded:
		return "·"
	default:
		return "?"
	}
}

func statusWord(s core.Status) string {
	switch s {
	case core.StatusBusy, core.StatusIdle, core.StatusWaiting, core.StatusEnded:
		return string(s)
	default:
		return "unknown"
	}
}

// mark is the status glyph alone, coloured. Each status has its own shape,
// so colour is never the only signal.
func (st styles) mark(s core.Status) string {
	return st.statusStyle(s).Render(statusGlyph(s))
}

// badge is the status glyph and word, coloured.
func (st styles) badge(s core.Status) string {
	return st.statusStyle(s).Render(statusGlyph(s) + " " + statusWord(s))
}

// gaugeStyle picks the gauge colour for a fill percentage.
func (st styles) gaugeStyle(pct float64) lipgloss.Style {
	switch {
	case pct >= 85:
		return st.gauges[2]
	case pct >= 60:
		return st.gauges[1]
	default:
		return st.gauges[0]
	}
}

// gauge draws how full a session's context window is in exactly w cells:
// a bar and a percentage. With an unknown window it shows the token count
// alone, and "—" when there is nothing to show.
func (st styles) gauge(s core.Session, w int) string {
	pct, ok := s.ContextPercent()
	if !ok {
		label := "—"
		if s.ContextTokens > 0 {
			label = fmtTokens(s.ContextTokens)
		}
		return st.muted.Render(fitRight(label, w))
	}
	gs := st.gaugeStyle(pct)
	label := fmt.Sprintf("%.0f%%", math.Floor(pct))
	bar := w - 5
	if bar < 2 {
		return gs.Render(fitRight(label, w))
	}
	filled := int(math.Round(pct / 100 * float64(bar)))
	if filled == 0 && pct > 0 {
		filled = 1
	}
	return gs.Render(strings.Repeat("━", filled)) +
		st.faint.Render(strings.Repeat("─", bar-filled)) +
		gs.Render(fitRight(label, 5))
}

// gaugeInline is the gauge for running text: a short bar and percentage,
// or the bare token count when the window is unknown.
func (st styles) gaugeInline(s core.Session) string {
	if _, ok := s.ContextPercent(); ok {
		return st.gauge(s, 9)
	}
	if s.ContextTokens > 0 {
		return st.muted.Render(fmtTokens(s.ContextTokens) + " tokens")
	}
	return st.muted.Render("—")
}

// on returns a copy of the styles with background bg on every text style,
// for rendering a highlighted row segment by segment.
func (st styles) on(bg color.Color) styles {
	b := func(s lipgloss.Style) lipgloss.Style { return s.Background(bg) }
	st.text, st.bold, st.muted, st.faint = b(st.text), b(st.bold), b(st.muted), b(st.faint)
	st.accent, st.err, st.marker = b(st.accent), b(st.err), b(st.marker)
	status := make(map[core.Status]lipgloss.Style, len(st.status))
	for k, v := range st.status {
		status[k] = b(v)
	}
	st.status = status
	for i := range st.gauges {
		st.gauges[i] = b(st.gauges[i])
	}
	return st
}

// burnBins sums sample tokens into n equal bins covering the hour before
// now, oldest first. Samples outside the hour are ignored.
func burnBins(samples []core.Sample, now time.Time, n int) []int64 {
	bins := make([]int64, max(n, 0))
	if n <= 0 {
		return bins
	}
	start := now.Add(-time.Hour)
	for _, s := range samples {
		if s.Time.Before(start) || s.Time.After(now) {
			continue
		}
		i := min(int(s.Time.Sub(start)*time.Duration(n)/time.Hour), n-1)
		bins[i] += s.Tokens
	}
	return bins
}

var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// sparkline draws bins scaled to top: empty bins are blank, any activity
// shows at least the lowest bar.
func sparkline(bins []int64, top int64) string {
	var b strings.Builder
	for _, v := range bins {
		switch {
		case v <= 0 || top <= 0:
			b.WriteByte(' ')
		default:
			i := int(float64(v) / float64(top) * float64(len(sparkLevels)-1))
			b.WriteRune(sparkLevels[min(max(i, 0), len(sparkLevels)-1)])
		}
	}
	return b.String()
}
