package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestDefaultPath(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{"xdg", "/xdg/conf", "/home/u", "/xdg/conf/ccdeck/config.toml"},
		{"xdg unset", "", "/home/u", "/home/u/.config/ccdeck/config.toml"},
		{"xdg relative is ignored", "rel/dir", "/home/u", "/home/u/.config/ccdeck/config.toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)
			got, err := DefaultPath()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("DefaultPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Load of a missing file = %+v, want defaults", got)
	}
}

func TestLoadUsesDefaultPathWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, filepath.Join(dir, "ccdeck", "config.toml"), "theme = \"dusk\"\n")
	got, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dusk" {
		t.Errorf("Theme = %q, want dusk", got.Theme)
	}
}

func TestLoadDirectoryIsAnError(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("want an error for a directory")
	}
}

func TestParseValid(t *testing.T) {
	const src = `
theme = "mine"

[layout]
preview_ratio = 0.4
live_panes = 6
group_by_project = true

[resume]
mode = "tmux-split"
claude = "/opt/bin/claude"

[keys]
quit = ["q"]
fork = ["f", "ctrl+f"]

[themes.mine]
base = "ember"
accent = "#f80"
border = 240
text = "#FFFFFF"

[themes.other]
base = "mine"

[prices."model-x.1"]
input = 1.5
cache_read = 0.15
`
	got, err := parse([]byte(src), "test.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Theme:  "mine",
		Layout: Layout{PreviewRatio: 0.4, LivePanes: 6, GroupByProject: true},
		Resume: Resume{Mode: "tmux-split", Claude: "/opt/bin/claude"},
		Keys:   map[string][]string{"quit": {"q"}, "fork": {"f", "ctrl+f"}},
		Themes: map[string]Theme{
			"mine":  {Base: "ember", Colors: map[string]string{"accent": "#f80", "border": "240", "text": "#FFFFFF"}},
			"other": {Base: "mine", Colors: map[string]string{}},
		},
		Prices: map[string]Price{"model-x.1": {Input: 1.5, CacheRead: 0.15}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parse =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParsePartialKeepsDefaults(t *testing.T) {
	got, err := parse([]byte("[layout]\nlive_panes = 2\n"), "test.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Layout.LivePanes = 2
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parse = %+v, want %+v", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // substrings the error must contain
	}{
		{"syntax", "theme = \n", []string{"test.toml", "line 1"}},
		{"unknown top-level key", "colour = \"x\"\n", []string{"test.toml", "unknown key colour"}},
		{"unknown nested key", "[layout]\npreview_ratoi = 0.5\n", []string{"unknown key layout.preview_ratoi"}},
		{"unknown price key", "[prices.\"m.1\"]\ninptu = 1\n", []string{`unknown key prices."m.1".inptu`}},
		{"wrong type", "[layout]\nlive_panes = \"four\"\n", []string{"test.toml", "live_panes"}},
		{"bad mode", "[resume]\nmode = \"sideways\"\n", []string{"test.toml", "resume.mode", `"sideways"`}},
		{"empty claude", "[resume]\nclaude = \" \"\n", []string{"resume.claude"}},
		{"ratio too big", "[layout]\npreview_ratio = 1.5\n", []string{"layout.preview_ratio"}},
		{"ratio too small", "[layout]\npreview_ratio = 0\n", []string{"layout.preview_ratio"}},
		{"ratio nan", "[layout]\npreview_ratio = nan\n", []string{"layout.preview_ratio"}},
		{"no panes", "[layout]\nlive_panes = 0\n", []string{"layout.live_panes"}},
		{"too many panes", "[layout]\nlive_panes = 17\n", []string{"layout.live_panes"}},
		{"empty key list", "[keys]\nquit = []\n", []string{"keys.quit", "at least one"}},
		{"blank key", "[keys]\nquit = [\"q\", \"\"]\n", []string{"keys.quit", "empty key"}},
		{"bad colour word", "[themes.t]\naccent = \"orange\"\n", []string{"themes.t.accent", `"orange"`}},
		{"bad colour hex length", "[themes.t]\naccent = \"#ff88\"\n", []string{"themes.t.accent"}},
		{"bad colour hex digit", "[themes.t]\naccent = \"#ggg\"\n", []string{"themes.t.accent"}},
		{"colour out of ansi range", "[themes.t]\naccent = 256\n", []string{"themes.t.accent", `"256"`}},
		{"negative ansi colour", "[themes.t]\naccent = -1\n", []string{"themes.t.accent"}},
		{"colour wrong type", "[themes.t]\naccent = true\n", []string{"themes.t.accent"}},
		{"base wrong type", "[themes.t]\nbase = 3\n", []string{"themes.t.base"}},
		{"blank base", "[themes.t]\nbase = \" \"\n", []string{"themes.t.base"}},
		{"theme cycle", "[themes.a]\nbase = \"b\"\n[themes.b]\nbase = \"a\"\n", []string{"themes.a.base", "ancestor"}},
		{"theme is its own base", "[themes.a]\nbase = \"a\"\n", []string{"themes.a.base", "ancestor"}},
		{"negative price", "[prices.\"m.1\"]\noutput = -0.01\n", []string{`prices."m.1".output`}},
		{"nan price", "[prices.m]\ninput = nan\n", []string{"prices.m.input"}},
		{"infinite price", "[prices.m]\ncache_read = inf\n", []string{"prices.m.cache_read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.src), "test.toml")
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestParseReportsEveryUnknownKey(t *testing.T) {
	_, err := parse([]byte("a = 1\nb = 2\n"), "test.toml")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, k := range []string{"unknown key a", "unknown key b"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error %q does not contain %q", err, k)
		}
	}
}

func TestValidColor(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"#fff", true},
		{"#FfF", true},
		{"#a1b2c3", true},
		{"0", true},
		{"7", true},
		{"255", true},
		{"256", false},
		{"-1", false},
		{"+5", false},
		{"", false},
		{"#", false},
		{"#ff", false},
		{"#ffff", false},
		{"#fffff", false},
		{"#fffffff", false},
		{"#12345g", false},
		{"fff", false},
		{"red", false},
		{"1.5", false},
		{"99999999999999999999", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := validColor(tt.in); got != tt.want {
				t.Errorf("validColor(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestExampleIsTheDefaults(t *testing.T) {
	got, err := parse([]byte(Example()), "example")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Example loads as %+v, want the defaults %+v", got, Default())
	}
}

// settingLine matches the commented-out settings in the example: "# [table]"
// and "# key = value". Prose lines must not look like either.
var settingLine = regexp.MustCompile(`^# (\[[^\]]+\]|[a-z0-9_]+ = .+)$`)

func TestExampleWithEverythingUncommentedLoads(t *testing.T) {
	var b strings.Builder
	n := 0
	for _, line := range strings.Split(Example(), "\n") {
		if m := settingLine.FindStringSubmatch(line); m != nil {
			line = m[1]
			n++
		}
		b.WriteString(line + "\n")
	}
	if n == 0 {
		t.Fatal("no commented-out settings found in the example")
	}
	got, err := parse([]byte(b.String()), "example")
	if err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if got.Theme != "ember" {
		t.Errorf("Theme = %q, want ember", got.Theme)
	}
	if !reflect.DeepEqual(got.Keys["quit"], []string{"q", "ctrl+c"}) {
		t.Errorf("Keys[quit] = %v", got.Keys["quit"])
	}
	if got.Themes["mine"].Colors["accent"] != "#ff8800" || got.Themes["mine"].Base != "ember" {
		t.Errorf("Themes[mine] = %+v", got.Themes["mine"])
	}
	if got.Prices["example-model"].CacheWrite1h != 6.0 {
		t.Errorf("Prices[example-model] = %+v", got.Prices["example-model"])
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
