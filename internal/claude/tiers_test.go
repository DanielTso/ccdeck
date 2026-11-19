package claude

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// tieredCosts is a price cache shaped like Claude Code's: a model with a
// fast tier, a model with a long-prompt tier, a model with both, and a
// plain one. The figures are invented.
const tieredCosts = `{"additionalModelCostsCache":{
 "claude-quick-1":{"inputTokens":4,"outputTokens":20,"promptCacheWriteTokens":5,"promptCacheWrite1hTokens":8,"promptCacheReadTokens":0.2,"webSearchRequests":0.01,
   "fast":{"inputTokens":8,"outputTokens":40,"promptCacheWriteTokens":10,"promptCacheWrite1hTokens":16,"promptCacheReadTokens":0.4}},
 "claude-wide-1":{"inputTokens":1,"outputTokens":5,"promptCacheWriteTokens":1.25,"promptCacheWrite1hTokens":2,"promptCacheReadTokens":0.1,"webSearchRequests":0.01,
   "longPrompt":{"abovePromptTokens":1000,"inputTokens":2,"outputTokens":10,"promptCacheWriteTokens":2.5,"promptCacheWrite1hTokens":4,"promptCacheReadTokens":0.2,"webSearchRequests":0.02}},
 "claude-both-1":{"inputTokens":1,"outputTokens":1,"promptCacheWriteTokens":1,"promptCacheReadTokens":1,
   "fast":{"inputTokens":2,"outputTokens":2,"promptCacheWriteTokens":2,"promptCacheReadTokens":2},
   "longPrompt":{"abovePromptTokens":500,"inputTokens":3,"outputTokens":3,"promptCacheWriteTokens":3,"promptCacheReadTokens":3}},
 "claude-plain-1":{"inputTokens":1,"outputTokens":1,"promptCacheWriteTokens":1,"promptCacheReadTokens":1},
 "claude-bad-1":{"inputTokens":1,"fast":"nope","longPrompt":{"abovePromptTokens":0,"inputTokens":9}}
}}`

func readTiered(t *testing.T) (Prices, Thresholds) {
	t.Helper()
	p, th, err := ReadPrices(writeFile(t, ".claude.json", tieredCosts))
	if err != nil {
		t.Fatal(err)
	}
	return p, th
}

func TestReadPricesTiers(t *testing.T) {
	p, th := readTiered(t)
	want := Prices{
		"claude-quick-1":      {Input: 4, Output: 20, CacheRead: 0.2, CacheWrite5m: 5, CacheWrite1h: 8, WebSearch: 0.01},
		"claude-quick-1+fast": {Input: 8, Output: 40, CacheRead: 0.4, CacheWrite5m: 10, CacheWrite1h: 16, WebSearch: 0.01},
		"claude-wide-1":       {Input: 1, Output: 5, CacheRead: 0.1, CacheWrite5m: 1.25, CacheWrite1h: 2, WebSearch: 0.01},
		// Web searches are charged at the base rate whatever the tier says,
		// as Claude Code does.
		"claude-wide-1+long": {Input: 2, Output: 10, CacheRead: 0.2, CacheWrite5m: 2.5, CacheWrite1h: 4, WebSearch: 0.01},
		"claude-both-1":      {Input: 1, Output: 1, CacheRead: 1, CacheWrite5m: 1},
		"claude-both-1+fast": {Input: 2, Output: 2, CacheRead: 2, CacheWrite5m: 2},
		"claude-both-1+long": {Input: 3, Output: 3, CacheRead: 3, CacheWrite5m: 3},
		"claude-plain-1":     {Input: 1, Output: 1, CacheRead: 1, CacheWrite5m: 1},
		// A malformed tier is no tier; a long tier without a positive
		// threshold could never apply.
		"claude-bad-1": {Input: 1},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("prices =\n%+v\nwant\n%+v", p, want)
	}
	if wantTh := (Thresholds{"claude-wide-1": 1000, "claude-both-1": 500}); !reflect.DeepEqual(th, wantTh) {
		t.Errorf("thresholds = %v, want %v", th, wantTh)
	}
}

