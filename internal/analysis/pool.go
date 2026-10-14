package analysis

import (
	"context"
	"slices"
	"sync"
)

// pool bounds how many loads run at once. A load holds a go/packages
// process tree, so it is heavy on CPU and memory. Waiting loads a page asked
// for go before warm-up loads, in arrival order otherwise.
type pool struct {
	mu    sync.Mutex
	free  int
	queue []*waiter
}

type waiter struct {
	key    Key
	urgent bool
	ready  chan struct{}
}

func newPool(n int) *pool { return &pool{free: n} }

// run calls f once a slot is free, or returns ctx's error if it ends first.
// urgent is read as the load queues.
func (p *pool) run(ctx context.Context, key Key, urgent func() bool, f func()) error {
	p.mu.Lock()
	if p.free > 0 {
		p.free--
		p.mu.Unlock()
	} else {
		w := &waiter{key: key, urgent: urgent(), ready: make(chan struct{})}
		p.queue = append(p.queue, w)
		p.mu.Unlock()
		select {
		case <-w.ready:
		case <-ctx.Done():
			p.mu.Lock()
			i := slices.Index(p.queue, w)
			if i >= 0 {
				p.queue = slices.Delete(p.queue, i, i+1)
			}
			p.mu.Unlock()
			if i < 0 {
				// The slot was handed over as ctx ended: pass it on.
				p.done()
			}
			return ctx.Err()
		}
	}
	defer p.done()
	f()
	return nil
}

// promote moves a waiting load of key ahead of the warm-up loads.
func (p *pool) promote(key Key) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range p.queue {
		if w.key == key {
			w.urgent = true
		}
	}
}

func (p *pool) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		p.free++
		return
	}
	i := max(0, slices.IndexFunc(p.queue, func(w *waiter) bool { return w.urgent }))
	w := p.queue[i]
	p.queue = slices.Delete(p.queue, i, i+1)
	close(w.ready)
}
