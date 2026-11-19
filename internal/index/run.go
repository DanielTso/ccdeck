package index

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/core"
)

// run owns the parse state: it does the start-up scan, then applies file
// events, polls and sweeps until the store is closed.
func (s *Store) run() {
	defer s.wg.Done()
	s.loadPrices()
	s.loadCatalog()
	s.addWatches()
	s.scan()
	close(s.ready)
	if s.closed() {
		return
	}
	s.scanned = true
	s.save()
	s.prevCache = nil

	poll := time.NewTicker(s.tn.poll)
	defer poll.Stop()
	lastSweep := time.Now()
	for {
		select {
		case <-s.done:
			return
		case <-s.kick:
			s.process()
		case now := <-poll.C:
			if now.Sub(lastSweep) >= s.tn.sweep {
				s.sweep()
				lastSweep = now
			} else {
				s.pollLive()
			}
		}
	}
}

// scan reads every transcript in a bounded worker pool and publishes each
// session as it completes, live sessions and then the most recently
// modified first.
func (s *Store) scan() {
	s.regs = s.readRegistry()
	cache := s.loadCache()
	s.prevCache = cache // kept by a save before the scan completes
	files, err := s.paths.Transcripts()
	if err != nil {
		s.log.Warn("index: list transcripts", "err", err)
	}
	s.order(files)

	var stats Stats
	for t := range s.loadAll(files, cache) {
		if t.main == nil {
			continue // vanished since it was listed
		}
		s.tracked[t.id] = t
		s.publish(t, false)
		for _, f := range t.files() {
			st := f.acc.State()
			stats.Files++
			stats.Lines += st.Lines
			stats.Skipped += st.Skipped
			if f.resumed {
				stats.Resumed++
			}
		}
	}
	for id := range s.regs {
		if _, ok := s.tracked[id]; !ok {
			t := &tracked{id: id, subs: map[string]*subagent{}}
			s.tracked[id] = t
			s.publish(t, false)
		}
	}
	s.dirty = true

	s.mu.Lock()
	stats.Sessions = len(s.sums)
	stats.ScanTime = time.Since(s.start)
	s.stats = stats
	s.mu.Unlock()
}

// order sorts files so that live sessions come first, then the most
// recently modified.
func (s *Store) order(files []claude.TranscriptFile) {
	mtime := make(map[string]time.Time, len(files))
	for _, f := range files {
		if fi, err := os.Stat(f.Path); err == nil {
			mtime[f.Path] = fi.ModTime()
		}
	}
	slices.SortStableFunc(files, func(a, b claude.TranscriptFile) int {
		_, liveA := s.regs[a.SessionID]
		_, liveB := s.regs[b.SessionID]
		if liveA != liveB {
			if liveA {
				return -1
			}
			return 1
		}
		return mtime[b.Path].Compare(mtime[a.Path])
	})
}

