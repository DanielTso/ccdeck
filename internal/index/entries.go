package index

import (
	"slices"
	"sync"

	"github.com/DanielTso/ccdeck/internal/claude"
	"github.com/DanielTso/ccdeck/internal/core"
)

// convCache keeps the conversations of the few most recently viewed
// sessions, so that following a live session reads only what was appended.
type convCache struct {
	mu    sync.Mutex
	max   int
	items map[string]*conv
	order []string // session ids, least recently used first
}

// conv is one session's conversation, read as far as its tailer got.
type conv struct {
	mu   sync.Mutex
	path string
	tail *claude.Tailer
	c    claude.Conversation
}

func newConvCache(n int) *convCache {
	return &convCache{max: max(n, 1), items: map[string]*conv{}}
}

// entries returns the last limit entries of the transcript at path,
// reading whatever was appended since the last call.
func (cc *convCache) entries(id, path string, limit int) ([]core.Entry, error) {
	cv := cc.get(id, path)
	cv.mu.Lock()
	defer cv.mu.Unlock()
	if _, err := cv.tail.Read(&cv.c); err != nil {
		return nil, err
	}
	return cv.c.Entries(limit), nil
}

// get returns the conversation for id, creating it if needed, marks it most
// recently used, and evicts the least recently used beyond max.
func (cc *convCache) get(id, path string) *conv {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cv, ok := cc.items[id]
	if !ok || cv.path != path {
		cv = &conv{path: path, tail: claude.NewTailer(path, 0)}
		cc.items[id] = cv
	}
	if i := slices.Index(cc.order, id); i >= 0 {
		cc.order = slices.Delete(cc.order, i, i+1)
	}
	cc.order = append(cc.order, id)
	for len(cc.order) > cc.max {
		delete(cc.items, cc.order[0])
		cc.order = cc.order[1:]
	}
	return cv
}
