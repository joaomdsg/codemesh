package app_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

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