func TestUsageKeys(t *testing.T) {
	tests := []struct {
		model      string
		speed      string
		long       bool
		key        string
		splitModel string
		tiers      []string
	}{
		{"claude-x-1", "", false, "claude-x-1", "claude-x-1", nil},
		{"claude-x-1", "standard", false, "claude-x-1", "claude-x-1", nil},
		{"claude-x-1", "fast", false, "claude-x-1+fast", "claude-x-1", []string{"fast"}},
		{"claude-x-1", "", true, "claude-x-1+long", "claude-x-1", []string{"long"}},
		{"claude-x-1", "fast", true, "claude-x-1+fast+long", "claude-x-1", []string{"fast", "long"}},
		// A speed ccdeck does not know is a tier it cannot price.
		{"claude-x-1", "turbo", false, "claude-x-1+unknown", "claude-x-1", []string{"unknown"}},
		{"unknown", "fast", false, "unknown+fast", "unknown", []string{"fast"}},
	}
	for _, tt := range tests {
		if got := usageKey(tt.model, tt.speed, tt.long); got != tt.key {
			t.Errorf("usageKey(%q, %q, %v) = %q, want %q", tt.model, tt.speed, tt.long, got, tt.key)
		}
		m, tiers := SplitUsageKey(tt.key)
		if m != tt.splitModel || !reflect.DeepEqual(tiers, tt.tiers) {
			t.Errorf("SplitUsageKey(%q) = %q %v, want %q %v", tt.key, m, tiers, tt.splitModel, tt.tiers)
		}
	}
}

