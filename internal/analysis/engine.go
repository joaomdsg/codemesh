// Package analysis loads and caches snapshots of repositories at given
// revisions, and compares two of them. A snapshot of a commit reads its
// source from a worktree that lives as long as the cache entry does.
package analysis

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// Worktree is the Rev of the working tree, which changes under the engine.
// Any other Rev is a ref the engine resolves to a commit sha.
const Worktree = "WORKTREE"

// churnWindow is how far back from the last commit churn counts commits.
const churnWindow = 90 * 24 * time.Hour

// Key names one snapshot: a repository, a revision and a root inside it.
type Key struct {
	Repo string
	Rev  string
	Root string // directory relative to the repository root
}

// Config sets up an Engine.
type Config struct {
	// Worktrees is the directory the engine checks commits out under, one
	// subdirectory per repo id.
	Worktrees string
	// Repo finds a repository by id.
	Repo func(id string) (*gitx.Repo, error)
	// Load analyses the project in dir.
	Load func(ctx context.Context, dir string) (*code.Snapshot, error)
	// Workers bounds concurrent loads. Zero means half of GOMAXPROCS, at least one.
	Workers int
	// Cap is how many snapshots stay cached. Zero means 8. Snapshots in use
	// are never evicted, so more can be resident.
	Cap int
	Log *slog.Logger
}

// Result is a loaded snapshot with its findings. Err is set instead when the
// load failed. Release gives the result back; until then the cache keeps the
// snapshot and its worktree.
type Result struct {
	Key      Key // Rev is the resolved sha, or Worktree
	Snap     *code.Snapshot
	Findings []smell.Finding
	Took     time.Duration
	Err      error

	release func()
}

// Release ends the caller's use of the result. It is safe to call more than
// once, and on a failed result.
func (r *Result) Release() {
	if r.release != nil {
		r.release()
	}
}

// Engine loads snapshots on a bounded pool and caches them by commit.
type Engine struct {
	cfg   Config
	cache *cache
	group singleflight.Group
	pool  pool

	// wtMu serialises worktree changes; wtRefs counts the entries reading
	// each worktree, as several roots of one commit share it.
	wtMu   sync.Mutex
	wtRefs map[string]int
}

// New returns an Engine.
func New(cfg Config) *Engine {
	if cfg.Workers <= 0 {
		cfg.Workers = max(1, runtime.GOMAXPROCS(0)/2)
	}
	if cfg.Cap <= 0 {
		cfg.Cap = 8
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Engine{
		cfg:    cfg,
		cache:  newCache(cfg.Cap),
		pool:   make(pool, cfg.Workers),
		wtRefs: map[string]int{},
	}
}

// Get returns the snapshot for key, loading it unless it is cached or being
// loaded. Callers must Release the result. A cancelled ctx stops the wait,
// not the load, which other callers or a later Get can use.
func (e *Engine) Get(ctx context.Context, key Key) *Result {
	repo, err := e.cfg.Repo(key.Repo)
	if err != nil {
		return &Result{Key: key, Err: err}
	}
	if key.Rev != Worktree {
		if key.Rev, err = repo.ResolveRef(key.Rev); err != nil {
			return &Result{Key: key, Err: err}
		}
	}
	for {
		if ent := e.cache.acquire(key); ent != nil {
			return e.result(ent)
		}
		ch := e.group.DoChan(key.Repo+"\x00"+key.Rev+"\x00"+key.Root, func() (any, error) {
			if e.cache.has(key) {
				return nil, nil
			}
			return nil, e.load(repo, key)
		})
		select {
		case <-ctx.Done():
			return &Result{Key: key, Err: ctx.Err()}
		case r := <-ch:
			if r.Err != nil {
				return &Result{Key: key, Err: r.Err}
			}
		}
	}
}

// Invalidate drops the cached snapshot of rev in repo, for a Worktree whose
// files changed. Holders keep their result; the next Get loads afresh.
func (e *Engine) Invalidate(repo, rev string) {
	e.finish(e.cache.drop(func(k Key) bool { return k.Repo == repo && k.Rev == rev }))
}

func (e *Engine) result(ent *entry) *Result {
	var once sync.Once
	return &Result{
		Key: ent.key, Snap: ent.snap, Findings: ent.findings,
		release: func() { once.Do(func() { e.finish(e.cache.release(ent)) }) },
	}
}

func (e *Engine) load(repo *gitx.Repo, key Key) (err error) {
	ent := &entry{key: key, repo: repo}
	e.pool.run(func() {
		start := time.Now()
		checkout := repo.Dir
		if key.Rev != Worktree {
			if checkout, err = e.checkout(repo, key); err != nil {
				return
			}
			ent.wt = checkout
		}
		// The load must not die with the caller that started it.
		ctx := context.Background()
		if ent.snap, err = e.cfg.Load(ctx, filepath.Join(checkout, key.Root)); err != nil {
			e.dropWorktree(ent)
			return
		}
		var churn map[string]int
		if key.Rev == Worktree {
			churn, err = repo.Churn(churnWindow)
		} else {
			churn, err = repo.ChurnAt(key.Rev, churnWindow)
		}
		if err == nil {
			applyChurn(ent.snap, checkout, churn)
		}
		ent.findings = smell.Find(ent.snap)
		e.cfg.Log.Debug("analysed", "repo", key.Repo, "rev", key.Rev, "root", key.Root, "took", time.Since(start).Round(time.Millisecond))
		err = nil
	})
	if err != nil {
		return err
	}
	e.finish(e.cache.add(ent))
	return nil
}

func (e *Engine) checkout(repo *gitx.Repo, key Key) (string, error) {
	e.wtMu.Lock()
	defer e.wtMu.Unlock()
	dir, err := repo.WorktreeAt(filepath.Join(e.cfg.Worktrees, key.Repo), key.Rev)
	if err != nil {
		return "", err
	}
	e.wtRefs[dir]++
	return dir, nil
}

// finish releases the worktrees of entries that left the cache unheld.
func (e *Engine) finish(gone []*entry) {
	for _, ent := range gone {
		e.dropWorktree(ent)
	}
}

func (e *Engine) dropWorktree(ent *entry) {
	if ent.wt == "" {
		return
	}
	e.wtMu.Lock()
	defer e.wtMu.Unlock()
	if e.wtRefs[ent.wt]--; e.wtRefs[ent.wt] > 0 {
		return
	}
	delete(e.wtRefs, ent.wt)
	if err := ent.repo.RemoveWorktree(ent.wt); err != nil {
		e.cfg.Log.Warn("remove worktree", "dir", ent.wt, "err", err)
	}
}

// applyChurn copies per-path commit counts onto the snapshot's files. Churn
// paths are relative to the repo root, file paths to the module root.
func applyChurn(snap *code.Snapshot, repoDir string, churn map[string]int) {
	prefix := ""
	if rel, err := filepath.Rel(repoDir, snap.Dir); err == nil && rel != "." {
		prefix = filepath.ToSlash(rel) + "/"
	}
	for _, p := range snap.Packages {
		for _, f := range p.Files {
			f.Churn = churn[prefix+f.Path]
		}
	}
}
