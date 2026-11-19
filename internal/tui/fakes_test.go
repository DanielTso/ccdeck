package tui

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// fakeStore is a core.Store over fixed data that counts its heavier calls,
// for scale tests and for checking those calls stay off the update loop.
type fakeStore struct {
	mu       sync.Mutex
	sessions []core.Session
	entries  map[string][]core.Entry
	days     []core.DayUsage

	entriesCalls, dailyCalls, searchCalls atomic.Int64
}

var _ core.Store = (*fakeStore)(nil)

func (f *fakeStore) Sessions() []core.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.Session(nil), f.sessions...)
}

func (f *fakeStore) Session(id string) (core.Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == id {
			return s, true
		}
	}
	return core.Session{}, false
}

func (f *fakeStore) Entries(id string, limit int) ([]core.Entry, error) {
	f.entriesCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok {
		return nil, errors.New("no such session")
	}
	if limit > 0 && len(e) > limit {
		e = e[len(e)-limit:]
	}
	return append([]core.Entry(nil), e...), nil
}

func (f *fakeStore) SearchPrompts(string, int) []core.PromptHit {
	f.searchCalls.Add(1)
	return nil
}

func (f *fakeStore) Daily(int) []core.DayUsage {
	f.dailyCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.DayUsage(nil), f.days...)
}

func (f *fakeStore) Subscribe() (<-chan core.Event, func()) {
	ch := make(chan core.Event)
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }
}

func (f *fakeStore) Close() error { return nil }

func (f *fakeStore) appendEntries(id string, e ...core.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[id] = append(f.entries[id], e...)
}

// bigStore has n sessions, the first one live, and a conversation of
// entries entries in the first session; the others have none.
func bigStore(n, entries int) *fakeStore {
	f := &fakeStore{entries: map[string][]core.Entry{}}
	for i := range n {
		s := core.Session{
			ID:            fmt.Sprintf("s-%04d", i),
			Title:         fmt.Sprintf("Invented session number %d about widgets", i),
			ProjectDir:    fmt.Sprintf("/home/demo/src/project-%d", i%17),
			Model:         "claude-opus-5-5",
			LastActivity:  testNow.Add(-time.Duration(i) * time.Minute),
			StartedAt:     testNow.Add(-time.Duration(i+30) * time.Minute),
			ContextTokens: int64(1000 * (i % 190)),
			ContextWindow: 200_000,
			CostKind:      core.CostEstimated,
			CostUSD:       float64(i%50) / 7,
			Usage:         map[string]core.Usage{"claude-opus-5-5": {Input: 100, Output: int64(1000 + i)}},
		}
		if i == 0 {
			s.Live = &core.Live{PID: 4242, Status: core.StatusBusy, StatusSince: testNow}
			for j := range 40 {
				s.Recent = append(s.Recent, core.Sample{Time: testNow.Add(-time.Duration(j) * time.Minute), Tokens: int64(500 + j*10)})
			}
		}
		f.sessions = append(f.sessions, s)
		f.entries[s.ID] = nil
	}
	f.entries["s-0000"] = makeEntries(0, entries)
	return f
}

// makeEntries invents entries from..to-1 of a conversation: prompts, prose
// and tool calls with results.
func makeEntries(from, to int) []core.Entry {
	var out []core.Entry
	for i := from; i < to; i++ {
		at := testNow.Add(-time.Duration(to-i) * time.Second)
		var e core.Entry
		switch i % 5 {
		case 0:
			e = core.Entry{Kind: core.EntryPrompt, Text: fmt.Sprintf("Prompt %d: please look at widget %d and explain it.", i, i)}
		case 1:
			e = core.Entry{Kind: core.EntryText, Text: fmt.Sprintf("Reply %d. The widget is fine.\n\nIt has two parts, both of which work as intended in every case we tried.", i)}
		case 2:
			e = core.Entry{Kind: core.EntryToolUse, Tool: "Bash", Text: fmt.Sprintf("go test ./widget/%d", i), ToolID: fmt.Sprintf("t%d", i)}
		case 3:
			e = core.Entry{Kind: core.EntryToolResult, Text: fmt.Sprintf("ok widget %d\nline two\nline three", i), ToolID: fmt.Sprintf("t%d", i-1)}
		case 4:
			e = core.Entry{Kind: core.EntryThinking, Text: "Considering the widget."}
		}
		e.Time = at
		out = append(out, e)
	}
	return out
}
