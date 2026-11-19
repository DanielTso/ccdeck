package claude

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// StateVersion is the version of the State encoding. RestoreAccumulator
// rejects a State written with another version.
const StateVersion = 1

// noLine marks a TextOffsets field with no line behind it.
const noLine = -1

// recentWindow is how much history Session.Recent covers.
const recentWindow = time.Hour

// ErrStaleState means a cached State does not match the transcript any more:
// a line it points at is missing or says something else. Parse the
// transcript from the start.
var ErrStaleState = errors.New("claude: cached state does not match transcript")

// State is the serialisable part of an Accumulator: derived numbers and
// identifiers (model ids, paths, branch), never conversation text. It
// round-trips through encoding/json. Treat it as opaque apart from Offset,
// Lines and Skipped.
type State struct {
	V       int   `json:"v"`       // StateVersion
	Offset  int64 `json:"offset"`  // bytes of the transcript folded so far; resume reading here
	Lines   int   `json:"lines"`   // non-blank lines folded
	Skipped int   `json:"skipped"` // malformed lines among them

	// ThreadKnown is set once the first conversation line has been seen;
	// SidechainFile then says whether the file is a subagent transcript, in
	// which case its sidechain lines are its main thread.
	ThreadKnown   bool `json:"threadKnown"`
	SidechainFile bool `json:"sidechainFile"`

	ProjectDir   string    `json:"projectDir"` // first cwd seen
	GitBranch    string    `json:"gitBranch"`
	Model        string    `json:"model"`
	Version      string    `json:"version"`
	StartedAt    time.Time `json:"startedAt"`
	LastActivity time.Time `json:"lastActivity"`

	Turns         int                   `json:"turns"`
	Messages      int                   `json:"messages"`
	ToolCalls     int                   `json:"toolCalls"`
	Usage         map[string]core.Usage `json:"usage"`
	ContextTokens int64                 `json:"contextTokens"`

	// ReportedCost is the last cost-state total. ReportedStale is set when an
	// assistant message arrived after it (the session was resumed), so the
	// figure no longer covers the whole session.
	ReportedCost  float64 `json:"reportedCost"`
	ReportedStale bool    `json:"reportedStale"`

	// Recent holds main-thread samples from the hour before LastActivity.
	Recent []core.Sample `json:"recent,omitempty"`

	Last LastMessage `json:"last"`
	Text TextOffsets `json:"text"`
}

// LastMessage is the assistant message whose usage was counted most
// recently. A following line with the same ID is part of the same message:
// its usage replaces this one instead of adding to it.
type LastMessage struct {
	ID      string     `json:"id"`
	Model   string     `json:"model"`
	Usage   core.Usage `json:"usage"`
	Sampled bool       `json:"sampled"` // the message is the last entry of Recent
}

// TextOffsets are the byte offsets of the lines that supplied the current
// title and prompt texts, or -1 for none.
type TextOffsets struct {
	CustomTitle int64 `json:"customTitle"`
	AITitle     int64 `json:"aiTitle"`
	FirstPrompt int64 `json:"firstPrompt"`
	LastPrompt  int64 `json:"lastPrompt"` // a human prompt or a last-prompt line
}

// texts holds the conversation text an Accumulator knows. It is never part
// of the State.
type texts struct {
	customTitle, aiTitle, firstPrompt, lastPrompt string
}

// Accumulator folds the lines of one transcript, main or subagent, into the
// numbers of a session summary. Usage is counted once per message id. It
// implements Sink. An Accumulator is not safe for concurrent use.
type Accumulator struct {
	st      State
	text    texts
	onUsage func(at time.Time, model string, u core.Usage)
}

// OnUsage registers f to be called each time usage is counted, with the
// model and the amount added. at is the time of the line that carried it,
// or the latest activity before it when that line has no timestamp (zero
// when there is none yet). The amounts add up to State().Usage, which lets
// a caller spread usage over time. f survives Reset; nil unregisters it.
func (a *Accumulator) OnUsage(f func(at time.Time, model string, u core.Usage)) {
	a.onUsage = f
}

// NewAccumulator returns an empty Accumulator.
func NewAccumulator() *Accumulator {
	a := &Accumulator{}
	a.Reset()
	return a
}

// Reset forgets everything, as if no line had been seen.
func (a *Accumulator) Reset() {
	a.st = State{
		V:     StateVersion,
		Usage: map[string]core.Usage{},
		Text:  TextOffsets{CustomTitle: noLine, AITitle: noLine, FirstPrompt: noLine, LastPrompt: noLine},
	}
	a.text = texts{}
}

