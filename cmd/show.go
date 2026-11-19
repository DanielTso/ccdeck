package cmd

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/core"
)

func newShowCmd() *cobra.Command {
	var asJSON bool
	var limit int
	c := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one session's summary and recent conversation",
		Long:  "Show one session's summary and recent conversation. The id may be\nabbreviated to any prefix that matches only one session.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := settingsOf(cmd)
			cfg, err := loadConfig(s)
			if err != nil {
				return err
			}
			st, err := openStore(s, cfg.Config, nil, false)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only; Close only saves the cache
			if err := waitReady(cmd.Context(), st); err != nil {
				return err
			}
			sess, err := findSession(st.Sessions(), args[0])
			if err != nil {
				return err
			}
			entries, err := st.Entries(sess.ID, limit)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), struct {
					Session core.Session `json:"session"`
					Entries []core.Entry `json:"entries"`
				}{sess, entries})
			}
			return writeShow(cmd.OutOrStdout(), sess, entries, time.Now())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the session and entries as JSON")
	c.Flags().IntVarP(&limit, "entries", "n", 20, "recent conversation entries to show; 0 for all")
	return c
}

// findSession finds the one session whose id starts with prefix.
func findSession(sessions []core.Session, prefix string) (core.Session, error) {
	if prefix == "" {
		return core.Session{}, errors.New("empty session id")
	}
	var found []core.Session
	for _, s := range sessions {
		if s.ID == prefix {
			return s, nil
		}
		if strings.HasPrefix(s.ID, prefix) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return core.Session{}, fmt.Errorf("no session id starts with %q; ccdeck list shows them", prefix)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, s := range found {
		ids[i] = s.ID
	}
	return core.Session{}, fmt.Errorf("%q matches %d sessions: %s", prefix, len(found), strings.Join(ids, ", "))
}

func writeShow(w io.Writer, s core.Session, entries []core.Entry, now time.Time) error {
	row := func(k, v string) { _, _ = fmt.Fprintf(w, "%-10s %s\n", k, v) }
	row("session", s.ID)
	row("title", clean(s.Title))
	row("status", status(s))
	if s.Live != nil {
		row("process", fmt.Sprintf("pid %d %s, %s since %s", s.Live.PID, clean(s.Live.Name), s.Live.Status, age(s.Live.StatusSince, now)))
	}
	row("project", tildePath(s.ProjectDir))
	row("branch", orDash(s.GitBranch))
	row("model", orDash(s.Model))
	row("started", stamp(s.StartedAt))
	row("active", stamp(s.LastActivity)+" ("+age(s.LastActivity, now)+")")
	row("activity", fmt.Sprintf("%d turns, %d messages, %d tool calls", s.Turns, s.Messages, s.ToolCalls))
	ctx := tokens(s.ContextTokens)
	if p, ok := s.ContextPercent(); ok {
		ctx = fmt.Sprintf("%s of %s (%.0f%%)", ctx, tokens(s.ContextWindow), p)
	} else {
		ctx += " (window unknown)"
	}
	row("context", ctx)
	row("cost", costLong(s.CostUSD, s.CostKind))
	for _, model := range slices.Sorted(maps.Keys(s.Usage)) {
		u := s.Usage[model]
		row("tokens", fmt.Sprintf("%s: %s total, %s fresh (in %s, out %s, cache read %s, write %s)",
			model, tokens(u.Total()), tokens(u.Fresh()), tokens(u.Input), tokens(u.Output),
			tokens(u.CacheRead), tokens(u.CacheWrite5m+u.CacheWrite1h)))
	}
	for _, a := range s.Subagents {
		row("subagent", fmt.Sprintf("%s %s: %s (%s tokens)", shortID(a.ID), orDash(a.Type), clip(clean(a.Description), 60), tokens(a.Usage.Total())))
	}
	if len(entries) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	for _, e := range entries {
		_, _ = fmt.Fprintln(w, entryLine(e))
	}
	return nil
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// entryLine is one conversation entry on one line.
func entryLine(e core.Entry) string {
	var tag string
	switch e.Kind {
	case core.EntryPrompt:
		tag = "you"
	case core.EntryText:
		tag = "claude"
	case core.EntryThinking:
		tag = "thinking"
	case core.EntryToolUse:
		tag = "▸ " + e.Tool
	case core.EntryToolResult:
		tag = "  ↳"
		if e.IsError {
			tag = "  ✗"
		}
	case core.EntryCommand:
		tag = "command"
	case core.EntryCompact:
		tag = "compacted"
	default:
		tag = string(e.Kind)
	}
	t := "     "
	if !e.Time.IsZero() {
		t = e.Time.Local().Format("15:04")
	}
	return clip(t+" "+tag+"  "+clean(e.Text), 160)
}
