package live_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEngine(t *testing.T, dirs map[string]string) *analysis.Engine {
	t.Helper()
	repos := map[string]*gitx.Repo{}
	for id, dir := range dirs {
		r, err := gitx.Open(dir)
		require.NoError(t, err)
		repos[id] = r
	}
	return analysis.New(analysis.Config{
		Worktrees: t.TempDir(),
		Repo: func(id string) (*gitx.Repo, error) {
			if r := repos[id]; r != nil {
				return r, nil
			}
			return nil, errors.New("unknown repo " + id)
		},
		Load: func(_ context.Context, dir string) (*code.Snapshot, error) { return lang.Load(dir) },
		Log:  quiet(),
	})
}

func worktreeKey(id string) analysis.Key {
	return analysis.Key{Repo: id, Rev: analysis.Worktree, Root: "."}
}

func expectUpdate(t *testing.T, sub *topic.Sub[live.Update]) live.Update {
	t.Helper()
	select {
	case <-sub.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("nothing published")
	}
	us, _ := sub.Drain()
	require.NotEmpty(t, us)
	return us[len(us)-1]
}

func expectQuiet(t *testing.T, sub *topic.Sub[live.Update]) {
	t.Helper()
	select {
	case <-sub.Ready():
		t.Fatal("published without a change")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWatcher_editPublishesOnTheReposTopicAndTheEngineReloads(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	eng := newEngine(t, map[string]string{"calc": dir})
	w := live.NewWatcher(t.Context(), eng, 20*time.Millisecond, quiet())
	sub := w.Add("calc", dir).Subscribe()
	t.Cleanup(sub.Stop)
	before := eng.Get(t.Context(), worktreeKey("calc"))
	require.NoError(t, before.Err)
	t.Cleanup(before.Release)
	// Let the watcher take its first fingerprint before the edit.
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, testrepo.Write(dir, map[string]string{"calc/extra.go": "package calc\n\nfunc Extra() {}\n"}))

	u := expectUpdate(t, sub)
	assert.Equal(t, "calc", u.Repo)
	assert.NoError(t, u.Err)
	r := eng.Get(t.Context(), worktreeKey("calc"))
	t.Cleanup(r.Release)
	require.NoError(t, r.Err)
	assert.NotNil(t, r.Snap.Decl("example.com/calc/calc.Extra"))
}

func TestWatcher_reposAreWatchedIndependently(t *testing.T) {
	t.Parallel()
	a := testrepo.New(t, testrepo.CalcBase, nil)
	b := testrepo.New(t, testrepo.CalcBase, nil)
	eng := newEngine(t, map[string]string{"a": a, "b": b})
	w := live.NewWatcher(t.Context(), eng, 20*time.Millisecond, quiet())
	subA, subB := w.Add("a", a).Subscribe(), w.Add("b", b).Subscribe()
	t.Cleanup(subA.Stop)
	t.Cleanup(subB.Stop)
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, testrepo.Write(a, map[string]string{"calc/extra.go": "package calc\n\nfunc Extra() {}\n"}))
	assert.Equal(t, "a", expectUpdate(t, subA).Repo)
	expectQuiet(t, subB)

	require.NoError(t, testrepo.Write(b, map[string]string{"calc/other.go": "package calc\n\nfunc Other() {}\n"}))
	assert.Equal(t, "b", expectUpdate(t, subB).Repo)
}

func TestWatcher_editsToOtherLanguagesCount(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	eng := newEngine(t, map[string]string{"calc": dir})
	w := live.NewWatcher(t.Context(), eng, 20*time.Millisecond, quiet())
	sub := w.Add("calc", dir).Subscribe()
	t.Cleanup(sub.Stop)
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, testrepo.Write(dir, map[string]string{"tools/run.py": "def run():\n    pass\n"}))

	expectUpdate(t, sub)
}

func TestWatcher_removeStopsWatching(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	eng := newEngine(t, map[string]string{"calc": dir})
	w := live.NewWatcher(t.Context(), eng, 20*time.Millisecond, quiet())
	sub := w.Add("calc", dir).Subscribe()
	t.Cleanup(sub.Stop)
	time.Sleep(100 * time.Millisecond)

	w.Remove("calc")
	require.NoError(t, testrepo.Write(dir, map[string]string{"calc/extra.go": "package calc\n\nfunc Extra() {}\n"}))

	expectQuiet(t, sub)
	assert.Nil(t, w.Topic("calc"))
}
