// Package demo is a core.Store filled with invented sessions that keep
// moving: statuses flip, tokens climb and conversations grow. It backs
// ccdeck --demo, UI tests and reproducible screenshots.
//
// Everything is synthetic. Given the same seed and the same clock, a Store
// produces the same sessions and evolves the same way step by step.
package demo

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Options configures a Store.
type Options struct {
	// Seed drives every random choice.
	Seed uint64
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Interval, when positive, advances the simulation by one Step on that
	// period in a background goroutine. Zero leaves stepping to the caller.
	Interval time.Duration
}

// Store is a synthetic, moving core.Store. It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	now      func() time.Time
	rng      *rand.Rand
	sessions []*session
	days     map[string]*core.DayUsage // keyed by local date, YYYY-MM-DD
	subs     map[int]chan core.Event
	nextSub  int
	closed   bool
	limits   []core.RateLimit
	limitsAt time.Time

	stop chan struct{}
	done chan struct{}
}

// session is one simulated session and its conversation.
type session struct {
	s       core.Session
	entries []core.Entry
	active  bool // the simulation moves it
	beat    int  // position in the work script
	tools   int  // tool calls made, for tool ids
	pending string
}

var _ core.Store = (*Store)(nil)

// New builds a Store holding a dozen invented sessions.
func New(opts Options) *Store {
	st := &Store{
		now:  opts.Now,
		rng:  rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x9e3779b97f4a7c15)),
		days: make(map[string]*core.DayUsage),
		subs: make(map[int]chan core.Event),
	}
	if st.now == nil {
		st.now = time.Now
	}
	now := st.now()
	for _, sp := range specs {
		st.sessions = append(st.sessions, st.build(sp, now))
	}
	st.seedHistory(now)
	st.limits, st.limitsAt = demoLimits(now), now.Add(-20*time.Second)
	if opts.Interval > 0 {
		st.stop = make(chan struct{})
		st.done = make(chan struct{})
		go st.run(opts.Interval)
	}
	return st
}

func (st *Store) run(every time.Duration) {
	defer close(st.done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-st.stop:
			return
		case <-t.C:
			st.Step()
		}
	}
}

// Sessions returns copies of all sessions, most recently active first.
func (st *Store) Sessions() []core.Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]core.Session, 0, len(st.sessions))
	for _, x := range st.sessions {
		out = append(out, clone(x.s))
	}
	slices.SortStableFunc(out, func(a, b core.Session) int {
		return b.LastActivity.Compare(a.LastActivity)
	})
	return out
}

// Session returns a copy of one session.
func (st *Store) Session(id string) (core.Session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if x := st.find(id); x != nil {
		return clone(x.s), true
	}
	return core.Session{}, false
}

// Entries returns up to limit most recent entries of a session, oldest first.
func (st *Store) Entries(id string, limit int) ([]core.Entry, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	x := st.find(id)
	if x == nil {
		return nil, errors.New("demo: no session " + id)
	}
	e := x.entries
	if limit > 0 && len(e) > limit {
		e = e[len(e)-limit:]
	}
	return slices.Clone(e), nil
}

// SearchPrompts finds prompts containing query, case-insensitively, newest
// first. limit <= 0 means no limit.
func (st *Store) SearchPrompts(query string, limit int) []core.PromptHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	st.mu.Lock()
	var hits []core.PromptHit
	for _, x := range st.sessions {
		for _, e := range x.entries {
			if e.Kind == core.EntryPrompt && strings.Contains(strings.ToLower(e.Text), q) {
				hits = append(hits, core.PromptHit{SessionID: x.s.ID, Time: e.Time, Text: e.Text})
			}
		}
	}
	st.mu.Unlock()
	slices.SortStableFunc(hits, func(a, b core.PromptHit) int { return b.Time.Compare(a.Time) })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Daily returns usage for the last days local calendar days, oldest first.
func (st *Store) Daily(days int) []core.DayUsage {
	st.mu.Lock()
	defer st.mu.Unlock()
	today := midnight(st.now())
	out := make([]core.DayUsage, 0, max(days, 0))
	for i := days - 1; i >= 0; i-- {
		date := today.AddDate(0, 0, -i)
		d := core.DayUsage{Date: date, Usage: map[string]core.Usage{}}
		if have, ok := st.days[dayKey(date)]; ok {
			d.CostUSD, d.Sessions = have.CostUSD, have.Sessions
			d.SessionIDs = slices.Sorted(slices.Values(have.SessionIDs))
			for m, u := range have.Usage {
				d.Usage[m] = u
			}
		}
		out = append(out, d)
	}
	return out
}

// Limits returns the invented rate-limit windows that have not reset, and
// when they were reported.
func (st *Store) Limits() ([]core.RateLimit, time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	var out []core.RateLimit
	for _, l := range st.limits {
		if l.ResetsAt.After(now) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, time.Time{}
	}
	return out, st.limitsAt
}

// Subscribe delivers change events until cancel is called. Events are
// dropped, never queued without bound, when the subscriber falls behind.
func (st *Store) Subscribe() (<-chan core.Event, func()) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ch := make(chan core.Event, 32)
	if st.closed {
		close(ch)
		return ch, func() {}
	}
	id := st.nextSub
	st.nextSub++
	st.subs[id] = ch
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			st.mu.Lock()
			defer st.mu.Unlock()
			if c, ok := st.subs[id]; ok {
				delete(st.subs, id)
				close(c)
			}
		})
	}
}

// Close stops the background ticker and closes every subscription.
func (st *Store) Close() error {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return nil
	}
	st.closed = true
	for id, c := range st.subs {
		delete(st.subs, id)
		close(c)
	}
	st.mu.Unlock()
	if st.stop != nil {
		close(st.stop)
		<-st.done
	}
	return nil
}

// Step advances the simulation by one tick at the current clock time and
// notifies subscribers of what changed.
func (st *Store) Step() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return
	}
	now := st.now()
	// A window that resets starts again, as the account's would.
	for i, l := range st.limits {
		if !l.ResetsAt.After(now) {
			fresh := demoLimits(now)[i]
			fresh.UsedPercent = 3
			st.limits[i], st.limitsAt = fresh, now
		}
	}
	var changed []string
	for _, x := range st.sessions {
		if x.active && st.advance(x, now) {
			changed = append(changed, x.s.ID)
		}
	}
	if len(changed) == 0 {
		return
	}
	st.publish(core.Event{Kind: core.SessionsChanged})
	for _, id := range changed {
		st.publish(core.Event{Kind: core.EntriesChanged, SessionID: id})
	}
}

// publish sends ev to every subscriber that has room. Callers hold mu.
func (st *Store) publish(ev core.Event) {
	for _, c := range st.subs {
		select {
		case c <- ev:
		default:
		}
	}
}

func (st *Store) find(id string) *session {
	for _, x := range st.sessions {
		if x.s.ID == id {
			return x
		}
	}
	return nil
}

// clone copies the parts of a session that the simulation mutates, so a
// snapshot handed out never changes under its reader.
func clone(s core.Session) core.Session {
	usage := make(map[string]core.Usage, len(s.Usage))
	for m, u := range s.Usage {
		usage[m] = u
	}
	s.Usage = usage
	s.Recent = slices.Clone(s.Recent)
	s.Subagents = slices.Clone(s.Subagents)
	if s.Live != nil {
		l := *s.Live
		s.Live = &l
	}
	if s.Meter != nil {
		m := *s.Meter
		s.Meter = &m
	}
	return s
}

func midnight(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func dayKey(t time.Time) string { return t.Local().Format("2006-01-02") }
