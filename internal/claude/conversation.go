package claude

import (
	"bytes"
	"fmt"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Conversation collects the displayable entries of a transcript's main
// thread: prompts and assistant text verbatim, tool calls as one-line
// summaries, tool results truncated. It implements Sink, so a Tailer can
// extend it as the file grows. A Conversation is not safe for concurrent
// use.
type Conversation struct {
	entries       []core.Entry
	threadKnown   bool
	sidechainFile bool
}

// Line adds the entries of one transcript line. It implements Sink.
func (c *Conversation) Line(_ int64, line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	l, ok := decodeLine(line)
	if !ok {
		return
	}
	switch l.Type {
	case "user", "assistant", "system":
	default:
		return
	}
	if !c.threadKnown {
		c.threadKnown, c.sidechainFile = true, l.IsSidechain
	}
	if l.IsSidechain != c.sidechainFile {
		return
	}
	c.entries = appendEntries(c.entries, &l)
}

// Reset drops every entry. It implements Sink.
func (c *Conversation) Reset() { *c = Conversation{} }

// Len is the number of entries collected.
func (c *Conversation) Len() int { return len(c.entries) }

// Entries returns a copy of the last limit entries, oldest first; limit <= 0
// means all.
func (c *Conversation) Entries(limit int) []core.Entry {
	es := c.entries
	if limit > 0 && len(es) > limit {
		es = es[len(es)-limit:]
	}
	return append([]core.Entry(nil), es...)
}

// appendEntries appends the entries one conversation line produces.
func appendEntries(dst []core.Entry, l *wireLine) []core.Entry {
	t := l.time()
	switch l.Type {
	case "user":
		kind, text, results := classifyUser(l)
		switch kind {
		case userPrompt:
			dst = append(dst, core.Entry{Kind: core.EntryPrompt, Time: t, Text: text})
		case userCommand:
			dst = append(dst, core.Entry{Kind: core.EntryCommand, Time: t, Text: text})
		case userToolResult:
			for _, b := range results {
				dst = append(dst, core.Entry{
					Kind:    core.EntryToolResult,
					Time:    t,
					Text:    resultText(b.Content),
					ToolID:  b.ToolUseID,
					IsError: b.IsError,
				})
			}
		}
	case "assistant":
		m := l.message()
		s, isString, bs := blocks(m.Content)
		if isString {
			bs = []wireBlock{{Type: "text", Text: s}}
		}
		for _, b := range bs {
			e := core.Entry{Time: t, Model: m.Model}
			switch b.Type {
			case "text":
				e.Kind, e.Text = core.EntryText, b.Text
			case "thinking", "redacted_thinking":
				e.Kind, e.Text = core.EntryThinking, b.Thinking
			case "tool_use":
				e.Kind, e.Tool, e.ToolID = core.EntryToolUse, b.Name, b.ID
				e.Text = toolSummary(b.Name, b.Input)
			default:
				continue
			}
			dst = append(dst, e)
		}
	case "system":
		if l.Subtype == "compact_boundary" {
			text := "conversation compacted"
			if cm := l.CompactMetadata; cm != nil && cm.PreTokens > 0 {
				text = fmt.Sprintf("conversation compacted from %d tokens", cm.PreTokens)
			}
			dst = append(dst, core.Entry{Kind: core.EntryCompact, Time: t, Text: text})
		}
	}
	return dst
}
