package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Rate limits: the account's usage windows, from Claude Code's status-line
// meter. When the store has none, nothing about them is drawn.

// limitWarn is the use at which a window stands out: with "!" and the
// error style, so it never rests on colour alone.
const limitWarn = 80

// currentLimits are the windows that have not reset by now.
func (e *env) currentLimits() []core.RateLimit {
	now := e.now()
	var out []core.RateLimit
	for _, l := range e.limits {
		if l.ResetsAt.IsZero() || l.ResetsAt.After(now) {
			out = append(out, l)
		}
	}
	return out
}

// tightestLimit is the current window with the highest use, the earlier
// reset breaking ties.
func (e *env) tightestLimit() (core.RateLimit, bool) {
	var best core.RateLimit
	found := false
	for _, l := range e.currentLimits() {
		if !found || l.UsedPercent > best.UsedPercent ||
			l.UsedPercent == best.UsedPercent && !l.ResetsAt.IsZero() && (best.ResetsAt.IsZero() || l.ResetsAt.Before(best.ResetsAt)) {
			best, found = l, true
		}
	}
	return best, found
}

// limitPercent is a window's use as shown: whole percent, rounded down,
// with "!" at or above limitWarn.
func limitPercent(l core.RateLimit) string {
	p := math.Floor(l.UsedPercent)
	s := fmt.Sprintf("%.0f%%", p)
	if p >= limitWarn {
		s += "!"
	}
	return s
}

// limitStyle styles a window's figure: the error style at or above
// limitWarn, else the gauge colour for its fill.
func (st styles) limitStyle(l core.RateLimit) func(...string) string {
	if math.Floor(l.UsedPercent) >= limitWarn {
		return st.err.Render
	}
	return st.gaugeStyle(l.UsedPercent).Render
}

// limitGlyph is a one-cell gauge of a window's use.
func limitGlyph(pct float64) string {
	i := int(math.Round(min(max(pct, 0), 100) / 100 * float64(len(sparkLevels)-1)))
	return string(sparkLevels[i])
}

// untilReset is a compact time to t: "<1m", "45m", "2h10m", "3d4h".
func untilReset(now, t time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
}

// limitBadge is the top bar's element: the tightest window's name, a gauge
// glyph, its use and, when withReset, the time to its reset, such as
// "5h ▆ 84%! ↻1h12m". Empty when there are no limits.
func (m *Model) limitBadge(withReset bool) string {
	l, ok := m.env.tightestLimit()
	if !ok {
		return ""
	}
	st := m.env.st
	style := st.limitStyle(l)
	s := st.muted.Render(l.Window+" ") + style(limitGlyph(l.UsedPercent)+" "+limitPercent(l))
	if withReset && !l.ResetsAt.IsZero() {
		s += st.muted.Render(" ↻" + untilReset(m.env.now(), l.ResetsAt))
	}
	return s
}

// limitsLines is the Usage view's block: a title and a line per current
// window. Nil when there are none.
func (v *usageView) limitsLines() []string {
	limits := v.env.currentLimits()
	if len(limits) == 0 {
		return nil
	}
	st := v.env.st
	now := v.env.now()
	lines := []string{" " + st.bold.Render("Rate limits") + st.muted.Render(" · from Claude Code, "+ago(now, v.env.limitsAt))}
	bar := min(max(v.w/4, 10), 24)
	for _, l := range limits {
		style := st.limitStyle(l)
		filled := int(math.Round(min(l.UsedPercent, 100) / 100 * float64(bar)))
		if filled == 0 && l.UsedPercent > 0 {
			filled = 1
		}
		line := " " + st.text.Render(fit(limitName(l), 8)) + " " +
			style(strings.Repeat("━", filled)) + st.faint.Render(strings.Repeat("─", bar-filled)) +
			style(fitRight(limitPercent(l), 6))
		var detail []string
		if l.Window == "spend" && l.LimitUSD > 0 {
			d := fmt.Sprintf("$%.2f of $%.2f", l.UsedUSD, l.LimitUSD)
			if l.Period != "" {
				d += " " + l.Period
			}
			detail = append(detail, d)
		}
		if !l.ResetsAt.IsZero() {
			detail = append(detail, "resets in "+untilReset(now, l.ResetsAt)+" ("+resetClock(now, l.ResetsAt)+")")
		}
		if len(detail) > 0 {
			line += "  " + st.muted.Render(strings.Join(detail, " · "))
		}
		lines = append(lines, truncate(line, v.w))
	}
	return lines
}

// limitName names a window in the Usage view.
func limitName(l core.RateLimit) string {
	switch l.Window {
	case "5h":
		return "5 hours"
	case "7d":
		return "7 days"
	case "spend":
		return "spend"
	}
	return oneLine(l.Window)
}

// resetClock is a reset time on the local clock: "16:16" today, "Fri 19:00"
// within a week, "Mar 2" beyond.
func resetClock(now, t time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		return t.Format("15:04")
	case t.Sub(now) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	default:
		return t.Format("Jan 2")
	}
}

// meterContext reports whether a session's context figures are Claude
// Code's own, from its status-line report.
func meterContext(s core.Session) bool {
	m := s.Meter
	return m != nil && m.HasContext && m.ContextWindow > 0 && s.ContextWindow == m.ContextWindow &&
		(s.ContextTokens == m.ContextTokens || m.ContextTokens == 0)
}
