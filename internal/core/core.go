// Package core holds the types every other package agrees on: the session
// model, the store the UI reads from, and the launcher the UI acts through.
//
// It is the frozen contract of the project. It imports only the standard
// library, and no other package may be imported from here.
package core

import (
	"os/exec"
	"time"
)

// Status is what a session is doing right now.
type Status string

// Session statuses. The live ones mirror the status field Claude Code writes
// to its session registry.
const (
	StatusEnded   Status = "ended"   // no live process owns the session
	StatusBusy    Status = "busy"    // working on a turn
	StatusIdle    Status = "idle"    // waiting for the next prompt
	StatusWaiting Status = "waiting" // blocked on the user; see Live.WaitingFor
	StatusUnknown Status = "unknown" // live, but the registry status is not one we know
)

// Usage is a token count. Thinking is a subset of Output, kept for display;
// it is not added again by Total.
type Usage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	Thinking     int64 `json:"thinking"`
	CacheRead    int64 `json:"cacheRead"`
	CacheWrite5m int64 `json:"cacheWrite5m"`
	CacheWrite1h int64 `json:"cacheWrite1h"`
	WebSearches  int64 `json:"webSearches"`
}

// Add returns the sum of u and o.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		Input:        u.Input + o.Input,
		Output:       u.Output + o.Output,
		Thinking:     u.Thinking + o.Thinking,
		CacheRead:    u.CacheRead + o.CacheRead,
		CacheWrite5m: u.CacheWrite5m + o.CacheWrite5m,
		CacheWrite1h: u.CacheWrite1h + o.CacheWrite1h,
		WebSearches:  u.WebSearches + o.WebSearches,
	}
}

// Total is every token that was sent or received, cache traffic included.
func (u Usage) Total() int64 {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h
}

// Fresh is the tokens that were not served from cache: a better measure of
// work done than Total, which cache reads dominate.
func (u Usage) Fresh() int64 {
	return u.Input + u.Output + u.CacheWrite5m + u.CacheWrite1h
}

// CostKind says where a dollar figure came from.
type CostKind string

// Cost kinds.
const (
	CostNone      CostKind = "none"      // no figure: unknown model price and nothing reported
	CostEstimated CostKind = "estimated" // tokens multiplied by a price table
	CostReported  CostKind = "reported"  // the total Claude Code recorded when the session ended
	// CostClaude: Claude Code's own running figure, from the status-line
	// meter. Still an estimate at list price, but it knows the price tiers.
	CostClaude CostKind = "claude"
)

// Meter is what Claude Code last reported about a session through the
// status-line helper. Nil on Session when the helper is not enabled.
type Meter struct {
	At             time.Time `json:"at"`             // when the report was written
	ContextPercent float64   `json:"contextPercent"` // 0–100; valid only when HasContext
	HasContext     bool      `json:"hasContext"`
	ContextWindow  int64     `json:"contextWindow"`
	ContextTokens  int64     `json:"contextTokens"`
	CostUSD        float64   `json:"costUSD"`
	LinesAdded     int       `json:"linesAdded"`
	LinesRemoved   int       `json:"linesRemoved"`
}

// RateLimit is one usage window of the account.
type RateLimit struct {
	Window      string    `json:"window"`      // "5h", "7d" or "spend"
	UsedPercent float64   `json:"usedPercent"` // may exceed 100 for spend
	ResetsAt    time.Time `json:"resetsAt"`
	UsedUSD     float64   `json:"usedUSD,omitempty"` // spend only; 0 when unknown
	LimitUSD    float64   `json:"limitUSD,omitempty"`
	Period      string    `json:"period,omitempty"` // spend only: daily, weekly, monthly
}

// Live describes the running process that owns a session.
type Live struct {
	PID         int       `json:"pid"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"` // "interactive", or a background kind
	Status      Status    `json:"status"`
	WaitingFor  string    `json:"waitingFor,omitempty"` // e.g. "approve Bash: go test ./..."
	StartedAt   time.Time `json:"startedAt"`
	StatusSince time.Time `json:"statusSince"`
}

// Subagent is one agent a session delegated work to.
type Subagent struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Description  string    `json:"description"`
	Model        string    `json:"model"`
	Usage        Usage     `json:"usage"`
	LastActivity time.Time `json:"lastActivity"`
}

// Sample is the token usage of one assistant message, for burn-rate charts.
type Sample struct {
	Time   time.Time `json:"time"`
	Tokens int64     `json:"tokens"` // Usage.Fresh of the message
}

// Session is everything known about one Claude Code session. Values are
// snapshots: a Store hands out copies, and callers must not mutate the
// slices or maps they share.
type Session struct {
	ID         string `json:"id"`
	Path       string `json:"path"`       // transcript file
	ProjectDir string `json:"projectDir"` // directory the session was started in; resume from here

	Title      string `json:"title"`      // best available: custom title, AI title, registry name, first prompt
	LastPrompt string `json:"lastPrompt"` // most recent human prompt, single line
	GitBranch  string `json:"gitBranch"`
	Model      string `json:"model"`   // most recent main-thread model id
	Version    string `json:"version"` // Claude Code version that last wrote the transcript

	StartedAt    time.Time `json:"startedAt"`
	LastActivity time.Time `json:"lastActivity"`

	Turns     int `json:"turns"`     // human prompts
	Messages  int `json:"messages"`  // distinct assistant messages
	ToolCalls int `json:"toolCalls"` // tool_use blocks, main thread

	Usage         map[string]Usage `json:"usage"`         // by model id; main thread plus subagents
	ContextTokens int64            `json:"contextTokens"` // tokens in the window after the latest main-thread message
	ContextWindow int64            `json:"contextWindow"` // window size for Model; 0 when unknown
	CostUSD       float64          `json:"costUSD"`
	CostKind      CostKind         `json:"costKind"`

	Recent    []Sample   `json:"recent,omitempty"`    // last hour of assistant messages, oldest first
	Subagents []Subagent `json:"subagents,omitempty"` // most recently active first

	Live *Live `json:"live,omitempty"` // nil when no process is running the session

	// Meter is Claude Code's last status-line report for the session; nil
	// when the helper is not enabled or has not run for it.
	Meter *Meter `json:"meter,omitempty"`
}

