package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func themeByName(t *testing.T, all []theme, name string) theme {
	t.Helper()
	for _, th := range all {
		if th.name == name {
			return th
		}
	}
	t.Fatalf("theme %q not resolved", name)
	return theme{}
}

func colorOf(t *testing.T, th theme, token string) string {
	t.Helper()
	c, ok := th.tokens()[token]
	if !ok {
		t.Fatalf("no token %q", token)
	}
	r, g, b, _ := (*c).RGBA()
	return fmt.Sprintf("%04x%04x%04x", r, g, b)
}

func TestCustomThemeBases(t *testing.T) {
	paper, _ := builtin("paper")
	ember, _ := builtin("ember")

	t.Run("a custom theme may be based on another custom theme", func(t *testing.T) {
		all, err := resolveThemes(map[string]ThemeSpec{
			"child":  {Base: "parent", Colors: map[string]string{"busy": "#010203"}},
			"parent": {Base: "paper", Colors: map[string]string{"accent": "#ff8800"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		child, parent := themeByName(t, all, "child"), themeByName(t, all, "parent")
		if child.dark || child.name != "child" {
			t.Errorf("child = %q dark=%v, want child on the light paper base", child.name, child.dark)
		}
		if got, want := colorOf(t, child, "accent"), colorOf(t, parent, "accent"); got != want {
			t.Errorf("child accent %s, want the parent's %s", got, want)
		}
		if got, want := colorOf(t, child, "background"), colorOf(t, paper, "background"); got != want {
			t.Errorf("child background %s, want paper's %s", got, want)
		}
		if got := colorOf(t, child, "busy"); got == colorOf(t, parent, "busy") {
			t.Error("child's own busy colour did not override the parent's")
		}
		if got, want := colorOf(t, parent, "busy"), colorOf(t, paper, "busy"); got != want {
			t.Error("the parent changed when the child overrode a token")
		}
	})

	t.Run("a three-level chain resolves through every level", func(t *testing.T) {
		all, err := resolveThemes(map[string]ThemeSpec{
			"a": {Base: "b", Colors: map[string]string{"error": "#aa0000"}},
			"b": {Base: "c", Colors: map[string]string{"muted": "#00bb00", "error": "#bb0000"}},
			"c": {Base: "paper", Colors: map[string]string{"text": "#0000cc", "muted": "#cc0000", "error": "#cc0000"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		a := themeByName(t, all, "a")
		for token, want := range map[string]string{
			"error": "#aa0000", // a's own
			"muted": "#00bb00", // b's
			"text":  "#0000cc", // c's
		} {
			if got := colorOf(t, a, token); got != rgbOf(t, want) {
				t.Errorf("a.%s = %s, want %s", token, got, rgbOf(t, want))
			}
		}
		if got, want := colorOf(t, a, "background"), colorOf(t, paper, "background"); got != want {
			t.Errorf("a.background = %s, want paper's %s", got, want)
		}
		if a.dark {
			t.Error("a should inherit paper's light setting")
		}
	})

	t.Run("custom themes are listed after the built-ins by name", func(t *testing.T) {
		all, err := resolveThemes(map[string]ThemeSpec{"z": {Base: "a"}, "a": {}})
		if err != nil {
			t.Fatal(err)
		}
		n := len(builtins)
		if len(all) != n+2 || all[n].name != "a" || all[n+1].name != "z" {
			t.Errorf("order wrong: %d themes", len(all))
		}
		if got, want := colorOf(t, all[n+1], "background"), colorOf(t, ember, "background"); got != want {
			t.Error("z via a, which has no base, should end on the default ember")
		}
	})
}

// rgbOf is the colour a config value such as "#aa0000" resolves to.
func rgbOf(t *testing.T, col string) string {
	t.Helper()
	c, err := parseColor(col)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("%04x%04x%04x", r, g, b)
}

func TestCustomThemeBaseErrors(t *testing.T) {
	chain := func(n int) map[string]ThemeSpec {
		m := map[string]ThemeSpec{}
		for i := range n {
			m[fmt.Sprintf("t%02d", i)] = ThemeSpec{Base: fmt.Sprintf("t%02d", i+1)}
		}
		m[fmt.Sprintf("t%02d", n)] = ThemeSpec{Base: "paper"}
		return m
	}
	tests := []struct {
		name   string
		themes map[string]ThemeSpec
		key    string
		want   []string
	}{
		{"unknown base", map[string]ThemeSpec{"x": {Base: "neon"}}, "themes.x.base", []string{`base "neon"`}},
		{"unknown base down a chain", map[string]ThemeSpec{"a": {Base: "b"}, "b": {Base: "neon"}},
			"themes.b.base", []string{`base "neon"`}},
		{"two-theme cycle", map[string]ThemeSpec{"a": {Base: "b"}, "b": {Base: "a"}},
			"themes.b.base", []string{"cycle", "a -> b -> a"}},
		{"theme based on itself", map[string]ThemeSpec{"a": {Base: "a"}},
			"themes.a.base", []string{"cycle", "a -> a"}},
		{"cycle behind a tail", map[string]ThemeSpec{"c": {Base: "a"}, "a": {Base: "b"}, "b": {Base: "a"}},
			"themes.b.base", []string{"cycle", "a -> b -> a"}},
		{"chain too deep", chain(MaxThemeChain), "themes.t00.base", []string{"deeper than", fmt.Sprint(MaxThemeChain)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveThemes(tt.themes)
			if err == nil {
				t.Fatal("want an error")
			}
			var oe *OptionError
			if !errors.As(err, &oe) || oe.Key != tt.key {
				t.Errorf("err = %v, want key %q (got %+v)", err, tt.key, oe)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q, want it to contain %q", err, w)
				}
			}
		})
	}

	t.Run("a chain of exactly the limit is fine", func(t *testing.T) {
		if _, err := resolveThemes(chain(MaxThemeChain - 1)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a custom theme named like a built-in is still rejected", func(t *testing.T) {
		_, err := resolveThemes(map[string]ThemeSpec{"paper": {Base: "ember"}})
		var oe *OptionError
		if !errors.As(err, &oe) || oe.Key != "themes.paper" {
			t.Errorf("err = %v", err)
		}
	})
}

// A theme chosen by name must start up when it sits on a custom base.
func TestStartsOnThemeWithCustomBase(t *testing.T) {
	h := newHarness(t, 120, 35, func(o *Options) {
		o.Theme = "child"
		o.Themes = map[string]ThemeSpec{
			"child":  {Base: "parent"},
			"parent": {Base: "paper", Colors: map[string]string{"accent": "#ff8800"}},
		}
	})
	if got := h.m.env.st.theme; got.name != "child" || got.dark {
		t.Fatalf("theme = %s dark=%v, want child on the paper base", got.name, got.dark)
	}
}
