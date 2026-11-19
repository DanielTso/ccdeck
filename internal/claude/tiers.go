package claude

import (
	"encoding/json"
	"strings"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Price tiers. Claude Code's price cache gives some models higher rates for
// requests served in fast mode (usage.speed "fast") and for requests whose
// prompt is above a size (longPrompt.abovePromptTokens). Usage counted in
// such a tier is kept under the model id plus a tier suffix, in this order:
// "claude-x+fast", "claude-x+long", "claude-x+fast+long". A speed ccdeck
// does not know is counted under "+unknown", which has no price, rather
// than at a rate that may be wrong. Session.Model stays the plain id.
const (
	tierFast    = "fast"
	tierUnknown = "unknown" // a usage.speed value other than standard or fast
	tierLong    = "long"
)

// usageKey is the usage key of a request to model served at speed, with a
// prompt above the model's long-prompt threshold when long is set.
func usageKey(model, speed string, long bool) string {
	switch speed {
	case "", "standard":
	case tierFast:
		model += "+" + tierFast
	default:
		model += "+" + tierUnknown
	}
	if long {
		model += "+" + tierLong
	}
	return model
}

// SplitUsageKey splits a usage key into the model id and its tiers, in
// order; tiers is nil for the standard tier.
func SplitUsageKey(key string) (model string, tiers []string) {
	model, rest, ok := strings.Cut(key, "+")
	if !ok {
		return key, nil
	}
	return model, strings.Split(rest, "+")
}

// stripTier returns the model id of a usage key.
func stripTier(key string) string {
	model, _, _ := strings.Cut(key, "+")
	return model
}

// tierOf returns the tier suffix of a usage key, "" for the standard tier.
func tierOf(key string) string {
	if i := strings.IndexByte(key, '+'); i >= 0 {
		return key[i:]
	}
	return ""
}

// DescribeUsageKey renders a usage key for people: "claude-x (fast, long
// prompt)".
func DescribeUsageKey(key string) string {
	model, tiers := SplitUsageKey(key)
	if len(tiers) == 0 {
		return model
	}
	words := make([]string, len(tiers))
	for i, t := range tiers {
		switch t {
		case tierLong:
			words[i] = "long prompt"
		case tierUnknown:
			words[i] = "unknown speed"
		default:
			words[i] = t
		}
	}
	return model + " (" + strings.Join(words, ", ") + ")"
}

// Thresholds maps model ids to the prompt size, in tokens, above which a
// request to the model is billed at its long-prompt rates. The prompt size
// is input plus cache reads plus cache writes.
type Thresholds map[string]int64

// Lookup returns the threshold for model, matched exactly and then by
// prefix.
func (t Thresholds) Lookup(model string) (int64, bool) {
	return lookupModel(t, stripTier(model))
}

// SetThresholds sets the long-prompt thresholds usage is filed with. Nil
// means no model has a long-prompt tier. It survives Reset. Lines already
// folded keep the tier they were filed under: a State folded with other
// thresholds must be parsed again from the start.
func (a *Accumulator) SetThresholds(t Thresholds) {
	a.thresholds = t
}

// usagePart is usage filed under one key.
type usagePart struct {
	key string
	u   core.Usage
}

// wireIteration is one request of a message that took several, as listed
// in usage.iterations.
type wireIteration struct {
	Type string `json:"type"`
	wireUsage
}

// tierParts files the usage u of one message to model under its tier keys.
// Usually that is one key. A message to a model with a long-prompt tier
// whose usage lists several requests (usage.iterations) is filed request
// by request, as Claude Code prices it, when the requests add up to the
// message's usage; the requests above the threshold then go under the long
// key and the rest, thinking and web searches included, under the other.
func (a *Accumulator) tierParts(model string, w *wireUsage, u core.Usage) []usagePart {
	speed := ""
	if w != nil {
		speed = w.Speed
	}
	limit, ok := a.thresholds.Lookup(model)
	if !ok {
		return []usagePart{{usageKey(model, speed, false), u}}
	}
	above := func(x core.Usage) bool {
		return x.Input+x.CacheRead+x.CacheWrite5m+x.CacheWrite1h > limit
	}
	its := w.iterations()
	if len(its) < 2 {
		return []usagePart{{usageKey(model, speed, above(u)), u}}
	}
	var long, all core.Usage
	nLong := 0
	for _, it := range its {
		all = all.Add(it)
		if above(it) {
			long = long.Add(it)
			nLong++
		}
	}
	if all.Input != u.Input || all.Output != u.Output || all.CacheRead != u.CacheRead ||
		all.CacheWrite5m+all.CacheWrite1h != u.CacheWrite5m+u.CacheWrite1h {
		return []usagePart{{usageKey(model, speed, above(u)), u}}
	}
	switch nLong {
	case 0:
		return []usagePart{{usageKey(model, speed, false), u}}
	case len(its):
		return []usagePart{{usageKey(model, speed, true), u}}
	}
	return []usagePart{
		{usageKey(model, speed, false), sub(u, long)},
		{usageKey(model, speed, true), long},
	}
}

// iterations decodes the message requests listed in usage.iterations; nil
// when there are none or they cannot be read.
func (w *wireUsage) iterations() []core.Usage {
	if w == nil || len(w.Iterations) == 0 {
		return nil
	}
	var raw []json.RawMessage
	if json.Unmarshal(w.Iterations, &raw) != nil {
		return nil
	}
	out := make([]core.Usage, 0, len(raw))
	for _, r := range raw {
		var it wireIteration
		if decodeTolerant(r, &it) != nil || it.Type != "message" {
			continue
		}
		u := it.usage()
		u.Thinking, u.WebSearches = 0, 0
		out = append(out, u)
	}
	return out
}
