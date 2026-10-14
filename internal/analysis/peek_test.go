package analysis_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// landed collects Config.Loaded calls.
type landed chan analysis.Key

func (l landed) hook(k analysis.Key) { l <- k }

func (l landed) wait(t *testing.T, want analysis.Key) {
	t.Helper()
	for {
		select {
		case k := <-l:
			if k == want {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v never landed", want)
		}
	}
}

func TestPeek_isEmptyUntilAnEnsuredLoadLands(t *testing.T) {
	t.Parallel()
	l := make(landed, 8)
	f := newFixture(t, analysis.Config{Loaded: l.hook})
	k := f.key(f.base)

	r := f.eng.Peek(k)
	assert.Nil(t, r.Snap)
	assert.NoError(t, r.Err)
	r.Release()

	f.eng.Ensure(k)
	f.eng.Ensure(k)
	l.wait(t, k)
	r = f.eng.Peek(k)
	defer r.Release()
	require.NotNil(t, r.Snap)
	assert.False(t, r.Stale)
	assert.EqualValues(t, 1, f.loads.Load())
}

func TestRefresh_showsTheOldSnapshotUntilTheChangedFilesLand(t *testing.T) {
	t.Parallel()
	l := make(landed, 8)
	proceed := make(chan struct{})
	var calls atomic.Int32
	f := newFixture(t, analysis.Config{Loaded: l.hook, Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
		if calls.Add(1) == 2 {
			<-proceed
		}
		return code.NewBuilder(code.Go, fmt.Sprintf("v%d", calls.Load()), dir).Finish(), nil
	}})
	k := f.key(analysis.Worktree)
	f.eng.Get(context.Background(), k).Release()
	l.wait(t, k)

	f.eng.Refresh("calc", analysis.Worktree)
	r := f.eng.Peek(k)
	assert.Equal(t, "v1", r.Snap.Module)
	assert.True(t, r.Stale)
	r.Release()
	close(proceed)
	l.wait(t, k)

	r = f.eng.Peek(k)
	defer r.Release()
	assert.Equal(t, "v2", r.Snap.Module)
	assert.False(t, r.Stale)
}

func TestRefresh_loadsOnlyTheRootsThatWereCached(t *testing.T) {
	t.Parallel()
	f := newFixture(t, analysis.Config{})
	f.eng.Refresh("calc", analysis.Worktree)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, f.loads.Load())
}

func TestEnsure_doesNotRetryAFailedLoadUntilTheFilesChange(t *testing.T) {
	t.Parallel()
	l := make(landed, 8)
	var calls atomic.Int32
	f := newFixture(t, analysis.Config{Loaded: l.hook, Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("boom")
		}
		return code.NewBuilder(code.Go, "calc", dir).Finish(), nil
	}})
	k := f.key(analysis.Worktree)

	f.eng.Ensure(k)
	l.wait(t, k)
	r := f.eng.Peek(k)
	assert.EqualError(t, r.Err, "boom")
	f.eng.Ensure(k)
	assert.EqualValues(t, 1, calls.Load())

	f.eng.Invalidate("calc", analysis.Worktree)
	f.eng.Ensure(k)
	l.wait(t, k)
	r = f.eng.Peek(k)
	defer r.Release()
	assert.NoError(t, r.Err)
	assert.EqualValues(t, 2, calls.Load())
}

func TestStore_aRestartShowsTheStoredSnapshotWithoutLoadingWhileTheFilesMatch(t *testing.T) {
	t.Parallel()
	disk := analysis.NewDisk(t.TempDir())
	fp := func(string) string { return "same" }
	first := newFixture(t, analysis.Config{Store: disk, Fingerprint: fp})
	k := first.key(analysis.Worktree)
	first.eng.Get(context.Background(), k).Release()

	l := make(landed, 8)
	second := first.restart(t, analysis.Config{Store: disk, Fingerprint: fp, Loaded: l.hook})
	second.eng.Ensure(k)
	l.wait(t, k)
	r := second.eng.Peek(k)
	defer r.Release()

	require.NotNil(t, r.Snap)
	assert.False(t, r.Stale)
	assert.Equal(t, first.dir, r.Snap.Dir)
	assert.Zero(t, second.loads.Load())
}

func TestStore_aRestartShowsTheStoredSnapshotStaleWhileChangedFilesLoad(t *testing.T) {
	t.Parallel()
	disk := analysis.NewDisk(t.TempDir())
	first := newFixture(t, analysis.Config{Store: disk, Fingerprint: func(string) string { return "before" }})
	k := first.key(analysis.Worktree)
	first.eng.Get(context.Background(), k).Release()

	l := make(landed, 8)
	proceed := make(chan struct{})
	second := first.restart(t, analysis.Config{Store: disk, Fingerprint: func(string) string { return "after" }, Loaded: l.hook,
		Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
			<-proceed
			return code.NewBuilder(code.Go, "fresh", dir).Finish(), nil
		}})
	second.eng.Ensure(k)
	l.wait(t, k)
	r := second.eng.Peek(k)
	assert.Equal(t, "calc", r.Snap.Module)
	assert.True(t, r.Stale)
	r.Release()

	close(proceed)
	l.wait(t, k)
	r = second.eng.Peek(k)
	defer r.Release()
	assert.Equal(t, "fresh", r.Snap.Module)
	assert.False(t, r.Stale)
}

