package review_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	rev  *review.Review
	head *code.Snapshot
}

var calc = sync.OnceValues(func() (fixture, error) { return build(testrepo.CalcBase, testrepo.CalcHead) })

// build runs the whole pipeline the way live does: commit base, write head,
// diff, load both sides, find smells.
func build(baseFiles, headFiles map[string]string) (fixture, error) {
	dir, err := testrepo.Make(baseFiles, headFiles)
	defer os.RemoveAll(dir)
	if err != nil {
		return fixture{}, err
	}
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
}

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
		"Clamp":                review.Contract,
		"Old":                  review.Contract,
		"Scale":                review.Logic,
		"helper":               review.Logic,
		"tangle":               review.Logic,
		"TestClamp":            review.Tests,
		"README.md":            review.Other,
		"calc/testdata/in.txt": review.Tests,
		"Add":                  review.Noise,
		"Moved":                review.Noise,
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
	assert.Contains(t, unit(t, rev, "Clamp").Reasons, "1 test caller")
	assert.NotContains(t, unit(t, rev, "Clamp").Reasons, "1 caller", "a test caller is no blast radius")
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

func TestBuild_putsNewSmellsOnTheirUnits(t *testing.T) {
	t.Parallel()
	rev := load(t).rev

	var rules []smell.Rule
	for _, f := range unit(t, rev, "tangle").Smells {
		rules = append(rules, f.Rule)
	}
	assert.Equal(t, []smell.Rule{smell.ComplexFunc}, rules)
	assert.True(t, slices.ContainsFunc(rev.Fixed, func(f smell.Finding) bool {
		return f.Rule == smell.ManyParams && f.Subject == "Old"
	}), "removing Old removes its smell")
}

const twisty = `func Twisty(x int) int {
	if x == 1 { return 1 }
	if x == 2 { return 2 }
	if x == 3 { return 3 }
	if x == 4 { return 4 }
	if x == 5 { return 5 }
	if x == 6 { return 6 }
	if x == 7 { return 7 }
	if x == 8 { return 8 }
	if x == 9 { return 9 }
	if x == 10 { return 10 }
	return 0
}
`

var moves = sync.OnceValues(func() (fixture, error) {
	return build(map[string]string{
		"go.mod":          "module example.com/m\n\ngo 1.27\n",
		"a/a.go":          "package a\n\nfunc Shared() int { return 1 }\n\n" + twisty,
		"internal/x/x.go": "package x\n\nfunc Helper() int { return 1 }\n",
		"sub/go.mod":      "module example.com/sub\n\ngo 1.27\n",
		"sub/s.go":        "package sub\n\nfunc S() {}\n",
		"main.go":         "package main\n\nimport (\n\t\"example.com/m/a\"\n\t\"example.com/m/internal/x\"\n)\n\nfunc main() { println(a.Shared(), a.Twisty(1), x.Helper()) }\n",
	}, map[string]string{
		"a/a.go":          "package a\n",
		"b/b.go":          "package b\n\nfunc Shared() int { return 2 }\n\n" + twisty,
		"internal/x/x.go": "package x\n\nfunc Helper() int { return 1 }\n\nfunc New() int { return 2 }\n",
		"sub/s.go":        "package sub\n\nfunc S() { println() }\n",
		"main.go":         "package main\n\nimport (\n\t\"example.com/m/b\"\n\t\"example.com/m/internal/x\"\n)\n\nfunc main() { println(b.Shared(), b.Twisty(1), x.Helper(), x.New()) }\n",
	})
})

func TestBuild_readsAnEditedMoveAsOneModifiedUnit(t *testing.T) {
	t.Parallel()
	f, err := moves()
	require.NoError(t, err)

	var shared []*review.Unit
	for _, u := range f.rev.Units {
		if u.Name == "Shared" {
			shared = append(shared, u)
		}
	}
	require.Len(t, shared, 1, "not a removal plus an addition")
	assert.Equal(t, review.Modified, shared[0].Change)
	assert.Contains(t, shared[0].Reasons, "moved from a/a.go")
	assert.Equal(t, 1, shared[0].Added)
	assert.Equal(t, 1, shared[0].Deleted)
}

