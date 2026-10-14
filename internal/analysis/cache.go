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
	at       time.Time // when the snapshot was analysed
	wt       string    // worktree the snapshot reads from; "" for WORKTREE
	basis    string    // what the load read; see Engine.basis
	stale    bool      // newer files are due; the entry stays until they land
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

// acquire returns the entry for key with one more hold on it, or nil. With
// current set, a stale entry counts as none.
func (c *cache) acquire(k Key, current bool) (e *entry, stale bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e = c.m[k]
	if e == nil || current && e.stale {
		return nil, false
	}
	c.tick++
	e.refs++
	e.used = c.tick
	return e, e.stale
}

// current reports whether key has an entry that is not stale.
func (c *cache) current(k Key) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[k] != nil && !c.m[k].stale
}

// holds reports whether key has an entry, stale or not.
func (c *cache) holds(k Key) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[k] != nil
}

// add stores e, unheld, in place of any entry of its key, or only when there
// is none if absent is set. It reports whether it stored e, and returns the
// entries now out of the cache and unheld.
func (c *cache) add(e *entry, absent bool) (added bool, gone []*entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if absent && c.m[e.key] != nil {
		return false, nil
	}
	if old := c.m[e.key]; old != nil {
		old.removed = true
		if old.refs == 0 {
			gone = append(gone, old)
		}
	}
	c.tick++
	e.used = c.tick
	c.m[e.key] = e
	return true, append(gone, c.trim(e)...)
}

// stale marks the entries match selects as stale and returns their keys.
func (c *cache) stale(match func(Key) bool) (keys []Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if match(k) {
			e.stale = true
			keys = append(keys, k)
		}
	}
	return keys
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
