package app

import (
	"cmp"
	"fmt"
	"time"

	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/joaomdsg/codemesh/internal/workspace"
)

// Analysis is one root of a repo at one revision, loaded. The snapshot reads
// its source from disk, so Release it once the page is rendered.
type Analysis struct {
	Repo     workspace.Repo
	Root     string // directory relative to the repo; "." for the repo itself
	Rev      string // commit sha, or analysis.Worktree
	At       time.Time
	Snap     *code.Snapshot
	Findings []smell.Finding
	Err      error // the load failed; Snap is nil

	// Set by WorkingReview: the working tree against the merge-base of Base
	// and HEAD. Review is nil, and Note says why, when there is none.
	Base, BaseSHA string
	Review        *review.Review
	Note          string

	release func()
}

// Release gives the analysis back. It is safe to call more than once.
func (a *Analysis) Release() {
	if a.release != nil {
		a.release()
	}
}

// Prognoses says where the code needs attention, most urgent first.
func (a *Analysis) Prognoses() []prognosis.Prognosis {
	return prognosis.Find(a.Snap, a.Findings)
}

// defaultRev is what a repo shows when asked for no revision: a local repo's
// working tree, a clone's default branch.
func defaultRev(r workspace.Repo) string {
	if r.Path != "" {
		return analysis.Worktree
	}
	return "HEAD"
}

// Analyse loads root of repo id at rev. An empty root means the repo's first
// root, an empty rev its working tree, or the default branch of a clone. The
// result is never nil; Err says why a load failed.
func (a *App) Analyse(id, root, rev string) *Analysis {
	out := &Analysis{}
	rs, err := a.repo(id)
	if err != nil {
		out.Err = err
		return out
	}
	out.Repo = rs.info
	if out.Root, err = a.root(id, root); err != nil {
		out.Err = err
		return out
	}
	r := a.eng.Get(a.ctx, analysis.Key{Repo: id, Rev: cmp.Or(rev, defaultRev(rs.info)), Root: out.Root})
	out.Rev, out.Snap, out.Findings, out.Err, out.release = r.Key.Rev, r.Snap, r.Findings, r.Err, r.Release
	out.At = a.Updated(id)
	return out
}

// treeReview is the review of a working tree, kept while its head snapshot
// is the engine's current one.
type treeReview struct {
	snap          *code.Snapshot
	base, baseSHA string
	note          string
	review        *review.Review
	sum           *summary
}

// WorkingReview is Analyse of the working tree with its review against the
// repo's base branch. A clone has no working tree: it gets its default branch
// and a Note.
func (a *App) WorkingReview(id, root string) *Analysis {
	rs, err := a.repo(id)
	if err != nil {
		return &Analysis{Err: err}
	}
	if rs.info.Path == "" {
		out := a.Analyse(id, root, "")
		out.Note = "a cloned repo has no working tree, so there is nothing to review"
		return out
	}
	out := a.Analyse(id, root, analysis.Worktree)
	if out.Err != nil {
		return out
	}
	rs.wt.Lock()
	defer rs.wt.Unlock()
	rs.mu.Lock()
	t := rs.tree[out.Root]
	rs.mu.Unlock()
	if t == nil || t.snap != out.Snap {
		t = a.buildTree(rs, out)
		rs.mu.Lock()
		rs.tree[out.Root] = t
		rs.mu.Unlock()
	}
	out.Base, out.BaseSHA, out.Note, out.Review = t.base, t.baseSHA, t.note, t.review
	return out
}

func (a *App) buildTree(rs *repoState, head *Analysis) *treeReview {
	t := &treeReview{snap: head.Snap}
	t.base = cmp.Or(rs.info.DefaultBase, rs.git.DefaultBase())
	sha, err := rs.git.MergeBase(t.base)
	if err != nil {
		t.note = fmt.Sprintf("no merge-base with %s: %v", t.base, err)
		return t
	}
	t.baseSHA = sha
	c, err := a.eng.Compare(a.ctx, rs.info.ID, head.Root, sha, analysis.Worktree)
	if err != nil {
		t.note = "the comparison failed: " + err.Error()
		return t
	}
	defer c.Release()
	t.snap = c.Head.Snap
	t.review = review.Build(review.Input{
		Base: c.Base.Snap, Head: c.Head.Snap, Diffs: scopeFiles(c.Files, head.Root),
		BaseFindings: c.Base.Findings, HeadFindings: c.Head.Findings, Graph: c.Diff,
	})
	t.sum = summarize(head.Root, t.review, c.Files)
	return t
}
