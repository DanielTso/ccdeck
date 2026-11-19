package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/config"
	"github.com/DanielTso/ccdeck/internal/index"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report what ccdeck finds: directories, counts, parse problems, live sessions",
		Long: `Report what ccdeck finds: the Claude directory and what is in it, how the
index scan went (files, skipped lines, time), the live sessions, tmux, the
config file and the terminal's colour support, followed by an example config.

Run it first when Claude Code changes a file format.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := settingsOf(cmd)
			cfg, err := loadConfig(s)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "ccdeck %s (%s)\n", version, commit)
			if s.demo {
				_, _ = fmt.Fprintln(w, "\n--demo is ignored here: doctor always inspects the Claude directory.")
			}
			if err := doctorClaudeDir(cmd.Context(), w, s, cfg.Config); err != nil {
				return err
			}
			doctorEnvironment(cmd.Context(), w, cfg)
			_, _ = fmt.Fprintf(w, "\nExample config (save as %s):\n\n%s", tildePath(cfg.path), config.Example())
			return nil
		},
	}
}

// doctorClaudeDir reports on the Claude directory and runs the index over
// it once.
func doctorClaudeDir(ctx context.Context, w io.Writer, s settings, cfg config.Config) error {
	p, err := claude.ResolvePaths(s.claudeDir)
	if err != nil {
		return err
	}
	row := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", k, fmt.Sprintf(format, args...))
	}

	_, _ = fmt.Fprintf(w, "\nClaude directory  %s\n", tildePath(p.Dir))
	if _, err := os.Stat(p.Dir); err != nil {
		row("status", "unreadable: %v", err)
	}
	projects, perr := os.ReadDir(p.ProjectsDir())
	transcripts, terr := p.Transcripts()
	switch {
	case perr != nil:
		row("projects", "unreadable: %v", perr)
	case terr != nil:
		row("projects", "%d directories; listing transcripts failed: %v", len(projects), terr)
	default:
		row("projects", "%d directories, %d session transcripts", len(projects), len(transcripts))
	}
	if ents, err := os.ReadDir(p.SessionsDir()); err != nil {
		row("sessions", "unreadable: %v", err)
	} else {
		n := 0
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".json") {
				n++
			}
		}
		live, err := claude.ReadRegistry(p.SessionsDir(), claude.SystemProcesses())
		if err != nil {
			row("sessions", "%d registry files; reading them failed: %v", n, err)
		} else {
			row("sessions", "%d registry files, %d with a live process", n, len(live))
		}
	}
	if hits, err := claude.ReadHistory(p.HistoryFile()); err != nil {
		row("history", "unreadable: %v", err)
	} else {
		row("history", "%d prompts", len(hits))
	}
	if cat, err := claude.ReadCatalog(p.CatalogDir()); err != nil {
		row("model catalog", "unreadable: %v", err)
	} else {
		row("model catalog", "%d models with a context-window size", len(cat))
	}
	if prices, err := claude.ReadPrices(p.ConfigFile); err != nil {
		row("prices", "unreadable: %v", err)
	} else {
		row("prices", "%d models priced in %s, %d in the config file", len(prices), tildePath(p.ConfigFile), len(cfg.Prices))
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	st, err := index.New(index.Options{ClaudeDir: s.claudeDir, Prices: indexPrices(cfg.Prices), Logger: log})
	if err != nil {
		return err
	}
	if err := waitReady(ctx, st); err != nil {
		_ = st.Close()
		return err
	}
	stats := st.Stats()
	sessions := st.Sessions()
	if err := st.Close(); err != nil {
		return err
	}

	live := 0
	for _, sess := range sessions {
		if sess.Live != nil {
			live++
		}
	}
	_, _ = fmt.Fprintln(w, "\nIndex")
	row("sessions", "%d, %d live", len(sessions), live)
	row("transcripts", "%d files read (main and subagent), %d continued from the cache", stats.Files, stats.Resumed)
	row("lines", "%d read, %d malformed and skipped", stats.Lines, stats.Skipped)
	row("scan time", "%s", stats.ScanTime.Round(time.Millisecond))
	if stats.Watching {
		row("watching", "%d directories", stats.Watches)
	} else {
		row("watching", "unavailable: polling only")
	}
	row("cache", "%s", orDash(tildePath(stats.Cache)))
	if logs.Len() == 0 {
		row("warnings", "none")
	} else {
		row("warnings", "")
		for line := range strings.Lines(logs.String()) {
			_, _ = fmt.Fprintf(w, "    %s", line)
		}
	}

	_, _ = fmt.Fprintln(w, "\nLive sessions")
	if live == 0 {
		_, _ = fmt.Fprintln(w, "  none")
	}
	for _, sess := range sessions {
		if sess.Live == nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "  %s  pid %-7d %-8s %-18s context %-5s %s\n",
			shortID(sess.ID), sess.Live.PID, sess.Live.Status, orDash(sess.Model), contextFill(sess), tildePath(sess.ProjectDir))
	}
	return nil
}

// doctorEnvironment reports on what resuming needs and on the terminal.
func doctorEnvironment(ctx context.Context, w io.Writer, cfg loadedConfig) {
	row := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(w, "%-17s %s\n", k, fmt.Sprintf(format, args...))
	}
	_, _ = fmt.Fprintln(w)

	inTmux := os.Getenv("TMUX") != ""
	if path, err := exec.LookPath("tmux"); err != nil {
		row("tmux", "not found (Focus and the tmux resume modes need it)")
	} else {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		out, err := exec.CommandContext(ctx, path, "-V").Output()
		cancel()
		v := strings.TrimSpace(string(out))
		if err != nil {
			v = "version unknown: " + err.Error()
		}
		row("tmux", "%s (%s); inside tmux: %s", v, path, yesNo(inTmux))
	}
	if path, err := exec.LookPath(cfg.Resume.Claude); err != nil {
		row("claude", "%q not found; set [resume] claude in the config file", cfg.Resume.Claude)
	} else {
		row("claude", "%s", path)
	}
	mode := cfg.Resume.Mode
	if mode == config.ModeAuto {
		mode = config.ModeSuspend
		if inTmux {
			mode = config.ModeTmuxWindow
		}
		mode = "auto, which means " + mode + " here"
	}
	row("resume mode", "%s", mode)

	if cfg.found {
		row("config", "%s", tildePath(cfg.path))
	} else {
		row("config", "%s (not found: using defaults)", tildePath(cfg.path))
	}
	row("colour profile", "%s (TERM=%s COLORTERM=%s NO_COLOR=%s)",
		colorprofile.Detect(w, os.Environ()), orDash(os.Getenv("TERM")), orDash(os.Getenv("COLORTERM")), orDash(os.Getenv("NO_COLOR")))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
