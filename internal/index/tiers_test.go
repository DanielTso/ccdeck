package index

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// tieredPrices is a price cache in which claude-test-1 has fast rates at
// twice the base and long-prompt rates at ten times the base above
// threshold prompt tokens.
func tieredPrices(threshold string) string {
	return `{"additionalModelCostsCache":{"claude-test-1":{"inputTokens":3,"outputTokens":15,"promptCacheWriteTokens":3.75,"promptCacheWrite1hTokens":6,"promptCacheReadTokens":0.3,"webSearchRequests":0.01,` +
		`"fast":{"inputTokens":6,"outputTokens":30,"promptCacheWriteTokens":7.5,"promptCacheWrite1hTokens":12,"promptCacheReadTokens":0.6},` +
		`"longPrompt":{"abovePromptTokens":` + threshold + `,"inputTokens":30,"outputTokens":150,"promptCacheWriteTokens":37.5,"promptCacheWrite1hTokens":60,"promptCacheReadTokens":3}}}}`
}

// tierLines is one API response of claude-test-1 over two lines, served
// at speed.
func tierLines(t *testing.T, ts time.Time, msgID, speed string, in, out int64) string {
	usage := map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "speed": speed}
	mk := func(block map[string]any) string {
		return line(t, map[string]any{
			"type": "assistant", "timestamp": stamp(ts), "cwd": projDir, "isSidechain": false,
			"message": map[string]any{"id": msgID, "model": model1, "role": "assistant", "content": []any{block}, "usage": usage},
		})
	}
	return mk(map[string]any{"type": "thinking", "thinking": ""}) + mk(map[string]any{"type": "text", "text": "ok"})
}

// seedTiers writes session A with a standard, a fast and a long-prompt
// message today, and session B with one fast message yesterday.
func seedTiers(t *testing.T, e *env) {
	t.Helper()
	y, m, d := time.Now().Date()
	today := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	e.write(".claude.json", tieredPrices("1000"))
	e.write(transcript("-home-user-widgets", sessA),
		userLine(t, today, "Spin")+
			tierLines(t, today, "m1", "standard", 100, 10)+
			tierLines(t, today.Add(time.Minute), "m2", "fast", 100, 10)+
			tierLines(t, today.Add(2*time.Minute), "m3", "standard", 1001, 10))
	e.write(transcript("-home-user-gadgets", sessB),
		userLine(t, today.AddDate(0, 0, -1), "Shine")+tierLines(t, today.AddDate(0, 0, -1), "m4", "fast", 500, 5))
}

func TestTierUsageAndCost(t *testing.T) {
	e := newEnv(t)
	seedTiers(t, e)
	s := e.open()

	a := session(t, s, sessA)
	want := map[string]core.Usage{
		model1:           {Input: 100, Output: 10},
		model1 + "+fast": {Input: 100, Output: 10},
		model1 + "+long": {Input: 1001, Output: 10},
	}
	if !reflect.DeepEqual(a.Usage, want) {
		t.Errorf("usage = %+v, want %+v", a.Usage, want)
	}
	if a.Model != model1 || a.ContextWindow != 200000 {
		t.Errorf("model %q window %d, want the plain id and its window", a.Model, a.ContextWindow)
	}
	wantUSD := (100*3 + 10*15 + 100*6 + 10*30 + 1001*30 + 10*150) / 1e6
	if a.CostKind != core.CostEstimated || !near(a.CostUSD, wantUSD) {
		t.Errorf("cost = %v %v, want %v", a.CostUSD, a.CostKind, wantUSD)
	}

	// Daily totals add up to the sessions' totals, key by key and in cost.
	days := s.Daily(3)
	dayUsage := map[string]core.Usage{}
	var dayUSD float64
	for _, d := range days {
		for k, u := range d.Usage {
			dayUsage[k] = dayUsage[k].Add(u)
		}
		dayUSD += d.CostUSD
	}
	sessUsage := map[string]core.Usage{}
	var sessUSD float64
	for _, x := range s.Sessions() {
		for k, u := range x.Usage {
			sessUsage[k] = sessUsage[k].Add(u)
		}
		sessUSD += x.CostUSD
	}
	if !reflect.DeepEqual(dayUsage, sessUsage) {
		t.Errorf("daily usage %+v, sessions %+v", dayUsage, sessUsage)
	}
	if !near(dayUSD, sessUSD) {
		t.Errorf("daily cost %v, sessions %v", dayUSD, sessUSD)
	}
	if u := days[1].Usage[model1+"+fast"]; u.Input != 500 {
		t.Errorf("yesterday's fast usage = %+v", u)
	}
}

