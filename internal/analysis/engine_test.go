package analysis_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture is a repo with two commits, base then head, plus the engine over it.
type fixture struct {
	eng        *analysis.Engine
	repo       *gitx.Repo
	dir        string
	worktrees  string
	base, head string
	loads      atomic.Int32
}

// newFixture loads with a fake that counts calls and returns an empty snapshot,
// unless load is given.
func newFixture(t *testing.T, cfg analysis.Config) *fixture {
	t.Helper()
	dir := testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead)
	require.NoError(t, testrepo.Git(dir, "add", "-A"))
	require.NoError(t, testrepo.Git(dir, "commit", "-qm", "head"))
	repo, err := gitx.Open(dir)
	require.NoError(t, err)
	f := &fixture{repo: repo, dir: dir, worktrees: t.TempDir()}
	f.head, err = repo.ResolveRef("HEAD")
	require.NoError(t, err)
	f.base, err = repo.ResolveRef("HEAD~1")
	require.NoError(t, err)
	cfg.Worktrees = f.worktrees
	cfg.Repo = func(id string) (*gitx.Repo, error) {
		if id != "calc" {
			return nil, errors.New("unknown repo " + id)
		}
		return repo, nil
	}
	if cfg.Load == nil {
		cfg.Load = func(_ context.Context, dir string) (*code.Snapshot, error) {
			f.loads.Add(1)
			time.Sleep(20 * time.Millisecond)
			return code.NewBuilder(code.Go, "calc", dir).Finish(), nil
		}
	}
	f.eng = analysis.New(cfg)
	return f
}

func (f *fixture) key(rev string) analysis.Key {
	return analysis.Key{Repo: "calc", Rev: rev, Root: "."}
}

func TestGet_concurrentCallsForOneKeyLoadOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})

	var wg sync.WaitGroup
	snaps := make([]*code.Snapshot, 8)
	for i := range snaps {
		wg.Go(func() {
			r := f.eng.Get(context.Background(), f.key(f.base))
			defer r.Release()
			require.NoError(t, r.Err)
			snaps[i] = r.Snap
		})
	}
	wg.Wait()

	assert.EqualValues(t, 1, f.loads.Load())
	for _, s := range snaps {
		assert.Same(t, snaps[0], s)
	}
}

func TestGet_keysAreResolvedToShasAndLoadIndependently(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})

	base := f.eng.Get(context.Background(), f.key(f.base))
	defer base.Release()
	head := f.eng.Get(context.Background(), f.key(f.head))
	defer head.Release()
	byName := f.eng.Get(context.Background(), f.key("HEAD~1"))
	defer byName.Release()

	require.NoError(t, base.Err)
	require.NoError(t, head.Err)
	assert.NotSame(t, base.Snap, head.Snap)
	assert.Same(t, base.Snap, byName.Snap, "HEAD~1 is the base sha")
	assert.EqualValues(t, 2, f.loads.Load())
	assert.Equal(t, filepath.Join(f.worktrees, "calc", f.base), base.Snap.Dir)
	assert.Equal(t, filepath.Join(f.worktrees, "calc", f.head), head.Snap.Dir)
}

func TestGet_evictionKeepsLeasedEntriesAndRemovesTheWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Cap: 1})
	ctx := context.Background()

	a := f.eng.Get(ctx, f.key(f.base))
	b := f.eng.Get(ctx, f.key(f.head))
	require.NoError(t, a.Err)
	require.NoError(t, b.Err)
	assert.DirExists(t, a.Snap.Dir, "a is leased, so adding b over the cap does not evict it")

	a2 := f.eng.Get(ctx, f.key(f.base))
	assert.Same(t, a.Snap, a2.Snap)
	assert.EqualValues(t, 2, f.loads.Load())
	a2.Release()
	assert.DirExists(t, a.Snap.Dir, "a is still held once")
	a.Release()
	assert.NoDirExists(t, filepath.Join(f.worktrees, "calc", f.base), "unheld and over the cap, a goes")
	assert.DirExists(t, filepath.Join(f.worktrees, "calc", f.head))
	b.Release()
	assert.DirExists(t, filepath.Join(f.worktrees, "calc", f.head), "within the cap, b stays cached")
}

func TestGet_evictedKeyLoadsAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Cap: 1})
	ctx := context.Background()

	f.eng.Get(ctx, f.key(f.base)).Release()
	f.eng.Get(ctx, f.key(f.head)).Release()
	r := f.eng.Get(ctx, f.key(f.base))
	defer r.Release()

	require.NoError(t, r.Err)
	assert.EqualValues(t, 3, f.loads.Load())
	assert.DirExists(t, r.Snap.Dir)
}

func TestGet_roots_ofOneShaShareAWorktreeUntilTheLastLeavesTheCache(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Cap: 1})
	ctx := context.Background()
	other := analysis.Key{Repo: "calc", Rev: f.base, Root: "calc"}

	a := f.eng.Get(ctx, f.key(f.base))
	b := f.eng.Get(ctx, other)
	require.NoError(t, a.Err)
	require.NoError(t, b.Err)
	wt := filepath.Join(f.worktrees, "calc", f.base)
	a.Release()
	b.Release()
	assert.DirExists(t, wt, "one root still cached")

	f.eng.Get(ctx, f.key(f.head)).Release()
	f.eng.Get(ctx, analysis.Key{Repo: "calc", Rev: f.head, Root: "calc"}).Release()
	assert.NoDirExists(t, wt)
}

