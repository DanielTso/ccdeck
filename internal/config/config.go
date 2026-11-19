// Package config loads and validates ccdeck's TOML config file.
//
// It holds plain values only. It knows nothing about Lip Gloss, Bubble Tea or
// any other ccdeck package: the caller translates a Config into whatever the
// UI needs. Names that only the UI can check (built-in theme names, theme
// token names, key action names) are passed through unvalidated.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Resume modes: how a session is opened.
const (
	ModeAuto       = "auto"        // tmux-window inside tmux, suspend otherwise
	ModeSuspend    = "suspend"     // hand the terminal to claude until it exits
	ModeTmuxWindow = "tmux-window" // open a new tmux window
	ModeTmuxSplit  = "tmux-split"  // split the current tmux window
)

// Bounds enforced on [layout]. They are the ranges the UI accepts.
const (
	MinPreviewRatio = 0.2
	MaxPreviewRatio = 0.8
	MinLivePanes    = 1
	MaxLivePanes    = 16
)

// Config is the parsed and validated configuration.
type Config struct {
	// Theme names a built-in theme or a key of Themes. Empty means choose
	// light or dark from the terminal background.
	Theme  string
	Layout Layout
	Resume Resume
	// Keys maps an action to the keys that trigger it. Actions that are not
	// listed keep their default keys.
	Keys map[string][]string
	// Themes holds the custom themes by name.
	Themes map[string]Theme
	// Prices holds the per-model price overrides by model id.
	Prices map[string]Price
}

// Layout is the shape of the screen.
type Layout struct {
	PreviewRatio   float64 `toml:"preview_ratio"` // share of the width given to the preview pane
	LivePanes      int     `toml:"live_panes"`    // panes in the live grid
	GroupByProject bool    `toml:"group_by_project"`
}

// Resume says how sessions are opened.
type Resume struct {
	Mode   string `toml:"mode"`   // one of the Mode constants
	Claude string `toml:"claude"` // the claude binary: a name looked up on PATH, or a path
}

// Theme is a custom theme: a built-in or custom theme to start from, and
// colour overrides on top of it.
type Theme struct {
	Base   string            // empty means the default theme
	Colors map[string]string // token name to "#rgb", "#rrggbb" or an ANSI number "0".."255"
}

// Price is a model's price in dollars per million tokens.
type Price struct {
	Input        float64 `toml:"input"`
	Output       float64 `toml:"output"`
	CacheRead    float64 `toml:"cache_read"`
	CacheWrite5m float64 `toml:"cache_write_5m"`
	CacheWrite1h float64 `toml:"cache_write_1h"`
}

// Default returns the configuration used when no file exists. The returned
// maps are non-nil and safe to modify.
func Default() Config {
	return Config{
		Layout: Layout{PreviewRatio: 0.55, LivePanes: 4},
		Resume: Resume{Mode: ModeAuto, Claude: "claude"},
		Keys:   map[string][]string{},
		Themes: map[string]Theme{},
		Prices: map[string]Price{},
	}
}

// DefaultPath is where the config file lives when no path is given:
// $XDG_CONFIG_HOME/ccdeck/config.toml, or ~/.config/ccdeck/config.toml.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "ccdeck", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot find the config directory: %w", err)
	}
	return filepath.Join(home, ".config", "ccdeck", "config.toml"), nil
}

// Load reads the config file at path, or at DefaultPath when path is empty.
// A file that does not exist yields Default() and no error. A file that does
// exist but cannot be read or is invalid yields an error naming the file and,
// where there is one, the offending key.
func Load(path string) (Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return parse(data, path)
}

// rawFile mirrors the file layout. Themes are decoded loosely so that colours
// may be written as strings or as integers.
type rawFile struct {
	Theme  string                    `toml:"theme"`
	Layout Layout                    `toml:"layout"`
	Resume Resume                    `toml:"resume"`
	Keys   map[string][]string       `toml:"keys"`
	Themes map[string]map[string]any `toml:"themes"`
	Prices map[string]Price          `toml:"prices"`
}

// parse decodes data over the defaults and validates the result. name is used
// in error messages.
func parse(data []byte, name string) (Config, error) {
	def := Default()
	raw := rawFile{
		Theme:  def.Theme,
		Layout: def.Layout,
		Resume: def.Resume,
	}
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", name, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		errs := make([]error, len(undecoded))
		for i, k := range undecoded {
			errs[i] = fmt.Errorf("config: %s: unknown key %s", name, joinKey(k...))
		}
		return Config{}, errors.Join(errs...)
	}

	cfg := def
	cfg.Theme, cfg.Layout, cfg.Resume = raw.Theme, raw.Layout, raw.Resume
	if raw.Keys != nil {
		cfg.Keys = raw.Keys
	}
	if raw.Prices != nil {
		cfg.Prices = raw.Prices
	}
	if cfg.Themes, err = themes(raw.Themes); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", name, err)
	}
	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", name, err)
	}
	return cfg, nil
}

