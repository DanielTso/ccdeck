// Package cmd defines ccdeck's command-line interface.
package cmd

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/tui"
)

// Build information, overridden at build time via -ldflags (see Makefile).
var (
	version = "dev"
	commit  = "none"
)

// NewRootCmd builds the root command and its subcommands.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ccdeck",
		Short: "Terminal dashboard for Claude Code sessions",
		Long: `ccdeck is a companion to Claude Code. It reads the files Claude Code keeps
under ~/.claude, shows every session across every project, follows the live
ones, and hands the terminal to claude when you want to resume one.`,
		Version:       version + " (" + commit + ")",
		SilenceUsage:  true,
		SilenceErrors: false,
		Args:          cobra.NoArgs,
		RunE:          runDashboard,
	}
	root.SetVersionTemplate("ccdeck {{.Version}}\n")

	flags := root.PersistentFlags()
	flags.String("claude-dir", "", "Claude Code directory (default $CLAUDE_CONFIG_DIR, else ~/.claude)")
	flags.String("config", "", "config file (default $XDG_CONFIG_HOME/ccdeck/config.toml)")
	flags.String("theme", "", "colour theme, overriding the config file")
	flags.Bool("demo", false, "use synthetic demo data instead of reading the Claude directory")

	root.AddCommand(newListCmd(), newShowCmd(), newDoctorCmd(), newVersionCmd())
	return root
}

// optionSource says where a value the UI rejected came from: --theme, or
// the config file and the key in it, as the config package's own errors do.
func optionSource(err error, cfg loadedConfig, flagTheme string) error {
	var oe *tui.OptionError
	switch {
	case !errors.As(err, &oe):
		return err
	case oe.Key == "theme" && flagTheme != "":
		return fmt.Errorf("--theme: %w", oe.Err)
	case cfg.found:
		return fmt.Errorf("config: %s: key %s: %w", cfg.path, oe.Key, oe.Err)
	}
	return err
}

// Execute runs the CLI.
func Execute() error {
	return NewRootCmd().Execute()
}

// runDashboard runs the TUI until the user quits. Nothing is logged while it
// runs, because the screen belongs to the UI; doctor reports what the index
// would have warned about.
func runDashboard(cmd *cobra.Command, _ []string) error {
	s := settingsOf(cmd)
	cfg, err := loadConfig(s)
	if err != nil {
		return err
	}
	st, err := openStore(s, cfg.Config, nil, true)
	if err != nil {
		return err
	}
	m, err := tui.New(tuiOptions(cfg.Config, s.theme, st, newLauncher(cfg.Config, s)))
	err = optionSource(err, cfg, s.theme)
	if err == nil {
		_, err = tea.NewProgram(m, tea.WithContext(cmd.Context())).Run()
	}
	return errors.Join(err, st.Close())
}
