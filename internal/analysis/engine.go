// Package analysis loads and caches snapshots of repositories at given
// revisions, and compares two of them. A snapshot of a commit reads its
// source from a worktree that lives as long as the cache entry does.
package analysis

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

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
	// Store keeps snapshots across restarts. Nil keeps them in memory only.
	Store Store
	// Fingerprint names the state of the files a load of dir reads, so a
	// stored snapshot of the working tree is known to be current. Nil means a
	// stored one is shown while it is analysed again.
	Fingerprint func(dir string) string
	// Loaded is told each time a snapshot of key lands, current or not, and
	// each time a load of it fails.
	Loaded func(Key)
	Log    *slog.Logger
}

// Result is a loaded snapshot with its findings. Err is set instead when the
// load failed. Release gives the result back; until then the cache keeps the
// snapshot and its worktree.
type Result struct {
	Key      Key // Rev is the resolved sha, or Worktree
	Snap     *code.Snapshot
	Findings []smell.Finding
	Took     time.Duration // how long the load took, for a cached result the load that made it
	Stale    bool          // Snap is the last one; a load of newer files is due
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
	pool  *pool

	// wtMu serialises worktree changes; wtRefs counts the entries reading
	// each worktree, as several roots of one commit share it.
	wtMu   sync.Mutex
	wtRefs map[string]int

	// mu guards closed and flights: the loads in progress, which Close and
	// Forget cancel and wait for, found by key and epoch so one key loads once.
	// It also guards epochs, which count the invalidations of a repo's rev, so
	// a load that began before one is not cached, and failed, the error of
	// each key's last load, so a broken root is not loaded on every Ensure.
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	flights map[string]*flight
	epochs  map[string]int
	failed  map[Key]error
}

type flight struct {
	repo   string
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error // set before done closes
	// urgent is set when a page asks for a warm load; the pool reads it when
	// the load queues, and promote moves it once queued.
	urgent atomic.Bool
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
		pool:    newPool(cfg.Workers),
		wtRefs:  map[string]int{},
		ctx:     ctx,
		cancel:  cancel,
		flights: map[string]*flight{},
		epochs:  map[string]int{},
		failed:  map[Key]error{},
	}
}

// Close cancels every load in progress, waits for them to end or for ctx to,
// refuses later loads, and drops the cache, which removes the worktrees nobody
// holds. Results still held stay valid until released.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.cancel()
	fs := slices.Collect(maps.Values(e.flights))
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
	for _, f := range e.flights {
		if f.repo == repo {
			f.cancel()
			fs = append(fs, f)
		}
	}
	for k := range e.failed {
		if k.Repo == repo {
			delete(e.failed, k)
		}
	}
	e.mu.Unlock()
	for _, f := range fs {
		<-f.done
	}
	e.finish(e.cache.drop(func(k Key) bool { return k.Repo == repo }))
	if e.cfg.Store != nil {
		if err := e.cfg.Store.Forget(repo); err != nil {
			e.cfg.Log.Warn("forget stored snapshots", "repo", repo, "err", err)
		}
	}
}

// Get returns the current snapshot for key, loading it unless it is cached
// or being loaded. Callers must Release the result. A cancelled ctx stops the
// wait, not the load, which other callers or a later Get can use; Close and
// Forget stop it.
func (e *Engine) Get(ctx context.Context, key Key) *Result {
	repo, key, err := e.resolve(key)
	if err != nil {
		return &Result{Key: key, Err: err}
	}
	for {
		if ent, _ := e.cache.acquire(key, true); ent != nil {
			return e.result(ent, false)
		}
		f := e.start(repo, key, false)
		select {
		case <-ctx.Done():
			return &Result{Key: key, Err: ctx.Err()}
		case <-f.done:
			if f.err != nil {
				return &Result{Key: key, Err: f.err}
			}
		}
	}
}

// Peek returns what is cached for key without loading or waiting: the
// snapshot, Stale while newer files are due; else the error of its last load;
// else neither. A stored snapshot of the working tree counts as cached, as
// reading it needs no checkout. Callers must Release the result.
func (e *Engine) Peek(key Key) *Result {
	repo, key, err := e.resolve(key)
	if err != nil {
		return &Result{Key: key, Err: err}
	}
	if key.Rev == Worktree && !e.cache.holds(key) {
		e.mu.Lock()
		epoch := e.epochs[epochKey(key.Repo, key.Rev)]
		e.mu.Unlock()
		if ent := e.restore(repo, key); ent != nil {
			ent.stale = !e.current(repo, ent)
			e.add(ent, epoch, true)
		}
	}
	ent, stale := e.cache.acquire(key, false)
	e.mu.Lock()
	failed := e.failed[key]
	e.mu.Unlock()
	if ent != nil {
		// A stale snapshot whose reload failed waits for the next change.
		return e.result(ent, stale && failed == nil)
	}
	return &Result{Key: key, Err: failed}
}

