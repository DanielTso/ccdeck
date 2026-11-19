package index

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// addWatches watches the Claude directory (for projects/ and sessions/
// appearing), the registry, and every directory under projects/:
// fsnotify is not recursive.
func (s *Store) addWatches() {
	if s.w == nil {
		return
	}
	s.watchDir(s.paths.Dir)
	s.watchDir(s.paths.SessionsDir())
	s.watchTree(s.paths.ProjectsDir())
}

func (s *Store) watchDir(dir string) {
	err := s.w.Add(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fsnotify.ErrClosed) {
		s.log.Warn("index: watch", "dir", dir, "err", err)
	}
}

// watchTree watches root and every directory below it.
func (s *Store) watchTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			s.watchDir(p)
		}
		return nil // unreadable entries are left to the sweep
	})
}

// watch turns file events into pending work and kicks run once the first
// event of a burst is debounce old, so a busy transcript is picked up at
// a steady pace rather than never.
func (s *Store) watch() {
	defer s.wg.Done()
	timer := time.NewTimer(s.tn.debounce)
	timer.Stop()
	armed := false
	arm := func() {
		if !armed {
			timer.Reset(s.tn.debounce)
			armed = true
		}
	}
	for {
		select {
		case <-s.done:
			timer.Stop()
			return
		case ev, ok := <-s.w.Events:
			if !ok {
				return
			}
			if s.tn.beforeEvent != nil {
				s.tn.beforeEvent(ev)
			}
			if s.onEvent(ev) {
				arm()
			}
		case err, ok := <-s.w.Errors:
			if !ok {
				return
			}
			s.log.Warn("index: watch", "err", err)
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				s.mark(func(p *pending) { p.sweep = true })
				arm()
			}
		case <-timer.C:
			armed = false
			select {
			case s.kick <- struct{}{}:
			default: // run has a kick waiting already
			}
		}
	}
}

// onEvent records the work one event calls for and reports whether there
// is any. New directories are watched at once and their contents queued,
// because files created before the watch was added send no events. A
// directory under a project belongs to a session (SessionOf), even the
// session's own directory, whose whole subagents tree may be written by the
// time its event is handled; refreshing the session then lists the tree,
// after the watches were added, so nothing falls between the two.
func (s *Store) onEvent(ev fsnotify.Event) bool {
	name := filepath.Clean(ev.Name)
	projects, sessions := s.paths.ProjectsDir(), s.paths.SessionsDir()
	switch dir := filepath.Dir(name); {
	case s.meterDir != "" && (dir == s.meterDir || name == s.meterDir):
		s.mark(func(p *pending) { p.meter = true })
		return true
	case name == projects:
		if ev.Has(fsnotify.Create) {
			s.watchTree(name)
			s.mark(func(p *pending) { p.sweep = true })
			return true
		}
		return false
	case name == sessions:
		if ev.Has(fsnotify.Create) {
			s.watchDir(name)
			s.mark(func(p *pending) { p.registry = true })
			return true
		}
		return false
	case dir == sessions:
		s.mark(func(p *pending) { p.registry = true })
		return true
	case dir == s.paths.Dir:
		return false // settings, history and the like
	}

	if ev.Has(fsnotify.Create) {
		if fi, err := os.Lstat(name); err == nil && fi.IsDir() {
			s.watchTree(name)
			if filepath.Dir(name) == projects {
				s.queueProject(name)
				return true
			}
		}
	}
	id, main, ok := s.sessionFile(name)
	if !ok {
		return false
	}
	s.mark(func(p *pending) { p.sessions[id] = main })
	return true
}

// queueProject queues every transcript in a new project directory.
func (s *Store) queueProject(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok && e.Type().IsRegular() {
			path := filepath.Join(dir, e.Name())
			s.mark(func(p *pending) { p.sessions[id] = path })
		}
	}
}

// sessionFile maps a path under projects/ to its session and the session's
// main transcript.
func (s *Store) sessionFile(name string) (id, main string, ok bool) {
	id, ok = s.paths.SessionOf(name)
	if !ok {
		return "", "", false
	}
	rel, err := filepath.Rel(s.paths.ProjectsDir(), name)
	if err != nil {
		return "", "", false
	}
	slug, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return id, filepath.Join(s.paths.ProjectsDir(), slug, id+".jsonl"), true
}