func TestGet_worktreeKeyReloadsAfterInvalidate(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})
	ctx := context.Background()
	k := f.key(analysis.Worktree)

	r := f.eng.Get(ctx, k)
	require.NoError(t, r.Err)
	assert.Equal(t, f.dir, r.Snap.Dir, "the working tree is read in place")
	f.eng.Get(ctx, k).Release()
	assert.EqualValues(t, 1, f.loads.Load())

	f.eng.Invalidate("calc", analysis.Worktree)
	r2 := f.eng.Get(ctx, k)
	assert.EqualValues(t, 2, f.loads.Load())
	assert.NotSame(t, r.Snap, r2.Snap)
	assert.NotNil(t, r.Snap, "the old result stays valid for its holder")
	r.Release()
	r2.Release()
	assert.DirExists(t, f.dir)
}

func TestGet_invalidateLeavesShaEntriesAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})
	ctx := context.Background()

	f.eng.Get(ctx, f.key(f.base)).Release()
	f.eng.Invalidate("calc", analysis.Worktree)
	f.eng.Get(ctx, f.key(f.base)).Release()

	assert.EqualValues(t, 1, f.loads.Load())
}

func TestGet_failedLoadIsNotCached(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	f := newFixture(t, analysis.Config{Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("boom")
		}
		return code.NewBuilder(code.Go, "calc", dir).Finish(), nil
	}})
	ctx := context.Background()

	r := f.eng.Get(ctx, f.key(f.base))
	assert.EqualError(t, r.Err, "boom")
	r.Release()
	r = f.eng.Get(ctx, f.key(f.base))
	defer r.Release()

	assert.NoError(t, r.Err)
	assert.EqualValues(t, 2, calls.Load())
}

func TestGet_unknownRepoAndRevReportErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})

	assert.Error(t, f.eng.Get(context.Background(), analysis.Key{Repo: "nope", Rev: f.base, Root: "."}).Err)
	assert.Error(t, f.eng.Get(context.Background(), f.key("no-such-rev")).Err)
	assert.Zero(t, f.loads.Load())
}

func TestGet_workersBoundConcurrentLoads(t *testing.T) {
	t.Parallel()
	var running, peak atomic.Int32
	f := newFixture(t, analysis.Config{Workers: 1, Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(30 * time.Millisecond)
		return code.NewBuilder(code.Go, "calc", dir).Finish(), nil
	}})

	var wg sync.WaitGroup
	for _, rev := range []string{f.base, f.head, analysis.Worktree} {
		wg.Go(func() { f.eng.Get(context.Background(), f.key(rev)).Release() })
	}
	wg.Wait()

	assert.EqualValues(t, 1, peak.Load())
}

func TestGet_cancelledCallerStopsWaitingButTheLoadFinishes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := f.eng.Get(ctx, f.key(f.base))
	assert.ErrorIs(t, r.Err, context.Canceled)

	later := f.eng.Get(context.Background(), f.key(f.base))
	defer later.Release()
	assert.NoError(t, later.Err)
	assert.EqualValues(t, 1, f.loads.Load(), "the abandoned load is reused")
}

func TestGet_setsChurnFromTheRevsOwnHistory(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
		b := code.NewBuilder(code.Go, "calc", dir)
		p := b.Package(code.Package{Path: "calc/calc", Rel: "calc"})
		b.File(p, code.File{Path: "calc/calc.go", Package: p.Path})
		return b.Finish(), nil
	}})

	base := f.eng.Get(context.Background(), f.key(f.base))
	head := f.eng.Get(context.Background(), f.key(f.head))
	defer base.Release()
	defer head.Release()

	assert.Equal(t, 1, base.Snap.Packages[0].Files[0].Churn, "one commit touched it by base")
	assert.Equal(t, 2, head.Snap.Packages[0].Files[0].Churn, "two by head")
}

func TestCompare_diffsBaseAgainstACommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Load: realLoad})

	c, err := f.eng.Compare(context.Background(), "calc", ".", f.base, f.head)
	require.NoError(t, err)
	defer c.Release()

	assert.Equal(t, f.base, c.Base.Key.Rev)
	assert.Equal(t, f.head, c.Head.Key.Rev)
	var added []string
	for _, d := range c.Diff.Added {
		added = append(added, d.Name)
	}
	assert.Contains(t, added, "Clamp")
	assert.NotEmpty(t, c.Files)
	assert.NotEmpty(t, c.Diff.Introduced, "tangle is a new complex function")
}

func TestCompare_worktreeHeadDiffsTheWorkingTree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Load: realLoad})
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "calc", "extra.go"), []byte("package calc\n\nfunc Extra() int { return 1 }\n"), 0o644))

	c, err := f.eng.Compare(context.Background(), "calc", ".", f.head, analysis.Worktree)
	require.NoError(t, err)
	defer c.Release()

	require.Len(t, c.Diff.Added, 1)
	assert.Equal(t, "Extra", c.Diff.Added[0].Name)
	require.Len(t, c.Files, 1)
	assert.Equal(t, "calc/extra.go", c.Files[0].NewPath, "untracked files count")
}

func TestCompare_failureReleasesTheOtherSide(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{Cap: 1})

	_, err := f.eng.Compare(context.Background(), "calc", ".", f.base, "no-such-rev")
	require.Error(t, err)
	f.eng.Get(context.Background(), f.key(f.head)).Release()

	assert.NoDirExists(t, filepath.Join(f.worktrees, "calc", f.base), "base was released, so head's entry evicted it")
}

func realLoad(_ context.Context, dir string) (*code.Snapshot, error) { return lang.Load(dir) }
