package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Clone clones url into dest without blobs, which git fetches when it needs
// them. The history stays complete: merge bases and churn need it.
func Clone(url, dest string) (*Repo, error) {
	dest, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	if _, err := run(parent, "clone", "--quiet", "--filter=blob:none", "--", url, dest); err != nil {
		return nil, err
	}
	return &Repo{Dir: dest}, nil
}

// Fetch updates the remote-tracking branches from origin.
func (r *Repo) Fetch() error {
	_, err := run(r.Dir, "fetch", "--quiet", "--prune", "origin")
	return err
}

// FetchPR fetches the head of pull request n into refs/codemesh/pr/n and
// returns its sha. Forks work, as the head lives on the base repository's
// pull/n/head ref. The leading + follows force pushes.
func (r *Repo) FetchPR(n int) (string, error) {
	ref := "refs/codemesh/pr/" + strconv.Itoa(n)
	if _, err := run(r.Dir, "fetch", "--quiet", "--no-tags", "origin", fmt.Sprintf("+pull/%d/head:%s", n, ref)); err != nil {
		return "", err
	}
	return r.ResolveRef(ref)
}

// ResolveRef returns the full sha of the commit ref names.
func (r *Repo) ResolveRef(ref string) (string, error) {
	out, err := run(r.Dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// WorktreeAt checks out rev detached at root/<sha> and returns that
// directory. It is idempotent: an existing checkout of the sha is reused and
// touched, so GCWorktrees can tell what was used last. Unlike Worktree, it is
// not locked and outlives the process.
func (r *Repo) WorktreeAt(root, rev string) (string, error) {
	sha, err := r.ResolveRef(rev)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, sha)
	if head, err := run(dir, "rev-parse", "HEAD"); err == nil && strings.TrimSpace(string(head)) == sha {
		now := time.Now()
		return dir, os.Chtimes(dir, now, now)
	}
	// What is here is no usable worktree: a half-made checkout, or one whose
	// repository was replaced. Its registration, if any, goes with it.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if _, err := run(r.Dir, "worktree", "prune"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if _, err := run(r.Dir, "worktree", "add", "--detach", dir, sha); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// RemoveWorktree deletes a worktree made by WorktreeAt.
func (r *Repo) RemoveWorktree(dir string) error {
	_, err := run(r.Dir, "worktree", "remove", "--force", dir)
	return errors.Join(err, os.RemoveAll(dir))
}

// GCWorktrees removes the worktrees under root except the keep most recently
// used and those whose sha is in use.
func (r *Repo) GCWorktrees(root string, keep int, inUse map[string]bool) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type wt struct {
		sha  string
		used time.Time
	}
	var all []wt
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		all = append(all, wt{e.Name(), info.ModTime()})
	}
	slices.SortStableFunc(all, func(a, b wt) int { return b.used.Compare(a.used) })
	var errs []error
	for i, w := range all {
		if i < keep || inUse[w.sha] {
			continue
		}
		errs = append(errs, r.RemoveWorktree(filepath.Join(root, w.sha)))
	}
	_, err = run(r.Dir, "worktree", "prune")
	return errors.Join(append(errs, err)...)
}
