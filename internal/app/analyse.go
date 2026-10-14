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

// Analysis is one root of a repo at one revision. The snapshot reads
// its source from disk, so Release it once the page is rendered.
type Analysis struct {
	Repo     workspace.Repo
	Root     string         // directory relative to the repo; "." for the repo itself
	Rev      string         // commit sha, or analysis.Worktree
	At       time.Time      // when Snap was analysed
	Snap     *code.Snapshot // nil until the first load lands
	Findings []smell.Finding
	Stale    bool  // Snap is the last analysis; a newer one is loading
	Err      error // the last load failed; Snap, if set, is the one before it

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

// Analyse loads root of repo id at rev, waiting for it. An empty root means
// the repo's first root, an empty rev its working tree, or the default branch
// of a clone. The result is never nil; Err says why a load failed.
func (a *App) Analyse(id, root, rev string) *Analysis {
	rs, key, err := a.key(id, root, rev)
	if err != nil {
		return &Analysis{Repo: rs.info, Err: err}
	}
	return fromResult(rs, key.Root, a.eng.Get(a.ctx, key))
}

// Peek is Analyse without the wait, for a page: it returns what is cached
// and has what is missing or stale loaded ahead of the warm-up, announcing
// it with an Event. Snap is nil until there is something to show.
func (a *App) Peek(id, root, rev string) *Analysis {
	rs, key, err := a.key(id, root, rev)
	if err != nil {
		return &Analysis{Repo: rs.info, Err: err}
	}
	a.eng.Ensure(key)
	return fromResult(rs, key.Root, a.eng.Peek(key))
}

// Cached is Peek that loads nothing: what is in memory or stored, if any.
func (a *App) Cached(id, root, rev string) *Analysis {
	rs, key, err := a.key(id, root, rev)
	if err != nil {
		return &Analysis{Repo: rs.info, Err: err}
	}
	return fromResult(rs, key.Root, a.eng.Peek(key))
}

// key names root of repo id at rev, as Analyse reads its arguments. rs is
// never nil.
func (a *App) key(id, root, rev string) (*repoState, analysis.Key, error) {
	rs, err := a.repo(id)
	if err != nil {
		return &repoState{}, analysis.Key{}, err
	}
	if root, err = a.root(id, root); err != nil {
		return rs, analysis.Key{}, err
	}
	return rs, analysis.Key{Repo: id, Rev: cmp.Or(rev, defaultRev(rs.info)), Root: root}, nil
}

func fromResult(rs *repoState, root string, r *analysis.Result) *Analysis {
	return &Analysis{
		Repo: rs.info, Root: root, Rev: r.Key.Rev, At: r.At,
		Snap: r.Snap, Findings: r.Findings, Stale: r.Stale, Err: r.Err, release: r.Release,
	}
}

// Open warms what a repo's pages read: each root at its default revision
// and, for a local repo, the base its working-tree review compares with. It
// loads behind what pages ask for and returns at once.
func (a *App) Open(id string) {
	rs, err := a.repo(id)
	if err != nil {
		return
	}
	rs.mu.Lock()
	warm := rs.warmGen != rs.gen || !rs.warmed
	rs.warmGen, rs.warmed = rs.gen, true
	rs.mu.Unlock()
	if !warm {
		return
	}
	a.background("warm:"+id, id, func() {
		var keys []analysis.Key
		base := ""
		if rs.info.Path != "" {
			if _, sha, err := reviewBase(rs); err == nil {
				base = sha
			}
		}
		for _, root := range a.Roots(id) {
			keys = append(keys, analysis.Key{Repo: id, Rev: defaultRev(rs.info), Root: root.Dir})
			if base != "" {
				keys = append(keys, analysis.Key{Repo: id, Rev: base, Root: root.Dir})
			}
		}
		a.eng.Warm(keys...)
	})
}

// reviewBase is the branch a working tree is reviewed against and its
// merge-base with HEAD.
func reviewBase(rs *repoState) (name, sha string, err error) {
	name = cmp.Or(rs.info.DefaultBase, rs.git.DefaultBase())
	sha, err = rs.git.MergeBase(name)
	return name, sha, err
}

// treeReview is the review of a working tree, kept while its head snapshot
// is the engine's current one.
type treeReview struct {
	snap          *code.Snapshot
	at            time.Time // when snap was analysed
	findings      []smell.Finding
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
	t := a.treeFor(rs, out)
	out.Base, out.BaseSHA, out.Note, out.Review = t.base, t.baseSHA, t.note, t.review
	return out
}

// PeekReview is WorkingReview without the wait. Until the review of the
// current snapshot is built, it shows the last one built, Stale, or nothing.
func (a *App) PeekReview(id, root string) *Analysis {
	rs, err := a.repo(id)
	if err != nil {
		return &Analysis{Err: err}
	}
	if rs.info.Path == "" {
		out := a.Peek(id, root, "")
		out.Note = "a cloned repo has no working tree, so there is nothing to review"
		return out
	}
	out := a.Peek(id, root, analysis.Worktree)
	if out.Snap == nil {
		return out
	}
	rs.mu.Lock()
	t := rs.tree[out.Root]
	rs.mu.Unlock()
	if t == nil || t.snap != out.Snap {
		// Built from what Peek shows, as WorkingReview would load again a
		// root whose last load failed.
		a.background("review:"+id+":"+out.Root, id, func() {
			head := a.Peek(id, out.Root, analysis.Worktree)
			defer head.Release()
			if head.Snap != nil {
				a.treeFor(rs, head)
			}
		})
	}
	switch {
	case t == nil:
		out.Release()
		return &Analysis{Repo: out.Repo, Root: out.Root, Rev: out.Rev, At: out.At}
	case t.snap != out.Snap:
		// The review is of an older snapshot: show the two together.
		out.Snap, out.Findings, out.At, out.Stale = t.snap, t.findings, t.at, true
	}
	out.Base, out.BaseSHA, out.Note, out.Review = t.base, t.baseSHA, t.note, t.review
	return out
}

// treeFor is the review of head's root, built unless the kept one is of
// head's snapshot.
func (a *App) treeFor(rs *repoState, head *Analysis) *treeReview {
	rs.wt.Lock()
	defer rs.wt.Unlock()
	rs.mu.Lock()
	t := rs.tree[head.Root]
	rs.mu.Unlock()
	if t == nil || t.snap != head.Snap {
		t = a.buildTree(rs, head)
		rs.mu.Lock()
		rs.tree[head.Root] = t
		rs.mu.Unlock()
	}
	return t
}

func (a *App) buildTree(rs *repoState, head *Analysis) *treeReview {
	t := &treeReview{snap: head.Snap, findings: head.Findings, at: head.At}
	var sha string
	var err error
	if t.base, sha, err = reviewBase(rs); err != nil {
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
	t.snap, t.findings, t.at = c.Head.Snap, c.Head.Findings, c.Head.At
	t.review = review.Build(review.Input{
		Base: c.Base.Snap, Head: c.Head.Snap, Diffs: scopeFiles(c.Files, head.Root, a.Roots(rs.info.ID)),
		BaseFindings: c.Base.Findings, HeadFindings: c.Head.Findings, Graph: c.Diff,
	})
	t.sum = summarize(head.Root, t.review, c.Files)
	return t
}