func TestDescribeUsageKey(t *testing.T) {
	for key, want := range map[string]string{
		"claude-x-1":           "claude-x-1",
		"claude-x-1+fast":      "claude-x-1 (fast)",
		"claude-x-1+long":      "claude-x-1 (long prompt)",
		"claude-x-1+fast+long": "claude-x-1 (fast, long prompt)",
		"claude-x-1+unknown":   "claude-x-1 (unknown speed)",
	} {
		if got := DescribeUsageKey(key); got != want {
			t.Errorf("DescribeUsageKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestTieredLookup(t *testing.T) {
	p, _ := readTiered(t)
	tests := []struct {
		key   string
		input float64
		ok    bool
	}{
		{"claude-quick-1", 4, true},
		{"claude-quick-1+fast", 8, true},
		{"claude-quick-1-20270101+fast", 8, true}, // prefix match keeps the tier
		{"claude-quick-1+long", 0, false},         // no long tier: no price, not the base rate
		{"claude-plain-1+fast", 0, false},
		{"claude-both-1+fast+long", 0, false}, // no combined price in the cache
		{"claude-quick-1+unknown", 0, false},
	}
	for _, tt := range tests {
		got, ok := p.Lookup(tt.key)
		if ok != tt.ok || got.Input != tt.input {
			t.Errorf("Lookup(%q) = %+v %v, want input %v %v", tt.key, got, ok, tt.input, tt.ok)
		}
	}
	// A configured price may name a tier, combined ones included.
	p["claude-both-1+fast+long"] = Price{Input: 7}
	if got, ok := p.Lookup("claude-both-1+fast+long"); !ok || got.Input != 7 {
		t.Errorf("configured combined tier: %+v %v", got, ok)
	}
}

func TestCatalogIgnoresTier(t *testing.T) {
	cat := Catalog{"claude-x-1": 1000}
	for _, key := range []string{"claude-x-1+fast", "claude-x-1+fast+long", "claude-x-1-20270101+long"} {
		if got := cat.Window(key); got != 1000 {
			t.Errorf("Window(%q) = %d, want 1000", key, got)
		}
	}
}

// tierLine is a main-thread assistant line with full usage fields.
func tierLine(id, model, speed string, input, cacheRead, cacheWrite, output int) string {
	sp := ""
	if speed != "" {
		sp = `,"speed":"` + speed + `"`
	}
	return line(`"type":"assistant"`, `"timestamp":"2026-01-10T10:00:00.000Z"`,
		`"message":{"id":"`+id+`","model":"`+model+`","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":`+itoa(input)+
			`,"cache_read_input_tokens":`+itoa(cacheRead)+`,"cache_creation_input_tokens":`+itoa(cacheWrite)+
			`,"output_tokens":`+itoa(output)+sp+`}}`)
}

func foldTiered(th Thresholds, lines ...string) *Accumulator {
	acc := NewAccumulator()
	acc.SetThresholds(th)
	var off int64
	for _, l := range lines {
		b := []byte(l + "\n")
		acc.Line(off, b)
		off += int64(len(b))
	}
	return acc
}

func TestFastAndStandardPricedApart(t *testing.T) {
	p, th := readTiered(t)
	acc := foldTiered(th,
		tierLine("m1", "claude-quick-1", "standard", 1000, 0, 0, 100),
		tierLine("m2", "claude-quick-1", "fast", 1000, 0, 0, 100),
		tierLine("m2", "claude-quick-1", "fast", 1000, 0, 0, 100), // same message, second block
		tierLine("m3", "claude-quick-1", "", 500, 0, 0, 0),
	)
	want := map[string]core.Usage{
		"claude-quick-1":      {Input: 1500, Output: 100},
		"claude-quick-1+fast": {Input: 1000, Output: 100},
	}
	if got := acc.State().Usage; !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	s := acc.Session(SessionInput{Prices: p})
	// standard: 1500×4 + 100×20 = 8000; fast: 1000×8 + 100×40 = 12000 (per million)
	if s.CostKind != core.CostEstimated || math.Abs(s.CostUSD-0.02) > 1e-12 {
		t.Errorf("cost = %v %v, want 0.02 estimated", s.CostUSD, s.CostKind)
	}
	if s.Model != "claude-quick-1" {
		t.Errorf("model = %q, want the plain id", s.Model)
	}
}

func TestLongPromptThreshold(t *testing.T) {
	p, th := readTiered(t) // claude-wide-1 goes long above 1000 prompt tokens
	tests := []struct {
		name                         string
		input, cacheRead, cacheWrite int
		key                          string
		usd                          float64
	}{
		{"below", 999, 0, 0, "claude-wide-1", (999*1 + 10*5) / 1e6},
		{"exactly at", 400, 500, 100, "claude-wide-1", (400*1 + 500*0.1 + 100*1.25 + 10*5) / 1e6},
		// Cache reads and writes count toward the prompt size, and every
		// token of the request is then billed at the long rates.
		{"above", 400, 500, 101, "claude-wide-1+long", (400*2 + 500*0.2 + 101*2.5 + 10*10) / 1e6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := foldTiered(th, tierLine("m1", "claude-wide-1", "standard", tt.input, tt.cacheRead, tt.cacheWrite, 10))
			u := acc.State().Usage
			if _, ok := u[tt.key]; !ok || len(u) != 1 {
				t.Fatalf("usage keys = %v, want only %q", u, tt.key)
			}
			s := acc.Session(SessionInput{Prices: p})
			if s.CostKind != core.CostEstimated || math.Abs(s.CostUSD-tt.usd) > 1e-12 {
				t.Errorf("cost = %v %v, want %v", s.CostUSD, s.CostKind, tt.usd)
			}
		})
	}
	// Without thresholds nothing is long: the tier needs the price cache.
	acc := foldTiered(nil, tierLine("m1", "claude-wide-1", "", 5000, 0, 0, 10))
	if _, ok := acc.State().Usage["claude-wide-1"]; !ok {
		t.Errorf("no thresholds: usage = %v", acc.State().Usage)
	}
}

func TestLongPromptByIteration(t *testing.T) {
	_, th := readTiered(t)
	iter := func(in, out int) string {
		return `{"type":"message","input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) +
			`,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`
	}
	msg := func(its string, in, out int) string {
		return line(`"type":"assistant"`, `"timestamp":"2026-01-10T10:00:00.000Z"`,
			`"message":{"id":"m1","model":"claude-wide-1","content":[],"usage":{"input_tokens":`+itoa(in)+
				`,"output_tokens":`+itoa(out)+`,"output_tokens_details":{"thinking_tokens":3},"iterations":[`+its+`]}}`)
	}
	tests := []struct {
		name string
		line string
		want map[string]core.Usage
	}{
		{"one iteration", msg(iter(1500, 9), 1500, 9),
			map[string]core.Usage{"claude-wide-1+long": {Input: 1500, Output: 9, Thinking: 3}}},
		{"all below although the sum is above", msg(iter(600, 4)+","+iter(700, 5), 1300, 9),
			map[string]core.Usage{"claude-wide-1": {Input: 1300, Output: 9, Thinking: 3}}},
		{"straddling: each iteration in its tier", msg(iter(600, 4)+","+iter(1200, 5), 1800, 9),
			map[string]core.Usage{"claude-wide-1": {Input: 600, Output: 4, Thinking: 3}, "claude-wide-1+long": {Input: 1200, Output: 5}}},
		{"iterations that do not add up are ignored", msg(iter(600, 4)+","+iter(1200, 5), 1700, 9),
			map[string]core.Usage{"claude-wide-1+long": {Input: 1700, Output: 9, Thinking: 3}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := foldTiered(th, tt.line, tt.line) // the second line repeats the message
			if got := acc.State().Usage; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTierWithoutPriceIsNoPrice(t *testing.T) {
	p, th := readTiered(t)
	for _, tt := range []struct{ name, line string }{
		{"fast on a model without fast rates", tierLine("m1", "claude-plain-1", "fast", 10, 0, 0, 10)},
		{"fast and long without a combined rate", tierLine("m1", "claude-both-1", "fast", 600, 0, 0, 10)},
		{"unknown speed", tierLine("m1", "claude-quick-1", "turbo", 10, 0, 0, 10)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acc := foldTiered(th, tierLine("m0", "claude-quick-1", "", 10, 0, 0, 10), tt.line)
			s := acc.Session(SessionInput{Prices: p})
			if s.CostKind != core.CostNone || s.CostUSD != 0 {
				t.Errorf("cost = %v %v, want none; usage %v", s.CostUSD, s.CostKind, s.Usage)
			}
		})
	}
}

func TestOnUsageCarriesTierKey(t *testing.T) {
	_, th := readTiered(t)
	acc := NewAccumulator()
	acc.SetThresholds(th)
	got := map[string]core.Usage{}
	acc.OnUsage(func(_ time.Time, key string, u core.Usage) { got[key] = got[key].Add(u) })
	for i, l := range []string{
		tierLine("m1", "claude-quick-1", "fast", 10, 0, 0, 1),
		tierLine("m1", "claude-quick-1", "fast", 10, 0, 0, 5), // a later block with more output
		tierLine("m2", "claude-wide-1", "", 2000, 0, 0, 1),
		tierLine("m3", "claude-quick-1", "", 10, 0, 0, 1),
	} {
		acc.Line(int64(i*1000), []byte(l+"\n"))
	}
	if want := acc.State().Usage; !reflect.DeepEqual(got, want) {
		t.Errorf("hook totals %+v, state %+v", got, want)
	}
	if got["claude-quick-1+fast"].Output != 5 {
		t.Errorf("fast output = %d, want 5 (the message counted once)", got["claude-quick-1+fast"].Output)
	}
}

func TestResumeAcrossTierChange(t *testing.T) {
	_, th := readTiered(t)
	lines := []string{
		tierLine("m1", "claude-quick-1", "standard", 100, 0, 0, 10),
		tierLine("m2", "claude-quick-1", "fast", 100, 0, 0, 10),
		tierLine("m3", "claude-wide-1", "standard", 2000, 0, 0, 10),
		tierLine("m4", "claude-quick-1", "standard", 100, 0, 0, 10),
		tierLine("m4", "claude-quick-1", "standard", 100, 0, 0, 12),
		tierLine("m5", "claude-wide-1", "standard", 20, 0, 0, 10),
	}
	data := strings.Join(lines, "\n") + "\n"
	full := foldTiered(th, lines...)
	for cut := 0; cut <= len(data); cut++ {
		if cut > 0 && data[cut-1] != '\n' {
			continue
		}
		head := writeFile(t, "head.jsonl", data[:cut])
		first := NewAccumulator()
		first.SetThresholds(th)
		if _, err := NewTailer(head, 0).Read(first); err != nil {
			t.Fatal(err)
		}
		enc, err := json.Marshal(first.State())
		if err != nil {
			t.Fatal(err)
		}
		var st State
		if err := json.Unmarshal(enc, &st); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(head, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(head)
		if err != nil {
			t.Fatal(err)
		}
		acc, err := RestoreAccumulator(st, f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		acc.SetThresholds(th)
		if _, err := NewTailer(head, st.Offset).Read(acc); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(acc.State(), full.State()) {
			t.Fatalf("cut %d: resumed usage %+v, full %+v", cut, acc.State().Usage, full.State().Usage)
		}
	}
	if len(full.State().Usage) != 4 {
		t.Errorf("usage keys = %v, want quick, quick+fast, wide and wide+long", full.State().Usage)
	}
}

func TestStateVersionCoversTierKeys(t *testing.T) {
	// A State from before tier keys counted fast and long messages under
	// the plain model id; it must not be resumed.
	if StateVersion < 3 {
		t.Errorf("StateVersion = %d, want at least 3", StateVersion)
	}
}
