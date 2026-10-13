package analysis

import (
	"sync"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/smell"
)

type entry struct {
	key      Key
	snap     *code.Snapshot
	findings []smell.Finding
	repo     *gitx.Repo
	took     time.Duration
	wt       string // worktree the snapshot reads from; "" for WORKTREE
	refs     int
	used     uint64
	removed  bool
}

// cache holds loaded snapshots. It evicts the least recently used entry
// nobody holds once it is over cap; held entries stay, so it can exceed cap.
type cache struct {
	mu   sync.Mutex
	m    map[Key]*entry
	cap  int
	tick uint64
}

func newCache(cap int) *cache { return &cache{m: map[Key]*entry{}, cap: cap} }

// acquire returns the entry for key with one more hold on it, or nil.
func (c *cache) acquire(k Key) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[k]
	if e == nil {
		return nil
	}
	c.tick++
	e.refs++
	e.used = c.tick
	return e
}

func (c *cache) has(k Key) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[k] != nil
}

// add stores e, unheld, and returns the entries it evicted.
func (c *cache) add(e *entry) []*entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick++
	e.used = c.tick
	c.m[e.key] = e
	return c.trim(e)
}

// trim evicts unheld entries, least recently used first, until the cache is
// within cap. keep is never evicted.
func (c *cache) trim(keep *entry) (evicted []*entry) {
	for len(c.m) > c.cap {
		var oldest *entry
		for _, o := range c.m {
			if o != keep && o.refs == 0 && (oldest == nil || o.used < oldest.used) {
				oldest = o
			}
		}
		if oldest == nil {
			break
		}
		delete(c.m, oldest.key)
		oldest.removed = true
		evicted = append(evicted, oldest)
	}
	return evicted
}

// release drops one hold. It returns the entries that are now out of the
// cache and unheld, so their worktrees can go: e if it was dropped, and any
// entry the cache can now evict.
func (c *cache) release(e *entry) (gone []*entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.refs--
	if e.removed && e.refs == 0 {
		gone = append(gone, e)
	}
	return append(gone, c.trim(nil)...)
}

// drop removes the entries match selects. Held ones are returned later by release.
func (c *cache) drop(match func(Key) bool) (gone []*entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if !match(k) {
			continue
		}
		delete(c.m, k)
		e.removed = true
		if e.refs == 0 {
			gone = append(gone, e)
		}
	}
	return gone
}
