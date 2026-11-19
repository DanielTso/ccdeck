package claude

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// SessionInput is what Accumulator.Session needs besides the transcript.
// Every field is optional.
type SessionInput struct {
	ID           string          // session id: the transcript's file name without .jsonl
	Path         string          // transcript path; empty for a live session with no transcript yet
	Registration *Registration   // the live registry entry; nil when nothing runs the session
	Subagents    []SubagentInput // the session's subagent transcripts
	Catalog      Catalog         // context-window sizes
	Prices       Prices          // for estimated cost; config prices merged over ReadPrices
	Now          time.Time       // for Recent; zero means time.Now()
}

// SubagentInput is one subagent transcript of a session.
type SubagentInput struct {
	ID   string       // agent id, from SubagentFile.ID
	Meta SubagentMeta // from ReadSubagentMeta; may be zero
	Acc  *Accumulator // the subagent transcript, folded
}

// Session builds the session summary. Subagent usage is added to Usage by
// model and listed in Subagents, but does not touch ContextTokens. The
// title is the custom title, else the AI title, else the registry name,
// else the first prompt. A live session with no transcript yet is built
// from an empty Accumulator and its Registration.
func (a *Accumulator) Session(in SessionInput) core.Session {
	st := &a.st
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-recentWindow)

	s := core.Session{
		ID:            in.ID,
		Path:          in.Path,
		ProjectDir:    st.ProjectDir,
		LastPrompt:    a.text.lastPrompt,
		GitBranch:     st.GitBranch,
		Model:         st.Model,
		Version:       st.Version,
		StartedAt:     st.StartedAt,
		LastActivity:  st.LastActivity,
		Turns:         st.Turns,
		Messages:      st.Messages,
		ToolCalls:     st.ToolCalls,
		Usage:         maps.Clone(st.Usage),
		ContextTokens: st.ContextTokens,
		Recent:        recentSince(nil, st.Recent, cutoff),
	}
	if s.Usage == nil {
		s.Usage = map[string]core.Usage{}
	}

	for _, sa := range in.Subagents {
		if sa.Acc == nil {
			continue
		}
		sub := &sa.Acc.st
		var total core.Usage
		for model, u := range sub.Usage {
			s.Usage[model] = s.Usage[model].Add(u)
			total = total.Add(u)
		}
		s.Subagents = append(s.Subagents, core.Subagent{
			ID:           sa.ID,
			Type:         sa.Meta.AgentType,
			Description:  sa.Meta.Description,
			Model:        orElse(sub.Model, sa.Meta.Model),
			Usage:        total,
			LastActivity: sub.LastActivity,
		})
		s.Recent = recentSince(s.Recent, sub.Recent, cutoff)
	}
	slices.SortStableFunc(s.Subagents, func(x, y core.Subagent) int {
		if c := y.LastActivity.Compare(x.LastActivity); c != 0 {
			return c
		}
		return cmp.Compare(x.ID, y.ID)
	})
	slices.SortStableFunc(s.Recent, func(x, y core.Sample) int { return x.Time.Compare(y.Time) })

	var regName string
	if r := in.Registration; r != nil {
		live := r.Live
		s.Live = &live
		regName = live.Name
		s.ProjectDir = orElse(s.ProjectDir, r.CWD)
		s.Version = orElse(s.Version, r.Version)
		if s.StartedAt.IsZero() {
			s.StartedAt = live.StartedAt
		}
		if s.LastActivity.IsZero() {
			s.LastActivity = later(live.StartedAt, live.StatusSince)
		}
	}
	s.Title = orElse(a.text.customTitle, orElse(a.text.aiTitle, orElse(regName, a.text.firstPrompt)))

	if s.ContextTokens > 0 {
		// Without a context (no reply yet, or a compaction since the last
		// one) the window is left unknown too, so the context shows as
		// unknown rather than as 0%.
		s.ContextWindow = in.Catalog.Window(s.Model)
	}
	switch {
	case st.ReportedCost > 0 && !st.ReportedStale:
		s.CostUSD, s.CostKind = st.ReportedCost, core.CostReported
	default:
		if c, ok := in.Prices.Estimate(s.Usage); ok {
			s.CostUSD, s.CostKind = c, core.CostEstimated
		} else {
			s.CostKind = core.CostNone
		}
	}
	return s
}

// recentSince appends the samples of src at or after cutoff to dst.
func recentSince(dst, src []core.Sample, cutoff time.Time) []core.Sample {
	for _, x := range src {
		if !x.Time.Before(cutoff) {
			dst = append(dst, x)
		}
	}
	return dst
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
