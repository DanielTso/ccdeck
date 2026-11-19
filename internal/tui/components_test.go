package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/DanielTso/ccdeck/internal/core"
)

func TestTruncateByDisplayWidth(t *testing.T) {
	tests := []struct {
		in   string
		w    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello w…"},
		{"認証フロー", 6, "認証…"}, // each kanji is two cells
		{"認証フロー", 5, "認証…"}, // a kanji never splits
		{"toggle 🌙 now", 8, "toggle …"},
		{"anything", 0, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.w)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.w, got, tt.want)
		}
		if width(got) > tt.w {
			t.Errorf("truncate(%q, %d) is %d wide", tt.in, tt.w, width(got))
		}
	}
	for _, s := range []string{"認証フローの改善", "dark mode 🌙", "plain"} {
		for w := 1; w < 14; w++ {
			if got := width(fit(s, w)); got != w {
				t.Errorf("fit(%q, %d) is %d wide", s, w, got)
			}
			if got := width(fitRight(s, w)); got != w {
				t.Errorf("fitRight(%q, %d) is %d wide", s, w, got)
			}
		}
	}
	if got := truncateLeft("/home/demo/src/orbit-api", 12); got != "…c/orbit-api" {
		t.Errorf("truncateLeft = %q", got)
	}
	if got := oneLine("a\n\tb   c\r\n"); got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
}

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		ago  time.Duration
		want string
	}{
		{-time.Minute, "now"}, {30 * time.Second, "now"}, {12 * time.Minute, "12m"},
		{3 * time.Hour, "3h"}, {50 * time.Hour, "2d"}, {20 * 24 * time.Hour, "2w"},
		{100 * 24 * time.Hour, "3mo"}, {800 * 24 * time.Hour, "2y"},
	}
	for _, tt := range tests {
		if got := relTime(now, now.Add(-tt.ago)); got != tt.want {
			t.Errorf("relTime(-%v) = %q, want %q", tt.ago, got, tt.want)
		}
	}
	if relTime(now, time.Time{}) != "—" {
		t.Error("zero time should be a dash")
	}
}

func TestFormatting(t *testing.T) {
	tokens := map[int64]string{0: "0", 950: "950", 9_500: "9.5k", 124_300: "124k", 3_290_000: "3.29M", 42_000_000: "42.0M", 3e9: "3.00B"}
	for n, want := range tokens {
		if got := fmtTokens(n); got != want {
			t.Errorf("fmtTokens(%d) = %q, want %q", n, got, want)
		}
	}
	costs := []struct {
		kind core.CostKind
		usd  float64
		want string
	}{
		{core.CostReported, 3.18, "$3.18"}, {core.CostEstimated, 2.691, "~$2.69"},
		{core.CostEstimated, 0.001, "~<$0.01"}, {core.CostNone, 0, "—"},
	}
	for _, c := range costs {
		if got := fmtCost(c.kind, c.usd); got != c.want {
			t.Errorf("fmtCost(%s, %g) = %q, want %q", c.kind, c.usd, got, c.want)
		}
	}
	models := map[string]string{
		"claude-opus-5-5": "opus 5.5", "claude-sonnet-5": "sonnet 5",
		"claude-haiku-4-5-20251001": "haiku 4.5", "acme-research-preview": "acme-research-preview",
		"claude-instant": "instant", "": "",
	}
	for id, want := range models {
		if got := shortModel(id); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", id, got, want)
		}
	}
	if projectName("") != "—" || projectName("/a/b/kiln") != "kiln" {
		t.Error("projectName")
	}
}

func TestStatusNeverColourAlone(t *testing.T) {
	st := newStyles(builtins[0])
	seen := map[string]core.Status{}
	for _, s := range []core.Status{core.StatusBusy, core.StatusIdle, core.StatusWaiting, core.StatusEnded, core.StatusUnknown, "weird"} {
		plain := ansi.Strip(st.badge(s))
		g, word, _ := strings.Cut(plain, " ")
		if prev, dup := seen[g]; dup && prev != s && s != "weird" {
			t.Errorf("%s and %s share the glyph %q", prev, s, g)
		}
		seen[g] = s
		if word == "" {
			t.Errorf("badge for %s has no word", s)
		}
	}
	if ansi.Strip(st.badge("weird")) != "? unknown" {
		t.Error("unrecognised status should read as unknown")
	}
}

