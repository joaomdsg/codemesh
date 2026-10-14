package live

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/gitx"
)

// Update announces that a repo's working tree changed. The engine reloads
// the snapshots it held of it under analysis.Worktree, showing the old ones
// until each lands.
type Update struct {
	Repo string
	At   time.Time
}

// Watcher watches the working trees of local repos and keeps the engine's
// Worktree snapshots current. Each repo has its own topic. Safe for
// concurrent use.
type Watcher struct {
	ctx      context.Context
	eng      *analysis.Engine
	interval time.Duration
	log      *slog.Logger

	mu    sync.Mutex
	repos map[string]*watch
}

type watch struct {
	topic  *topic.Topic[Update]
	cancel context.CancelFunc
}

// NewWatcher returns a Watcher that polls each repo every interval until ctx ends.
func NewWatcher(ctx context.Context, eng *analysis.Engine, interval time.Duration, log *slog.Logger) *Watcher {
	return &Watcher{ctx: ctx, eng: eng, interval: interval, log: log, repos: map[string]*watch{}}
}

// Add starts watching the repo id at dir and returns its topic. A repo
// already watched keeps its watch.
func (w *Watcher) Add(id, dir string) *topic.Topic[Update] {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r := w.repos[id]; r != nil {
		return r.topic
	}
	ctx, cancel := context.WithCancel(w.ctx)
	r := &watch{topic: topic.New[Update](), cancel: cancel}
	w.repos[id] = r
	go w.poll(ctx, id, dir, r.topic)
	return r.topic
}

// Topic returns the topic of a watched repo, or nil.
func (w *Watcher) Topic(id string) *topic.Topic[Update] {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r := w.repos[id]; r != nil {
		return r.topic
	}
	return nil
}

// Remove stops watching id.
func (w *Watcher) Remove(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r := w.repos[id]; r != nil {
		r.cancel()
		delete(w.repos, id)
	}
}

func (w *Watcher) poll(ctx context.Context, id, dir string, t *topic.Topic[Update]) {
	repo, _ := gitx.Open(dir)
	last := fingerprint(dir, repo)
	tick := time.NewTicker(w.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if fp := fingerprint(dir, repo); fp != last {
				last = fp
				t.Publish(w.reload(id))
			}
		}
	}
}

// reload has the engine reload the repo's working-tree snapshots.
func (w *Watcher) reload(id string) Update {
	w.eng.Refresh(id, analysis.Worktree)
	return Update{Repo: id, At: time.Now()}
}
