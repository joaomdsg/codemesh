package app_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoModules edits a file of the root module and one of a nested module, so
// the repo has two roots and one change that spans both.
func twoModules(t *testing.T) string {
	t.Helper()
	base := map[string]string{
		"tools/go.mod": "module example.com/tools\n\ngo 1.27\n",
		"tools/t.go":   "package tools\n\nfunc T() int { return 1 }\n",
	}
	for k, v := range testrepo.CalcBase {
		base[k] = v
	}
	return testrepo.New(t, base, map[string]string{
		"tools/t.go":   "package tools\n\nfunc T() int {\n\tx := 1\n\treturn x + 1\n}\n\n\n",
		"calc/calc.go": strings.Replace(testrepo.CalcBase["calc/calc.go"], "return a + b", "return b + a", 1),
	})
}

func TestWorkingReview_aNestedRootReadsOnlyItsOwnFiles(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, twoModules(t))

	wr := a.WorkingReview(repo.ID, "tools")
	defer wr.Release()

	require.NoError(t, wr.Err)
	require.NotNil(t, wr.Review, wr.Note)
	require.Len(t, wr.Review.Units, 1, "calc/calc.go belongs to the root module")
	u := wr.Review.Units[0]
	assert.Equal(t, "T", u.Name)
	assert.Equal(t, "t.go", u.File, "paths are the root's own, as its snapshot names them")
	assert.NotZero(t, u.Added, "the unit's lines come from the file, not from nothing")
}

func TestWorkingReview_theRepoRootLeavesNestedRootsFilesToThem(t *testing.T) {
	a := newApp(t)
	repo := add(t, a, twoModules(t))

	wr := a.WorkingReview(repo.ID, ".")
	defer wr.Release()

	require.NoError(t, wr.Err)
	require.NotNil(t, wr.Review, wr.Note)
	require.Len(t, wr.Review.Units, 1, "tools/t.go belongs to the nested root")
	assert.Equal(t, "Add", wr.Review.Units[0].Name)
}

func TestReading_countsTheWholeChangeOnceAcrossRoots(t *testing.T) {
	a := newApp(t)
	dir := twoModules(t)
	repo := add(t, a, dir)
	out, err := exec.Command("git", "-C", dir, "diff", "--numstat", "HEAD").Output()
	require.NoError(t, err)
	wantAdded, wantDeleted := 0, 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var x, y int
		_, err := fmt.Sscan(l, &x, &y)
		require.NoError(t, err)
		wantAdded, wantDeleted = wantAdded+x, wantDeleted+y
	}

	a.Reading(repo.ID, 0) // starts the analysis
	a.Wait()
	got := a.Reading(repo.ID, 0)

	require.True(t, got.Ready)
	assert.Equal(t, wantAdded, got.Added, "git's own count, not the sum of the units' diffs")
	assert.Equal(t, wantDeleted, got.Deleted)
	require.Len(t, got.Roots, 2)
	assert.Equal(t, "tools", got.Roots[1].Dir)
	assert.Equal(t, 1, got.Roots[1].Units)
	assert.Equal(t, got.Units, got.Roots[0].Units+got.Roots[1].Units, "every unit belongs to one root")
}

// prAddingModule is a repo whose PR edits Add and adds the nested module
// tools/, which the base branch does not have. It returns the repo's id.
func prAddingModule(t *testing.T, a *app.App) string {
	t.Helper()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	g := func(args ...string) { require.NoError(t, testrepo.Git(dir, args...)) }
	repoAt, err := gitx.Open(dir)
	require.NoError(t, err)
	base, err := repoAt.ResolveRef("main")
	require.NoError(t, err)
	g("checkout", "-q", "-b", "feat")
	require.NoError(t, testrepo.Write(dir, map[string]string{
		"calc/calc.go": strings.Replace(testrepo.CalcBase["calc/calc.go"], "return a + b", "return b + a", 1),
		"tools/go.mod": "module example.com/tools\n\ngo 1.27\n",
		"tools/t.go":   "package tools\n\nfunc T() int { return 1 }\n",
	}))
	g("add", "-A")
	g("commit", "-qm", "feat")
	head, err := repoAt.ResolveRef("feat")
	require.NoError(t, err)
	g("update-ref", "refs/pull/7/head", head)
	g("checkout", "-q", "main")
	g("remote", "add", "origin", dir)
	gh(t, 7, head, base)
	return add(t, a, dir).ID
}

func unitNames(r *review.Review) []string {
	var names []string
	for _, u := range r.Units {
		names = append(names, u.Name)
	}
	return names
}

func TestPRReview_aRootTheHeadAddsIsReadAsAllAdded(t *testing.T) {
	a := newApp(t)
	id := prAddingModule(t, a)

	tools, err := a.PRReview(id, "tools", 7)
	require.NoError(t, err)
	defer tools.Release()
	top, err := a.PRReview(id, ".", 7)
	require.NoError(t, err)
	defer top.Release()

	assert.Equal(t, "tools", tools.Root)
	assert.ElementsMatch(t, []string{"T", "go.mod"}, unitNames(tools.Review), "the new module's declarations and file are units")
	assert.Nil(t, tools.Base.Snap.Decl("example.com/tools.T"))
	assert.NotNil(t, tools.Head.Snap.Decl("example.com/tools.T"))
	assert.Equal(t, []string{"Add"}, unitNames(top.Review), "tools/ files are not the repo root's")
}

func TestReading_aPRListsTheRootsItsHeadHolds(t *testing.T) {
	a := newApp(t)
	id := prAddingModule(t, a)

	a.Reading(id, 7) // starts the analysis
	a.Wait()
	got := a.Reading(id, 7)

	require.True(t, got.Ready)
	require.NoError(t, got.Err)
	assert.Equal(t, []app.RootUnits{{Dir: ".", Units: 1}, {Dir: "tools", Units: 2}}, got.Roots)
}

func TestAnalyse_anUnknownRootIsAnErrorNotTheFirstRoot(t *testing.T) {
	a := newApp(t)
	id := prAddingModule(t, a)

	an := a.Analyse(id, "nope", "")
	defer an.Release()
	_, err := a.PRReview(id, "nope", 7)

	assert.ErrorIs(t, an.Err, app.ErrNoRoot)
	assert.ErrorIs(t, err, app.ErrNoRoot)
	assert.Nil(t, an.Snap)
}
