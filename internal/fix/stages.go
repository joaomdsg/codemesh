package fix

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
)

// prepare opens the repository and cuts the two worktrees; it returns the
// starting point's and the module's path inside a worktree.
func (r *Run) prepare(life context.Context, dir string) (start, rel string, ok bool) {
	repo, head, ok := r.open(dir)
	if !ok {
		return "", "", false
	}
	// The starting point gets a worktree of its own, kept for the run: the
	// review reads each side's source from its snapshot's directory when it
	// is built, after Claude has edited Claude's copy.
	wt, start, err := r.worktrees(repo, head)
	if err != nil {
		r.fail(err)
		return "", "", false
	}
	if life.Err() != nil {
		return "", "", false
	}
	abs, _ := filepath.Abs(dir)
	rel, err = filepath.Rel(repo.Dir, abs)
	if err != nil {
		rel = "."
	}
	mod := filepath.Join(wt, rel)
	check := checkOf(wt, mod)
	r.set(func() { r.check, r.wt, r.mod, r.tree = check, wt, mod, newTree(wt, mod) })
	return start, rel, true
}

// open opens the repository and records where the run starts; it fails the
// run and reports false when it cannot.
func (r *Run) open(dir string) (*gitx.Repo, string, bool) {
	repo, err := gitx.Open(dir)
	if err != nil {
		r.fail(fmt.Errorf("not a git repository: %w", err))
		return nil, "", false
	}
	head, err := repo.Head()
	if err != nil || head == "" {
		r.fail(errors.New("the repository has no commits to start from"))
		return nil, "", false
	}
	base, err := repo.Branch()
	if err != nil {
		r.fail(err)
		return nil, "", false
	}
	_, originErr := command(context.Background(), repo.Dir, nil, "git", "remote", "get-url", "origin")
	r.set(func() { r.repo, r.head, r.base, r.origin = repo.Dir, head, base, originErr == nil })
	r.note("Copying the last commit (" + head[:7] + ") into a throwaway worktree. Uncommitted edits are not included.")
	return repo, head, true
}

func (r *Run) worktrees(repo *gitx.Repo, head string) (wt, start string, err error) {
	wt, cleanup, err := repo.Worktree(head)
	if err != nil {
		return "", "", err
	}
	r.set(func() { r.cleanup = cleanup })
	start, cleanStart, err := repo.Worktree(head)
	if err != nil {
		return "", "", err
	}
	r.set(func() { r.cleanup = func() error { return errors.Join(cleanup(), cleanStart()) } })
	return wt, start, nil
}

// brief is what the first prompt needs beyond the prognosis.
func (r *Run) brief(self string, before *Side) prognosis.Brief {
	b := prognosis.Brief{Check: r.check.Name, Where: r.check.Where, Nearby: prognosis.Nearby(before.Prognoses, r.p)}
	if self != "" {
		b.List = self + " prognoses " + r.mod
	}
	return b
}

// work lets Claude edit the worktree. It returns Claude's error when the
// run was not stopped.
func (r *Run) work(ctx context.Context, agent, prompt string) error {
	working, worked := context.WithCancel(ctx)
	go r.remapping(working, r.mod)
	err := r.claude(ctx, agent, r.mod, prompt)
	worked()
	if ctx.Err() != nil {
		r.set(func() { r.state = Stopped })
		r.note("Stopped. Showing what changed so far.")
		return nil
	}
	r.set(func() { r.state = Checking })
	if err == nil {
		r.note("Analysing the result and running the checks again.")
	}
	return err
}

// result analyses and checks the worktree and reviews it against the start.
func (r *Run) result(life context.Context, before *Side) (*Side, *review.Review, bool) {
	if life.Err() != nil {
		return nil, nil, false
	}
	after, err := analyse(life, r.mod, r.check)
	if err != nil {
		r.fail(fmt.Errorf("the edited code did not load: %w", err))
		return nil, nil, false
	}
	diffs, err := (&gitx.Repo{Dir: r.wt}).Diff(r.head)
	if err != nil {
		r.fail(err)
		return nil, nil, false
	}
	rev := review.Build(review.Input{Base: before.Snap, Head: after.Snap, Diffs: diffs, BaseFindings: before.Findings, HeadFindings: after.Findings})
	return after, rev, true
}

// addsFiles reports whether changes write a Go file the starting tree lacks.
func (r *Run) addsFiles(changes []Change) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.before == nil {
		return false
	}
	for _, c := range changes {
		test := strings.HasSuffix(c.File, "_test.go") || strings.HasPrefix(c.File, "test/")
		if test || !strings.HasSuffix(c.File, ".go") && !strings.HasSuffix(c.File, ".jl") {
			continue
		}
		if !hasFile(r.before.Snap, c.File) {
			return true
		}
	}
	return false
}

func hasFile(s *code.Snapshot, path string) bool {
	for _, p := range s.Packages {
		for _, f := range p.Files {
			if f.Path == path {
				return true
			}
		}
	}
	return false
}

// remapping re-analyses the worktree each time Claude adds or writes a new
// file, so the replay map gains its tile while the run is live. A tree that
// does not load mid-edit keeps the last map.
func (r *Run) remapping(ctx context.Context, mod string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.remap:
		}
		snap, err := code.Load(mod)
		if err != nil || ctx.Err() != nil {
			continue
		}
		r.set(func() { r.now, r.nowN = snap, r.nowN+1 })
	}
}
