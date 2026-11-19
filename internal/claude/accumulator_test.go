package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestAccumulatorMainTranscript(t *testing.T) {
	acc := fold(t, transcriptOne())
	st := acc.State()

	wantUsage := map[string]core.Usage{
		// msg_01 counted once despite three lines; msg_02 without the
		// cache_creation object counts all writes as 5-minute.
		"claude-test-1": {Input: 15, Output: 55, Thinking: 25, CacheRead: 300, CacheWrite5m: 90, CacheWrite1h: 30},
		"claude-test-2": {Input: 3, Output: 7, CacheRead: 300, WebSearches: 2},
	}
	if !reflect.DeepEqual(st.Usage, wantUsage) {
		t.Errorf("usage = %+v\nwant    %+v", st.Usage, wantUsage)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"lines", st.Lines, 23},
		{"skipped", st.Skipped, 1},
		{"turns", st.Turns, 2},
		{"messages", st.Messages, 3},
		{"tool calls", st.ToolCalls, 2},
		{"context", st.ContextTokens, int64(303)},
		{"model", st.Model, "claude-test-2"},
		{"project dir (first cwd)", st.ProjectDir, "/home/user/demo"},
		{"branch (last)", st.GitBranch, "feature/widgets"},
		{"version (last)", st.Version, "2.1.301"},
		// Plumbing lines carry later timestamps; they must not count.
		{"started", st.StartedAt, at(t, "2026-01-10T10:00:00Z")},
		{"last activity", st.LastActivity, at(t, "2026-01-10T10:02:20Z")},
		{"sidechain file", st.SidechainFile, false},
		{"first prompt", acc.text.firstPrompt, "Add a parser for the widget format"},
		{"last prompt", acc.text.lastPrompt, "Now handle nested widgets"},
		{"ai title (last wins)", acc.text.aiTitle, "Nested widget parser"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	wantRecent := []core.Sample{
		{Time: at(t, "2026-01-10T10:00:05Z"), Tokens: 100},
		{Time: at(t, "2026-01-10T10:00:12Z"), Tokens: 90},
		{Time: at(t, "2026-01-10T10:02:10Z"), Tokens: 10},
	}
	if !reflect.DeepEqual(st.Recent, wantRecent) {
		t.Errorf("recent = %+v, want %+v", st.Recent, wantRecent)
	}
}

func TestUsageDecoding(t *testing.T) {
	tests := []struct {
		name string
		json string
		want core.Usage
	}{
		{
			name: "breakdown present",
			json: `{"input_tokens":1,"output_tokens":9,"cache_read_input_tokens":3,"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30},"output_tokens_details":{"thinking_tokens":4},"server_tool_use":{"web_search_requests":2}}`,
			want: core.Usage{Input: 1, Output: 9, Thinking: 4, CacheRead: 3, CacheWrite5m: 20, CacheWrite1h: 30, WebSearches: 2},
		},
		{
			name: "breakdown absent: all writes are 5m",
			json: `{"input_tokens":1,"cache_creation_input_tokens":70}`,
			want: core.Usage{Input: 1, CacheWrite5m: 70},
		},
		{
			name: "breakdown without total",
			json: `{"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":6}}`,
			want: core.Usage{CacheWrite5m: 5, CacheWrite1h: 6},
		},
		{
			name: "breakdown short of total: the rest is 5m",
			json: `{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40}}`,
			want: core.Usage{CacheWrite5m: 60, CacheWrite1h: 40},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w wireUsage
			if err := json.Unmarshal([]byte(tt.json), &w); err != nil {
				t.Fatal(err)
			}
			if got := w.usage(); got != tt.want {
				t.Errorf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// assistantLine is a main-thread assistant line with the given message id,
// model, output tokens and content block type.
func assistantLine(id, model string, output int, block string) string {
	content := map[string]string{
		"thinking": `{"type":"thinking","thinking":"hm"}`,
		"text":     `{"type":"text","text":"done"}`,
		"tool_use": `{"type":"tool_use","id":"t-` + id + `","name":"Bash","input":{"command":"ls"}}`,
	}[block]
	return line(`"type":"assistant"`, `"timestamp":"2026-01-10T10:00:00.000Z"`, `"isSidechain":false`,
		`"message":{"id":"`+id+`","model":"`+model+`","content":[`+content+`],"usage":{"input_tokens":10,"output_tokens":`+itoa(output)+`}}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestUsageDedup(t *testing.T) {
	tests := []struct {
		name         string
		lines        []string
		wantOutput   int64
		wantMessages int
		wantTools    int
	}{
		{
			name: "three lines of one message count once",
			lines: []string{
				assistantLine("m1", "claude-test-1", 40, "thinking"),
				assistantLine("m1", "claude-test-1", 40, "text"),
				assistantLine("m1", "claude-test-1", 40, "tool_use"),
			},
			wantOutput: 40, wantMessages: 1, wantTools: 1,
		},
		{
			name: "a later line's usage replaces an earlier partial one",
			lines: []string{
				assistantLine("m1", "claude-test-1", 5, "thinking"),
				assistantLine("m1", "claude-test-1", 40, "tool_use"),
			},
			wantOutput: 40, wantMessages: 1, wantTools: 1,
		},
		{
			name: "a tool result between lines of one message",
			lines: []string{
				assistantLine("m1", "claude-test-1", 40, "tool_use"),
				line(`"type":"user"`, `"message":{"content":[{"type":"tool_result","tool_use_id":"t-m1","content":"x"}]}`),
				assistantLine("m1", "claude-test-1", 40, "tool_use"),
			},
			wantOutput: 40, wantMessages: 1, wantTools: 2,
		},
		{
			name: "distinct messages add up",
			lines: []string{
				assistantLine("m1", "claude-test-1", 40, "text"),
				assistantLine("m2", "claude-test-1", 2, "text"),
			},
			wantOutput: 42, wantMessages: 2,
		},
		{
			name: "lines without a message id cannot be deduplicated",
			lines: []string{
				assistantLine("", "claude-test-1", 3, "text"),
				assistantLine("", "claude-test-1", 3, "text"),
			},
			wantOutput: 6, wantMessages: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := foldLines(tt.lines...).State()
			if got := st.Usage["claude-test-1"].Output; got != tt.wantOutput {
				t.Errorf("output = %d, want %d", got, tt.wantOutput)
			}
			if st.Messages != tt.wantMessages || st.ToolCalls != tt.wantTools {
				t.Errorf("messages, tools = %d, %d; want %d, %d", st.Messages, st.ToolCalls, tt.wantMessages, tt.wantTools)
			}
		})
	}
}

func TestAccumulatorTolerance(t *testing.T) {
	acc := foldLines(
		`{"type":"user","message":{"content":"first"},"cwd":"/a"}`,
		`not json`,
		`[1,2,3]`,
		``,
		`{"type":"user","gitBranch":5,"cwd":"/b","message":{"content":"second"}}`, // wrong field type: keep the rest
		`{"type":"assistant","message":"surprise"}`,
		`{"type":"assistant","message":{"id":"m","content":"plain string","usage":{"output_tokens":"many"}}}`,
		`{"type":"mystery","message":{"id":"x","usage":{"output_tokens":1000}}}`,
		`{"type":"api-request-blob","message":{"id":"y","usage":{"output_tokens":1000}}}`,
	)
	st := acc.State()
	if st.Lines != 8 || st.Skipped != 2 {
		t.Errorf("lines, skipped = %d, %d; want 8, 2", st.Lines, st.Skipped)
	}
	if st.Turns != 2 || st.ProjectDir != "/a" {
		t.Errorf("turns = %d, project = %q", st.Turns, st.ProjectDir)
	}
	if st.Usage["unknown"].Output != 0 || len(st.Usage) != 0 {
		t.Errorf("usage from ignored lines: %+v", st.Usage)
	}
}

func TestOldInlineSidechainLines(t *testing.T) {
	// Old transcripts interleaved subagent lines into the main file: their
	// usage counts, nothing else does.
	side := line(`"type":"assistant"`, `"isSidechain":true`, `"cwd":"/side"`,
		`"message":{"id":"s1","model":"claude-side","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}],"usage":{"input_tokens":500,"cache_read_input_tokens":9000}}`)
	acc := foldLines(
		line(`"type":"user"`, `"isSidechain":false`, `"cwd":"/main"`, `"message":{"content":"go"}`),
		assistantLine("m1", "claude-test-1", 4, "text"),
		line(`"type":"user"`, `"isSidechain":true`, `"message":{"content":"sub task"}`),
		side,
	)
	st := acc.State()
	if st.Usage["claude-side"].Input != 500 {
		t.Errorf("sidechain usage not counted: %+v", st.Usage)
	}
	if st.Turns != 1 || st.Messages != 1 || st.ToolCalls != 0 || st.Model != "claude-test-1" || st.ContextTokens != 10 || st.ProjectDir != "/main" {
		t.Errorf("sidechain lines leaked into main thread: %+v", st)
	}
}

func TestSubagentTranscriptIsItsOwnMainThread(t *testing.T) {
	subs, err := SubagentFiles(transcriptOne())
	if err != nil {
		t.Fatal(err)
	}
	for _, sf := range subs {
		if sf.ID != "abc123" {
			continue
		}
		st := fold(t, sf.Path).State()
		if !st.SidechainFile || st.Model != "claude-test-3" || st.Messages != 2 || st.ToolCalls != 1 || st.Turns != 1 {
			t.Errorf("subagent state: %+v", st)
		}
		if got := st.Usage["claude-test-3"]; got != (core.Usage{Input: 1050, Output: 105}) {
			t.Errorf("subagent usage = %+v", got)
		}
		return
	}
	t.Fatal("subagent abc123 not found")
}

func TestCostStateStaleAfterResume(t *testing.T) {
	cost := `{"type":"cost-state","totalCostUSD":2.5}`
	if st := foldLines(assistantLine("m1", "claude-test-1", 1, "text"), cost).State(); st.ReportedCost != 2.5 || st.ReportedStale {
		t.Errorf("ended session: %+v", st)
	}
	if st := foldLines(assistantLine("m1", "claude-test-1", 1, "text"), cost, assistantLine("m2", "claude-test-1", 1, "text")).State(); !st.ReportedStale {
		t.Errorf("resumed session should mark reported cost stale: %+v", st)
	}
}

// TestResumeMatchesFullParse cuts the fixture at every line boundary, caches
// the state through JSON, restores it and reads the rest. The result must be
// identical to one uninterrupted parse, including cuts inside the three-line
// message.
func TestResumeMatchesFullParse(t *testing.T) {
	for _, path := range []string{transcriptOne(), transcriptTwo()} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		full := fold(t, path)
		in := SessionInput{ID: "x", Now: at(t, "2026-01-10T10:30:00Z")}
		want := full.Session(in)

		for cut := 0; cut <= len(data); cut++ {
			if cut > 0 && data[cut-1] != '\n' {
				continue
			}
			head := writeFile(t, "head.jsonl", string(data[:cut]))
			first := fold(t, head)
			encoded, err := json.Marshal(first.State())
			if err != nil {
				t.Fatal(err)
			}
			var st State
			if err := json.Unmarshal(encoded, &st); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(head, data, 0o600); err != nil { // the file grows
				t.Fatal(err)
			}
			f, err := os.Open(head)
			if err != nil {
				t.Fatal(err)
			}
			acc, err := RestoreAccumulator(st, f)
			_ = f.Close()
			if err != nil {
				t.Fatalf("cut %d: restore: %v", cut, err)
			}
			if _, err := NewTailer(head, st.Offset).Read(acc); err != nil {
				t.Fatal(err)
			}
			if got := acc.Session(in); !reflect.DeepEqual(got, want) {
				t.Fatalf("cut %d of %s: resumed session differs\n got %+v\nwant %+v", cut, path, got, want)
			}
			if !reflect.DeepEqual(acc.State(), full.State()) {
				t.Fatalf("cut %d: resumed state differs", cut)
			}
		}
	}
}

func TestStateHoldsNoConversationText(t *testing.T) {
	encoded, err := json.Marshal(fold(t, transcriptOne()).State())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"widget format", "nested widgets", "Nested widget parser", "run the tests", "package widget"} {
		if bytes.Contains(encoded, []byte(text)) {
			t.Errorf("state contains conversation text %q", text)
		}
	}
}

func TestRestoreRejectsStaleState(t *testing.T) {
	data, err := os.ReadFile(transcriptOne())
	if err != nil {
		t.Fatal(err)
	}
	st := fold(t, transcriptOne()).State()

	// Overwrite the ai-title line in place, keeping its length.
	changed := bytes.Replace(data, []byte(`"type":"ai-title","aiTitle":"Nested widget parser"`), []byte(`"type":"ai-titlX","aiTitle":"Nested widget parser"`), 1)
	if bytes.Equal(changed, data) {
		t.Fatal("fixture changed: ai-title line not found")
	}
	tests := []struct {
		name string
		st   State
		file []byte
	}{
		{"rewritten line", st, changed},
		{"truncated file", st, data[:st.Text.AITitle+5]},
		{"other version", func() State { s := st; s.V = StateVersion + 1; return s }(), data},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RestoreAccumulator(tt.st, bytes.NewReader(tt.file))
			if !errors.Is(err, ErrStaleState) {
				t.Errorf("err = %v, want ErrStaleState", err)
			}
		})
	}
}

func TestRecentIsBounded(t *testing.T) {
	var lines []string
	for i, ts := range []string{"08:00:00", "08:30:00", "09:10:00", "09:20:00"} {
		lines = append(lines, line(`"type":"assistant"`, `"timestamp":"2026-01-10T`+ts+`.000Z"`,
			`"message":{"id":"m`+itoa(i)+`","model":"claude-test-1","content":[],"usage":{"output_tokens":1}}`))
	}
	acc := foldLines(lines...)
	if got := len(acc.State().Recent); got != 3 {
		t.Errorf("state keeps %d samples, want the 3 within an hour of the last", got)
	}
	s := acc.Session(SessionInput{Now: at(t, "2026-01-10T10:15:00Z")})
	if len(s.Recent) != 1 || !s.Recent[0].Time.Equal(at(t, "2026-01-10T09:20:00Z")) {
		t.Errorf("session recent = %+v, want only the 09:20 sample", s.Recent)
	}
}

func TestOnUsageAddsUpToState(t *testing.T) {
	for _, path := range []string{transcriptOne(), transcriptTwo()} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			acc := NewAccumulator()
			got := map[string]core.Usage{}
			calls := 0
			acc.OnUsage(func(at time.Time, model string, u core.Usage) {
				calls++
				if at.IsZero() {
					t.Errorf("usage of %s reported with no time", model)
				}
				got[model] = got[model].Add(u)
			})
			if _, err := NewTailer(path, 0).Read(acc); err != nil {
				t.Fatal(err)
			}
			if want := acc.State().Usage; calls == 0 || !reflect.DeepEqual(got, want) {
				t.Errorf("hook totals (%d calls) = %v, want %v", calls, got, want)
			}
		})
	}
}

