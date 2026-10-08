package live_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSource_refreshAnalysesTheTreeAndReviewsItAgainstMain(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead)
	src := live.New(dir, "", quiet())
	t.Cleanup(src.Close)

	a := src.Refresh()
	require.NoError(t, a.Err)
	assert.Equal(t, "example.com/calc", a.Snap.Module)
	assert.Equal(t, "main", a.Base)
	assert.Len(t, a.BaseSHA, 40)
	require.NotNil(t, a.Review)
	assert.NotEmpty(t, a.Review.Units)
	assert.Same(t, a, src.Current())
}

func TestSource_keepsTheLastGoodAnalysisWhenALoadFails(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	src := live.New(dir, "", quiet())
	t.Cleanup(src.Close)
	good := src.Refresh()
	require.NoError(t, good.Err)

	require.NoError(t, testrepo.Write(dir, map[string]string{"go.mod": "not a go.mod"}))
	bad := src.Refresh()

	assert.Error(t, bad.Err)
	assert.Same(t, good.Snap, bad.Snap)
}

func TestSource_watchRepublishesWhenAFileChanges(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	src := live.New(dir, "", quiet())
	t.Cleanup(src.Close)
	src.Refresh()
	sub := src.Updates.Subscribe()
	t.Cleanup(sub.Stop)

	go src.Watch(t.Context(), 20*time.Millisecond)
	// Let the watcher take its first fingerprint before the edit.
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, testrepo.Write(dir, map[string]string{"calc/extra.go": "package calc\n\nfunc Extra() {}\n"}))

	select {
	case <-sub.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("no analysis published after the edit")
	}
	assert.NotNil(t, src.Current().Snap.Decl("example.com/calc/calc.Extra"))
}

func TestSource_withoutGitStillMapsButHasNoReview(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, testrepo.Write(dir, testrepo.CalcBase))
	src := live.New(dir, "", quiet())

	a := src.Refresh()
	require.NoError(t, a.Err)
	assert.NotNil(t, a.Snap)
	assert.Nil(t, a.Review)
	assert.Contains(t, a.Note, "not a git repository")
}

func TestStatePath_livesInsideTheGitDir(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	got, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(live.StatePath(filepath.Join(dir, "calc")))))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(resolved, ".git"), got)
}
