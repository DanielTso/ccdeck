package index

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"

	"github.com/DanielTso/ccdeck/internal/claude"
)

// cacheVersion is the version of the cache file format. Bump it whenever
// cacheFile or anything it holds changes meaning; a file with another
// version, or another claude.StateVersion, is ignored and rewritten.
// Version 3 added Thresholds, with usage filed by price tier.
const cacheVersion = 3

// cacheFile is the on-disk cache: derived numbers and identifiers per
// transcript, never conversation text. claude.State guarantees that for the
// accumulator; subagent meta files, which hold descriptions, are not cached.
type cacheFile struct {
	Version      int    `json:"version"`
	StateVersion int    `json:"stateVersion"`
	Dir          string `json:"dir"` // the Claude directory
	// Thresholds are the long-prompt thresholds the states were folded
	// with: under others, usage would be filed under other tier keys.
	Thresholds claude.Thresholds     `json:"thresholds,omitempty"`
	Files      map[string]cachedFile `json:"files"`
}

// cachedFile is one transcript, main or subagent, keyed by its path.
type cachedFile struct {
	Stamp fileStamp    `json:"stamp"`
	State claude.State `json:"state"`
	Days  dayBuckets   `json:"days,omitempty"`
	// FP is the claude.Fingerprint of the transcript before State.Offset,
	// which tells a file that only grew from one that was replaced.
	FP uint64 `json:"fp,omitempty"`
}

// cacheFileName gives every Claude directory its own cache file.
func cacheFileName(claudeDir string) string {
	sum := sha256.Sum256([]byte(claudeDir))
	return fmt.Sprintf("index-%x.json", sum[:8])
}

// loadCache reads the cache at path. A missing, unreadable or outdated
// cache is no cache.
func (s *Store) loadCache() map[string]cachedFile {
	if s.cachePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("index: read cache", "err", err)
		}
		return nil
	}
	var c cacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		s.log.Warn("index: decode cache", "err", err)
		return nil
	}
	if c.Version != cacheVersion || c.StateVersion != claude.StateVersion || c.Dir != s.paths.Dir ||
		!maps.Equal(c.Thresholds, s.thresholds) {
		return nil
	}
	return c.Files
}

// save writes the parse state of every tracked transcript to the cache. It
// runs on the run goroutine, or in Close once run has stopped.
func (s *Store) save() {
	if s.cachePath == "" {
		return
	}
	c := cacheFile{
		Version:      cacheVersion,
		StateVersion: claude.StateVersion,
		Dir:          s.paths.Dir,
		Thresholds:   s.thresholds,
		Files:        map[string]cachedFile{},
	}
	for path, cf := range s.prevCache {
		c.Files[path] = cf // replaced below by every transcript read since
	}
	for _, t := range s.tracked {
		for _, f := range t.files() {
			st := f.acc.State()
			var fp uint64
			if f.tail.Offset() == st.Offset {
				fp = f.tail.Fingerprint()
			}
			c.Files[f.path] = cachedFile{Stamp: f.stamp, State: st, Days: f.days.buckets, FP: fp}
		}
	}
	if err := writeCache(s.cachePath, &c); err != nil {
		s.log.Warn("index: write cache", "err", err)
		return
	}
	s.dirty = false
}

// writeCache replaces the file at path atomically, readable by the owner
// only.
func writeCache(path string, c *cacheFile) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp") // created with mode 0600
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name()) // best effort: the write already failed
	}
	return err
}