func TestOnUsageSplitMessage(t *testing.T) {
	acc := NewAccumulator()
	var outputs []int64
	acc.OnUsage(func(at time.Time, _ string, u core.Usage) {
		if want := time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC); !at.Equal(want) {
			t.Errorf("at = %v, want %v", at, want)
		}
		outputs = append(outputs, u.Output)
	})
	acc.Line(0, []byte(assistantLine("m1", "claude-test-1", 5, "thinking")+"\n"))
	acc.Line(1000, []byte(assistantLine("m1", "claude-test-1", 40, "text")+"\n"))
	acc.Line(2000, []byte(assistantLine("m1", "claude-test-1", 40, "tool_use")+"\n"))
	acc.Reset()
	acc.Line(0, []byte(assistantLine("m2", "claude-test-1", 2, "text")+"\n"))
	if want := []int64{5, 35, 2}; !slices.Equal(outputs, want) {
		t.Errorf("output increments = %v, want %v (the hook survives Reset)", outputs, want)
	}
}

// ContextAt follows the latest main-thread assistant line: later lines of
// the same message move it, sidechain lines, user lines and lines without a
// timestamp do not, and it survives the cache.
func TestContextAt(t *testing.T) {
	asst := func(ts, id string, side bool) string {
		return line(`"type":"assistant"`, `"timestamp":"`+ts+`"`, `"isSidechain":`+map[bool]string{true: "true", false: "false"}[side],
			`"message":{"id":"`+id+`","model":"claude-test-1","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":10}}`)
	}
	user := line(`"type":"user"`, `"timestamp":"2026-01-10T10:09:00.000Z"`, `"isSidechain":false`, `"message":{"content":"go on"}`)
	noTime := line(`"type":"assistant"`, `"isSidechain":false`,
		`"message":{"id":"m9","model":"claude-test-1","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":10}}`)

	if got := NewAccumulator().ContextAt(); !got.IsZero() {
		t.Errorf("empty accumulator: ContextAt = %v, want zero", got)
	}
	acc := foldLines(
		asst("2026-01-10T10:00:00.000Z", "m1", false),
		asst("2026-01-10T10:00:02.500Z", "m1", false), // second line of m1
		asst("2026-01-10T10:05:00.000Z", "s1", true),  // sidechain
		user,
		noTime,
	)
	want := at(t, "2026-01-10T10:00:02.500Z")
	if got := acc.ContextAt(); !got.Equal(want) {
		t.Errorf("ContextAt = %v, want %v", got, want)
	}
	data, err := json.Marshal(acc.State())
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if !st.ContextAt.Equal(want) {
		t.Errorf("ContextAt after a JSON round trip = %v, want %v", st.ContextAt, want)
	}
}