// Status reports the live status, or StatusEnded when nothing is running.
func (s Session) Status() Status {
	if s.Live == nil {
		return StatusEnded
	}
	return s.Live.Status
}

// TotalUsage sums usage across models.
func (s Session) TotalUsage() Usage {
	var t Usage
	for _, u := range s.Usage {
		t = t.Add(u)
	}
	return t
}

// ContextPercent is how full the context window is, 0 to 100. The second
// result is false when the window size is unknown.
func (s Session) ContextPercent() (float64, bool) {
	if s.ContextWindow <= 0 {
		return 0, false
	}
	p := 100 * float64(s.ContextTokens) / float64(s.ContextWindow)
	if p > 100 {
		p = 100
	}
	return p, true
}

// EntryKind classifies a line of the conversation as shown to the user.
type EntryKind string

// Entry kinds.
const (
	EntryPrompt     EntryKind = "prompt"      // typed by the human
	EntryText       EntryKind = "text"        // assistant prose
	EntryThinking   EntryKind = "thinking"    // assistant thinking; Text may be empty
	EntryToolUse    EntryKind = "tool_use"    // Tool is set, Text is a one-line summary of the input
	EntryToolResult EntryKind = "tool_result" // Text is a truncated result; IsError marks failures
	EntryCommand    EntryKind = "command"     // slash command the user ran
	EntryCompact    EntryKind = "compact"     // the conversation was compacted here
)

// Entry is one displayable step of a conversation.
type Entry struct {
	Kind    EntryKind `json:"kind"`
	Time    time.Time `json:"time"`
	Text    string    `json:"text"`
	Tool    string    `json:"tool,omitempty"`
	ToolID  string    `json:"toolId,omitempty"` // pairs a tool_use with its tool_result
	IsError bool      `json:"isError,omitempty"`
	Model   string    `json:"model,omitempty"`
}

// PromptHit is one match from a search over prompts the user has typed.
type PromptHit struct {
	SessionID string    `json:"sessionId"`
	Time      time.Time `json:"time"`
	Text      string    `json:"text"`
}

// DayUsage is the usage of one local calendar day across all sessions.
type DayUsage struct {
	Date     time.Time        `json:"date"` // local midnight
	Usage    map[string]Usage `json:"usage"`
	CostUSD  float64          `json:"costUSD"`  // estimated; 0 when no prices are known
	Sessions int              `json:"sessions"` // sessions that used tokens that day
	// SessionIDs names the sessions counted in Sessions, sorted, so totals
	// over several days can count each session once.
	SessionIDs []string `json:"sessionIds,omitempty"`
}

// EventKind says what changed in a Store.
type EventKind int

// Event kinds.
const (
	SessionsChanged EventKind = iota // the set of sessions, or any session's summary or live state
	EntriesChanged                   // new conversation entries for Event.SessionID
)

// Event is a change notification. Events are hints to re-read; they may be
// coalesced, and a slow subscriber may miss some.
type Event struct {
	Kind      EventKind
	SessionID string // empty for SessionsChanged affecting several sessions
}

// Store is the read side of the application: a live, in-memory view of every
// session. Implementations are safe for concurrent use.
type Store interface {
	// Sessions returns a snapshot of all sessions, most recently active first.
	Sessions() []Session
	// Session returns one session by id.
	Session(id string) (Session, bool)
	// Entries returns up to limit most recent conversation entries of a
	// session's main thread, oldest first. limit <= 0 means all.
	Entries(id string, limit int) ([]Entry, error)
	// SearchPrompts finds prompts containing query, case-insensitively, newest
	// first.
	SearchPrompts(query string, limit int) []PromptHit
	// Daily returns usage for the last days local calendar days, oldest first,
	// including days with no activity.
	Daily(days int) []DayUsage
	// Subscribe delivers change events until cancel is called. The channel is
	// buffered and never blocks the store.
	Subscribe() (events <-chan Event, cancel func())
	// Limits returns the account's rate-limit windows as reported by the
	// status-line helper for this store's sessions, and when the newest
	// report behind those figures was written. The newest report decides
	// which windows exist; each window carries the freshest figure any
	// report gives. Windows whose reset time has passed are dropped. Empty
	// when the status-line helper is not enabled.
	Limits() (limits []RateLimit, at time.Time)
	// Close stops watchers and releases resources.
	Close() error
}

// Launch is a command that starts or reaches a Claude Code session.
type Launch struct {
	Cmd *exec.Cmd
	// Detached is true when the command opens the session somewhere else,
	// such as a new tmux window, and returns at once: run it in the
	// background and keep the screen. When false the command needs the
	// terminal: suspend the UI and hand the terminal over until it exits.
	Detached bool
}

// Launcher turns user intent into commands. It never runs them.
type Launcher interface {
	// Resume continues an ended session in its project directory. With fork
	// it branches into a new session id and leaves the original untouched.
	Resume(s Session, fork bool) (Launch, error)
	// New starts a fresh session in dir.
	New(dir string) (Launch, error)
	// Focus returns a command that brings an already-running session to the
	// front, such as switching to its tmux pane. ok is false when the
	// session cannot be reached from here.
	Focus(s Session) (l Launch, ok bool)
}