// Ensure starts loading key unless it is current, being loaded or failed
// last time, ahead of warm-up loads. It does not wait; Config.Loaded hears
// when the snapshot lands.
func (e *Engine) Ensure(key Key) { e.ensure(key, false) }

// Warm starts loading keys behind the loads Ensure and Get ask for. Loads
// started here carry code.Bulk in their context.
func (e *Engine) Warm(keys ...Key) {
	for _, k := range keys {
		e.ensure(k, true)
	}
}

func (e *Engine) ensure(key Key, warm bool) {
	repo, key, err := e.resolve(key)
	if err != nil || e.cache.current(key) {
		return
	}
	e.mu.Lock()
	_, failed := e.failed[key]
	e.mu.Unlock()
	if failed {
		return
	}
	e.start(repo, key, warm)
}

// start loads key unless a load of it at the current epoch is under way, and
// returns that load. The load is registered before start returns, so Busy
// sees it. A load started or joined by a page goes ahead of the warm-up loads.
func (e *Engine) start(repo *gitx.Repo, key Key, warm bool) *flight {
	e.mu.Lock()
	defer e.mu.Unlock()
	epoch := e.epochs[epochKey(key.Repo, key.Rev)]
	id := key.Repo + "\x00" + key.Rev + "\x00" + key.Root + "\x00" + strconv.Itoa(epoch)
	if f := e.flights[id]; f != nil {
		if !warm {
			f.urgent.Store(true)
			e.pool.promote(key)
		}
		return f
	}
	f := &flight{repo: key.Repo, done: make(chan struct{})}
	f.urgent.Store(!warm)
	if e.closed {
		f.err = ErrClosed
		close(f.done)
		return f
	}
	f.ctx, f.cancel = context.WithCancel(e.ctx)
	e.flights[id] = f
	go func() {
		if !e.cache.current(key) {
			f.err = e.load(f.ctx, repo, key, epoch, f, warm)
		}
		f.cancel()
		e.mu.Lock()
		delete(e.flights, id)
		e.mu.Unlock()
		close(f.done)
	}()
	return f
}

// resolve finds key's repo and pins a commit rev to its sha, so a cached
// key keeps naming the same files.
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
	e.bump(repo, rev)
	e.finish(e.cache.drop(func(k Key) bool { return k.Repo == repo && k.Rev == rev }))
}

// Refresh is Invalidate that keeps showing the old snapshots: Peek returns
// each, Stale, until a load of the changed files replaces it. It loads only
// the roots that were cached.
func (e *Engine) Refresh(repo, rev string) {
	e.bump(repo, rev)
	for _, k := range e.cache.stale(func(k Key) bool { return k.Repo == repo && k.Rev == rev }) {
		e.Ensure(k)
	}
}

// bump starts a new epoch of rev in repo, forgetting its failed loads.
func (e *Engine) bump(repo, rev string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.epochs[epochKey(repo, rev)]++
	for k := range e.failed {
		if k.Repo == repo && k.Rev == rev {
			delete(e.failed, k)
		}
	}
}

// epochKey names what Invalidate bumps: every root of one rev of a repo.
func epochKey(repo, rev string) string { return repo + "\x00" + rev }

func (e *Engine) result(ent *entry, stale bool) *Result {
	var once sync.Once
	return &Result{
		Key: ent.key, Snap: ent.snap, Findings: ent.findings, Took: ent.took, Stale: stale,
		release: func() { once.Do(func() { e.finish(e.cache.release(ent)) }) },
	}
}

// load brings key into the cache: from the store when it holds the key,
// then from the files unless the stored snapshot is current.
func (e *Engine) load(ctx context.Context, repo *gitx.Repo, key Key, epoch int, f *flight, warm bool) error {
	if warm {
		ctx = code.Bulk(ctx)
	}
	err := e.fill(ctx, repo, key, epoch, f)
	if err != nil && ctx.Err() == nil {
		e.mu.Lock()
		e.failed[key] = err
		e.mu.Unlock()
		e.loaded(key)
	}
	return err
}

