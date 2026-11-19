package tui

import (
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// theme is a palette of semantic colour tokens. Views never use colours
// directly: every style is derived from a theme in newStyles.
type theme struct {
	name string
	dark bool

	background    color.Color
	surface       color.Color // selected rows, overlays
	border        color.Color // rules, separators, empty gauge cells
	borderFocused color.Color
	text          color.Color
	muted         color.Color
	accent        color.Color

	busy, idle, waiting, ended, unknown color.Color
	err                                 color.Color

	gaugeLow, gaugeMid, gaugeHigh color.Color
}

// tokens maps token names, as written in a config file, to the fields.
func (t *theme) tokens() map[string]*color.Color {
	return map[string]*color.Color{
		"background":     &t.background,
		"surface":        &t.surface,
		"border":         &t.border,
		"border_focused": &t.borderFocused,
		"text":           &t.text,
		"muted":          &t.muted,
		"accent":         &t.accent,
		"busy":           &t.busy,
		"idle":           &t.idle,
		"waiting":        &t.waiting,
		"ended":          &t.ended,
		"unknown":        &t.unknown,
		"error":          &t.err,
		"gauge_low":      &t.gaugeLow,
		"gauge_mid":      &t.gaugeMid,
		"gauge_high":     &t.gaugeHigh,
	}
}

// ThemeTokens lists the colour token names a ThemeSpec may set.
func ThemeTokens() []string {
	var t theme
	return slices.Sorted(maps.Keys(t.tokens()))
}

// BuiltinThemes lists the names of the built-in themes, the default dark
// theme first.
func BuiltinThemes() []string {
	names := make([]string, len(builtins))
	for i, t := range builtins {
		names[i] = t.name
	}
	return names
}

var rgb = lipgloss.Color

var builtins = []theme{
	{
		name: "ember", dark: true,
		background: rgb("#1b1917"), surface: rgb("#2a2724"), border: rgb("#544d46"), borderFocused: rgb("#a39a91"),
		text: rgb("#e8e4df"), muted: rgb("#968d84"), accent: rgb("#f0a35e"),
		busy: rgb("#7fb0e8"), idle: rgb("#9bb59a"), waiting: rgb("#ff7a66"), ended: rgb("#7d756d"), unknown: rgb("#c7a6e0"),
		err:      rgb("#f2675a"),
		gaugeLow: rgb("#8fb08e"), gaugeMid: rgb("#e3b55b"), gaugeHigh: rgb("#f2675a"),
	},
	{
		name: "tidepool", dark: true,
		background: rgb("#0f1820"), surface: rgb("#18242f"), border: rgb("#384b5c"), borderFocused: rgb("#86a8c2"),
		text: rgb("#d9e2e9"), muted: rgb("#8094a5"), accent: rgb("#5fc7b8"),
		busy: rgb("#82b6ea"), idle: rgb("#93b39e"), waiting: rgb("#f4a05a"), ended: rgb("#617281"), unknown: rgb("#b9a1de"),
		err:      rgb("#ec6f6f"),
		gaugeLow: rgb("#5fc7b8"), gaugeMid: rgb("#e7c66b"), gaugeHigh: rgb("#ec6f6f"),
	},
	{
		name: "paper", dark: false,
		background: rgb("#f6f3ee"), surface: rgb("#e8e3da"), border: rgb("#bcb3a4"), borderFocused: rgb("#6b6258"),
		text: rgb("#2a2621"), muted: rgb("#6e665d"), accent: rgb("#b0520e"),
		busy: rgb("#2c5fae"), idle: rgb("#3f7442"), waiting: rgb("#c23a12"), ended: rgb("#7b7369"), unknown: rgb("#7646a6"),
		err:      rgb("#b3221c"),
		gaugeLow: rgb("#3f7442"), gaugeMid: rgb("#9a6408"), gaugeHigh: rgb("#b3221c"),
	},
	{
		name: "contrast", dark: true,
		background: rgb("#000000"), surface: rgb("#262626"), border: rgb("#8a8a8a"), borderFocused: rgb("#ffffff"),
		text: rgb("#ffffff"), muted: rgb("#d0d0d0"), accent: rgb("#ffd75f"),
		busy: rgb("#5fd7ff"), idle: rgb("#5fff87"), waiting: rgb("#ff5f5f"), ended: rgb("#bcbcbc"), unknown: rgb("#ff87ff"),
		err:      rgb("#ff5f5f"),
		gaugeLow: rgb("#5fff87"), gaugeMid: rgb("#ffd75f"), gaugeHigh: rgb("#ff5f5f"),
	},
}

const (
	defaultDarkTheme  = "ember"
	defaultLightTheme = "paper"
)

func builtin(name string) (theme, bool) {
	for _, t := range builtins {
		if t.name == name {
			return t, true
		}
	}
	return theme{}, false
}

// MaxThemeChain is the most custom themes one chain of bases may hold, the
// theme itself included. The config validator enforces the same limit.
const MaxThemeChain = 5

// resolveThemes returns every available theme, built-ins first and then the
// custom ones sorted by name.
func resolveThemes(custom map[string]ThemeSpec) ([]theme, error) {
	all := slices.Clone(builtins)
	for _, name := range slices.Sorted(maps.Keys(custom)) {
		if _, clash := builtin(name); clash {
			return nil, optionErr("themes."+name, "custom theme %q has the name of a built-in theme", name)
		}
		t, err := resolveCustom(name, custom)
		if err != nil {
			return nil, err
		}
		all = append(all, t)
	}
	return all, nil
}

// resolveCustom builds one custom theme by following its chain of bases down
// to a built-in and applying each theme's colours on the way back up. A base
// is a custom theme when one has that name, else a built-in. A cycle, an
// unknown base and a chain longer than MaxThemeChain are errors that name the
// key at fault.
func resolveCustom(name string, custom map[string]ThemeSpec) (theme, error) {
	chain := []string{name}
	var t theme
	for cur := name; ; {
		base := custom[cur].Base
		if base == "" {
			base = defaultDarkTheme
		}
		if _, isCustom := custom[base]; isCustom {
			if i := slices.Index(chain, base); i >= 0 {
				return theme{}, optionErr("themes."+cur+".base", "theme %q: base %q is part of a cycle (%s)",
					cur, base, strings.Join(append(slices.Clone(chain[i:]), base), " -> "))
			}
			if len(chain) >= MaxThemeChain {
				return theme{}, optionErr("themes."+name+".base",
					"theme %q: its chain of bases is deeper than %d custom themes (%s -> %s ...)",
					name, MaxThemeChain, strings.Join(chain, " -> "), base)
			}
			chain = append(chain, base)
			cur = base
			continue
		}
		b, ok := builtin(base)
		if !ok {
			return theme{}, optionErr("themes."+cur+".base",
				"theme %q: base %q is neither a custom theme nor a built-in theme (built-ins: %s)",
				cur, base, strings.Join(BuiltinThemes(), ", "))
		}
		t = b
		break
	}
	for _, n := range slices.Backward(chain) {
		var err error
		if t, err = customTheme(n, custom[n], t); err != nil {
			return theme{}, err
		}
	}
	return t, nil
}

// customTheme returns base renamed to name with the spec's colours applied.
func customTheme(name string, spec ThemeSpec, t theme) (theme, error) {
	t.name = name
	tokens := t.tokens()
	for _, tok := range slices.Sorted(maps.Keys(spec.Colors)) {
		field, ok := tokens[tok]
		if !ok {
			return theme{}, optionErr("themes."+name+"."+tok, "theme %q: unknown colour token %q (have %s)",
				name, tok, strings.Join(ThemeTokens(), ", "))
		}
		col, err := parseColor(spec.Colors[tok])
		if err != nil {
			return theme{}, &OptionError{Key: "themes." + name + "." + tok, Err: fmt.Errorf("theme %q: %s: %w", name, tok, err)}
		}
		*field = col
	}
	return t, nil
}

func parseColor(s string) (color.Color, error) {
	if hex, ok := strings.CutPrefix(s, "#"); ok {
		if len(hex) == 3 || len(hex) == 6 {
			if _, err := strconv.ParseUint(hex, 16, 32); err == nil {
				return lipgloss.Color(s), nil
			}
		}
		return nil, fmt.Errorf("bad colour %q: want #rgb or #rrggbb", s)
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 255 {
		return lipgloss.Color(s), nil
	}
	return nil, fmt.Errorf("bad colour %q: want #rrggbb or 0 to 255", s)
}
