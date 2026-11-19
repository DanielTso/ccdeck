package tui

import (
	"strings"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Conversation rendering shared by the Sessions preview, the Live panes and
// the transcript.

// convoTail renders the end of a conversation in at most n lines of width
// w, newest at the bottom. err is why the entries could not be read.
func convoTail(st styles, entries []core.Entry, err error, w, n int) []string {
	if err != nil {
		return []string{st.err.Render(truncate("Cannot read the conversation: "+oneLine(err.Error()), w))}
	}
	if len(entries) == 0 {
		return []string{st.muted.Render("Nothing said yet.")}
	}
	// Render backwards from the newest entry and stop once n lines are
	// filled, so a long conversation costs only what is shown.
	var blocks [][]string
	total := 0
	for i := len(entries) - 1; i >= 0 && total < n; i-- {
		b := entryLines(st, entries[i], w)
		if entries[i].Kind == core.EntryPrompt && i > 0 {
			b = append([]string{""}, b...)
		}
		blocks = append(blocks, b)
		total += len(b)
	}
	lines := make([]string, 0, total)
	for i := len(blocks) - 1; i >= 0; i-- {
		lines = append(lines, blocks[i]...)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// entryLines renders one entry compactly: prompts and prose wrapped to a
// few lines, everything else on one line.
func entryLines(st styles, e core.Entry, w int) []string {
	switch e.Kind {
	case core.EntryPrompt:
		return textBlock(st.accent.Render("› "), st.bold.Render, e.Text, w, 3)
	case core.EntryText:
		return textBlock("  ", st.text.Render, e.Text, w, 4)
	}
	return []string{entryLine(st, e, w)}
}

// textBlock wraps text to w cells under a two-cell lead, keeping at most
// maxLines (0 means all) and marking a cut with an ellipsis.
func textBlock(lead string, style func(...string) string, text string, w, maxLines int) []string {
	ls := wrap(oneLine(text), w-2)
	if maxLines > 0 && len(ls) > maxLines {
		ls = ls[:maxLines]
		ls[maxLines-1] = truncate(ls[maxLines-1]+" …", w-2)
	}
	out := make([]string, len(ls))
	for i, l := range ls {
		p := "  "
		if i == 0 {
			p = lead
		}
		out[i] = p + style(l)
	}
	return out
}

// entryLine is the one-line form of an entry.
func entryLine(st styles, e core.Entry, w int) string {
	switch e.Kind {
	case core.EntryPrompt:
		return st.accent.Render("› ") + st.bold.Render(truncate(oneLine(e.Text), w-2))
	case core.EntryText:
		return "  " + st.text.Render(truncate(oneLine(e.Text), w-2))
	case core.EntryThinking:
		t := "thinking"
		if s := oneLine(e.Text); s != "" {
			t += ": " + s
		}
		return "  " + st.muted.Italic(true).Render(truncate("∴ "+t, w-2))
	case core.EntryToolUse:
		tool := toolName(e)
		return "  " + st.muted.Render("▸ ") + st.text.Render(tool) + " " +
			st.muted.Render(truncate(oneLine(e.Text), w-4-width(tool)-1))
	case core.EntryToolResult:
		return "    " + resultLine(st, e, w-4)
	case core.EntryCommand:
		return st.accent.Render("› ") + st.muted.Render(truncate(oneLine(e.Text), w-2))
	case core.EntryCompact:
		label := " " + oneLine(e.Text) + " "
		return "  " + st.faint.Render(truncate("──"+label+strings.Repeat("─", max(0, w-6-width(label))), w-2))
	}
	return ""
}

// resultLine is a tool result's first line, marked as a failure or not.
func resultLine(st styles, e core.Entry, w int) string {
	first, _, _ := strings.Cut(strings.TrimSpace(e.Text), "\n")
	if e.IsError {
		return st.err.Render(truncate("✗ "+oneLine(first), w))
	}
	return st.muted.Render(truncate("↳ "+oneLine(first), w))
}

func toolName(e core.Entry) string {
	if e.Tool == "" {
		return "tool"
	}
	return e.Tool
}
