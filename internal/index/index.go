// Package index implements core.Store over a Claude directory. At start-up
// it reads every transcript in a bounded worker pool, continuing from an
// on-disk cache of derived numbers where it can, and publishes sessions as
// they complete. After that it keeps them current by watching the files,
// with a slow poll as a safety net.
//
// Concurrency: one goroutine (run) owns the parse state of every session and
// is the only writer of the published summaries; a second (watch) turns
// file-system events into pending work for it. The public methods read the
// published summaries under a mutex and never wait for parsing.
package index

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/core"
	"github.com/DanielTso/ccdeck/internal/meter"
)

// Price is what a model costs, in dollars per million tokens (WebSearch is
// dollars per request).
type Price = claude.Price

// ErrUnknownSession is returned by Entries for a session id the store does
// not know.
var ErrUnknownSession = errors.New("index: unknown session")

// ErrClaudeDir is returned by New, wrapped with the directory and the
// reason, when the Claude directory is missing or cannot be read.
var ErrClaudeDir = errors.New("index: cannot read the Claude directory")

// Options configures a Store. Every field is optional.
type Options struct {
	// ClaudeDir is the Claude directory. Empty means $CLAUDE_CONFIG_DIR,
	// else ~/.claude.
	ClaudeDir string
	// CacheDir holds the index cache. Empty means $XDG_CACHE_HOME/ccdeck,
	// else ~/.cache/ccdeck.
	CacheDir string
	// Prices maps usage keys (model ids, or model ids with a tier suffix
	// such as "+fast"; see claude.Prices) to prices that override Claude
	// Code's own price cache, typically the [prices] table of the config
	// file.
	Prices map[string]Price
	// MeterDir is the status-line meter's state directory (see package
	// meter). Empty means the meter is off: no reports, no limits.
	MeterDir string
	// Logger receives warnings about unreadable files, failed watches and
	// cache problems. Nil discards them.
	Logger *slog.Logger
}

// Stats describes the start-up scan, for diagnostics.
type Stats struct {
	Sessions int           // sessions published, live ones without a transcript included
	Files    int           // transcripts read, main and subagent
	Resumed  int           // of Files, those continued from the cache
	Lines    int           // non-blank transcript lines folded
	Skipped  int           // malformed lines among them
	ScanTime time.Duration // from New until every session was published
	Watches  int           // directories watched now
	Watching bool          // fsnotify works; false means polling only
	Cache    string        // the cache file; empty when there is no cache directory
}

// tuning holds what tests change; New uses defaultTuning.
type tuning struct {
	procs    claude.ProcessChecker
	debounce time.Duration // quiet time collected before acting on file events
	poll     time.Duration // registry and live-session check
	sweep    time.Duration // full rescan of every transcript
	workers  int           // start-up parsers
	entries  int           // conversations kept for Entries
	noWatch  bool          // poll and sweep only, as when fsnotify is unavailable
}

// followedMax is how many conversations Entries keeps incremental: the
// most the UI follows at once, which is sixteen Live panes (the config's
// maximum), the Sessions preview and an open transcript, with room to
// spare. Beyond it, round-robin reads would evict every conversation and
// reread each transcript from the start on every refresh.
const followedMax = 24

func defaultTuning() tuning {
	return tuning{
		procs:    claude.SystemProcesses(),
		debounce: 100 * time.Millisecond,
		poll:     2 * time.Second,
		sweep:    30 * time.Second,
		workers:  min(max(runtime.GOMAXPROCS(0), 2), 8),
		entries:  followedMax,
	}
}

