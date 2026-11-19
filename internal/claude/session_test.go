package claude

import (
	"math"
	"reflect"
	"testing"

	"github.com/DanielTso/ccdeck/internal/core"
)

// fixtureSession builds session one the way an index would: transcript,
// subagents with their meta files, registry, catalog and prices.
func fixtureSession(t *testing.T, now string) core.Session {
	t.Helper()
	p := fixturePaths(t)
	files, err := SubagentFiles(transcriptOne())
	if err != nil {
		t.Fatal(err)
	}
	var subs []SubagentInput
	for _, f := range files {
		meta, _ := ReadSubagentMeta(f.MetaPath) // the workflow agent has none
		subs = append(subs, SubagentInput{ID: f.ID, Meta: meta, Acc: fold(t, f.Path)})
	}
	regs, err := ReadRegistry(p.SessionsDir(), fakeProcs{101: "5000"})
	if err != nil || len(regs) != 1 {
		t.Fatalf("registry: %v %+v", err, regs)
	}
	cat, err := ReadCatalog(p.CatalogDir())
	if err != nil {
		t.Fatal(err)
	}
	prices, _, err := ReadPrices(p.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	return fold(t, transcriptOne()).Session(SessionInput{
		ID:           sessionOne,
		Path:         transcriptOne(),
		Registration: &regs[0],
		Subagents:    subs,
		Catalog:      cat,
		Prices:       prices,
		Now:          at(t, now),
	})
}

func TestSessionFromFixture(t *testing.T) {
	s := fixtureSession(t, "2026-01-10T10:30:00Z")

	wantUsage := map[string]core.Usage{
		"claude-test-1": {Input: 22, Output: 58, Thinking: 25, CacheRead: 300, CacheWrite5m: 90, CacheWrite1h: 30}, // main + workflow agent
		"claude-test-2": {Input: 3, Output: 7, CacheRead: 300, WebSearches: 2},
		"claude-test-3": {Input: 1050, Output: 105}, // subagent only
	}
	if !reflect.DeepEqual(s.Usage, wantUsage) {
		t.Errorf("usage = %+v\nwant    %+v", s.Usage, wantUsage)
	}
	wantSubs := []core.Subagent{
		{ID: "def456", Model: "claude-test-1", Usage: core.Usage{Input: 7, Output: 3}, LastActivity: at(t, "2026-01-10T10:03:05Z")},
		{ID: "abc123", Type: "Explore", Description: "Survey the widget package", Model: "claude-test-3",
			Usage: core.Usage{Input: 1050, Output: 105}, LastActivity: at(t, "2026-01-10T10:00:40Z")},
	}
	if !reflect.DeepEqual(s.Subagents, wantSubs) {
		t.Errorf("subagents = %+v\nwant        %+v", s.Subagents, wantSubs)
	}
	wantLive := &core.Live{
		PID: 101, Name: "demo-1", Kind: "interactive", Status: core.StatusWaiting,
		WaitingFor: "approve Bash: rm -rf build",
		StartedAt:  at(t, "2026-01-10T10:00:00Z"), StatusSince: at(t, "2026-01-10T10:03:20Z"),
	}
	if !reflect.DeepEqual(s.Live, wantLive) {
		t.Errorf("live = %+v, want %+v", s.Live, wantLive)
	}

	var wantCost float64
	prices, _, _ := ReadPrices(fixturePaths(t).ConfigFile)
	for m, u := range wantUsage {
		p, _ := prices.Lookup(m)
		wantCost += p.Cost(u)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"id", s.ID, sessionOne},
		{"title", s.Title, "Nested widget parser"},
		{"last prompt", s.LastPrompt, "Now handle nested widgets"},
		{"project", s.ProjectDir, "/home/user/demo"},
		{"model", s.Model, "claude-test-2"},
		{"context tokens (main thread only)", s.ContextTokens, int64(303)},
		{"context window", s.ContextWindow, int64(1000000)},
		{"cost kind", s.CostKind, core.CostEstimated},
		{"turns", s.Turns, 2},
		{"status", s.Status(), core.StatusWaiting},
		{"recent samples", len(s.Recent), 6},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if math.Abs(s.CostUSD-wantCost) > 1e-12 || wantCost == 0 {
		t.Errorf("cost = %v, want %v", s.CostUSD, wantCost)
	}
	for i := 1; i < len(s.Recent); i++ {
		if s.Recent[i].Time.Before(s.Recent[i-1].Time) {
			t.Fatalf("recent not oldest first: %+v", s.Recent)
		}
	}
	if pct, ok := s.ContextPercent(); !ok || pct <= 0 {
		t.Errorf("context percent = %v, %v", pct, ok)
	}
}

func TestSessionRecentFollowsClock(t *testing.T) {
	s := fixtureSession(t, "2026-01-10T11:01:00Z")
	want := []core.Sample{
		{Time: at(t, "2026-01-10T10:02:10Z"), Tokens: 10}, // main
		{Time: at(t, "2026-01-10T10:03:05Z"), Tokens: 10}, // workflow subagent
	}
	if !reflect.DeepEqual(s.Recent, want) {
		t.Errorf("recent = %+v, want %+v", s.Recent, want)
	}
}

func TestTitlePrecedence(t *testing.T) {
	prompt := line(`"type":"user"`, `"message":{"content":"  first\n prompt  "}`)
	ai := `{"type":"ai-title","aiTitle":"AI title"}`
	custom := `{"type":"custom-title","customTitle":"Custom title"}`
	reg := &Registration{Live: core.Live{Name: "registry-name"}}
	tests := []struct {
		name  string
		lines []string
		reg   *Registration
		want  string
	}{
		{"custom beats everything", []string{prompt, custom, ai}, reg, "Custom title"},
		{"ai beats registry name", []string{prompt, ai}, reg, "AI title"},
		{"registry name beats first prompt", []string{prompt}, reg, "registry-name"},
		{"first prompt, on one line", []string{prompt}, nil, "first prompt"},
		{"nothing", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := foldLines(tt.lines...).Session(SessionInput{Registration: tt.reg})
			if s.Title != tt.want {
				t.Errorf("title = %q, want %q", s.Title, tt.want)
			}
		})
	}
}