func (e *Engine) fill(ctx context.Context, repo *gitx.Repo, key Key, epoch int, f *flight) error {
	// Taken before the files are read, so an edit during the load leaves the
	// snapshot not current.
	basis := e.basis(repo, key)
	if !e.cache.holds(key) {
		if ent := e.restore(repo, key); ent != nil {
			ent.stale = basis == "" || ent.basis != basis
			if e.add(ent, epoch, true) {
				e.loaded(key)
			}
			if e.cache.current(key) {
				return nil
			}
		}
	}
	var ent *entry
	var err error
	if perr := e.pool.run(ctx, key, f.urgent.Load, func() { ent, err = e.produce(ctx, repo, key) }); perr != nil {
		return perr
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		e.dropWorktree(ent)
		return err
	}
	ent.basis = basis
	if !e.add(ent, epoch, false) {
		return nil
	}
	e.save(ent)
	e.loaded(key)
	return nil
}

// add caches ent unless an Invalidate or Refresh came after its load began,
// or, with absent set, the key is cached already. It reports whether it did.
// Under mu, so an Invalidate lands either before the check, which discards
// ent, or after the add, which drops or marks it.
func (e *Engine) add(ent *entry, epoch int, absent bool) bool {
	e.mu.Lock()
	if e.epochs[epochKey(ent.key.Repo, ent.key.Rev)] != epoch {
		e.mu.Unlock()
		e.dropWorktree(ent)
		return false
	}
	added, gone := e.cache.add(ent, absent)
	if added {
		delete(e.failed, ent.key)
	}
	e.mu.Unlock()
	e.finish(gone)
	if !added {
		e.dropWorktree(ent)
	}
	return added
}

func (e *Engine) loaded(key Key) {
	if e.cfg.Loaded != nil {
		e.cfg.Loaded(key)
	}
}

// current reports whether a restored entry read the files key names now.
func (e *Engine) current(repo *gitx.Repo, ent *entry) bool {
	basis := e.basis(repo, ent.key)
	return basis != "" && ent.basis == basis
}

// basis names the files a load of key reads: a commit's sha, or the working
// tree's fingerprint and HEAD, which churn counts from. "" means unknown.
func (e *Engine) basis(repo *gitx.Repo, key Key) string {
	if key.Rev != Worktree {
		return key.Rev
	}
	if e.cfg.Fingerprint == nil {
		return ""
	}
	head, _ := repo.Head()
	return e.cfg.Fingerprint(filepath.Join(repo.Dir, key.Root)) + " " + head
}

// restore reads key's stored snapshot, checking out the commit it reads its
// source from. It returns nil when there is none to use.
func (e *Engine) restore(repo *gitx.Repo, key Key) *entry {
	if e.cfg.Store == nil {
		return nil
	}
	s, err := e.cfg.Store.Load(key)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			e.cfg.Log.Warn("read stored snapshot", "repo", key.Repo, "rev", key.Rev, "root", key.Root, "err", err)
		}
		return nil
	}
	ent := &entry{key: key, repo: repo, snap: s.Snap, findings: s.Findings, took: s.Took, basis: s.Basis}
	checkout := repo.Dir
	if key.Rev != Worktree {
		if checkout, err = e.checkout(repo, key); err != nil {
			return nil
		}
		ent.wt = checkout
	}
	ent.snap.Dir = filepath.Join(checkout, key.Root)
	return ent
}

func (e *Engine) save(ent *entry) {
	if e.cfg.Store == nil {
		return
	}
	err := e.cfg.Store.Save(ent.key, &Saved{Basis: ent.basis, Snap: ent.snap, Findings: ent.findings, Took: ent.took})
	if err != nil {
		e.cfg.Log.Warn("store snapshot", "repo", ent.key.Repo, "rev", ent.key.Rev, "root", ent.key.Root, "err", err)
	}
}

// produce analyses the files key names: its snapshot, churn and smells.
func (e *Engine) produce(ctx context.Context, repo *gitx.Repo, key Key) (*entry, error) {
	start := time.Now()
	ent := &entry{key: key, repo: repo}
	checkout := repo.Dir
	if key.Rev != Worktree {
		var err error
		if checkout, err = e.checkout(repo, key); err != nil {
			return nil, err
		}
		ent.wt = checkout
	}
	snap, err := e.cfg.Load(ctx, filepath.Join(checkout, key.Root))
	if err != nil {
		e.dropWorktree(ent)
		return nil, err
	}
	ent.snap = snap
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
	return ent, nil
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
