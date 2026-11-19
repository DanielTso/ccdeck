package tui

import (
	"strings"
	"testing"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestShortModelTiers(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5+fast":               "opus 5.5 fast",
		"claude-haiku-5-5+long":              "haiku 5.5 long",
		"claude-opus-5-5+fast+long":          "opus 5.5 fast long",
		"claude-haiku-4-5-20251001+long":     "haiku 4.5 long",
		"claude-opus-5-5+unknown":            "opus 5.5 speed?",
		"acme-research-preview+fast":         "acme-research-preview fast",
		"claude-opus-5-5":                    "opus 5.5",
		"claude-opus-5-5+fast+unknown+extra": "opus 5.5 fast speed? extra",
	} {
		if got := shortModel(id); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestUsageModelTableShowsTiers checks that tier rows of the per-model
// table keep their whole name at 80 columns, apart from the base rows.
func TestUsageModelTableShowsTiers(t *testing.T) {
	f := bigStore(3, 0)
	f.days = []core.DayUsage{{
		Date: testNow,
		Usage: map[string]core.Usage{
			"claude-opus-5-5":           {Input: 9000, Output: 900},
			"claude-opus-5-5+fast":      {Input: 5000, Output: 500},
			"claude-haiku-5-5+long":     {Input: 3000, Output: 300},
			"claude-opus-5-5+fast+long": {Input: 1000, Output: 100},
		},
		Sessions: 1, SessionIDs: []string{"s-0000"},
	}}
	for _, size := range [][2]int{{80, 24}, {80, 40}, {140, 40}} {
		h := newHarnessOn(t, f, size[0], size[1])
		h.keys("3")
		v := h.view()
		for _, name := range []string{"opus 5.5 fast long", "opus 5.5 fast", "haiku 5.5 long"} {
			l := lineWith(t, v, name)
			if strings.Contains(l, "+") {
				t.Errorf("%dx%d: raw usage key in %q", size[0], size[1], l)
			}
		}
		for i, l := range strings.Split(v, "\n") {
			if w := width(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, w)
			}
		}
		// The base model has its own row, not merged with its tiers.
		if l := lineWith(t, v, "opus 5.5 "); !strings.Contains(v, "opus 5.5 fast") || l == "" {
			t.Errorf("%dx%d: base row missing", size[0], size[1])
		}
	}
}
