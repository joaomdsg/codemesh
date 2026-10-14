package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/store"
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
	testrepo.QuietGo(t)
	a, err := app.New(t.Context(), app.Config{Poll: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, a.Close(context.Background())) })
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

func TestPeekPR_aMovedHeadShowsTheOldReviewStaleUntilTheNewOneLands(t *testing.T) {
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
	main := sha("main")
	g("checkout", "-q", "-b", "feat")
	require.NoError(t, testrepo.Write(dir, testrepo.CalcHead))
	g("add", "-A")
	g("commit", "-qm", "feat")
	old := sha("feat")
	g("update-ref", "refs/pull/7/head", old)
	g("remote", "add", "origin", dir)
	gh(t, 7, old, main)
	repo := add(t, a, dir)
	peek := func() *app.PRReview {
		t.Helper()
		pr, err := a.PeekPR(repo.ID, "", 7)
		require.NoError(t, err)
		return pr
	}
	for range 3 {
		a.Wait()
		if pr := peek(); pr != nil {
			pr.Release()
		}
	}

	require.NoError(t, testrepo.Write(dir, map[string]string{"NOTES.md": "more\n"}))
	g("add", "-A")
	g("commit", "-qm", "more")
	moved := sha("feat")
	g("update-ref", "refs/pull/7/head", moved)
	gh(t, 7, moved, main)
	a.AgePRs(repo.ID, 2*time.Minute)
	if pr := peek(); pr != nil {
		pr.Release()
	}
	a.Wait()

	pr := peek()
	require.NotNil(t, pr)
	assert.Equal(t, old, pr.HeadSHA)
	assert.True(t, pr.Head.Stale, "the page marks the old head as being analysed again")
	pr.Release()
	for range 3 {
		a.Wait()
		pr = peek()
		require.NotNil(t, pr, "the old review stays up while the new head loads")
		if pr.HeadSHA == moved {
			break
		}
		assert.True(t, pr.Head.Stale)
		pr.Release()
	}
	defer pr.Release()
	assert.Equal(t, moved, pr.HeadSHA)
	assert.False(t, pr.Head.Stale)
}

func TestPRReview_aStaleListedBaseIsRefreshedBeforeTheMergeBase(t *testing.T) {
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
	oldMain := sha("main")
	g("checkout", "-q", "-b", "feat")
	require.NoError(t, testrepo.Write(dir, testrepo.CalcHead))
	g("add", "-A")
	g("commit", "-qm", "feat")
	g("remote", "add", "origin", dir)
	gh(t, 7, sha("feat"), oldMain)
	repo := add(t, a, dir)
	require.NoError(t, a.PRs(repo.ID).Err)
	a.Wait()

	g("checkout", "-q", "main")
	require.NoError(t, testrepo.Write(dir, map[string]string{"NOTES.md": "main moved on\n"}))
	g("add", "-A")
	g("commit", "-qm", "main moves")
	newMain := sha("main")
	g("checkout", "-q", "feat")
	g("rebase", "-q", "main")
	head := sha("feat")
	g("update-ref", "refs/pull/7/head", head)
	gh(t, 7, head, newMain)
	a.AgePRs(repo.ID, 2*time.Minute)

	pr, err := a.PRReview(repo.ID, "", 7)
	require.NoError(t, err)
	defer pr.Release()

	assert.Equal(t, newMain, pr.BaseSHA, "the diff starts where the PR's own commits start, not at the old base")
}

func TestReading_aFailedPRSummaryIsTriedAgainWhenTheListRefreshes(t *testing.T) {
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
	g("checkout", "-q", "-b", "feat")
	require.NoError(t, testrepo.Write(dir, testrepo.CalcHead))
	g("add", "-A")
	g("commit", "-qm", "feat")
	head := sha("feat")
	g("checkout", "-q", "main")
	g("remote", "add", "origin", dir)
	gh(t, 7, head, sha("main"))
	repo := add(t, a, dir)

	a.Reading(repo.ID, 7)
	a.Wait()
	rd := a.Reading(repo.ID, 7)
	require.True(t, rd.Ready)
	require.Error(t, rd.Err, "the PR head is not on the remote yet, so the fetch fails")

	g("update-ref", "refs/pull/7/head", head)
	a.RefreshPRs(repo.ID)
	a.Wait()
	a.Reading(repo.ID, 7)
	a.Wait()

	rd = a.Reading(repo.ID, 7)
	assert.NoError(t, rd.Err)
	assert.Positive(t, rd.Units)
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

func TestRemoveRepo_dropsItsSnapshotsAndWorktrees(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, testrepo.New(t, testrepo.CalcBase, nil))
	an := a.Analyse(repo.ID, "", "HEAD")
	require.NoError(t, an.Err)
	an.Release()
	cache, err := store.CacheDir()
	require.NoError(t, err)
	wts := filepath.Join(cache, "worktrees", repo.ID)
	left := func() int {
		entries, _ := os.ReadDir(wts)
		return len(entries)
	}
	require.Equal(t, 1, left(), "the cached commit keeps its worktree")

	require.NoError(t, a.RemoveRepo(repo.ID))

	assert.Zero(t, left())
	assert.Error(t, a.Analyse(repo.ID, "", "HEAD").Err, "the repo is gone")
}

