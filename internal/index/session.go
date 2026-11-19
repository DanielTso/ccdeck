package index

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/core"
)

// tracked is the parse state of one session.
type tracked struct {
	id   string
	path string               // main transcript; empty while unknown
	main *file                // nil while the session is live with no transcript
	subs map[string]*subagent // by transcript path
}

type subagent struct {
	id   string
	meta claude.SubagentMeta
	f    *file
}

// fileStamp is what decides whether a file changed since it was read.
type fileStamp struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"` // Unix nanoseconds
}

func stampOf(fi os.FileInfo) fileStamp {
	return fileStamp{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
}

// file is one transcript, main or subagent, folded as far as it has been
// read. It is the Sink of its own Tailer.
type file struct {
	path    string
	stamp   fileStamp // when last read
	acc     *claude.Accumulator
	days    *dayTracker
	tail    *claude.Tailer
	resumed bool // continued from the cache
}

// Line folds one line into the accumulator, whose usage hook records when
// the usage happened.
func (f *file) Line(offset int64, line []byte) {
	f.acc.Line(offset, line)
}

// Reset starts over: the file shrank or was replaced.
func (f *file) Reset() {
	f.acc.Reset()
	f.days.buckets = dayBuckets{}
}

// refile makes the next update read the file again from the start, filing
// usage under the thresholds th.
func (f *file) refile(th claude.Thresholds) {
	f.acc.SetThresholds(th)
	f.tail.Restart(f)
	f.stamp = fileStamp{}
}

// track sets the accumulator and the day buckets that follow its usage.
func (f *file) track(acc *claude.Accumulator, days dayBuckets) {
	f.acc = acc
	f.days = newDayTracker(days)
	acc.OnUsage(f.days.record)
}

// openFile reads the transcript at path, continuing from c when c is a
// cached state that still matches the file. th files usage of long prompts
// (see claude.Accumulator.SetThresholds); c must have been folded with the
// same thresholds, which loadCache checks.
func openFile(ctx context.Context, path string, c *cachedFile, th claude.Thresholds) (*file, error) {
	f := &file{path: path}
	if c != nil {
		f.resume(c)
	}
	if f.acc == nil {
		f.track(claude.NewAccumulator(), nil)
		f.tail = claude.NewTailer(path, 0)
	}
	f.acc.SetThresholds(th)
	_, err := f.update(ctx)
	return f, err
}

// resume restores the cached state c when the file may only have grown
// since: it is not smaller than the cached offset, not the same size with
// another mtime (rewritten in place), and the content before the offset
// still has the cached fingerprint. RestoreAccumulator checks the lines the
// state points at, and the Tailer checks that the cached offset still ends
// a line; any mismatch leaves f to be parsed from the start.
func (f *file) resume(c *cachedFile) {
	fi, err := os.Stat(f.path)
	if err != nil || fi.Size() < c.State.Offset || c.FP == 0 && c.State.Offset > 0 {
		return
	}
	if st := stampOf(fi); st.Size == c.Stamp.Size && st != c.Stamp {
		return
	}
	r, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer func() { _ = r.Close() }() // read-only
	if fp, err := claude.Fingerprint(r, c.State.Offset); err != nil || fp != c.FP {
		return
	}
	acc, err := claude.RestoreAccumulator(c.State, r)
	if err != nil {
		return
	}
	f.track(acc, c.Days)
	f.tail = claude.ResumeTailer(f.path, c.State.Offset, c.FP)
	f.stamp = c.Stamp
	f.resumed = true
}

// update reads whatever was appended since the last read. changed is true
// when lines were read or the file was found replaced and read again. When
// ctx ends it stops between lines; the stamp then stays as it was, so the
// rest is read next time.
func (f *file) update(ctx context.Context) (changed bool, err error) {
	fi, err := os.Stat(f.path)
	if err != nil {
		return false, err
	}
	st := stampOf(fi)
	if st == f.stamp {
		return false, nil
	}
	if st.Size == f.stamp.Size && f.stamp != (fileStamp{}) {
		// Appending always grows a transcript: the same size with another
		// mtime is a rewrite, which may differ anywhere.
		f.tail.Restart(f)
	}
	before := f.tail.Offset()
	n, err := f.tail.ReadContext(ctx, f)
	if err != nil {
		return n > 0, err
	}
	f.stamp = st
	return n > 0 || f.tail.Offset() != before, nil
}

// load reads one session's transcripts, continuing from cache where it
// can. It touches no Store state, so start-up workers run it in parallel.
// main is nil in the result when the transcript is gone. When ctx ends the
// result holds what was read so far, which is consistent and can be cached.
func load(ctx context.Context, id, path string, cache map[string]cachedFile, th claude.Thresholds, log *slog.Logger) *tracked {
	t := &tracked{id: id, path: path, subs: map[string]*subagent{}}
	f, err := openFile(ctx, path, cached(cache, path), th)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return t
	case ctx.Err() != nil:
		t.main = f
		return t
	case err != nil:
		log.Warn("index: read transcript", "path", path, "err", err)
	}
	t.main = f
	syncSubagents(ctx, t, cache, th, log)
	return t
}

