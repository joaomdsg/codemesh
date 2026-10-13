package analysis

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/graphdiff"
)

// Comparison is two results of one repo and root, and what differs between
// them. Release it when done.
type Comparison struct {
	Base, Head *Result
	Files      []gitx.FileDiff
	Diff       *graphdiff.Diff
}

// Release releases both results.
func (c *Comparison) Release() {
	c.Base.Release()
	c.Head.Release()
}

// Compare loads base and head independently and diffs them. head may be
// Worktree, which is compared as the working tree, untracked files included.
func (e *Engine) Compare(ctx context.Context, repoID, root, base, head string) (*Comparison, error) {
	repo, err := e.cfg.Repo(repoID)
	if err != nil {
		return nil, err
	}
	c := &Comparison{}
	var g errgroup.Group
	g.Go(func() error {
		c.Base = e.Get(ctx, Key{repoID, base, root})
		return c.Base.Err
	})
	g.Go(func() error {
		c.Head = e.Get(ctx, Key{repoID, head, root})
		return c.Head.Err
	})
	if err := g.Wait(); err != nil {
		c.Release()
		return nil, err
	}
	if c.Head.Key.Rev == Worktree {
		c.Files, err = repo.Diff(c.Base.Key.Rev)
	} else {
		c.Files, err = repo.DiffRefs(c.Base.Key.Rev, c.Head.Key.Rev)
	}
	if err != nil {
		c.Release()
		return nil, err
	}
	c.Diff = graphdiff.Between(c.Base.Snap, c.Head.Snap, c.Base.Findings, c.Head.Findings)
	return c, nil
}
