package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// build turns a spec into a session as of now.
func (st *Store) build(sp spec, now time.Time) *session {
	last := now.Add(-sp.ago)
	start := last.Add(-sp.length)
	s := core.Session{
		ID:            sp.id,
		Path:          "/home/demo/.claude/projects/" + strings.ReplaceAll(sp.project, "/", "-") + "/" + sp.id + ".jsonl",
		ProjectDir:    sp.project,
		Title:         sp.title,
		GitBranch:     sp.branch,
		Model:         sp.model,
		StartedAt:     start,
		LastActivity:  last,
		Turns:         sp.turns,
		Usage:         map[string]core.Usage{},
		ContextTokens: sp.context,
		ContextWindow: sp.window,
		CostKind:      sp.cost,
	}
	if sp.model != "" {
		s.Version = "2.1.3"
		s.Messages = sp.turns * (4 + st.rng.IntN(6))
		s.ToolCalls = s.Messages * 3 / 5
		// The conversation shown is the session's tail, so the counts are
		// at least what it holds.
		prompts, says, uses := counts(sp.convo)
		s.Turns = max(s.Turns, prompts)
		s.Messages = max(s.Messages, says)
		s.ToolCalls = max(s.ToolCalls, uses)
		s.Usage[sp.model] = st.usageFor(s.Messages, sp.context)
	}
	for i, a := range sp.agents {
		u := st.usageFor(3+st.rng.IntN(8), 8_000+int64(st.rng.IntN(30_000)))
		s.Subagents = append(s.Subagents, core.Subagent{
			ID:           sp.id[:8] + "-a" + string(rune('1'+i)),
			Type:         a.typ,
			Description:  a.desc,
			Model:        a.model,
			Usage:        u,
			LastActivity: last.Add(-time.Duration(i*7+2) * time.Minute),
		})
		s.Usage[a.model] = s.Usage[a.model].Add(u)
	}
	switch sp.cost {
	case core.CostReported:
		s.CostUSD = sp.reported
	case core.CostEstimated:
		s.CostUSD = costOf(s.Usage)
	}
	if sp.status != core.StatusEnded {
		s.Live = &core.Live{
			PID: sp.pid, Name: sp.name, Kind: "interactive", Status: sp.status,
			WaitingFor: sp.waitingFor, StartedAt: start,
			StatusSince: last.Add(-time.Duration(st.rng.IntN(90)) * time.Second),
		}
		for t := now.Add(-time.Hour); t.Before(last); t = t.Add(time.Duration(90+st.rng.IntN(150)) * time.Second) {
			if sp.model != "" && !t.Before(start) {
				s.Recent = append(s.Recent, core.Sample{Time: t, Tokens: int64(800 + st.rng.IntN(7000))})
			}
		}
	}

	x := &session{s: s, active: sp.active}
	n := len(sp.convo)
	for i, l := range sp.convo {
		at := start
		if n > 1 {
			at = start.Add(time.Duration(int64(sp.length) * int64(i) / int64(n-1)))
		}
		x.add(l, at)
		if l.kind == core.EntryPrompt {
			x.s.LastPrompt = l.text
		}
	}
	return x
}

// usageFor invents plausible usage for a number of assistant messages that
// ended with ctx tokens in the window.
func (st *Store) usageFor(messages int, ctx int64) core.Usage {
	m := int64(messages)
	out := m * int64(300+st.rng.IntN(900))
	return core.Usage{
		Input:        m * int64(5+st.rng.IntN(40)),
		Output:       out,
		Thinking:     out / 4,
		CacheRead:    m * ctx / 2,
		CacheWrite5m: ctx + ctx/3,
	}
}

// add appends a scripted line to the conversation. A tool call gets a
// fresh tool id and the next result carries it, as in a real transcript.
func (x *session) add(l line, at time.Time) {
	e := core.Entry{Kind: l.kind, Time: at, Text: l.text, Tool: l.tool, IsError: l.err}
	switch l.kind {
	case core.EntryText, core.EntryThinking, core.EntryToolUse:
		e.Model = x.s.Model
	}
	switch l.kind {
	case core.EntryToolUse:
		x.tools++
		x.pending = fmt.Sprintf("toolu_%s_%03d", x.s.ID[:8], x.tools)
		e.ToolID = x.pending
	case core.EntryToolResult:
		e.ToolID, x.pending = x.pending, ""
	}
	x.entries = append(x.entries, e)
}

