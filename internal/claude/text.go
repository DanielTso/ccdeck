package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Truncation limits for displayed text, in runes.
const (
	summaryRunes    = 200  // tool_use summaries and single-line prompts
	toolResultRunes = 2000 // tool_result bodies
)

// userKind classifies a user line.
type userKind int

const (
	userOther      userKind = iota // metadata, plumbing, interruption markers
	userPrompt                     // typed by the human
	userCommand                    // a slash command or a ! shell command
	userToolResult                 // tool results
)

// plumbingTags open user strings that Claude Code writes on the user's
// behalf: command output, notifications, injected reminders.
var plumbingTags = []string{
	"<local-command-stdout>", "<local-command-stderr>", "<local-command-caveat>",
	"<bash-stdout>", "<bash-stderr>", "<task-notification>", "<system-reminder>",
	"<user-prompt-submit-hook>", "<command-message>", "<command-stdout>",
}

// classifyUser decides what a user line is. text is the prompt or command
// text; results are the tool_result blocks.
func classifyUser(l *wireLine) (kind userKind, text string, results []wireBlock) {
	if l.IsMeta || l.IsCompactSummary {
		return userOther, "", nil
	}
	if l.Origin != nil && l.Origin.Kind != "" && l.Origin.Kind != "human" {
		return userOther, "", nil
	}
	s, isString, bs := blocks(l.message().Content)
	if isString {
		return classifyUserString(s)
	}
	var texts []string
	for _, b := range bs {
		switch b.Type {
		case "tool_result":
			results = append(results, b)
		case "text":
			texts = append(texts, b.Text)
		case "image":
			texts = append(texts, "[image]")
		}
	}
	if len(results) > 0 {
		return userToolResult, "", results
	}
	joined := strings.TrimSpace(strings.Join(texts, "\n"))
	if joined == "" || strings.HasPrefix(joined, "[Request interrupted") {
		return userOther, "", nil
	}
	return classifyUserString(joined)
}

func classifyUserString(s string) (userKind, string, []wireBlock) {
	trimmed := strings.TrimSpace(s)
	if name, ok := between(trimmed, "<command-name>", "</command-name>"); ok {
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		args, _ := between(trimmed, "<command-args>", "</command-args>")
		return userCommand, strings.TrimSpace(name + " " + strings.TrimSpace(args)), nil
	}
	if in, ok := between(trimmed, "<bash-input>", "</bash-input>"); ok && strings.HasPrefix(trimmed, "<bash-input>") {
		return userCommand, "!" + strings.TrimSpace(in), nil
	}
	for _, tag := range plumbingTags {
		if strings.HasPrefix(trimmed, tag) {
			return userOther, "", nil
		}
	}
	if trimmed == "" || strings.HasPrefix(trimmed, "[Request interrupted") {
		return userOther, "", nil
	}
	return userPrompt, s, nil
}

// between returns the text between the first open tag and the next close tag.
func between(s, open, closeTag string) (string, bool) {
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// oneLine collapses all whitespace runs to single spaces and truncates.
func oneLine(s string, maxRunes int) string {
	return truncate(strings.Join(strings.Fields(s), " "), maxRunes)
}

// truncate cuts s to at most maxRunes runes, ending in an ellipsis when cut.
func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes-1 {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// fileTools are the tools whose summary is the path they work on.
var fileTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "MultiEdit": true,
	"NotebookEdit": true, "NotebookRead": true,
}

// toolSummary is the one-line text shown for a tool_use: the command for
// Bash, the path for file tools, the description for Agent, otherwise the
// input as compact JSON.
func toolSummary(name string, input json.RawMessage) string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(input, &fields)
	str := func(keys ...string) string {
		for _, k := range keys {
			var s string
			if json.Unmarshal(fields[k], &s) == nil && s != "" {
				return s
			}
		}
		return ""
	}
	var s string
	switch {
	case name == "Bash":
		s = str("command")
	case name == "Agent" || name == "Task":
		s = str("description")
	case fileTools[name]:
		s = str("file_path", "notebook_path", "path")
	}
	if s == "" {
		var buf bytes.Buffer
		if err := json.Compact(&buf, input); err == nil {
			s = buf.String()
		} else {
			s = string(input)
		}
	}
	return oneLine(s, summaryRunes)
}

// resultText flattens tool_result content, a string or an array of blocks.
func resultText(content json.RawMessage) string {
	s, isString, bs := blocks(content)
	if isString {
		return truncate(s, toolResultRunes)
	}
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		} else if b.Type != "" {
			parts = append(parts, "["+b.Type+"]")
		}
	}
	return truncate(strings.Join(parts, "\n"), toolResultRunes)
}