func TestLiveSessionWithoutTranscript(t *testing.T) {
	regs, err := ReadRegistry(fixturePaths(t).SessionsDir(), fakeProcs{404: "8000"})
	if err != nil || len(regs) != 1 {
		t.Fatalf("registry: %v %+v", err, regs)
	}
	s := NewAccumulator().Session(SessionInput{ID: regs[0].SessionID, Registration: &regs[0]})
	if s.ID != "44444444-4444-4444-8444-444444444444" || s.Title != "fresh-1" || s.ProjectDir != "/home/user/fresh" ||
		s.Status() != core.StatusUnknown || s.Version != "2.1.301" || s.CostKind != core.CostNone {
		t.Errorf("session = %+v", s)
	}
	if !s.StartedAt.Equal(at(t, "2026-01-10T10:13:20Z")) || !s.LastActivity.Equal(at(t, "2026-01-10T10:14:10Z")) {
		t.Errorf("times = %v, %v", s.StartedAt, s.LastActivity)
	}
	if s.Usage == nil || s.Path != "" {
		t.Errorf("usage map must be non-nil and path empty: %+v", s)
	}
}

func TestSessionCostKinds(t *testing.T) {
	prices := Prices{"claude-test-1": {Input: 1e6}} // a dollar per input token
	tests := []struct {
		name     string
		lines    []string
		wantKind core.CostKind
		wantUSD  float64
	}{
		{"reported", []string{assistantLine("m1", "claude-test-1", 1, "text"), `{"type":"cost-state","totalCostUSD":0.5}`}, core.CostReported, 0.5},
		{"reported zero falls back to estimate", []string{assistantLine("m1", "claude-test-1", 1, "text"), `{"type":"cost-state","totalCostUSD":0}`}, core.CostEstimated, 10},
		{"stale report falls back to estimate", []string{assistantLine("m1", "claude-test-1", 1, "text"), `{"type":"cost-state","totalCostUSD":0.5}`, assistantLine("m2", "claude-test-1", 1, "text")}, core.CostEstimated, 20},
		{"unpriced model", []string{assistantLine("m1", "claude-test-1", 1, "text"), assistantLine("m2", "claude-nope", 1, "text")}, core.CostNone, 0},
		{"no usage", nil, core.CostNone, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := foldLines(tt.lines...).Session(SessionInput{Prices: prices})
			if s.CostKind != tt.wantKind || s.CostUSD != tt.wantUSD {
				t.Errorf("cost = %v %v, want %v %v", s.CostUSD, s.CostKind, tt.wantUSD, tt.wantKind)
			}
		})
	}
}

func TestSessionFixtureTwo(t *testing.T) {
	s := fold(t, transcriptTwo()).Session(SessionInput{ID: sessionTwo})
	if s.Title != "Docs pass" || s.CostKind != core.CostReported || s.CostUSD != 1.25 || s.Live != nil || s.Status() != core.StatusEnded {
		t.Errorf("session two = %+v", s)
	}
}

func TestSessionDoesNotAliasState(t *testing.T) {
	acc := fold(t, transcriptOne())
	s := acc.Session(SessionInput{Now: at(t, "2026-01-10T10:30:00Z")})
	s.Usage["claude-test-1"] = core.Usage{}
	s.Recent[0].Tokens = -1
	if acc.State().Usage["claude-test-1"].Input != 15 || acc.State().Recent[0].Tokens == -1 {
		t.Error("mutating a Session changed the accumulator")
	}
}
