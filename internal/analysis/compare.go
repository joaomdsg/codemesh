package analysis

import (
	"context"
	"errors"

	"golang.org/x/sync/errgroup"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/graphdiff"
	"github.com/joaomdsg/codemesh/internal/lang"
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

// fillMissing makes a side that lacks the root read as empty, so a project
// the change adds or deletes still compares. It reports whether that settles
// the failed load.
func (c *Comparison) fillMissing() bool {
	empty := func(missing *Result, other *code.Snapshot) *Result {
		return &Result{Key: missing.Key, Snap: code.NewBuilder(other.Lang, other.Module, other.Dir).Finish()}
	}
	switch {
	case c.Head.Err == nil && errors.Is(c.Base.Err, lang.ErrNoProject):
		c.Base = empty(c.Base, c.Head.Snap)
	case c.Base.Err == nil && errors.Is(c.Head.Err, lang.ErrNoProject):
		c.Head = empty(c.Head, c.Base.Snap)
	default:
		return false
	}
	return true
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
	if err := g.Wait(); err != nil && !c.fillMissing() {
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