func cached(cache map[string]cachedFile, path string) *cachedFile {
	if c, ok := cache[path]; ok {
		return &c
	}
	return nil
}

// syncSubagents brings t's subagent transcripts up to date: new ones are
// read (from cache when possible), known ones extended, vanished ones
// dropped. It reports whether anything changed.
func syncSubagents(ctx context.Context, t *tracked, cache map[string]cachedFile, th claude.Thresholds, log *slog.Logger) bool {
	if t.path == "" {
		return false
	}
	files, err := claude.SubagentFiles(t.path)
	if err != nil {
		log.Warn("index: list subagents", "path", t.path, "err", err)
		return false
	}
	changed := false
	seen := make(map[string]bool, len(files))
	for _, sf := range files {
		seen[sf.Path] = true
		sa, ok := t.subs[sf.Path]
		if !ok {
			f, err := openFile(ctx, sf.Path, cached(cache, sf.Path), th)
			if ctx.Err() != nil {
				return changed // stopping: what was read of f is not kept
			}
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					log.Warn("index: read subagent", "path", sf.Path, "err", err)
				}
				continue
			}
			sa = &subagent{id: sf.ID, f: f}
			t.subs[sf.Path] = sa
			changed = true
		} else {
			c, err := sa.f.update(ctx)
			if ctx.Err() != nil {
				return changed || c
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Warn("index: read subagent", "path", sf.Path, "err", err)
			}
			changed = changed || c
		}
		// The meta file may be written after the transcript, so keep
		// looking until it has something.
		if sa.meta == (claude.SubagentMeta{}) {
			if m, err := claude.ReadSubagentMeta(sf.MetaPath); err == nil && m != (claude.SubagentMeta{}) {
				sa.meta = m
				changed = true
			}
		}
	}
	for p := range t.subs {
		if !seen[p] {
			delete(t.subs, p)
			changed = true
		}
	}
	return changed
}

// files lists every transcript of t that has been read.
func (t *tracked) files() []*file {
	var out []*file
	if t.main != nil {
		out = append(out, t.main)
	}
	for _, sa := range t.subs {
		out = append(out, sa.f)
	}
	return out
}

// summarize builds t's summary from its transcripts, the registry, the
// catalog and the prices. It runs on the run goroutine.
func (s *Store) summarize(t *tracked, now time.Time) core.Session {
	acc, path := claude.NewAccumulator(), ""
	if t.main != nil {
		acc, path = t.main.acc, t.main.path
	}
	in := claude.SessionInput{ID: t.id, Path: path, Catalog: s.catalog, Prices: s.prices, Now: now}
	if r, ok := s.regs[t.id]; ok {
		in.Registration = &r
	}
	paths := make([]string, 0, len(t.subs))
	for p := range t.subs {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, strings.Compare)
	for _, p := range paths {
		sa := t.subs[p]
		in.Subagents = append(in.Subagents, claude.SubagentInput{ID: sa.id, Meta: sa.meta, Acc: sa.f.acc})
	}
	sum := acc.Session(in)
	if m, ok := s.meters[t.id]; ok {
		applyMeter(&sum, m.rep.Meter, acc.ContextAt())
	}
	return sum
}
