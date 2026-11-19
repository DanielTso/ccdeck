package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/config"
	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/demo"
	"github.com/DanielTso/ccdeck/internal/index"
	"github.com/DanielTso/ccdeck/internal/launch"
	"github.com/DanielTso/ccdeck/internal/tui"
)

// settings are the persistent flags every command shares.
type settings struct {
	claudeDir  string
	configPath string
	theme      string
	demo       bool
}

func settingsOf(cmd *cobra.Command) settings {
	f := cmd.Flags()
	var s settings
	s.claudeDir, _ = f.GetString("claude-dir")
	s.configPath, _ = f.GetString("config")
	s.theme, _ = f.GetString("theme")
	s.demo, _ = f.GetBool("demo")
	return s
}

// loadedConfig is a config and the file it came from.
type loadedConfig struct {
	config.Config
	path  string // the file read, or that would have been read
	found bool   // the file exists
}

// loadConfig reads the config file. A file named with --config must exist;
// the default file may be missing, which means defaults.
func loadConfig(s settings) (loadedConfig, error) {
	path := s.configPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return loadedConfig{}, err
		}
		path = p
	}
	_, err := os.Stat(path)
	found := err == nil
	if s.configPath != "" && !found {
		return loadedConfig{}, fmt.Errorf("config: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return loadedConfig{}, err
	}
	return loadedConfig{Config: cfg, path: path, found: found}, nil
}

// store is a core.Store that can say when its first scan is complete.
type store interface {
	core.Store
	Ready() <-chan struct{}
}

// demoStore is the demo store, which is complete from the start.
type demoStore struct{ *demo.Store }

var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

func (demoStore) Ready() <-chan struct{} { return closedChan }

// openStore opens the demo store, or the index over the Claude directory.
// animate makes the demo store move; the commands that print once leave
// it still.
func openStore(s settings, cfg config.Config, log *slog.Logger, animate bool) (store, error) {
	if s.demo {
		var every time.Duration
		if animate {
			every = time.Second
		}
		return demoStore{demo.New(demo.Options{Seed: 1, Interval: every})}, nil
	}
	st, err := index.New(index.Options{
		ClaudeDir: s.claudeDir,
		Prices:    indexPrices(cfg.Prices),
		Logger:    log,
	})
	if err != nil {
		if errors.Is(err, index.ErrClaudeDir) {
			err = fmt.Errorf("%w\n(choose the directory with --claude-dir or CLAUDE_CONFIG_DIR; ccdeck doctor shows what it finds)", err)
		}
		return nil, err
	}
	return st, nil
}

// waitReady waits for the first scan, or for ctx to end.
func waitReady(ctx context.Context, st store) error {
	select {
	case <-st.Ready():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func indexPrices(in map[string]config.Price) map[string]index.Price {
	out := make(map[string]index.Price, len(in))
	for model, p := range in {
		out[model] = index.Price{
			Input:        p.Input,
			Output:       p.Output,
			CacheRead:    p.CacheRead,
			CacheWrite5m: p.CacheWrite5m,
			CacheWrite1h: p.CacheWrite1h,
		}
	}
	return out
}

func newLauncher(cfg config.Config, s settings) *launch.Launcher {
	return launch.New(launch.Options{
		Mode:      launch.Mode(cfg.Resume.Mode),
		Claude:    cfg.Resume.Claude,
		ClaudeDir: launchClaudeDir(s, os.Getenv("CLAUDE_CONFIG_DIR")),
	})
}

// launchClaudeDir is the Claude directory to hand to claude as
// CLAUDE_CONFIG_DIR, so a resumed or new session uses the directory ccdeck
// is showing. It is empty, meaning leave the environment alone, for the
// demo and when the directory is the default ~/.claude with the variable
// unset. The result is absolute, because claude runs in another directory.
func launchClaudeDir(s settings, env string) string {
	if s.demo || (s.claudeDir == "" && env == "") {
		return ""
	}
	p, err := claude.ResolvePaths(s.claudeDir)
	if err != nil {
		return ""
	}
	dir := p.Dir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if env == "" {
		if home, err := os.UserHomeDir(); err == nil && dir == filepath.Join(home, ".claude") {
			return ""
		}
	}
	return dir
}

// tuiOptions translates the config into the UI's options. theme, when not
// empty, overrides the config's theme.
func tuiOptions(cfg config.Config, theme string, st core.Store, l core.Launcher) tui.Options {
	themes := make(map[string]tui.ThemeSpec, len(cfg.Themes))
	for name, t := range cfg.Themes {
		themes[name] = tui.ThemeSpec{Base: t.Base, Colors: t.Colors}
	}
	if theme == "" {
		theme = cfg.Theme
	}
	o := tui.Options{
		Store:    st,
		Launcher: l,
		Theme:    theme,
		Themes:   themes,
		Layout: tui.Layout{
			PreviewRatio:   cfg.Layout.PreviewRatio,
			LivePanes:      cfg.Layout.LivePanes,
			GroupByProject: cfg.Layout.GroupByProject,
		},
		Keys: cfg.Keys,
	}
	if r, ok := st.(interface{ Ready() <-chan struct{} }); ok {
		o.Ready = r.Ready()
	}
	return o
}