// State returns a copy of the accumulator's serialisable state.
func (a *Accumulator) State() State {
	st := a.st
	st.Usage = maps.Clone(a.st.Usage)
	st.Recent = append([]core.Sample(nil), a.st.Recent...)
	return st
}

// RestoreAccumulator rebuilds an Accumulator from a cached State. r is the
// transcript the State was taken from: the few lines that carried titles and
// prompts are re-read from it. It returns ErrStaleState when the State is
// from another version or does not match r, and other errors when r cannot
// be read; in every error case, parse the transcript from the start.
func RestoreAccumulator(st State, r io.ReaderAt) (*Accumulator, error) {
	if st.V != StateVersion {
		return nil, fmt.Errorf("%w: version %d", ErrStaleState, st.V)
	}
	a := &Accumulator{st: st}
	a.st.Usage = maps.Clone(st.Usage)
	if a.st.Usage == nil {
		a.st.Usage = map[string]core.Usage{}
	}
	a.st.Recent = append([]core.Sample(nil), st.Recent...)

	restore := func(off int64, want ...textKind) (string, error) {
		if off == noLine {
			return "", nil
		}
		stale := fmt.Errorf("%w: line at offset %d", ErrStaleState, off)
		if off < 0 || off >= st.Offset {
			return "", stale
		}
		line, err := readLineAt(r, off)
		if err != nil {
			return "", err
		}
		l, ok := decodeLine(line)
		if !ok {
			return "", stale
		}
		kind, text := a.textOf(&l)
		if text == "" || !slices.Contains(want, kind) {
			return "", stale
		}
		return text, nil
	}
	var err error
	if a.text.customTitle, err = restore(st.Text.CustomTitle, textCustomTitle); err != nil {
		return nil, err
	}
	if a.text.aiTitle, err = restore(st.Text.AITitle, textAITitle); err != nil {
		return nil, err
	}
	if a.text.firstPrompt, err = restore(st.Text.FirstPrompt, textPrompt); err != nil {
		return nil, err
	}
	if a.text.lastPrompt, err = restore(st.Text.LastPrompt, textPrompt, textLastPrompt); err != nil {
		return nil, err
	}
	return a, nil
}

// readLineAt reads the complete line starting at off.
func readLineAt(r io.ReaderAt, off int64) ([]byte, error) {
	br := bufio.NewReader(io.NewSectionReader(r, off, 1<<62))
	line, err := br.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: no complete line at offset %d", ErrStaleState, off)
	}
	if err != nil {
		return nil, fmt.Errorf("read line at %d: %w", off, err)
	}
	return line, nil
}

// textKind says which text field a line supplies.
type textKind int

const (
	textNone textKind = iota
	textCustomTitle
	textAITitle
	textLastPrompt // a last-prompt line
	textPrompt     // a human prompt on the main thread
)

// textOf reports the text a line supplies, already reduced to one line.
func (a *Accumulator) textOf(l *wireLine) (textKind, string) {
	var kind textKind
	var text string
	switch l.Type {
	case "custom-title":
		kind, text = textCustomTitle, l.CustomTitle
	case "ai-title":
		kind, text = textAITitle, l.AITitle
	case "last-prompt":
		kind, text = textLastPrompt, l.LastPrompt
	case "user":
		if a.isMain(l) {
			if k, t, _ := classifyUser(l); k == userPrompt {
				kind, text = textPrompt, t
			}
		}
	}
	text = oneLine(text, summaryRunes)
	if text == "" {
		return textNone, ""
	}
	return kind, text
}

// isMain reports whether a conversation line belongs to the file's main
// thread. Old transcripts interleave sidechain lines into the main file.
func (a *Accumulator) isMain(l *wireLine) bool {
	return !a.st.ThreadKnown || l.IsSidechain == a.st.SidechainFile
}

// Line folds one transcript line. It implements Sink.
func (a *Accumulator) Line(offset int64, line []byte) {
	a.st.Offset = offset + int64(len(line))
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	a.st.Lines++
	l, ok := decodeLine(line)
	if !ok {
		a.st.Skipped++
		return
	}
	switch l.Type {
	case "user", "assistant", "system":
		a.conversation(&l, offset)
	case "custom-title", "ai-title", "last-prompt":
		a.setText(&l, offset)
	case "cost-state":
		a.st.ReportedCost = l.TotalCostUSD
		a.st.ReportedStale = false
	}
}

