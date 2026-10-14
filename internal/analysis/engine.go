// Package analysis loads and caches snapshots of repositories at given
// revisions, and compares two of them. A snapshot of a commit reads its
// source from a worktree that lives as long as the cache entry does.
package analysis

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// ErrClosed is the error of a load asked of a closed Engine.
var ErrClosed = errors.New("analysis engine closed")

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
	Took     time.Duration // how long the load took, for a cached result the load that made it
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

	// mu guards closed and flights: the loads in progress, which Close and
	// Forget cancel and wait for. It also guards epochs, which count the
	// invalidations of a repo's rev, so a load that began before one is not cached.
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	flights map[*flight]struct{}
	epochs  map[string]int
}

type flight struct {
	repo   string
	cancel context.CancelFunc
	done   chan struct{}
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
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		cfg:     cfg,
		cache:   newCache(cfg.Cap),
		pool:    make(pool, cfg.Workers),
		wtRefs:  map[string]int{},
		ctx:     ctx,
		cancel:  cancel,
		flights: map[*flight]struct{}{},
		epochs:  map[string]int{},
	}
}

// begin registers a load of repo. Its ctx ends with Close or Forget.
func (e *Engine) begin(repo string) (*flight, context.Context, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(e.ctx)
	f := &flight{repo: repo, cancel: cancel, done: make(chan struct{})}
	e.flights[f] = struct{}{}
	return f, ctx, nil
}

func (e *Engine) end(f *flight) {
	f.cancel()
	e.mu.Lock()
	delete(e.flights, f)
	e.mu.Unlock()
	close(f.done)
}

// Close cancels every load in progress, waits for them to end or for ctx to,
// refuses later loads, and drops the cache, which removes the worktrees nobody
// holds. Results still held stay valid until released.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.cancel()
	fs := slices.Collect(maps.Keys(e.flights))
	e.mu.Unlock()
	for _, f := range fs {
		select {
		case <-f.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.finish(e.cache.drop(func(Key) bool { return true }))
	return nil
}

// Busy reports whether a load is in progress. A load outlives the Get that
// asked for it when that Get's context ends.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.flights) > 0
}

// Forget cancels the loads in progress for repo, waits for them, and drops
// everything cached for it, so its directories can be removed.
func (e *Engine) Forget(repo string) {
	e.mu.Lock()
	var fs []*flight
	for f := range e.flights {
		if f.repo == repo {
			f.cancel()
			fs = append(fs, f)
		}
	}
	e.mu.Unlock()
	for _, f := range fs {
		<-f.done
	}
	e.finish(e.cache.drop(func(k Key) bool { return k.Repo == repo }))
}

// Get returns the snapshot for key, loading it unless it is cached or being
// loaded. Callers must Release the result. A cancelled ctx stops the wait,
// not the load, which other callers or a later Get can use; Close and Forget
// stop it.
func (e *Engine) Get(ctx context.Context, key Key) *Result {
	repo, key, err := e.resolve(key)
	if err != nil {
		return &Result{Key: key, Err: err}
	}
	for {
		if ent := e.cache.acquire(key); ent != nil {
			return e.result(ent)
		}
		epoch := e.epoch(key)
		ch := e.group.DoChan(key.Repo+"\x00"+key.Rev+"\x00"+key.Root+"\x00"+strconv.Itoa(epoch), func() (any, error) {
			if e.cache.has(key) {
				return nil, nil
			}
			return nil, e.load(repo, key, epoch)
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

// resolve finds key's repo and pins its rev to a sha, so a key names one
// tree of files.
func (e *Engine) resolve(key Key) (*gitx.Repo, Key, error) {
	repo, err := e.cfg.Repo(key.Repo)
	if err != nil {
		return nil, key, err
	}
	if key.Rev != Worktree {
		if key.Rev, err = repo.ResolveRef(key.Rev); err != nil {
			return nil, key, err
		}
	}
	return repo, key, nil
}

// Invalidate drops the cached snapshot of rev in repo, for a Worktree whose
// files changed. Holders keep their result; the next Get loads afresh, even if
// a load of the old files is under way.
func (e *Engine) Invalidate(repo, rev string) {
	e.mu.Lock()
	e.epochs[epochKey(repo, rev)]++
	e.mu.Unlock()
	e.finish(e.cache.drop(func(k Key) bool { return k.Repo == repo && k.Rev == rev }))
}

// epochKey names what Invalidate bumps: every root of one rev of a repo.
func epochKey(repo, rev string) string { return repo + "\x00" + rev }

func (e *Engine) epoch(key Key) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epochs[epochKey(key.Repo, key.Rev)]
}

func (e *Engine) result(ent *entry) *Result {
	var once sync.Once
	return &Result{
		Key: ent.key, Snap: ent.snap, Findings: ent.findings, Took: ent.took,
		release: func() { once.Do(func() { e.finish(e.cache.release(ent)) }) },
	}
}

func (e *Engine) load(repo *gitx.Repo, key Key, epoch int) error {
	fl, ctx, err := e.begin(key.Repo)
	if err != nil {
		return err
	}
	defer e.end(fl)
	ent := &entry{key: key, repo: repo}
	if perr := e.pool.run(ctx, func() {
		start := time.Now()
		checkout := repo.Dir
		if key.Rev != Worktree {
			if checkout, err = e.checkout(repo, key); err != nil {
				return
			}
			ent.wt = checkout
		}
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
		ent.took = time.Since(start)
		e.cfg.Log.Debug("analysed", "repo", key.Repo, "rev", key.Rev, "root", key.Root, "took", ent.took.Round(time.Millisecond))
		err = nil
	}); perr != nil {
		return perr
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		e.dropWorktree(ent)
		return err
	}
	// Under mu, so an Invalidate lands either before this check, which
	// discards the snapshot, or after the add, which drops it.
	e.mu.Lock()
	if e.epochs[epochKey(key.Repo, key.Rev)] != epoch {
		e.mu.Unlock()
		e.dropWorktree(ent)
		return nil
	}
	gone := e.cache.add(ent)
	e.mu.Unlock()
	e.finish(gone)
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
	// Fails while other worktrees of the repo remain, which is fine.
	os.Remove(filepath.Dir(ent.wt))
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
