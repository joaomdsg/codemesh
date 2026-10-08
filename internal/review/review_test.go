package review_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var baseFiles = map[string]string{
	"go.mod": "module example.com/calc\n\ngo 1.27\n",
	"main.go": `package main

import "example.com/calc/calc"

func main() { println(calc.Add(1, 2), calc.Scale(2, 3), calc.Moved()) }
`,
	"calc/calc.go": `package calc

// Add adds.
func Add(a, b int) int { return a + b }

// Scale multiplies.
func Scale(x, k int) int {
	return helper(x) * k
}

func helper(x int) int { return x + 1 }

// Old is removed in head.
func Old() {}

// Moved moves to util.go unchanged.
func Moved() int { return 7 }
`,
	"calc/calc_test.go": `package calc_test

import (
	"testing"

	"example.com/calc/calc"
)

func TestAdd(t *testing.T) {
	if calc.Add(1, 2) != 3 {
		t.Fatal("sum")
	}
}
`,
}

var headFiles = map[string]string{
	"calc/calc.go": `package calc

// Add returns a plus b.
func Add(a, b int) int {
	return a + b
}

// Scale multiplies, never below zero.
func Scale(x, k int) int {
	if k < 0 {
		return 0
	}
	return helper(x, 1) * k
}

func helper(x, d int) int { return x + d }

// Clamp bounds x to [lo, hi].
func Clamp(x, lo, hi int) int { return min(max(x, lo), hi) }
`,
	"calc/util.go": `package calc

// Moved moves to util.go unchanged.
func Moved() int { return 7 }
`,
	"calc/calc_test.go": baseFiles["calc/calc_test.go"] + `
func TestClamp(t *testing.T) {
	if calc.Clamp(5, 0, 3) != 3 {
		t.Fatal("clamp")
	}
}
`,
	"README.md": "# calc\n",
}

type fixture struct {
	rev  *review.Review
	head *code.Snapshot
}

var calc = sync.OnceValues(func() (fixture, error) {
	dir, err := os.MkdirTemp("", "review-test-")
	if err != nil {
		return fixture{}, err
	}
	write := func(files map[string]string) {
		for name, body := range files {
			p := filepath.Join(dir, name)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(body), 0o644)
		}
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		return cmd.Run()
	}
	write(baseFiles)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-qm", "base"}} {
		if err := git(args...); err != nil {
			return fixture{}, err
		}
	}
	write(headFiles)

	repo, err := gitx.Open(dir)
	if err != nil {
		return fixture{}, err
	}
	diffs, err := repo.Diff("HEAD")
	if err != nil {
		return fixture{}, err
	}
	wt, cleanup, err := repo.Worktree("HEAD")
	if err != nil {
		return fixture{}, err
	}
	defer cleanup()
	base, err := code.Load(wt)
	if err != nil {
		return fixture{}, err
	}
	head, err := code.Load(dir)
	if err != nil {
		return fixture{}, err
	}
	rev := review.Build(review.Input{Base: base, Head: head, Diffs: diffs, BaseFindings: smell.Find(base), HeadFindings: smell.Find(head)})
	return fixture{rev, head}, nil
})

func load(t *testing.T) fixture {
	t.Helper()
	f, err := calc()
	require.NoError(t, err)
	return f
}

func unit(t *testing.T, rev *review.Review, name string) *review.Unit {
	t.Helper()
	for _, u := range rev.Units {
		if u.Name == name {
			return u
		}
	}
	require.Failf(t, "no unit", "%q", name)
	return nil
}

func TestBuild_triagesUnitsIntoLanes(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	lanes := map[string]review.Lane{}
	for _, u := range rev.Units {
		lanes[u.Name] = u.Lane
	}
	assert.Equal(t, map[string]review.Lane{
		"Clamp":     review.Contract,
		"Old":       review.Contract,
		"Scale":     review.Logic,
		"helper":    review.Logic,
		"TestClamp": review.Tests,
		"README.md": review.Other,
		"Add":       review.Noise,
		"Moved":     review.Noise,
	}, lanes)
}

func TestBuild_ordersByLaneThenRisk(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	for i := 1; i < len(rev.Units); i++ {
		a, b := rev.Units[i-1], rev.Units[i]
		assert.True(t, a.Lane < b.Lane || a.Lane == b.Lane && a.Risk >= b.Risk, "%s before %s", a.Name, b.Name)
	}
}

func TestBuild_saysWhyNoiseIsNoise(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	assert.Equal(t, review.Modified, unit(t, rev, "Add").Change)
	assert.Contains(t, unit(t, rev, "Add").Reasons, "comments or layout only")
	assert.Equal(t, review.Moved, unit(t, rev, "Moved").Change)
	assert.Contains(t, unit(t, rev, "Moved").Reasons, "moved from calc/calc.go")
}

func TestBuild_explainsRiskWithReasons(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	scale := unit(t, rev, "Scale")
	assert.Contains(t, scale.Reasons, "1 caller")
	assert.Contains(t, scale.Reasons, "no direct test")
	assert.Contains(t, scale.Reasons, "complexity 2 (+1)")
	assert.Equal(t, []string{"example.com/calc.main"}, scale.Callers)

	helper := unit(t, rev, "helper")
	assert.Contains(t, helper.Reasons, "signature changed")
	assert.Equal(t, review.Removed, unit(t, rev, "Old").Change)
	assert.Equal(t, review.Added, unit(t, rev, "Clamp").Change)
	assert.NotContains(t, unit(t, rev, "Clamp").Reasons, "no direct test", "TestClamp calls it")
}

func TestBuild_diffsWithinTheUnit(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	var got []string
	for _, l := range unit(t, rev, "Scale").Lines {
		got = append(got, string(l.Op)+l.Text)
	}
	assert.Equal(t, []string{
		"-// Scale multiplies.",
		"+// Scale multiplies, never below zero.",
		" func Scale(x, k int) int {",
		"-\treturn helper(x) * k",
		"+\tif k < 0 {",
		"+\t\treturn 0",
		"+\t}",
		"+\treturn helper(x, 1) * k",
		" }",
	}, got)
	assert.Equal(t, 5, unit(t, rev, "Scale").Added)
	assert.Equal(t, 2, unit(t, rev, "Scale").Deleted)
}

func TestBuild_numbersLinesOnBothSides(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	lines := unit(t, rev, "Scale").Lines
	assert.Equal(t, review.Line{Op: ' ', Old: 7, New: 9, Text: "func Scale(x, k int) int {"}, lines[2])
	assert.Equal(t, 0, lines[1].Old)
	assert.Equal(t, 8, lines[1].New)
}

func TestBuild_keysUnitsBySource(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	keys := map[string]bool{}
	for _, u := range rev.Units {
		assert.True(t, strings.HasPrefix(u.Key, u.ID+"@"), u.Key)
		assert.False(t, keys[u.Key], "duplicate key %s", u.Key)
		keys[u.Key] = true
	}
}