// seedHistory fills a month of daily totals: invented background traffic
// plus the usage of each session on the day it was last active.
func (st *Store) seedHistory(now time.Time) {
	today := midnight(now)
	for i := 1; i < 30; i++ {
		if st.rng.Float64() < 0.15 {
			continue // a day off
		}
		d := st.day(today.AddDate(0, 0, -i))
		d.Sessions = 1 + st.rng.IntN(8)
		for n := range d.Sessions {
			// Invented sessions that are no longer listed.
			d.SessionIDs = append(d.SessionIDs, fmt.Sprintf("past-%02d-%d", i, n))
		}
		for _, m := range []string{opus, sonnet, haiku}[:1+st.rng.IntN(3)] {
			u := core.Usage{
				Input:        int64(2_000 + st.rng.IntN(48_000)),
				Output:       int64(20_000 + st.rng.IntN(380_000)),
				CacheRead:    int64(1_000_000 + st.rng.IntN(29_000_000)),
				CacheWrite5m: int64(50_000 + st.rng.IntN(1_450_000)),
			}
			u.Thinking = u.Output / 4
			d.Usage[m] = d.Usage[m].Add(u)
		}
		d.CostUSD = costOf(d.Usage)
	}
	for _, x := range st.sessions {
		d := st.day(x.s.LastActivity)
		d.Sessions++
		d.SessionIDs = append(d.SessionIDs, x.s.ID)
		for m, u := range x.s.Usage {
			d.Usage[m] = d.Usage[m].Add(u)
		}
		d.CostUSD = costOf(d.Usage)
	}
}

// day returns the bucket for t's local date, creating it if needed.
func (st *Store) day(t time.Time) *core.DayUsage {
	k := dayKey(t)
	d, ok := st.days[k]
	if !ok {
		d = &core.DayUsage{Date: midnight(t), Usage: map[string]core.Usage{}}
		st.days[k] = d
	}
	return d
}

// advance moves one live session forward and reports whether it changed.
func (st *Store) advance(x *session, now time.Time) bool {
	live := x.s.Live
	if live == nil {
		return false
	}
	r := st.rng.Float64()
	switch live.Status {
	case core.StatusBusy:
		switch {
		case r < 0.55:
			st.work(x, now)
		case r < 0.62:
			setStatus(x, core.StatusIdle, "", now)
		case r < 0.67:
			setStatus(x, core.StatusWaiting, waits[st.rng.IntN(len(waits))], now)
		default:
			return false
		}
	case core.StatusIdle:
		if r >= 0.15 {
			return false
		}
		p := followUps[st.rng.IntN(len(followUps))]
		x.add(prompt(p), now)
		x.s.LastPrompt = p
		x.s.Turns++
		x.s.LastActivity = now
		setStatus(x, core.StatusBusy, "", now)
	case core.StatusWaiting:
		if r >= 0.2 {
			return false
		}
		x.add(result("approved"), now)
		x.s.LastActivity = now
		setStatus(x, core.StatusBusy, "", now)
	default:
		return false
	}
	return true
}

func setStatus(x *session, s core.Status, waitingFor string, now time.Time) {
	x.s.Live.Status = s
	x.s.Live.WaitingFor = waitingFor
	x.s.Live.StatusSince = now
}

// work appends the next step of the work script and the tokens it cost.
func (st *Store) work(x *session, now time.Time) {
	l := workScript[x.beat%len(workScript)]
	x.beat++
	x.add(l, now)
	x.s.LastActivity = now
	if l.kind == core.EntryToolResult {
		return // results arrive as user lines and cost nothing by themselves
	}

	out := int64(150 + st.rng.IntN(900))
	grow := int64(400 + st.rng.IntN(2_400))
	delta := core.Usage{
		Input:        int64(3 + st.rng.IntN(30)),
		Output:       out,
		Thinking:     out / 5,
		CacheRead:    x.s.ContextTokens,
		CacheWrite5m: grow,
	}
	x.s.ContextTokens += grow
	if x.s.ContextWindow > 0 {
		x.s.ContextTokens = min(x.s.ContextTokens, x.s.ContextWindow*99/100)
	}
	x.s.Messages++
	if l.kind == core.EntryToolUse {
		x.s.ToolCalls++
	}
	x.s.Usage[x.s.Model] = x.s.Usage[x.s.Model].Add(delta)
	if len(x.s.Subagents) > 0 && x.beat%4 == 0 {
		a := &x.s.Subagents[0]
		extra := core.Usage{Input: 20, Output: int64(100 + st.rng.IntN(500)), CacheRead: 9_000}
		a.Usage = a.Usage.Add(extra)
		a.LastActivity = now
		x.s.Usage[a.Model] = x.s.Usage[a.Model].Add(extra)
		delta = delta.Add(extra) // counted toward today below, by the main model for simplicity
	}
	x.s.Recent = append(x.s.Recent, core.Sample{Time: now, Tokens: delta.Fresh()})
	cut := 0
	for cut < len(x.s.Recent) && x.s.Recent[cut].Time.Before(now.Add(-time.Hour)) {
		cut++
	}
	x.s.Recent = x.s.Recent[cut:]

	before := x.s.CostUSD
	if x.s.CostKind == core.CostEstimated {
		x.s.CostUSD = costOf(x.s.Usage)
	}
	d := st.day(now)
	d.Usage[x.s.Model] = d.Usage[x.s.Model].Add(delta)
	d.CostUSD += x.s.CostUSD - before
}

// counts returns how many prompts, assistant replies and tool calls a
// conversation holds.
func counts(convo []line) (prompts, says, uses int) {
	for _, l := range convo {
		switch l.kind {
		case core.EntryPrompt:
			prompts++
		case core.EntryText:
			says++
		case core.EntryToolUse:
			uses++
		}
	}
	return prompts, says, uses
}
