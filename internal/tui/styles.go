package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/DanielTso/ccdeck/internal/core"
)

// styles is every style the UI uses, derived from one theme. View code
// takes styles from here and never builds colours of its own.
type styles struct {
	theme theme

	text   lipgloss.Style
	bold   lipgloss.Style // emphasised text: titles, group headers
	muted  lipgloss.Style // secondary text and labels
	faint  lipgloss.Style // rules, separators, empty gauge cells
	accent lipgloss.Style
	err    lipgloss.Style

	brand     lipgloss.Style // the app name in the header
	tab       lipgloss.Style
	tabActive lipgloss.Style
	key       lipgloss.Style // a key in hints and help
	marker    lipgloss.Style // the bar beside the selected row
	selected  color.Color    // background of the selected row

	status map[core.Status]lipgloss.Style
	gauges [3]lipgloss.Style // low, mid, high

	overlay lipgloss.Style // the help box
}

func newStyles(t theme) styles {
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	s := styles{
		theme:     t,
		text:      fg(t.text),
		bold:      fg(t.text).Bold(true),
		muted:     fg(t.muted),
		faint:     fg(t.border),
		accent:    fg(t.accent),
		err:       fg(t.err).Bold(true),
		brand:     fg(t.accent).Bold(true),
		tab:       fg(t.muted),
		tabActive: fg(t.text).Bold(true).Underline(true),
		key:       fg(t.accent),
		marker:    fg(t.accent).Bold(true),
		selected:  t.surface,
		status: map[core.Status]lipgloss.Style{
			core.StatusBusy:    fg(t.busy),
			core.StatusIdle:    fg(t.idle),
			core.StatusWaiting: fg(t.waiting).Bold(true),
			core.StatusEnded:   fg(t.ended),
			core.StatusUnknown: fg(t.unknown),
		},
		gauges: [3]lipgloss.Style{fg(t.gaugeLow), fg(t.gaugeMid), fg(t.gaugeHigh)},
		overlay: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.borderFocused).
			Padding(0, 2),
	}
	return s
}

// statusStyle returns the style for a status, falling back to unknown.
func (s styles) statusStyle(st core.Status) lipgloss.Style {
	if v, ok := s.status[st]; ok {
		return v
	}
	return s.status[core.StatusUnknown]
}
