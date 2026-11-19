package index

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/meter"
)

// The status-line meter: reports Claude Code's status-line helper writes
// to ccdeck's state directory (see package meter). They are read like the
// registry: watched when the directory exists, and checked on every poll.

// meterFile is one session's report and the stamp of the file it came from.
type meterFile struct {
	stamp fileStamp
	rep   meter.Report
}

// limitsReport is the published rate limits and when the newest report
// they came from was written.
type limitsReport struct {
	limits []core.RateLimit
	at     time.Time
}

// refreshMeters rereads the state files that changed, republishes the
// sessions whose report changed, and publishes the rate limits. It
// runs on the run goroutine.
func (s *Store) refreshMeters() {
	if s.meterDir == "" {
		return
	}
	s.watchMeters()
	ents, err := os.ReadDir(s.meterDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("index: read meter directory", "err", err)
	}
	seen := make(map[string]bool, len(ents))
	var changed []string
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !meter.ValidID(id) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		st := stampOf(fi)
		if old, ok := s.meters[id]; ok && old.stamp == st {
			seen[id] = true
			continue
		}
		r, err := meter.Read(filepath.Join(s.meterDir, e.Name()))
		if err != nil || r.SessionID != id {
			continue // dropped below, as if it were gone
		}
		seen[id] = true
		s.meters[id] = meterFile{stamp: st, rep: r}
		changed = append(changed, id)
	}
	for id := range s.meters {
		if !seen[id] {
			delete(s.meters, id)
			changed = append(changed, id)
		}
	}
	if len(changed) == 0 {
		return
	}
	s.publishLimits()
	slices.Sort(changed)
	for _, id := range changed {
		if t, ok := s.tracked[id]; ok {
			s.publish(t, false)
		}
	}
}

// watchMeters watches the meter directory once it exists, and again when
// it was removed and made anew (removing a directory ends its watch).
func (s *Store) watchMeters() {
	if s.w == nil {
		return
	}
	fi, err := os.Stat(s.meterDir)
	exists := err == nil && fi.IsDir()
	if exists && !s.meterWatched {
		s.watchDir(s.meterDir)
	}
	s.meterWatched = exists
}

// publishLimits publishes the rate limits the reports of this store's own
// sessions hold, merged by meter.Merge. The meter directory is shared by
// every Claude directory on the machine, and a report for a session that
// is neither tracked nor live here belongs to another one, which may be
// another account; publish and unpublish call this again when a session
// with a report comes or goes.
func (s *Store) publishLimits() {
	var reports []meter.Report
	for id, m := range s.meters {
		if _, ok := s.tracked[id]; ok || s.live(id) {
			reports = append(reports, m.rep)
		}
	}
	var lr limitsReport
	lr.limits, lr.at = meter.Merge(reports)
	s.mu.Lock()
	changed := !reflect.DeepEqual(s.limits, lr)
	s.limits = lr
	s.mu.Unlock()
	if changed {
		s.subs.notify(core.Event{Kind: core.SessionsChanged})
	}
}

// cleanMeters removes the state files of sessions that are no longer live
// and were last reported long ago. The directory is ccdeck's own.
func (s *Store) cleanMeters(now time.Time) {
	if s.meterDir == "" {
		return
	}
	if n := meter.Cleanup(s.meterDir, now, s.live); n > 0 {
		s.refreshMeters()
	}
}

// applyMeter lays a session's status-line report over the figures built
// from its transcript. The report is always kept on Session.Meter; it
// overrides only when it is at least as new as the latest main-thread
// assistant message or later compaction (contextAt), because otherwise the
// transcript has moved on. Then the context comes from it when it has a context figure
// and a window size, and the cost when it has one above zero, as
// CostClaude. A cost Claude Code recorded at the end of the session
// (CostReported) still wins.
func applyMeter(sum *core.Session, m core.Meter, contextAt time.Time) {
	sum.Meter = &m
	if m.At.Before(contextAt) {
		return
	}
	if m.HasContext && m.ContextWindow > 0 {
		sum.ContextWindow = m.ContextWindow
		sum.ContextTokens = m.ContextTokens
		if sum.ContextTokens == 0 && m.ContextPercent > 0 {
			sum.ContextTokens = int64(math.Round(m.ContextPercent / 100 * float64(m.ContextWindow)))
		}
	}
	if m.CostUSD > 0 && sum.CostKind != core.CostReported {
		sum.CostUSD, sum.CostKind = m.CostUSD, core.CostClaude
	}
}
