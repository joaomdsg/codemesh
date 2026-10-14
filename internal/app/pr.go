package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/joaomdsg/codemesh/internal/ghx"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/graphdiff"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/review"
)

// prTTL is how long a PR list is current. Pages ask again after it.
const prTTL = time.Minute

type prCache struct {
	prs []ghx.PR
	err error
	at  time.Time
}

// PRList is a repo's open pull requests as gh last listed them.
type PRList struct {
	PRs     []ghx.PR
	Err     error
	At      time.Time
	Loading bool // gh has not answered yet
}

// Hint says what to do about a failed listing, in one line, or "" when it
// worked. The error itself is not in it: a repo gh does not know, such as one
// off GitHub, is not a fault to report.
func (l PRList) Hint() string {
	switch {
	case l.Err == nil:
		return ""
	case errors.Is(l.Err, ghx.ErrNoGH):
		return "Pull requests need the gh CLI: https://cli.github.com"
	case errors.Is(l.Err, ghx.ErrNotAuthed):
		return "Pull requests need a login: run gh auth login"
	}
	return "Pull requests are not available for this repo."
}

// PRs returns the repo's open pull requests. The list comes from a cache;
// when it is missing or older than a minute, gh is asked in the background
// and the repo's event fires when it answers.
func (a *App) PRs(id string) PRList {
	rs, err := a.repo(id)
	if err != nil {
		return PRList{Err: err}
	}
	rs.mu.Lock()
	c := rs.prs
	rs.mu.Unlock()
	if c == nil || time.Since(c.at) > prTTL {
		a.RefreshPRs(id)
	}
	if c == nil {
		return PRList{Loading: true}
	}
	return PRList{PRs: c.prs, Err: c.err, At: c.at}
}

// RefreshPRs asks gh for the list again, in the background. A clone follows
// its remote first.
func (a *App) RefreshPRs(id string) {
	rs, err := a.repo(id)
	if err != nil {
		return
	}
	a.background("prs:"+id, id, func() {
		if rs.info.Path == "" {
			a.follow(rs)
		}
		list := a.list(rs)
		rs.mu.Lock()
		rs.prs = list
		// A failed summary is tried again, once per refresh.
		maps.DeleteFunc(rs.prSum, func(_ string, s *summary) bool { return s.err != nil })
		rs.mu.Unlock()
	})
}

// follow brings a clone's checkout up to its remote's branch. What was read of
// the old tree is stale once it moves.
func (a *App) follow(rs *repoState) {
	before, _ := rs.git.Head()
	err := rs.git.Fetch()
	if err == nil {
		err = rs.git.Advance()
	}
	if err != nil {
		a.log.Warn("following the remote", "repo", rs.info.ID, "err", err)
		return
	}
	if after, _ := rs.git.Head(); after != before {
		a.changed(rs.info.ID)
	}
}

func (a *App) list(rs *repoState) *prCache {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	prs, err := a.gh.List(ctx, rs.info.Dir())
	return &prCache{prs: prs, err: err, at: time.Now()}
}

// PRReview is a pull request read as a review: its head against the
// merge-base with its base branch. Release it when the page is rendered.
type PRReview struct {
	PR               ghx.PR
	Root             string
	BaseSHA, HeadSHA string
	Base, Head       *Analysis
	Files            []gitx.FileDiff
	Graph            *graphdiff.Diff
	Review           *review.Review
}

// Release gives both snapshots back.
func (r *PRReview) Release() {
	r.Base.Release()
	r.Head.Release()
}

// prRefs is a fetched pull request: the commits its review compares and the
// roots of both, which the working tree may not have.
type prRefs struct {
	pr         ghx.PR
	base, head string
	roots      []lang.Root
}

// fetchPR fetches pull request n and finds its merge-base with the base
// branch. Roots come from the two trees, so a project the change adds or
// deletes is one, and a file of a nested root is told from the root's own.
func (a *App) fetchPR(rs *repoState, n int) (prRefs, error) {
	pr, err := a.pr(rs, n, true)
	if err != nil {
		return prRefs{}, err
	}
	head, err := rs.git.FetchPR(n)
	if err != nil {
		return prRefs{}, err
	}
	// The base branch may have moved past what the repo has fetched.
	if _, err := rs.git.ResolveRef(pr.BaseOID); err != nil {
		if err := rs.git.Fetch(); err != nil {
			return prRefs{}, err
		}
	}
	base, err := rs.git.MergeBaseOf(head, pr.BaseOID)
	if err != nil {
		return prRefs{}, err
	}
	var roots []lang.Root
	for _, rev := range []string{base, head} {
		files, err := rs.git.Files(rev)
		if err != nil {
			return prRefs{}, err
		}
		in, err := lang.RootsIn(files)
		if err != nil {
			return prRefs{}, err
		}
		roots = append(roots, in...)
	}
	slices.SortFunc(roots, func(x, y lang.Root) int { return cmp.Or(cmp.Compare(x.Dir, y.Dir), cmp.Compare(x.Lang, y.Lang)) })
	return prRefs{pr: pr, base: base, head: head, roots: slices.Compact(roots)}, nil
}

// PRReview fetches pull request n, analyses its head and its merge-base with
// the base branch, and builds the review of one root. An empty root means the
// first root of either side. A root missing on one side reads as empty there.
func (a *App) PRReview(id, root string, n int) (*PRReview, error) {
	rs, err := a.repo(id)
	if err != nil {
		return nil, err
	}
	refs, err := a.fetchPR(rs, n)
	if err != nil {
		return nil, err
	}
	return a.prReview(rs, refs, root)
}

func (a *App) prReview(rs *repoState, refs prRefs, root string) (*PRReview, error) {
	root, err := pickRoot(refs.roots, root)
	if err != nil {
		return nil, err
	}
	c, err := a.eng.Compare(a.ctx, rs.info.ID, root, refs.base, refs.head)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &PRReview{
		PR: refs.pr, Root: root, BaseSHA: refs.base, HeadSHA: refs.head,
		Base: fromResult(rs, root, c.Base, now), Head: fromResult(rs, root, c.Head, now),
		Files: c.Files, Graph: c.Diff,
		Review: review.Build(review.Input{
			Base: c.Base.Snap, Head: c.Head.Snap, Diffs: scopeFiles(c.Files, root, refs.roots),
			BaseFindings: c.Base.Findings, HeadFindings: c.Head.Findings, Graph: c.Diff,
		}),
	}, nil
}

// pr finds an open pull request by number: in the cached list, else in a
// fresh one. With current set, a list older than prTTL does not count, as its
// base may have moved; it still answers if gh cannot.
func (a *App) pr(rs *repoState, n int, current bool) (ghx.PR, error) {
	rs.mu.Lock()
	old := rs.prs
	rs.mu.Unlock()
	if p, ok := old.find(n); ok && !(current && time.Since(old.at) > prTTL) {
		return p, nil
	}
	c := a.list(rs)
	rs.mu.Lock()
	rs.prs = c
	rs.mu.Unlock()
	if c.err != nil {
		if p, ok := old.find(n); ok {
			return p, nil
		}
		return ghx.PR{}, c.err
	}
	if p, ok := c.find(n); ok {
		return p, nil
	}
	return ghx.PR{}, fmt.Errorf("no open pull request #%d", n)
}

func (c *prCache) find(n int) (ghx.PR, bool) {
	if c != nil {
		for _, p := range c.prs {
			if p.Number == n {
				return p, true
			}
		}
	}
	return ghx.PR{}, false
}