func TestRefreshPRs_advancesACloneToItsRemote(t *testing.T) {
	a := newApp(t)
	gh(t, 7, "abc", "def")
	origin := testrepo.New(t, testrepo.CalcBase, nil)
	require.NoError(t, a.AddRepo("file://"+origin))
	t.Cleanup(a.Wait)
	var repo workspace.Repo
	require.Eventually(t, func() bool {
		repos, _ := a.Repos()
		if len(repos) == 1 {
			repo = repos[0]
		}
		return len(repos) == 1
	}, time.Minute, 20*time.Millisecond)
	clamp := func() bool {
		an := a.Analyse(repo.ID, "", "")
		defer an.Release()
		require.NoError(t, an.Err)
		return an.Snap.Decl("example.com/calc/calc.Clamp") != nil
	}
	require.False(t, clamp())
	require.NoError(t, testrepo.Write(origin, testrepo.CalcHead))
	require.NoError(t, testrepo.Git(origin, "add", "-A"))
	require.NoError(t, testrepo.Git(origin, "commit", "-qm", "head"))

	a.RefreshPRs(repo.ID)
	a.Wait()

	assert.True(t, clamp(), "the default view follows origin")
}

func TestClose_refusesLaterAnalyses(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, testrepo.New(t, testrepo.CalcBase, nil))

	require.NoError(t, a.Close(context.Background()))

	assert.ErrorIs(t, a.Analyse(repo.ID, "", "HEAD").Err, analysis.ErrClosed)
}

func TestClose_removesTheWorktreesOfCachedSnapshots(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, testrepo.New(t, testrepo.CalcBase, nil))
	an := a.Analyse(repo.ID, "", "HEAD")
	require.NoError(t, an.Err)
	an.Release()
	cache, err := store.CacheDir()
	require.NoError(t, err)
	wts := filepath.Join(cache, "worktrees", repo.ID)
	require.DirExists(t, wts)

	require.NoError(t, a.Close(context.Background()))

	assert.NoDirExists(t, wts)
}

func TestNew_sweepsTheWorktreesAnEarlierRunLeft(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, testrepo.New(t, testrepo.CalcBase, nil))
	an := a.Analyse(repo.ID, "", "HEAD")
	require.NoError(t, an.Err)
	an.Release()
	cache, err := store.CacheDir()
	require.NoError(t, err)
	wts := filepath.Join(cache, "worktrees", repo.ID)
	require.DirExists(t, wts)

	_, err = app.New(t.Context(), app.Config{Poll: time.Hour})
	require.NoError(t, err)

	entries, _ := os.ReadDir(wts)
	assert.Empty(t, entries)
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

func TestAddRepo_expandsAHomeDirectory(t *testing.T) {
	a := newApp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "src", "app")
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
	repo := testrepo.New(t, testrepo.CalcBase, nil)
	require.NoError(t, os.Rename(repo, dir))
	t.Cleanup(a.Wait)

	require.NoError(t, a.AddRepo("~/src/app"))

	repos, err := a.Repos()
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, dir, repos[0].Path)
}

func TestAddRepo_aPathThatIsNotThereIsAnErrorNotAClone(t *testing.T) {
	a := newApp(t)

	err := a.AddRepo(filepath.Join(t.TempDir(), "nowhere"))

	require.Error(t, err)
	assert.Empty(t, a.Pending(), "a path is not cloned")
}

func TestPRList_hintForAFailureOfGHIsOneQuietLineWithoutItsMessage(t *testing.T) {
	err := errors.New("gh pr list: exit status 1: none of the git remotes configured for this repository point to a known GitHub host")

	hint := app.PRList{Err: err}.Hint()

	assert.Equal(t, "Pull requests are not available for this repo.", hint)
}

func TestOpen_warmsEachRootAndTheBaseOfTheWorkingTreeReview(t *testing.T) {
	a := newApp(t)
	dir := testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead)
	repo := add(t, a, dir)
	repoAt, err := gitx.Open(dir)
	require.NoError(t, err)
	base, err := repoAt.ResolveRef("HEAD")
	require.NoError(t, err)

	a.Open(repo.ID)
	a.Wait()

	for _, rev := range []string{"", base} {
		an := a.Cached(repo.ID, "", rev)
		assert.NotNil(t, an.Snap, "rev %q", rev)
		an.Release()
	}
}

func TestPeekReview_showsNothingUntilTheReviewIsBuiltThenTheReview(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead))

	an := a.PeekReview(repo.ID, "")
	assert.Nil(t, an.Snap)
	an.Release()
	for range 3 {
		a.Wait()
		an = a.PeekReview(repo.ID, "")
		if an.Review != nil {
			break
		}
		an.Release()
	}
	defer an.Release()
	require.NotNil(t, an.Review)
	assert.False(t, an.Stale)
	assert.NotEmpty(t, an.Review.Units)
}

func TestPeekReview_aReviewOfAnOlderSnapshotCarriesThatSnapshotsTime(t *testing.T) {
	a := newApp(t)
	dir := testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead)
	repo := add(t, a, dir)
	var first *app.Analysis
	for range 3 {
		a.Wait()
		if first = a.PeekReview(repo.ID, ""); first.Review != nil {
			break
		}
		first.Release()
	}
	require.NotNil(t, first.Review)
	defer first.Release()

	require.NoError(t, testrepo.Write(dir, map[string]string{"calc/triple.go": "package calc\n\nfunc Triple(x int) int { return 3 * x }\n"}))
	a.Rescan(repo.ID)
	a.Wait()
	head := a.Cached(repo.ID, "", "")
	defer head.Release()
	require.NotSame(t, first.Snap, head.Snap, "the new snapshot landed")

	an := a.PeekReview(repo.ID, "")
	defer an.Release()
	require.Same(t, first.Snap, an.Snap, "the review is still of the old snapshot")
	assert.True(t, an.Stale)
	assert.True(t, first.At.Equal(an.At), "shown %v, analysed %v", an.At, first.At)
}
