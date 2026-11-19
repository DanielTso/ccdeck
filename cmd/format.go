package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/DanielTso/ccdeck/internal/core"
)

// clean makes transcript text safe to print on one line: escape sequences
// and control characters are removed and whitespace runs collapse.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
	return strings.Join(strings.Fields(s), " ")
}

// clip truncates s to w terminal cells, by display width.
func clip(s string, w int) string { return ansi.Truncate(s, w, "…") }

// shortID is the abbreviation commands print; show accepts any unique
// prefix. Ids come from file names, so they are cleaned too.
func shortID(id string) string {
	if len(id) > 8 {
		id = id[:8]
	}
	return clean(id)
}

// cleanPath is a path from a transcript, abbreviated and cleaned.
func cleanPath(p string) string { return clean(tildePath(p)) }

// tokens formats a token count compactly: 950, 12k, 3.4M.
func tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 100_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
	}
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// cost formats a session's cost, marking estimates with a tilde.
func cost(usd float64, kind core.CostKind) string {
	switch kind {
	case core.CostReported:
		return fmt.Sprintf("$%.2f", usd)
	case core.CostEstimated, core.CostClaude:
		return fmt.Sprintf("~$%.2f", usd)
	}
	return "-"
}

// costLong is cost with the kind spelled out.
func costLong(usd float64, kind core.CostKind) string {
	switch kind {
	case core.CostReported:
		return cost(usd, kind) + " reported"
	case core.CostEstimated:
		return cost(usd, kind) + " estimate"
	case core.CostClaude:
		return cost(usd, kind) + " estimate by Claude Code"
	}
	return "unknown (no price for the model)"
}

// contextFill formats how full the context window is.
func contextFill(s core.Session) string {
	if p, ok := s.ContextPercent(); ok {
		return fmt.Sprintf("%.0f%%", p)
	}
	if s.ContextTokens > 0 {
		return tokens(s.ContextTokens)
	}
	return "-"
}

// age is how long ago t was, compactly.
func age(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// status is the session status, with what a waiting session waits for.
func status(s core.Session) string {
	if s.Live != nil && s.Live.Status == core.StatusWaiting && s.Live.WaitingFor != "" {
		return "waiting: " + clean(s.Live.WaitingFor)
	}
	return string(s.Status())
}

// tildePath abbreviates the home directory to ~.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return p
}

// orDash is s cleaned for printing, or a dash when it is empty.
func orDash(s string) string {
	if s = clean(s); s == "" {
		return "-"
	}
	return s
}

// meterSummary is a status-line report in one line: what Claude Code said
// about the session's context and cost, and when.
func meterSummary(m core.Meter, now time.Time) string {
	var parts []string
	if m.HasContext {
		p := fmt.Sprintf("context %.0f%%", m.ContextPercent)
		if m.ContextWindow > 0 {
			p += " of " + tokens(m.ContextWindow)
		}
		parts = append(parts, p)
	}
	parts = append(parts, fmt.Sprintf("~$%.2f", m.CostUSD), fmt.Sprintf("+%d -%d lines", m.LinesAdded, m.LinesRemoved))
	return "Claude Code reported " + strings.Join(parts, ", ") + " " + ago(m.At, now)
}

// ago is age in a sentence: "just now", "5m ago".
func ago(t, now time.Time) string {
	switch a := age(t, now); a {
	case "now":
		return "just now"
	case "-":
		return a
	default:
		return a + " ago"
	}
}