func (a *Accumulator) setText(l *wireLine, offset int64) {
	kind, text := a.textOf(l)
	switch kind {
	case textCustomTitle:
		a.text.customTitle, a.st.Text.CustomTitle = text, offset
	case textAITitle:
		a.text.aiTitle, a.st.Text.AITitle = text, offset
	case textPrompt:
		if a.st.Text.FirstPrompt == noLine {
			a.text.firstPrompt, a.st.Text.FirstPrompt = text, offset
		}
		a.text.lastPrompt, a.st.Text.LastPrompt = text, offset
	case textLastPrompt:
		a.text.lastPrompt, a.st.Text.LastPrompt = text, offset
	}
}

// conversation folds a user, assistant or system line.
func (a *Accumulator) conversation(l *wireLine, offset int64) {
	st := &a.st
	if !st.ThreadKnown {
		st.ThreadKnown, st.SidechainFile = true, l.IsSidechain
	}
	t := l.time()
	if !t.IsZero() {
		if st.StartedAt.IsZero() || t.Before(st.StartedAt) {
			st.StartedAt = t
		}
		if t.After(st.LastActivity) {
			st.LastActivity = t
		}
	}
	main := a.isMain(l)
	if main {
		if st.ProjectDir == "" {
			st.ProjectDir = l.CWD
		}
		st.GitBranch = orElse(l.GitBranch, st.GitBranch)
		st.Version = orElse(l.Version, st.Version)
	}
	switch l.Type {
	case "user":
		if main {
			if k, _, _ := classifyUser(l); k == userPrompt {
				st.Turns++
				a.setText(l, offset)
			}
		}
	case "assistant":
		a.assistant(l, t, main)
	}
}

// orElse returns s, or fallback when s is empty.
func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// assistant counts one assistant line, deduplicating usage by message id.
func (a *Accumulator) assistant(l *wireLine, t time.Time, main bool) {
	st := &a.st
	m := l.message()
	model := m.Model
	if model == "" {
		model = "unknown"
	}
	u := m.Usage.usage()

	if m.ID != "" && m.ID == st.Last.ID {
		// Another line of the message already counted: its usage replaces
		// the earlier line's, so the message is counted once.
		a.addUsage(t, st.Last.Model, sub(u, st.Last.Usage))
		if st.Last.Sampled && len(st.Recent) > 0 {
			st.Recent[len(st.Recent)-1].Tokens = u.Fresh()
		}
		st.Last.Usage = u
	} else {
		a.addUsage(t, model, u)
		st.Last = LastMessage{ID: m.ID, Model: model, Usage: u}
		st.ReportedStale = st.ReportedCost > 0
		if main {
			st.Messages++
			if !t.IsZero() {
				a.sample(core.Sample{Time: t, Tokens: u.Fresh()})
				st.Last.Sampled = true
			}
		}
	}
	if !main {
		return
	}
	if !strings.HasPrefix(m.Model, "<") && m.Model != "" {
		st.Model = m.Model
	}
	if ctx := u.Input + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h; ctx > 0 {
		st.ContextTokens = ctx
	}
	_, _, bs := blocks(m.Content)
	for _, b := range bs {
		if b.Type == "tool_use" {
			st.ToolCalls++
		}
	}
}

func (a *Accumulator) addUsage(at time.Time, model string, u core.Usage) {
	if u == (core.Usage{}) {
		return
	}
	a.st.Usage[model] = a.st.Usage[model].Add(u)
	if a.onUsage != nil {
		if at.IsZero() {
			at = a.st.LastActivity
		}
		a.onUsage(at, model, u)
	}
}

// sample appends s to Recent and drops samples more than recentWindow older
// than the latest activity.
func (a *Accumulator) sample(s core.Sample) {
	st := &a.st
	st.Recent = append(st.Recent, s)
	cutoff := st.LastActivity.Add(-recentWindow)
	i := 0
	for i < len(st.Recent) && st.Recent[i].Time.Before(cutoff) {
		i++
	}
	if i > 0 {
		st.Recent = append(st.Recent[:0], st.Recent[i:]...)
	}
}

// sub returns u minus o.
func sub(u, o core.Usage) core.Usage {
	return core.Usage{
		Input:        u.Input - o.Input,
		Output:       u.Output - o.Output,
		Thinking:     u.Thinking - o.Thinking,
		CacheRead:    u.CacheRead - o.CacheRead,
		CacheWrite5m: u.CacheWrite5m - o.CacheWrite5m,
		CacheWrite1h: u.CacheWrite1h - o.CacheWrite1h,
		WebSearches:  u.WebSearches - o.WebSearches,
	}
}
