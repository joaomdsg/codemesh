package app_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/joaomdsg/codemesh/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newApp is an app in its own XDG directories. t.Setenv rules out t.Parallel.
func newApp(t *testing.T) *app.App {
	t.Helper()
	// XDG_CACHE_HOME moves Go's build cache too, which would start every test cold.
	goCache, err := exec.Command("go", "env", "GOCACHE").Output()
	require.NoError(t, err)
	t.Setenv("GOCACHE", strings.TrimSpace(string(goCache)))
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	// The engine finishes a load after its caller's context ends, so the app
	// outlives the test body: cleanups wait for its jobs, then cancel.
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	t.Cleanup(cancel)
	a, err := app.New(ctx, app.Config{Poll: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	t.Cleanup(a.Wait)
	return a
}

func add(t *testing.T, a *app.App, dir string) workspace.Repo {
	t.Helper()
	t.Cleanup(a.Wait) // after dir's own cleanup was registered, so it runs before it
	require.NoError(t, a.AddRepo(dir))
	repos, err := a.Repos()
	require.NoError(t, err)
	return repos[len(repos)-1]
}

// gh puts a gh on PATH that lists one open PR.
func gh(t *testing.T, number int, head, base string) {
	t.Helper()
	bin := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
cat <<'JSON'
[{"number":%d,"title":"Clamp it","author":{"login":"ana"},"headRefName":"feat","baseRefName":"main",
"headRefOid":%q,"baseRefOid":%q,"url":"https://github.com/o/r/pull/%d","isDraft":false,
"updatedAt":"2026-05-01T10:30:00Z","changedFiles":3,"additions":40,"deletions":5,"reviewDecision":""}]
JSON
`, number, head, base, number)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPRReview_comparesThePRHeadWithItsMergeBase(t *testing.T) {
	a := newApp(t)
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	g := func(args ...string) { require.NoError(t, testrepo.Git(dir, args...)) }
	repoAt, err := gitx.Open(dir)
	require.NoError(t, err)
	sha := func(rev string) string {
		s, err := repoAt.ResolveRef(rev)
		require.NoError(t, err)
		return s
	}
	fork := sha("main")
	g("checkout", "-q", "-b", "feat")
	require.NoError(t, testrepo.Write(dir, testrepo.CalcHead))
	g("add", "-A")
	g("commit", "-qm", "feat")
	head := sha("feat")
	g("update-ref", "refs/pull/7/head", head)
	g("checkout", "-q", "main")
	require.NoError(t, testrepo.Write(dir, map[string]string{"NOTES.md": "main moved on\n"}))
	g("add", "-A")
	g("commit", "-qm", "main moves")
	g("remote", "add", "origin", dir)
	gh(t, 7, head, sha("main"))
	repo := add(t, a, dir)

	pr, err := a.PRReview(repo.ID, "", 7)
	require.NoError(t, err)
	defer pr.Release()

	assert.Equal(t, fork, pr.BaseSHA, "the merge-base, not the base branch's tip")
	assert.Equal(t, head, pr.HeadSHA)
	assert.Equal(t, 7, pr.PR.Number)
	names := map[string]bool{}
	for _, u := range pr.Review.Units {
		names[u.Name] = true
	}
	assert.True(t, names["Clamp"], "the PR's new function is a unit")
	assert.NotNil(t, pr.Head.Snap.Decl("example.com/calc/calc.Clamp"))
	assert.Nil(t, pr.Base.Snap.Decl("example.com/calc/calc.Clamp"))
}

func TestReviewed_startsFromTheLegacyFileInTheGitDir(t *testing.T) {
	a := newApp(t)
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	legacy := filepath.Join(dir, ".git", "codemesh")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "reviewed.json"), []byte(`["calc.Add@abc"]`), 0o644))
	repo := add(t, a, dir)

	st, err := a.Reviewed(repo.ID)
	require.NoError(t, err)
	assert.True(t, st.Reviewed("calc.Add@abc"))

	require.NoError(t, st.Set("calc.Scale@def", true))
	again, err := a.Reviewed(repo.ID)
	require.NoError(t, err)
	assert.True(t, again.Reviewed("calc.Scale@def"))
	assert.FileExists(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "codemesh", "repos", repo.ID, "reviewed.json"))
}

func TestBasket_survivesARestartAndRendersEachRepoByName(t *testing.T) {
	a := newApp(t)
	one := add(t, a, testrepo.New(t, testrepo.CalcBase, nil))
	two := add(t, a, testrepo.New(t, testrepo.CalcBase, nil))
	first, err := a.BasketAdd(handoff.Item{Kind: handoff.Finding, Repo: one.ID, Title: "Split Clamp", Where: "calc/calc.go:3"})
	require.NoError(t, err)
	_, err = a.BasketAdd(handoff.Item{Kind: handoff.Finding, Repo: two.ID, Title: "Rename Add"})
	require.NoError(t, err)
	require.NoError(t, a.BasketMove(first.ID, 1))

	again, err := app.New(t.Context(), app.Config{Poll: time.Hour})
	require.NoError(t, err)

	items := again.Basket().Items
	require.Len(t, items, 2)
	assert.Equal(t, "Rename Add", items[0].Title, "the move persisted")
	text := again.BasketText()
	assert.Contains(t, text, "## "+one.Name)
	assert.Contains(t, text, "Split Clamp")

	require.NoError(t, again.BasketRemove(first.ID))
	assert.Len(t, again.Basket().Items, 1)
}