// Store is a live core.Store over one Claude directory. Create it with New
// and release it with Close.
type Store struct {
	paths     claude.Paths
	cachePath string // empty when there is no usable cache directory
	meterDir  string // empty when the meter is off
	overrides claude.Prices
	log       *slog.Logger
	tn        tuning
	start     time.Time
	w         *fsnotify.Watcher // nil when watching is unavailable

	// Owned by run. prices is also read by Daily, so run writes it under mu.
	tracked    map[string]*tracked
	regs       map[string]claude.Registration
	catalog    claude.Catalog
	priceStamp fileStamp
	// thresholds are the long-prompt thresholds every accumulator files
	// usage with; the start-up workers read them, run replaces them.
	thresholds claude.Thresholds
	scanned    bool // the start-up scan finished
	dirty      bool // the cache is behind the parse state
	// prevCache is the cache read at start-up, until the scan completes:
	// a save before then keeps the entries of transcripts not read yet.
	prevCache map[string]cachedFile
	// meters are the status-line reports by session id; meterWatched is
	// set once the meter directory is watched.
	meters       map[string]meterFile
	meterWatched bool

	mu     sync.Mutex
	sums   map[string]published
	sorted []core.Session // Sessions() order; nil when stale
	prices claude.Prices
	stats  Stats
	limits limitsReport // the rate limits of this store's sessions' reports

	pmu  sync.Mutex
	pend pending
	kick chan struct{}

	subs subscribers
	conv *convCache
	hist history

	ready     chan struct{}
	done      chan struct{}
	ctx       context.Context // ends with Close, stopping transcript reads between lines
	stop      context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// published is what the public methods read for one session.
type published struct {
	sum  core.Session
	days dayBuckets // usage over time, main thread and subagents
}

var _ core.Store = (*Store)(nil)

// New starts a Store. It returns at once: sessions appear as the start-up
// scan publishes them, and Ready is closed when the scan is complete.
func New(opts Options) (*Store, error) {
	return newStore(opts, defaultTuning())
}

func newStore(opts Options, tn tuning) (*Store, error) {
	paths, err := claude.ResolvePaths(opts.ClaudeDir)
	if err != nil {
		return nil, err
	}
	// Absolute, so that watch event names and cache keys are unambiguous.
	if paths.Dir, err = filepath.Abs(paths.Dir); err != nil {
		return nil, err
	}
	if err := checkClaudeDir(paths); err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Store{
		paths:     paths,
		overrides: claude.Prices(opts.Prices),
		log:       log,
		tn:        tn,
		start:     time.Now(),
		tracked:   map[string]*tracked{},
		regs:      map[string]claude.Registration{},
		meters:    map[string]meterFile{},
		meterDir:  opts.MeterDir,
		sums:      map[string]published{},
		kick:      make(chan struct{}, 1),
		conv:      newConvCache(tn.entries),
		ready:     make(chan struct{}),
		done:      make(chan struct{}),
	}
	if s.meterDir != "" {
		// Absolute, so that watch event names compare.
		if s.meterDir, err = filepath.Abs(s.meterDir); err != nil {
			return nil, err
		}
	}
	s.ctx, s.stop = context.WithCancel(context.Background())
	if dir, err := cacheDir(opts.CacheDir); err != nil {
		log.Warn("index: no cache directory", "err", err)
	} else {
		s.cachePath = filepath.Join(dir, cacheFileName(paths.Dir))
	}
	if !tn.noWatch {
		if s.w, err = fsnotify.NewWatcher(); err != nil {
			log.Warn("index: watching unavailable, polling only", "err", err)
			s.w = nil
		}
	}

	s.wg.Add(1)
	go s.run()
	if s.w != nil {
		s.wg.Add(1)
		go s.watch()
	}
	return s, nil
}

// checkClaudeDir makes sure the Claude directory can be read, so that a
// wrong path is reported rather than shown as no sessions. projects/ may be
// missing (nothing recorded yet) but not unreadable.
func checkClaudeDir(p claude.Paths) error {
	fi, err := os.Stat(p.Dir)
	switch {
	case err != nil:
		return fmt.Errorf("%w %s: %w", ErrClaudeDir, p.Dir, err)
	case !fi.IsDir():
		return fmt.Errorf("%w %s: not a directory", ErrClaudeDir, p.Dir)
	}
	if _, err := os.ReadDir(p.Dir); err != nil {
		return fmt.Errorf("%w %s: %w", ErrClaudeDir, p.Dir, err)
	}
	if _, err := os.ReadDir(p.ProjectsDir()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w %s: %w", ErrClaudeDir, p.Dir, err)
	}
	return nil
}

// cacheDir resolves the cache directory: dir, else $XDG_CACHE_HOME/ccdeck,
// else ~/.cache/ccdeck.
func cacheDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	if x := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "ccdeck"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "ccdeck"), nil
}

// Ready is closed when the start-up scan has published every session, or
// when the store is closed first.
func (s *Store) Ready() <-chan struct{} { return s.ready }

// Stats reports on the start-up scan. It is zero until Ready is closed,
// apart from Watches, Watching and Cache.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	st := s.stats
	s.mu.Unlock()
	if s.w != nil {
		st.Watching = true
		st.Watches = len(s.w.WatchList())
	}
	st.Cache = s.cachePath
	return st
}

// Sessions returns a snapshot of all sessions, most recently active first.
func (s *Store) Sessions() []core.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sorted == nil {
		s.sorted = make([]core.Session, 0, len(s.sums))
		for _, p := range s.sums {
			s.sorted = append(s.sorted, p.sum)
		}
		slices.SortFunc(s.sorted, func(a, b core.Session) int {
			if c := b.LastActivity.Compare(a.LastActivity); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	return slices.Clone(s.sorted)
}

// Session returns one session by id.
func (s *Store) Session(id string) (core.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.sums[id]
	return p.sum, ok
}

// Entries returns up to limit most recent conversation entries of a
// session's main thread, oldest first; limit <= 0 means all. The
// conversations of recently viewed sessions are kept and extended as their
// transcripts grow. A live session with no transcript yet has no entries.
func (s *Store) Entries(id string, limit int) ([]core.Entry, error) {
	sum, ok := s.Session(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSession, id)
	}
	if sum.Path == "" {
		return nil, nil
	}
	return s.conv.entries(id, sum.Path, limit)
}

// Limits returns the rate-limit windows of the newest status-line report
// that has any, without the windows whose reset time has passed, and when
// that report was written. Both are zero when the meter is off, has never
// run, or every window has reset.
func (s *Store) Limits() ([]core.RateLimit, time.Time) {
	s.mu.Lock()
	l := s.limits
	s.mu.Unlock()
	cur := meter.Current(l.limits, time.Now())
	if len(cur) == 0 {
		return nil, time.Time{}
	}
	return cur, l.at
}

// Subscribe delivers change events until cancel is called or the store is
// closed, and then closes the channel. Events are coalesced while the
// subscriber is not reading, and the store never waits for it.
func (s *Store) Subscribe() (events <-chan core.Event, cancel func()) {
	return s.subs.subscribe()
}

// Close stops the watchers and background work, saves the cache, and
// closes every subscription. It is safe to call more than once.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.stop()
		if s.w != nil {
			s.closeErr = s.w.Close()
		}
		s.wg.Wait()
		if s.dirty {
			// run has stopped, so its state is ours now. After an
			// interrupted scan this keeps what was read, so the next
			// start continues from it.
			s.save()
		}
		s.subs.closeAll()
	})
	return s.closeErr
}

// closed reports whether Close has been called.
func (s *Store) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}