func TestTierWithoutPriceIsNoPrice(t *testing.T) {
	e := newEnv(t) // claude-test-1 has base rates only
	e.write(transcript("-home-user-widgets", sessA),
		tierLines(t, t0, "m1", "standard", 100, 10)+tierLines(t, t0, "m2", "fast", 100, 10))
	s := e.open()
	if a := session(t, s, sessA); a.CostKind != core.CostNone {
		t.Errorf("cost = %v %v, want none: fast usage has no price", a.CostUSD, a.CostKind)
	}
}

func TestConfiguredTierPrice(t *testing.T) {
	e := newEnv(t)
	e.write(transcript("-home-user-widgets", sessA), tierLines(t, t0, "m1", "fast", 1000, 0))
	s, err := newStore(Options{ClaudeDir: e.dir, CacheDir: e.cache, Prices: map[string]Price{model1 + "+fast": {Input: 1}}},
		tuning{procs: e.procs, debounce: 20 * time.Millisecond, poll: 50 * time.Millisecond, sweep: time.Hour, workers: 1, entries: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	<-s.Ready()
	if a := session(t, s, sessA); a.CostKind != core.CostEstimated || !near(a.CostUSD, 1000/1e6) {
		t.Errorf("cost = %v %v, want the configured fast price", a.CostUSD, a.CostKind)
	}
}

func TestCacheResumeAcrossTiers(t *testing.T) {
	e := newEnv(t)
	seedTiers(t, e)
	_ = e.open().Close()

	// A fast message arrives while nothing runs.
	e.append(transcript("-home-user-widgets", sessA), tierLines(t, t0, "m9", "fast", 7, 7))
	warm := e.open()
	if st := warm.Stats(); st.Resumed != 2 {
		t.Errorf("Resumed = %d, want 2", st.Resumed)
	}
	e.cache = t.TempDir()
	cold := e.open()
	if w, c := warm.Sessions(), cold.Sessions(); !reflect.DeepEqual(w, c) {
		t.Errorf("resumed sessions differ from a full parse:\n%+v\n%+v", w, c)
	}
	if w, c := warm.Daily(400), cold.Daily(400); !reflect.DeepEqual(w, c) {
		t.Error("resumed daily usage differs from a full parse")
	}
	if u := session(t, warm, sessA).Usage[model1+"+fast"]; u.Input != 107 {
		t.Errorf("fast usage = %+v, want 107 input", u)
	}
}

func TestCacheRebuiltWhenThresholdChanges(t *testing.T) {
	e := newEnv(t)
	seedTiers(t, e)
	_ = e.open().Close()

	// With a higher threshold the 1001-token prompt is no longer long: the
	// cached state, which filed it as long, must not be resumed.
	e.write(".claude.json", tieredPrices("2000"))
	s := e.open()
	if st := s.Stats(); st.Resumed != 0 {
		t.Errorf("Resumed = %d after the threshold changed", st.Resumed)
	}
	if u := session(t, s, sessA).Usage; u[model1].Input != 1101 || u[model1+"+long"] != (core.Usage{}) {
		t.Errorf("usage = %+v, want the 1001-token prompt at the base rate", u)
	}
}

func TestThresholdChangeWhileRunning(t *testing.T) {
	e := newEnv(t)
	seedTiers(t, e)
	s := e.openWith(func(tn *tuning) { tn.sweep = 100 * time.Millisecond })
	if _, ok := session(t, s, sessA).Usage[model1+"+long"]; !ok {
		t.Fatal("no long usage to begin with")
	}
	// Another size, so that the change shows even within one tick of the
	// file system's clock: the index notices it by size and mtime.
	e.write(".claude.json", tieredPrices("10000"))
	waitFor(t, "usage refiled under the new threshold", func() bool {
		u := session(t, s, sessA).Usage
		_, long := u[model1+"+long"]
		return !long && u[model1].Input == 1101
	})
	days := s.Daily(3)
	var today core.Usage
	for k, u := range days[2].Usage {
		if strings.HasPrefix(k, model1) {
			today = today.Add(u)
		}
	}
	if _, long := days[2].Usage[model1+"+long"]; long || today.Input != 1201 {
		t.Errorf("today's usage after the change = %+v", days[2].Usage)
	}
}