// After a compaction the context of the last reply is gone, and Claude Code
// knows the new one only from the next API response: seen with Claude Code
// 2.1.328, a 63.7k context became 64.4k at the next reply, while the
// boundary's postTokens said 20.9k. So from a main-thread compact_boundary
// until the next reply the context is unknown (0), and ContextAt is the
// boundary, so that only a status-line report written after it counts.
func TestCompactionForgetsContext(t *testing.T) {
	asst := func(ts, id string, side bool, in int) string {
		return line(`"type":"assistant"`, `"timestamp":"`+ts+`"`, `"isSidechain":`+map[bool]string{true: "true", false: "false"}[side],
			`"message":{"id":"`+id+`","model":"claude-test-1","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":`+strconv.Itoa(in)+`}}`)
	}
	boundary := func(ts string, side bool) string {
		return line(`"type":"system"`, `"subtype":"compact_boundary"`, `"timestamp":"`+ts+`"`,
			`"isSidechain":`+map[bool]string{true: "true", false: "false"}[side],
			`"compactMetadata":{"trigger":"manual","preTokens":63670,"postTokens":20927}`)
	}
	reply := asst("2026-01-10T10:00:00.000Z", "m1", false, 63666)

	acc := foldLines(reply, boundary("2026-01-10T10:04:00.000Z", false))
	if st := acc.State(); st.ContextTokens != 0 {
		t.Errorf("after a compaction: context = %d, want 0 (unknown until the next reply)", st.ContextTokens)
	}
	if got, want := acc.ContextAt(), at(t, "2026-01-10T10:04:00.000Z"); !got.Equal(want) {
		t.Errorf("after a compaction: ContextAt = %v, want the boundary %v", got, want)
	}
	// An unknown context is shown as unknown, not as 0% of a known window.
	cat := Catalog{"claude-test-1": 1_000_000}
	if p, ok := acc.Session(SessionInput{ID: "s", Catalog: cat}).ContextPercent(); ok {
		t.Errorf("after a compaction: ContextPercent = %v, true; want unknown", p)
	}
	if p, ok := foldLines(reply).Session(SessionInput{ID: "s", Catalog: cat}).ContextPercent(); !ok || p == 0 {
		t.Errorf("before the compaction: ContextPercent = %v, %v; want the reply's", p, ok)
	}

	acc = foldLines(reply, boundary("2026-01-10T10:04:00.000Z", false), asst("2026-01-10T10:05:00.000Z", "m2", false, 64376))
	if st := acc.State(); st.ContextTokens != 64376 {
		t.Errorf("reply after a compaction: context = %d, want 64376", st.ContextTokens)
	}

	acc = foldLines(reply, boundary("2026-01-10T10:04:00.000Z", true))
	if st := acc.State(); st.ContextTokens != 63666 {
		t.Errorf("sidechain compaction: context = %d, want 63666 (the main thread's)", st.ContextTokens)
	}
}
