package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DanielTso/ccdeck/internal/core"
)

// lookupModel finds model in m: an exact key first, else the longest key
// that is a prefix of model and ends at a word boundary, so that
// "claude-x-1" matches "claude-x-1-20260101" but not "claude-x-10". A tier
// suffix must match exactly: "claude-x-1+fast" matches
// "claude-x-1-20260101+fast", never "claude-x-1-20260101" or
// "claude-x-1-20260101+long".
func lookupModel[V any](m map[string]V, model string) (V, bool) {
	if v, ok := m[model]; ok {
		return v, true
	}
	tier := tierOf(model)
	model = model[:len(model)-len(tier)]
	var best, bestModel string
	for k := range m {
		km := stripTier(k)
		if len(km) > len(bestModel) && len(km) < len(model) && strings.HasPrefix(model, km) &&
			!isWordByte(model[len(km)]) && tierOf(k) == tier {
			best, bestModel = k, km
		}
	}
	if best == "" {
		var zero V
		return zero, false
	}
	return m[best], true
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// Catalog maps model ids to their context-window size in tokens.
type Catalog map[string]int64

// Window returns the context-window size for model, matched exactly and
// then by prefix, or 0 when the model is unknown.
func (c Catalog) Window(model string) int64 {
	w, _ := lookupModel(c, stripTier(model))
	return w
}

// ReadCatalog reads every model-catalog file in dir
// (cache/model-catalog under the Claude directory). Files that are not
// catalogs are ignored. A missing dir is an empty catalog, not an error.
func ReadCatalog(dir string) (Catalog, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	c := Catalog{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct {
			Catalog struct {
				Config struct {
					Models []struct {
						ID      string `json:"id"`
						Runtime struct {
							MaxInputTokens int64 `json:"max_input_tokens"`
						} `json:"runtime"`
					} `json:"models"`
				} `json:"config"`
			} `json:"catalog"`
		}
		if decodeTolerant(data, &doc) != nil {
			continue
		}
		for _, m := range doc.Catalog.Config.Models {
			if m.ID != "" && m.Runtime.MaxInputTokens > 0 {
				c[m.ID] = m.Runtime.MaxInputTokens
			}
		}
	}
	return c, nil
}

// Price is what a model costs, in dollars per million tokens, except
// WebSearch, which is dollars per request.
type Price struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cacheRead"`
	CacheWrite5m float64 `json:"cacheWrite5m"`
	CacheWrite1h float64 `json:"cacheWrite1h"`
	WebSearch    float64 `json:"webSearch"`
}

// Cost prices u. Thinking tokens are part of Output and not charged again.
func (p Price) Cost(u core.Usage) float64 {
	const perM = 1e6
	return (float64(u.Input)*p.Input+
		float64(u.Output)*p.Output+
		float64(u.CacheRead)*p.CacheRead+
		float64(u.CacheWrite5m)*p.CacheWrite5m+
		float64(u.CacheWrite1h)*p.CacheWrite1h)/perM +
		float64(u.WebSearches)*p.WebSearch
}

// Prices maps usage keys to prices: model ids, and model ids with a tier
// suffix ("claude-x+fast") for the rates of a tier. Build it from
// ReadPrices and copy the config file's prices over it, so the config wins.
type Prices map[string]Price

// Lookup returns the price for a usage key, matched exactly and then by
// prefix of the model id. A tier with no price of its own has no price:
// it never falls back to the base rate.
func (p Prices) Lookup(key string) (Price, bool) {
	return lookupModel(p, key)
}

// Estimate prices usage by usage key. ok is false when usage is empty or
// any key with tokens has no price: a partial figure would look complete.
func (p Prices) Estimate(usage map[string]core.Usage) (cost float64, ok bool) {
	for model, u := range usage {
		if u == (core.Usage{}) {
			continue
		}
		price, found := p.Lookup(model)
		if !found {
			return 0, false
		}
		cost += price.Cost(u)
		ok = true
	}
	return cost, ok
}

// wireCosts is one entry of additionalModelCostsCache, or one of its tiers,
// in dollars per million tokens (per request for web searches).
type wireCosts struct {
	InputTokens              float64  `json:"inputTokens"`
	OutputTokens             float64  `json:"outputTokens"`
	PromptCacheWriteTokens   float64  `json:"promptCacheWriteTokens"`
	PromptCacheWrite1hTokens float64  `json:"promptCacheWrite1hTokens"`
	PromptCacheReadTokens    float64  `json:"promptCacheReadTokens"`
	WebSearchRequests        *float64 `json:"webSearchRequests"`
}

func (c *wireCosts) price(webSearch float64) Price {
	return Price{
		Input:        c.InputTokens,
		Output:       c.OutputTokens,
		CacheRead:    c.PromptCacheReadTokens,
		CacheWrite5m: c.PromptCacheWriteTokens,
		CacheWrite1h: c.PromptCacheWrite1hTokens,
		WebSearch:    webSearch,
	}
}

// ReadPrices reads the additionalModelCostsCache key of Claude Code's
// global config file (Paths.ConfigFile) and nothing else from it. A model's
// "fast" rates become the price of "<model>+fast" and its "longPrompt"
// rates the price of "<model>+long", with the threshold in thresholds.
// Web searches cost the same in every tier (the base or fast rate, as
// Claude Code charges them). A malformed tier, or a long tier without a
// positive threshold, is left out. A missing file is an error satisfying
// errors.Is(err, fs.ErrNotExist).
func ReadPrices(configFile string) (prices Prices, thresholds Thresholds, err error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, nil, err
	}
	type entry struct {
		wireCosts
		Fast       json.RawMessage `json:"fast"`
		LongPrompt json.RawMessage `json:"longPrompt"`
	}
	type long struct {
		wireCosts
		AbovePromptTokens float64 `json:"abovePromptTokens"`
	}
	var doc struct {
		Costs map[string]json.RawMessage `json:"additionalModelCostsCache"`
	}
	if err := decodeTolerant(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", configFile, err)
	}
	prices = make(Prices, len(doc.Costs))
	thresholds = Thresholds{}
	for model, raw := range doc.Costs {
		var c entry
		if decodeTolerant(raw, &c) != nil {
			continue
		}
		var web float64
		if c.WebSearchRequests != nil {
			web = *c.WebSearchRequests
		}
		prices[model] = c.price(web)
		var f wireCosts
		if len(c.Fast) > 0 && decodeTolerant(c.Fast, &f) == nil {
			fastWeb := web
			if f.WebSearchRequests != nil {
				fastWeb = *f.WebSearchRequests
			}
			prices[model+"+"+tierFast] = f.price(fastWeb)
		}
		var l long
		if len(c.LongPrompt) > 0 && decodeTolerant(c.LongPrompt, &l) == nil &&
			l.AbovePromptTokens >= 1 && l.AbovePromptTokens < 1<<62 {
			prices[model+"+"+tierLong] = l.price(web)
			thresholds[model] = int64(l.AbovePromptTokens)
		}
	}
	return prices, thresholds, nil
}