func TestStore_aRestoredCommitReadsItsSourceFromANewWorktree(t *testing.T) {
	t.Parallel()
	disk := analysis.NewDisk(t.TempDir())
	first := newFixture(t, analysis.Config{Store: disk})
	k := first.key(first.base)
	first.eng.Get(context.Background(), k).Release()
	require.NoError(t, first.eng.Close(context.Background()))

	l := make(landed, 8)
	second := first.restart(t, analysis.Config{Store: disk, Loaded: l.hook})
	second.eng.Ensure(k)
	l.wait(t, k)
	r := second.eng.Peek(k)
	defer r.Release()

	require.NotNil(t, r.Snap)
	assert.False(t, r.Stale, "a commit's files never change")
	assert.Equal(t, filepath.Join(second.worktrees, "calc", first.base), r.Snap.Dir)
	assert.DirExists(t, r.Snap.Dir)
	assert.Zero(t, second.loads.Load())
}

func TestEnsure_goesAheadOfWarmLoadsWhichLoadInBulk(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	var mu sync.Mutex
	var order []string
	f := newFixture(t, analysis.Config{Workers: 1, Load: func(ctx context.Context, dir string) (*code.Snapshot, error) {
		mu.Lock()
		order = append(order, fmt.Sprintf("%s bulk=%v", filepath.Base(dir), code.InBulk(ctx)))
		first := len(order) == 1
		mu.Unlock()
		if first {
			<-hold
		}
		return code.NewBuilder(code.Go, "calc", dir).Finish(), nil
	}})
	f.eng.Ensure(f.key(analysis.Worktree))
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 1 }, time.Second, time.Millisecond)

	f.eng.Warm(f.key(f.base))
	f.eng.Warm(f.key(f.head))
	f.eng.Ensure(analysis.Key{Repo: "calc", Rev: f.head, Root: "calc"})
	time.Sleep(20 * time.Millisecond)
	f.eng.Ensure(f.key(f.head))
	close(hold)
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 4 }, 5*time.Second, time.Millisecond)

	assert.Equal(t, []string{
		filepath.Base(f.dir) + " bulk=false",
		f.head + " bulk=true",
		"calc bulk=false",
		f.base + " bulk=true",
	}, order, "what pages asked for in arrival order, then the warm-up")
}

func TestPeek_readsAStoredWorkingTreeWithoutLoading(t *testing.T) {
	t.Parallel()
	disk := analysis.NewDisk(t.TempDir())
	fp := func(string) string { return "same" }
	first := newFixture(t, analysis.Config{Store: disk, Fingerprint: fp})
	k := first.key(analysis.Worktree)
	first.eng.Get(context.Background(), k).Release()

	second := first.restart(t, analysis.Config{Store: disk, Fingerprint: fp})
	r := second.eng.Peek(k)
	defer r.Release()

	require.NotNil(t, r.Snap)
	assert.False(t, r.Stale)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, second.loads.Load())
}

func TestRefresh_aFailedReloadShowsTheOldSnapshotAsItIs(t *testing.T) {
	t.Parallel()
	l := make(landed, 8)
	var calls atomic.Int32
	f := newFixture(t, analysis.Config{Loaded: l.hook, Load: func(_ context.Context, dir string) (*code.Snapshot, error) {
		if calls.Add(1) == 2 {
			return nil, errors.New("boom")
		}
		return code.NewBuilder(code.Go, "calc", dir).Finish(), nil
	}})
	k := f.key(analysis.Worktree)
	f.eng.Get(context.Background(), k).Release()
	l.wait(t, k)

	f.eng.Refresh("calc", analysis.Worktree)
	l.wait(t, k)
	r := f.eng.Peek(k)
	defer r.Release()
	require.NotNil(t, r.Snap)
	assert.EqualValues(t, 2, calls.Load())
	assert.False(t, r.Stale, "no newer snapshot is coming until the files change again")
}

func TestCompare_answersAFailedLoadWithoutLoadingAgain(t *testing.T) {
	t.Parallel()
	l := make(landed, 8)
	var calls atomic.Int32
	f := newFixture(t, analysis.Config{Loaded: l.hook, Load: func(context.Context, string) (*code.Snapshot, error) {
		calls.Add(1)
		return nil, errors.New("boom")
	}})
	f.eng.Ensure(f.key(f.base))
	l.wait(t, f.key(f.base))

	_, err := f.eng.Compare(context.Background(), "calc", ".", f.base, f.base)
	assert.EqualError(t, err, "boom")
	assert.EqualValues(t, 1, calls.Load())
}
