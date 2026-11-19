package tui

import (
	"errors"
	"fmt"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Options configures the application. It holds plain values only: the
// caller translates its own configuration into it.
type Options struct {
	// Store is where sessions come from. Required.
	Store core.Store
	// Launcher turns resume, fork, new and focus into commands. Required.
	Launcher core.Launcher
	// Theme is a built-in theme name or a key of Themes. Empty picks a
	// built-in theme that suits the terminal background.
	Theme string
	// Themes are custom themes by name.
	Themes map[string]ThemeSpec
	// Layout holds layout preferences.
	Layout Layout
	// Keys overrides key bindings: action name to the keys that trigger it,
	// spelled as Bubble Tea spells them ("q", "ctrl+c", "enter", "space",
	// "shift+tab"). Actions not listed keep their defaults. See Actions.
	Keys map[string][]string
	// Now is the clock used for relative times. Nil means time.Now.
	Now func() time.Time
	// Ready is closed when the store's start-up scan is complete. Until
	// then the lists keep their top row selected as sessions arrive; after
	// it the selection stays on its session. Nil means the store is
	// complete from the start.
	Ready <-chan struct{}
}

// Layout holds layout preferences. Zero values mean defaults.
type Layout struct {
	// PreviewRatio is the share of the width given to the Sessions preview
	// pane, between 0.2 and 0.8. Zero means 0.55.
	PreviewRatio float64
	// LivePanes is the number of panes in the Live grid, 1 to 16. Zero
	// means 4.
	LivePanes int
	// GroupByProject starts the Sessions list grouped by project.
	GroupByProject bool
}

// ThemeSpec is a custom theme: a built-in theme with some colour tokens
// replaced.
type ThemeSpec struct {
	// Base is the built-in theme to start from. Empty means "ember".
	Base string
	// Colors maps token names (see ThemeTokens) to colours written as
	// "#rrggbb", "#rgb" or an ANSI colour number "0" to "255".
	Colors map[string]string
}

const (
	defaultPreviewRatio = 0.55
	defaultLivePanes    = 4
)

// OptionError is a rejected option value. Key names the option the way the
// config file spells it ("theme", "themes.<name>", "themes.<name>.base",
// "themes.<name>.<token>", "keys.<action>"), so the caller can say where the
// value came from.
type OptionError struct {
	Key string
	Err error
}

func (e *OptionError) Error() string { return "tui: " + e.Err.Error() }

func (e *OptionError) Unwrap() error { return e.Err }

func optionErr(key string, format string, args ...any) error {
	return &OptionError{Key: key, Err: fmt.Errorf(format, args...)}
}

func (o Options) validate() error {
	if o.Store == nil {
		return errors.New("tui: no store")
	}
	if o.Launcher == nil {
		return errors.New("tui: no launcher")
	}
	if r := o.Layout.PreviewRatio; r != 0 && (r < 0.2 || r > 0.8) {
		return fmt.Errorf("tui: preview ratio %g is outside 0.2 to 0.8", r)
	}
	if n := o.Layout.LivePanes; n < 0 || n > 16 {
		return fmt.Errorf("tui: live panes %d is outside 1 to 16", n)
	}
	return nil
}

func (l Layout) withDefaults() Layout {
	if l.PreviewRatio == 0 {
		l.PreviewRatio = defaultPreviewRatio
	}
	if l.LivePanes == 0 {
		l.LivePanes = defaultLivePanes
	}
	return l
}
