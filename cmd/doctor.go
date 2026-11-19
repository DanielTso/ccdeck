package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/config"
	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/index"
	"github.com/DanielTso/ccdeck/internal/meter"
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
			_, _ = fmt.Fprintf(w, "ccdeck %s\n", versionString())
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
	prices, thresholds, err := claude.ReadPrices(p.ConfigFile)
	if err != nil {
		row("prices", "unreadable: %v", err)
	} else {
		row("prices", "%s in %s, %d in the config file", priceCounts(prices, thresholds), tildePath(p.ConfigFile), len(cfg.Prices))
	}
	if prices == nil {
		prices = claude.Prices{}
	}
	maps.Copy(prices, indexPrices(cfg.Prices))

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	st, err := index.New(index.Options{ClaudeDir: s.claudeDir, Prices: indexPrices(cfg.Prices), MeterDir: meterDir(), Logger: log})
	if errors.Is(err, index.ErrClaudeDir) {
		_, _ = fmt.Fprintln(w, "\nIndex")
		row("status", "not run: %v", err)
		return nil
	}
	if err != nil {
		return err
	}
	if err := waitReady(ctx, st); err != nil {
		_ = st.Close()
		return err
	}
	stats := st.Stats()
	sessions := st.Sessions()
	limits, limitsAt := st.Limits()
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
	tiers := tierUsage(sessions, prices)
	if len(tiers) == 0 {
		row("price tiers", "none used (no fast-mode or long-prompt usage)")
	}
	label := "price tiers"
	for _, t := range tiers {
		row(label, "%s", t)
		label = ""
	}
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
			shortID(sess.ID), sess.Live.PID, orDash(string(sess.Live.Status)), orDash(sess.Model), contextFill(sess), cleanPath(sess.ProjectDir))
	}
	doctorMeter(w, sessions, limits, limitsAt)
	return nil
}

// statuslineSnippet is the setting that enables the status-line meter.
const statuslineSnippet = `{ "statusLine": { "type": "command", "command": "ccdeck statusline", "refreshInterval": 30 } }`

// doctorMeter reports on the status-line meter: where its reports go, and
// whether it is feeding, which is when a live session has a report written
// since its process started. When it is not, it shows how to enable it.
func doctorMeter(w io.Writer, sessions []core.Session, limits []core.RateLimit, limitsAt time.Time) {
	row := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", k, fmt.Sprintf(format, args...))
	}
	now := time.Now()
	_, _ = fmt.Fprintln(w, "\nStatus-line meter")
	dir := meterDir()
	if dir == "" {
		row("state", "unavailable: no home directory")
		return
	}
	reports, err := meter.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		row("state", "%s (not created: the helper has never run)", tildePath(dir))
	case err != nil:
		row("state", "%s: unreadable: %v", tildePath(dir), err)
	default:
		var newest time.Time
		for _, r := range reports {
			if r.Meter.At.After(newest) {
				newest = r.Meter.At
			}
		}
		if len(reports) == 0 {
			row("state", "%s (no reports)", tildePath(dir))
		} else {
			row("state", "%s (%d %s, newest %s)", tildePath(dir), len(reports), plural(len(reports), "report", "reports"), ago(newest, now))
		}
	}

	live, fed := 0, 0
	for _, s := range sessions {
		if s.Live == nil {
			continue
		}
		live++
		if s.Meter != nil && !s.Meter.At.Before(s.Live.StartedAt) {
			fed++
		}
	}
	switch {
	case fed > 0:
		row("feeding", "yes: %d of %d live %s reported since it started", fed, live, plural(live, "session", "sessions"))
	case live == 0:
		row("feeding", "unknown: no live session to check")
	default:
		row("feeding", "no: none of the %d live %s has reported", live, plural(live, "session", "sessions"))
	}
	if len(limits) == 0 {
		row("rate limits", "none reported")
	} else {
		parts := make([]string, 0, len(limits))
		for _, l := range limits {
			p := fmt.Sprintf("%s %.0f%%", l.Window, math.Floor(l.UsedPercent))
			if !l.ResetsAt.IsZero() {
				p += " (resets in " + until(now, l.ResetsAt) + ")"
			}
			parts = append(parts, p)
		}
		row("rate limits", "%s, reported %s", strings.Join(parts, ", "), ago(limitsAt, now))
	}
	// The setting is shown when the meter is not feeding a live session,
	// or has never run; with no live session and reports on file it may
	// well be enabled.
	if fed == 0 && (live > 0 || len(reports) == 0) {
		_, _ = fmt.Fprintf(w, "\n  To feed it, add this to Claude Code's settings.json (ccdeck never edits it),\n"+
			"  or wrap an existing status line with --wrap '<command>' (see ccdeck statusline --help):\n\n    %s\n", statuslineSnippet)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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

// priceCounts describes Claude Code's price cache: the models, and how many
// have fast or long-prompt rates.
func priceCounts(prices claude.Prices, thresholds claude.Thresholds) string {
	models, fast := 0, 0
	for key := range prices {
		switch _, tiers := claude.SplitUsageKey(key); {
		case len(tiers) == 0:
			models++
		case slices.Equal(tiers, []string{"fast"}):
			fast++
		}
	}
	return fmt.Sprintf("%d %s priced (%d with fast rates, %d with long-prompt rates)",
		models, plural(models, "model", "models"), fast, len(thresholds))
}

// tierUsage lists each usage key of a fast or long-prompt tier with the
// number of sessions that used it, and says when it has no price.
func tierUsage(sessions []core.Session, prices claude.Prices) []string {
	count := map[string]int{}
	for _, s := range sessions {
		for key, u := range s.Usage {
			if _, tiers := claude.SplitUsageKey(key); len(tiers) > 0 && u != (core.Usage{}) {
				count[key]++
			}
		}
	}
	var out []string
	for _, key := range slices.Sorted(maps.Keys(count)) {
		l := fmt.Sprintf("%s in %d %s", claude.DescribeUsageKey(key), count[key], plural(count[key], "session", "sessions"))
		if _, ok := prices.Lookup(key); !ok {
			l += ", no price: those sessions show tokens only"
		}
		out = append(out, l)
	}
	return out
}