// loadAll loads files on tn.workers goroutines and delivers the results in
// completion order. The channel is closed when every file is done, or soon
// after the store is closed.
func (s *Store) loadAll(files []claude.TranscriptFile, cache map[string]cachedFile) <-chan *tracked {
	jobs := make(chan claude.TranscriptFile)
	results := make(chan *tracked)
	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case jobs <- f:
			case <-s.done:
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for range max(s.tn.workers, 1) {
		wg.Go(func() {
			for f := range jobs {
				// After Close, load stops between lines and returns what it
				// read, which is still delivered so that it can be cached.
				results <- load(s.ctx, f.SessionID, f.Path, cache, s.log)
			}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	return results
}

// pending is work collected from file events for run.
type pending struct {
	sessions map[string]string // session id → main transcript path
	registry bool              // the registry directory changed
	sweep    bool              // something was missed: rescan everything
}

// mark records work for run under pmu.
func (s *Store) mark(f func(p *pending)) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.pend.sessions == nil {
		s.pend.sessions = map[string]string{}
	}
	f(&s.pend)
}

// process applies the pending work.
func (s *Store) process() {
	s.pmu.Lock()
	p := s.pend
	s.pend = pending{}
	s.pmu.Unlock()

	if p.sweep {
		s.sweep()
		return
	}
	if p.registry {
		s.refreshLive()
	}
	for id, path := range p.sessions {
		s.refresh(id, path)
	}
}

// pollLive is the safety net that runs every poll interval: it notices
// dead processes and extends the transcripts of live sessions.
func (s *Store) pollLive() {
	s.refreshLive()
	for id := range s.regs {
		if t, ok := s.tracked[id]; ok {
			s.refresh(id, t.path)
		}
	}
}

// sweep rescans everything: prices, catalog, registry, and every
// transcript. It finds sessions whose events were missed, drops vanished
// ones, and rebuilds every summary, so recent samples age out. Then it
// saves the cache if anything changed.
func (s *Store) sweep() {
	s.loadPrices()
	s.loadCatalog()
	s.refreshLive()
	files, err := s.paths.Transcripts()
	if err != nil {
		s.log.Warn("index: list transcripts", "err", err)
		return
	}
	listed := make(map[string]bool, len(files))
	for _, f := range files {
		listed[f.SessionID] = true
		s.refresh(f.SessionID, f.Path)
	}
	for id, t := range s.tracked {
		if !listed[id] {
			s.refresh(id, t.path)
		}
	}
	if s.dirty {
		s.save()
	}
}

// refresh brings one session up to date and republishes it. path is its
// main transcript, or empty when not known. A session with neither a
// transcript nor a live process is dropped.
func (s *Store) refresh(id, path string) {
	t, ok := s.tracked[id]
	if !ok {
		if path == "" {
			return
		}
		t = load(s.ctx, id, path, nil, s.log)
		if t.main == nil && !s.live(id) {
			return
		}
		s.tracked[id] = t
		s.dirty = true
		s.publish(t, false)
		return
	}
	if t.path == "" && path != "" {
		t.path = path
	}

	grew := false
	if t.main == nil && t.path != "" {
		if f, err := openFile(s.ctx, t.path, nil); err == nil {
			t.main, grew = f, true
		}
	} else if t.main != nil {
		c, err := t.main.update(s.ctx)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			t.main, t.subs = nil, map[string]*subagent{}
		case s.ctx.Err() != nil:
		case err != nil:
			s.log.Warn("index: read transcript", "path", t.path, "err", err)
		}
		grew = c
	}
	subsChanged := t.main != nil && syncSubagents(s.ctx, t, nil, s.log)
	if grew || subsChanged {
		s.dirty = true
	}

	if t.main == nil && !s.live(id) {
		delete(s.tracked, id)
		s.unpublish(id)
		return
	}
	s.publish(t, grew)
}

// live reports whether a live process owns session id.
func (s *Store) live(id string) bool {
	_, ok := s.regs[id]
	return ok
}

func (s *Store) readRegistry() map[string]claude.Registration {
	regs, err := claude.ReadRegistry(s.paths.SessionsDir(), s.tn.procs)
	if err != nil {
		s.log.Warn("index: read registry", "err", err)
	}
	m := make(map[string]claude.Registration, len(regs))
	for _, r := range regs {
		m[r.SessionID] = r
	}
	return m
}

// beingRewritten reports whether a registration that just failed to read
// is more likely caught mid-write than gone: its file is still there, its
// process is still alive, and no other entry claims the process. Its last
// good reading is then kept until the file reads cleanly again.
func (s *Store) beingRewritten(r claude.Registration) bool {
	for _, now := range s.regs {
		if now.Live.PID == r.Live.PID {
			return false // the process moved on to another session
		}
	}
	name := filepath.Join(s.paths.SessionsDir(), strconv.Itoa(r.Live.PID)+".json")
	if _, err := os.Stat(name); err != nil {
		return false
	}
	return s.tn.procs.Alive(r.Live.PID, r.ProcStart)
}

// refreshLive rereads the registry and republishes the sessions whose live
// state changed. A live session with no transcript is listed; it is
// dropped again when its process goes away.
func (s *Store) refreshLive() {
	old := s.regs
	s.regs = s.readRegistry()
	for id, was := range old {
		if _, ok := s.regs[id]; !ok && s.beingRewritten(was) {
			s.regs[id] = was
		}
	}
	ids := slices.Collect(maps.Keys(old))
	for id := range s.regs {
		if _, ok := old[id]; !ok {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		was, wasLive := old[id]
		now, isLive := s.regs[id]
		if wasLive == isLive && reflect.DeepEqual(was, now) {
			continue
		}
		t, ok := s.tracked[id]
		switch {
		case ok && t.main == nil && !isLive:
			delete(s.tracked, id)
			s.unpublish(id)
		case ok:
			s.publish(t, false)
		case isLive:
			t = &tracked{id: id, subs: map[string]*subagent{}}
			s.tracked[id] = t
			s.publish(t, false)
		}
	}
}

// publish rebuilds t's summary and, when it differs from the published
// one, publishes it and notifies subscribers. entries says the main
// transcript grew, which subscribers hear about as EntriesChanged.
func (s *Store) publish(t *tracked, entries bool) {
	sum := s.summarize(t, time.Now())
	s.mu.Lock()
	old, ok := s.sums[t.id]
	changed := !ok || !reflect.DeepEqual(old.sum, sum)
	if changed {
		s.sums[t.id] = published{sum: sum, days: t.dayBuckets()}
		s.sorted = nil
	}
	s.mu.Unlock()
	if changed {
		s.subs.notify(core.Event{Kind: core.SessionsChanged, SessionID: t.id})
	}
	if entries {
		s.subs.notify(core.Event{Kind: core.EntriesChanged, SessionID: t.id})
	}
}

func (s *Store) unpublish(id string) {
	s.mu.Lock()
	_, ok := s.sums[id]
	delete(s.sums, id)
	s.sorted = nil
	s.mu.Unlock()
	if ok {
		s.subs.notify(core.Event{Kind: core.SessionsChanged, SessionID: id})
	}
}

// loadPrices rereads Claude Code's price cache when its file changed and
// lays the configured overrides over it.
func (s *Store) loadPrices() {
	var st fileStamp
	if fi, err := os.Stat(s.paths.ConfigFile); err == nil {
		st = stampOf(fi)
	}
	if s.prices != nil && st == s.priceStamp {
		return
	}
	p, err := claude.ReadPrices(s.paths.ConfigFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("index: read prices", "err", err)
	}
	merged := claude.Prices{}
	maps.Copy(merged, p)
	maps.Copy(merged, s.overrides)
	s.mu.Lock()
	s.prices = merged
	s.mu.Unlock()
	s.priceStamp = st
}

func (s *Store) loadCatalog() {
	c, err := claude.ReadCatalog(s.paths.CatalogDir())
	if err != nil {
		s.log.Warn("index: read model catalog", "err", err)
		return
	}
	s.catalog = c
}