// themes converts the loosely decoded [themes.*] tables.
func themes(raw map[string]map[string]any) (map[string]Theme, error) {
	out := make(map[string]Theme, len(raw))
	for _, name := range sortedKeys(raw) {
		t := Theme{Colors: map[string]string{}}
		for _, token := range sortedKeys(raw[name]) {
			key := joinKey("themes", name, token)
			if token == "" {
				return nil, fmt.Errorf("key %s: empty token name", key)
			}
			switch v := raw[name][token].(type) {
			case string:
				if token == "base" {
					t.Base = v
				} else {
					t.Colors[token] = v
				}
			case int64:
				if token == "base" {
					return nil, fmt.Errorf("key %s: must be a theme name (a string)", key)
				}
				t.Colors[token] = strconv.FormatInt(v, 10)
			default:
				return nil, fmt.Errorf("key %s: must be a string or an ANSI colour number, got %T", key, v)
			}
		}
		out[name] = t
	}
	return out, nil
}

func validate(c Config) error {
	switch c.Resume.Mode {
	case ModeAuto, ModeSuspend, ModeTmuxWindow, ModeTmuxSplit:
	default:
		return fmt.Errorf("key resume.mode: %q is not one of %s, %s, %s, %s",
			c.Resume.Mode, ModeAuto, ModeSuspend, ModeTmuxWindow, ModeTmuxSplit)
	}
	if strings.TrimSpace(c.Resume.Claude) == "" {
		return errors.New("key resume.claude: must not be empty")
	}
	if r := c.Layout.PreviewRatio; !(r >= MinPreviewRatio && r <= MaxPreviewRatio) {
		return fmt.Errorf("key layout.preview_ratio: %v is outside %v to %v", r, MinPreviewRatio, MaxPreviewRatio)
	}
	if n := c.Layout.LivePanes; n < MinLivePanes || n > MaxLivePanes {
		return fmt.Errorf("key layout.live_panes: %d is outside %d to %d", n, MinLivePanes, MaxLivePanes)
	}
	if err := validateKeys(c.Keys); err != nil {
		return err
	}
	if err := validateThemes(c.Themes); err != nil {
		return err
	}
	return validatePrices(c.Prices)
}

func validateKeys(keys map[string][]string) error {
	for _, action := range sortedKeys(keys) {
		key := joinKey("keys", action)
		if action == "" {
			return fmt.Errorf("key %s: empty action name", key)
		}
		if len(keys[action]) == 0 {
			return fmt.Errorf("key %s: must list at least one key", key)
		}
		for _, k := range keys[action] {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("key %s: empty key name", key)
			}
		}
	}
	return nil
}

func validateThemes(themes map[string]Theme) error {
	for _, name := range sortedKeys(themes) {
		t := themes[name]
		if name == "" {
			return errors.New("key themes: empty theme name")
		}
		if strings.TrimSpace(t.Base) == "" && t.Base != "" {
			return fmt.Errorf("key %s: must not be blank", joinKey("themes", name, "base"))
		}
		for _, token := range sortedKeys(t.Colors) {
			if !validColor(t.Colors[token]) {
				return fmt.Errorf("key %s: %q is not a colour; use #rgb, #rrggbb or an ANSI number 0 to 255",
					joinKey("themes", name, token), t.Colors[token])
			}
		}
		// Follow the chain of custom bases; a base that is not custom is a
		// built-in and ends it.
		seen := map[string]bool{name: true}
		for b := t.Base; b != ""; b = themes[b].Base {
			if _, custom := themes[b]; !custom {
				break
			}
			if seen[b] {
				return fmt.Errorf("key %s: theme %q is its own ancestor", joinKey("themes", name, "base"), b)
			}
			seen[b] = true
		}
	}
	return nil
}

func validatePrices(prices map[string]Price) error {
	for _, model := range sortedKeys(prices) {
		p := prices[model]
		for _, f := range []struct {
			name string
			v    float64
		}{
			{"input", p.Input},
			{"output", p.Output},
			{"cache_read", p.CacheRead},
			{"cache_write_5m", p.CacheWrite5m},
			{"cache_write_1h", p.CacheWrite1h},
		} {
			if !(f.v >= 0) || math.IsInf(f.v, 0) {
				return fmt.Errorf("key %s: %v is not a non-negative price", joinKey("prices", model, f.name), f.v)
			}
		}
	}
	return nil
}

// validColor reports whether s is "#rgb", "#rrggbb" or a decimal 0 to 255.
func validColor(s string) bool {
	if hex, ok := strings.CutPrefix(s, "#"); ok {
		if len(hex) != 3 && len(hex) != 6 {
			return false
		}
		for _, r := range hex {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n <= 255
}

// joinKey renders a key path the way TOML writes it, quoting parts that are
// not bare keys (model ids contain dots, for one).
func joinKey(parts ...string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p
		if !bareKey(p) {
			out[i] = strconv.Quote(p)
		}
	}
	return strings.Join(out, ".")
}

func bareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !ok {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