func TestBuild_keepsAMovedDeclarationsSmellOffTheNewList(t *testing.T) {
	t.Parallel()
	f, err := moves()
	require.NoError(t, err)

	assert.Equal(t, review.Moved, unit(t, f.rev, "Twisty").Change)
	for _, s := range f.rev.Introduced {
		assert.NotEqual(t, "Twisty", s.Subject, "its complexity moved with it")
	}
}

func TestBuild_treatsInternalPackagesAsNoContract(t *testing.T) {
	t.Parallel()
	f, err := moves()
	require.NoError(t, err)

	assert.Equal(t, review.Logic, unit(t, f.rev, "New").Lane)
}

func TestBuild_givesNestedModuleGoFilesTheirCodeLane(t *testing.T) {
	t.Parallel()
	f, err := moves()
	require.NoError(t, err)

	u := unit(t, f.rev, "sub/s.go")
	assert.Equal(t, review.Logic, u.Lane)
	assert.Contains(t, u.Reasons, "in another module")
}

var edges = sync.OnceValues(func() (fixture, error) {
	return build(map[string]string{
		"go.mod":               "module example.com/e\n\ngo 1.27\n",
		"page/page.go":         "package page\n\nconst Script = `\na\n`\n\nfunc Use() string { return Script }\n",
		"internal/k/k.go":      "package k\n\nfunc K() int { return 1 }\n",
		"internal/k/k_test.go": "package k\n\nimport \"testing\"\n\nfunc TestK(t *testing.T) { K() }\n",
		"sub/go.mod":           "module example.com/sub\n\ngo 1.27\n",
		"sub/doc.go":           "// Package sub does s.\npackage sub\n",
	}, map[string]string{
		"page/page.go":         "package page\n\nconst Script = `\na\nb\n`\n\nfunc Use() string { return Script }\n",
		"internal/k/k_test.go": "package k\n\nimport \"testing\"\n\nfunc TestK(t *testing.T) { _ = K() }\n",
		"sub/go.mod":           "module example.com/sub\n\ngo 1.27.0\n",
		"sub/doc.go":           "// Package sub does t.\npackage sub\n",
	})
})

func TestBuild_putsStringDataInOther(t *testing.T) {
	t.Parallel()
	f, err := edges()
	require.NoError(t, err)

	u := unit(t, f.rev, "Script")
	assert.Equal(t, review.Other, u.Lane)
	assert.Contains(t, u.Reasons, "long string literal")
}

func TestBuild_putsANestedGoModInOther(t *testing.T) {
	t.Parallel()
	f, err := edges()
	require.NoError(t, err)

	u := unit(t, f.rev, "sub/go.mod")
	assert.Equal(t, review.Other, u.Lane)
	assert.Contains(t, u.Reasons, "in another module")
}

func TestBuild_readsACommentOnlyNestedModuleFileAsNoise(t *testing.T) {
	t.Parallel()
	f, err := edges()
	require.NoError(t, err)

	u := unit(t, f.rev, "sub/doc.go")
	assert.Equal(t, review.Noise, u.Lane)
	assert.Contains(t, u.Reasons, "comments or layout only")
}

func TestBuild_givesTestFunctionsNoExportedChip(t *testing.T) {
	t.Parallel()
	f, err := edges()
	require.NoError(t, err)

	assert.NotContains(t, unit(t, f.rev, "TestK").Reasons, "exported")
}

func TestBuild_showsALinkAsItsTargetLikeGit(t *testing.T) {
	t.Parallel()
	dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("SECRET\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "link")))
	rev := review.Build(review.Input{Base: &code.Snapshot{Dir: t.TempDir()}, Head: &code.Snapshot{Dir: dir},
		Diffs: []gitx.FileDiff{{NewPath: "link", Status: gitx.Added}}})

	u := unit(t, rev, "link")
	require.Len(t, u.Lines, 1)
	assert.Equal(t, outside, u.Lines[0].Text, "the target, not what it points to")
}
