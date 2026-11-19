package index

import (
	"os"
	"strings"
	"sync"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/core"
)

// history is the prompt history, reread when its file changes.
type history struct {
	mu    sync.Mutex
	stamp fileStamp
	hits  []core.PromptHit // oldest first
	lower []string         // hits[i].Text in lower case
}

// SearchPrompts finds prompts from the prompt history that contain query,
// case-insensitively, newest first. limit <= 0 means all.
func (s *Store) SearchPrompts(query string, limit int) []core.PromptHit {
	h := &s.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	s.reloadHistory()
	q := strings.ToLower(query)
	var out []core.PromptHit
	for i := len(h.hits) - 1; i >= 0; i-- {
		if strings.Contains(h.lower[i], q) {
			out = append(out, h.hits[i])
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

// reloadHistory rereads the history file if it changed. The caller holds
// hist.mu.
func (s *Store) reloadHistory() {
	h := &s.hist
	path := s.paths.HistoryFile()
	fi, err := os.Stat(path)
	if err != nil {
		h.stamp, h.hits, h.lower = fileStamp{}, nil, nil
		return
	}
	st := stampOf(fi)
	if st == h.stamp {
		return
	}
	hits, err := claude.ReadHistory(path)
	if err != nil {
		s.log.Warn("index: read history", "err", err)
		return
	}
	h.stamp, h.hits = st, hits
	h.lower = make([]string, len(hits))
	for i, hit := range hits {
		h.lower[i] = strings.ToLower(hit.Text)
	}
}