func TestGauge(t *testing.T) {
	st := newStyles(builtins[0])
	s := core.Session{ContextTokens: 124_300, ContextWindow: 200_000}
	for _, w := range []int{4, 9, 13, 30} {
		g := ansi.Strip(st.gauge(s, w))
		if width(g) != w || !strings.HasSuffix(g, "62%") {
			t.Errorf("gauge(62%%, %d) = %q", w, g)
		}
	}
	if g := ansi.Strip(st.gauge(s, 9)); g != "━━──  62%" {
		t.Errorf("gauge bar = %q", g)
	}
	full := core.Session{ContextTokens: 250_000, ContextWindow: 200_000}
	if g := ansi.Strip(st.gauge(full, 9)); g != "━━━━ 100%" {
		t.Errorf("over-full gauge = %q", g)
	}
	tiny := core.Session{ContextTokens: 100, ContextWindow: 200_000}
	if g := ansi.Strip(st.gauge(tiny, 9)); !strings.HasPrefix(g, "━") {
		t.Errorf("any use should show at least one cell: %q", g)
	}
	unknown := core.Session{ContextTokens: 88_200}
	if g := ansi.Strip(st.gauge(unknown, 9)); g != "      88k" {
		t.Errorf("unknown window gauge = %q", g)
	}
	if g := ansi.Strip(st.gauge(core.Session{}, 9)); g != "        —" {
		t.Errorf("empty gauge = %q", g)
	}
	if st.gaugeStyle(10).GetForeground() == st.gaugeStyle(90).GetForeground() {
		t.Error("low and high gauges look the same")
	}
}

func TestOnKeepsBackgroundOnEverySegment(t *testing.T) {
	st := newStyles(builtins[0]).on(builtins[0].surface)
	for name, s := range map[string]string{
		"text": st.text.Render("x"), "muted": st.muted.Render("x"),
		"status": st.mark(core.StatusBusy), "gauge": st.gauges[0].Render("x"),
	} {
		if !strings.Contains(s, "48;") {
			t.Errorf("%s segment has no background: %q", name, s)
		}
	}
}

func TestThemeTokensAndBuiltins(t *testing.T) {
	if len(BuiltinThemes()) < 4 {
		t.Fatal("want at least four built-in themes")
	}
	var light, contrast bool
	for _, th := range builtins {
		light = light || !th.dark
		contrast = contrast || th.name == "contrast"
		for name, c := range th.tokens() {
			if *c == nil {
				t.Errorf("%s: token %s unset", th.name, name)
			}
		}
	}
	if !light || !contrast {
		t.Error("want a light and a high-contrast theme")
	}
	if len(ThemeTokens()) != 16 || len(Actions()) != 34 {
		t.Errorf("tokens %d, actions %d", len(ThemeTokens()), len(Actions()))
	}
}

// Previews and Live panes keep the line breaks of prompts and replies, so
// tables and code read as they were written, within each entry's cap.
func TestConversationKeepsLineBreaks(t *testing.T) {
	st := newStyles(builtins[0])
	text := "Here is the comparison:\n| Option | Latency |\n|---|---|\n| A | 10ms |\n| B | 20ms |"
	lines := entryLines(st, core.Entry{Kind: core.EntryText, Text: text}, 60)
	if len(lines) != 4 {
		t.Fatalf("%d lines, want the cap of 4: %q", len(lines), lines)
	}
	if got := ansi.Strip(lines[1]); strings.TrimSpace(got) != "| Option | Latency |" {
		t.Errorf("second line = %q, want the table header on its own line", got)
	}
	if got := ansi.Strip(lines[3]); !strings.HasSuffix(got, "…") {
		t.Errorf("a cut entry should end in an ellipsis: %q", got)
	}
	prompt := entryLines(st, core.Entry{Kind: core.EntryPrompt, Text: "fix this:\n```go\nfunc main() {}\n```"}, 60)
	if got := ansi.Strip(prompt[1]); strings.TrimSpace(got) != "```go" {
		t.Errorf("prompt line 2 = %q", got)
	}
}
