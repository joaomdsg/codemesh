package review_test

import (
	"slices"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitPair commits head over base and returns the repo, both commit SHAs
// and the diff between them.
func commitPair(t *testing.T, base, head map[string]string) (repo *gitx.Repo, baseSHA, headSHA string, diffs []gitx.FileDiff) {
	t.Helper()
	dir := testrepo.New(t, base, head)
	require.NoError(t, testrepo.Git(dir, "add", "-A"))
	require.NoError(t, testrepo.Git(dir, "commit", "-qm", "head"))
	repo, err := gitx.Open(dir)
	require.NoError(t, err)
	baseSHA, err = repo.ResolveRef("HEAD~1")
	require.NoError(t, err)
	headSHA, err = repo.ResolveRef("HEAD")
	require.NoError(t, err)
	diffs, err = repo.DiffRefs(baseSHA, headSHA)
	require.NoError(t, err)
	return repo, baseSHA, headSHA, diffs
}

// buildPR reviews two commits the way a PR compare does: a worktree and a
// snapshot per commit, and the commit diff.
func buildPR(t *testing.T, base, head map[string]string) *review.Review {
	t.Helper()
	repo, baseSHA, headSHA, diffs := commitPair(t, base, head)
	root := t.TempDir()
	snaps := map[string]*code.Snapshot{}
	for _, sha := range []string{baseSHA, headSHA} {
		wt, err := repo.WorktreeAt(root, sha)
		require.NoError(t, err)
		snaps[sha], err = lang.Load(wt)
		require.NoError(t, err)
	}
	b, h := snaps[baseSHA], snaps[headSHA]
	return review.Build(review.Input{Base: b, Head: h, Diffs: diffs, BaseFindings: smell.Find(b), HeadFindings: smell.Find(h)})
}

func TestBuild_ranksADeclThatGainedACallerAboveOneThatDidNot(t *testing.T) {
	t.Parallel()
	const mod = "module example.com/p\n\ngo 1.27\n"
	rev := buildPR(t, map[string]string{
		"go.mod": mod,
		"p/p.go": "package p\n\nfunc Alpha(x int) int { return x }\n\nfunc Zed(x int) int { return x }\n\nfunc UseAlpha() int { return Alpha(1) }\n",
	}, map[string]string{
		"p/p.go": "package p\n\nfunc Alpha(x int) int { return x + 1 }\n\nfunc Zed(x int) int { return x + 1 }\n\nfunc UseAlpha() int { return Alpha(1) }\n\nfunc UseZed() int { return Zed(1) }\n",
	})

	alpha, zed := unit(t, rev, "Alpha"), unit(t, rev, "Zed")
	require.Equal(t, []string{"example.com/p/p.UseAlpha"}, alpha.Callers)
	require.Equal(t, []string{"example.com/p/p.UseZed"}, zed.Callers)
	assert.Greater(t, zed.Risk, alpha.Risk, "same edit, same callers; Zed's is new")
	assert.Contains(t, zed.Reasons, "1 new reference")
	assert.NotContains(t, alpha.Reasons, "1 new reference")
}

func TestBuild_pairsMovesByShapeWhenNamesRepeat(t *testing.T) {
	t.Parallel()
	one := "func Run() int { return 1 }\n"
	two := "func Run(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 2\n}\n"
	rev := buildPR(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.27\n",
		"a/a.go": "package a\n\n" + one,
		"b/b.go": "package b\n\n" + two,
	}, map[string]string{
		"a/a.go": "package a\n",
		"b/b.go": "package b\n",
		"c/c.go": "package c\n\n" + one,
		"d/d.go": "package d\n\n" + two,
	})

	var runs []*review.Unit
	for _, u := range rev.Units {
		if u.Name == "Run" {
			runs = append(runs, u)
		}
	}
	require.Len(t, runs, 2, "two moves, not two removals and two additions")
	for _, u := range runs {
		assert.Equal(t, review.Moved, u.Change)
		assert.Equal(t, review.Noise, u.Lane)
	}
}

func TestBuild_readsAnyRegisteredLanguageFileAsCode(t *testing.T) {
	t.Parallel()
	if !slices.Contains(lang.Exts(), ".ts") {
		t.Skip("no .ts frontend registered")
	}
	_, _, _, diffs := commitPair(t, map[string]string{"src/a.ts": "export function fn() { return 1 }\n"},
		map[string]string{"src/a.ts": "export function fn() { return 2 }\n"})
	snap := func(shape string) *code.Snapshot {
		b := code.NewBuilder("typescript", "m", t.TempDir())
		p := b.Package(code.Package{Path: "src"})
		f := b.File(p, code.File{Path: "src/a.ts"})
		b.Decl(f, code.Decl{ID: "src.fn", Name: "fn", Kind: code.Func, Shape: shape, Start: 1, End: 1})
		return b.Finish()
	}
	rev := review.Build(review.Input{Base: snap("a"), Head: snap("b"), Diffs: diffs})

	assert.Equal(t, review.Logic, unit(t, rev, "fn").Lane)
}
