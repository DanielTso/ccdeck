package claude

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestConversationFromFixture(t *testing.T) {
	var c Conversation
	if _, err := NewTailer(transcriptOne(), 0).Read(&c); err != nil {
		t.Fatal(err)
	}
	ts := func(s string) string { return "2026-01-10T" + s + "Z" }
	want := []core.Entry{
		{Kind: core.EntryPrompt, Time: at(t, ts("10:00:00")), Text: "Add a parser for the widget format"},
		{Kind: core.EntryThinking, Time: at(t, ts("10:00:05")), Text: "Let me look at the format.", Model: "claude-test-1"},
		{Kind: core.EntryText, Time: at(t, ts("10:00:06")), Text: "I will run the tests first.", Model: "claude-test-1"},
		{Kind: core.EntryToolUse, Time: at(t, ts("10:00:07")), Text: "go test ./...", Tool: "Bash", ToolID: "toolu_01", Model: "claude-test-1"},
		{Kind: core.EntryToolResult, Time: at(t, ts("10:00:09")), Text: "ok  \texample.com/widget\t0.01s", ToolID: "toolu_01"},
		{Kind: core.EntryToolUse, Time: at(t, ts("10:00:12")), Text: "/home/user/demo/widget.go", Tool: "Read", ToolID: "toolu_02", Model: "claude-test-1"},
		{Kind: core.EntryToolResult, Time: at(t, ts("10:00:13")), Text: "package widget\n[image]", ToolID: "toolu_02", IsError: true},
		{Kind: core.EntryCommand, Time: at(t, ts("10:01:00")), Text: "/model claude-test-2"},
		{Kind: core.EntryPrompt, Time: at(t, ts("10:02:00")), Text: "Now handle\nnested widgets\n[image]"},
		{Kind: core.EntryText, Time: at(t, ts("10:02:10")), Text: "Nested widgets are handled.", Model: "claude-test-2"},
	}
	got := c.Entries(0)
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("entry %d = %+v\n want %+v", i, got[i], want[i])
		}
	}
	if last := c.Entries(2); len(last) != 2 || last[1].Text != "Nested widgets are handled." {
		t.Errorf("Entries(2) = %+v", last)
	}
	c.Reset()
	if c.Len() != 0 {
		t.Error("Reset kept entries")
	}
}

func TestConversationSkipsSidechainOfMainFile(t *testing.T) {
	var c Conversation
	for i, l := range []string{
		line(`"type":"user"`, `"isSidechain":false`, `"message":{"content":"main"}`),
		line(`"type":"user"`, `"isSidechain":true`, `"message":{"content":"side"}`),
		line(`"type":"system"`, `"subtype":"compact_boundary"`, `"isSidechain":false`, `"compactMetadata":{"preTokens":150000}`),
	} {
		c.Line(int64(i), []byte(l+"\n"))
	}
	got := c.Entries(0)
	if len(got) != 2 || got[0].Text != "main" || got[1].Kind != core.EntryCompact || !strings.Contains(got[1].Text, "150000") {
		t.Errorf("entries = %+v", got)
	}
}

func TestUserLineClassification(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wantKind userKind
		wantText string
	}{
		{"typed prompt", `{"message":{"content":"hello"}}`, userPrompt, "hello"},
		{"human origin", `{"origin":{"kind":"human"},"message":{"content":"hello"}}`, userPrompt, "hello"},
		{"task notification origin", `{"origin":{"kind":"task-notification"},"message":{"content":"<task-notification>x</task-notification>"}}`, userOther, ""},
		{"peer origin", `{"origin":{"kind":"peer"},"message":{"content":"from a peer"}}`, userOther, ""},
		{"meta", `{"isMeta":true,"message":{"content":"hello"}}`, userOther, ""},
		{"compact summary", `{"isCompactSummary":true,"message":{"content":"This session is being continued"}}`, userOther, ""},
		{"slash command", `{"message":{"content":"<command-name>/clear</command-name>\n<command-args></command-args>"}}`, userCommand, "/clear"},
		{"command without slash", `{"message":{"content":"<command-message>x</command-message><command-name>review</command-name><command-args>42</command-args>"}}`, userCommand, "/review 42"},
		{"shell escape", `{"message":{"content":"<bash-input>git status</bash-input>"}}`, userCommand, "!git status"},
		{"shell output", `{"message":{"content":"<bash-stdout>clean</bash-stdout><bash-stderr></bash-stderr>"}}`, userOther, ""},
		{"local command output", `{"message":{"content":"<local-command-stdout>ok</local-command-stdout>"}}`, userOther, ""},
		{"interrupted", `{"message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`, userOther, ""},
		{"text blocks with image", `{"message":{"content":[{"type":"text","text":"look"},{"type":"image"}]}}`, userPrompt, "look\n[image]"},
		{"tool results", `{"message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`, userToolResult, ""},
		{"empty", `{"message":{"content":"   "}}`, userOther, ""},
		{"no message", `{}`, userOther, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, ok := decodeLine([]byte(tt.json))
			if !ok {
				t.Fatal("fixture does not decode")
			}
			kind, text, _ := classifyUser(&l)
			if kind != tt.wantKind || text != tt.wantText {
				t.Errorf("classify = %v %q, want %v %q", kind, text, tt.wantKind, tt.wantText)
			}
		})
	}
}

func TestToolSummary(t *testing.T) {
	long := strings.Repeat("é", 300)
	tests := []struct {
		name, tool, input, want string
	}{
		{"bash command on one line", "Bash", `{"command":"cd x &&\n  make  test","description":"d"}`, "cd x && make test"},
		{"file path", "Edit", `{"file_path":"/a/b.go","old_string":"x","new_string":"y"}`, "/a/b.go"},
		{"notebook path", "NotebookEdit", `{"notebook_path":"/a/n.ipynb"}`, "/a/n.ipynb"},
		{"agent description", "Agent", `{"description":"Find callers","prompt":"long"}`, "Find callers"},
		{"other tool as compact json", "Grep", `{ "pattern" : "x",  "path":"/a" }`, `{"pattern":"x","path":"/a"}`},
		{"bash without command falls back to json", "Bash", `{}`, `{}`},
		{"truncated", "WebFetch", `{"url":"` + long + `"}`, truncate(`{"url":"`+long+`"}`, summaryRunes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolSummary(tt.tool, json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
			if n := utf8.RuneCountInString(got); n > summaryRunes {
				t.Errorf("summary has %d runes", n)
			}
		})
	}
}

func TestResultText(t *testing.T) {
	big := strings.Repeat("ab", 3000)
	tests := []struct {
		name, content, want string
	}{
		{"string", `"plain"`, "plain"},
		{"blocks", `[{"type":"text","text":"one"},{"type":"image"},{"type":"tool_reference","tool_name":"x"},{"type":"text","text":"two"}]`, "one\n[image]\n[tool_reference]\ntwo"},
		{"absent", ``, ""},
		{"truncated", `"` + big + `"`, big[:toolResultRunes-1] + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resultText(json.RawMessage(tt.content)); got != tt.want {
				t.Errorf("text = %q, want %q", truncate(got, 60), truncate(tt.want, 60))
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven runes", 10, "eleven ru…"},
		{"日本語のテキスト", 4, "日本語…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
