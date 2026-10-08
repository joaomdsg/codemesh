// Package gitx wraps the git CLI for the few operations codemesh needs.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// Repo is a git repository identified by its top-level directory.
type Repo struct{ Dir string }

// Status is how a file changed.
type Status int

const (
	Added Status = iota
	Deleted
	Modified
	Renamed
)

// Hunk is a changed line range. With -U0, a zero Lines count means a point
// insertion or deletion after Start.
type Hunk struct{ OldStart, OldLines, NewStart, NewLines int }

// FileDiff is one file's change. A path is "" when the file is absent on that side.
type FileDiff struct {
	OldPath, NewPath string
	Status           Status
	Binary           bool
	Hunks            []Hunk
}

func run(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Open returns the repository containing dir.
func Open(dir string) (*Repo, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	return &Repo{Dir: strings.TrimSpace(string(out))}, nil
}

// MergeBase returns the full sha of the merge base of ref and HEAD.
func (r *Repo) MergeBase(ref string) (string, error) {
	out, err := run(r.Dir, "merge-base", ref, "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultBase returns the first of origin/HEAD, main and master that resolves, else "HEAD".
func (r *Repo) DefaultBase() string {
	for _, ref := range []string{"origin/HEAD", "main", "master"} {
		if _, err := run(r.Dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	return "HEAD"
}

// Head returns the HEAD sha, or "" in a repository with no commits.
func (r *Repo) Head() (string, error) {
	out, err := run(r.Dir, "rev-parse", "--verify", "--quiet", "HEAD")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Diff compares the working tree (staged, unstaged and untracked files) with base.
func (r *Repo) Diff(base string) ([]FileDiff, error) {
	// Explicit prefixes keep diff.noprefix and diff.mnemonicPrefix from changing the header format.
	out, err := run(r.Dir, "-c", "core.quotepath=off", "diff", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", "-U0", "-M", base)
	if err != nil {
		return nil, err
	}
	files, _, err := gitdiff.Parse(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}
	var fds []FileDiff
	for _, f := range files {
		fd := FileDiff{OldPath: f.OldName, NewPath: f.NewName, Status: Modified, Binary: f.IsBinary}
		switch {
		case f.IsNew:
			fd.Status = Added
		case f.IsDelete:
			fd.Status = Deleted
		case f.IsRename:
			fd.Status = Renamed
		}
		for _, tf := range f.TextFragments {
			fd.Hunks = append(fd.Hunks, Hunk{int(tf.OldPosition), int(tf.OldLines), int(tf.NewPosition), int(tf.NewLines)})
		}
		fds = append(fds, fd)
	}

	untracked, err := run(r.Dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(string(untracked), "\x00") {
		if p == "" {
			continue
		}
		fd, err := r.untracked(p)
		if err != nil {
			return nil, err
		}
		fds = append(fds, fd)
	}
	return fds, nil
}

func (r *Repo) untracked(path string) (FileDiff, error) {
	fd := FileDiff{NewPath: path, Status: Added}
	data, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(path)))
	if err != nil {
		return fd, err
	}
	// Same heuristic git uses: a NUL byte in the first 8000 bytes.
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		fd.Binary = true
		return fd, nil
	}
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	if n > 0 {
		fd.Hunks = []Hunk{{NewStart: 1, NewLines: n}}
	}
	return fd, nil
}

// Show returns the contents of path at rev.
func (r *Repo) Show(rev, path string) ([]byte, error) {
	return run(r.Dir, "show", rev+":"+path)
}

// Churn counts commits per path in the since-long window ending at the last
// commit. Renames are not followed.
func (r *Repo) Churn(since time.Duration) (map[string]int, error) {
	if head, err := r.Head(); err != nil || head == "" {
		return map[string]int{}, err
	}
	// The window ends at the last commit, not now, so a repo nobody touched
	// for months still shows where its work went.
	last, err := run(r.Dir, "log", "-1", "--format=%cI")
	if err != nil {
		return nil, err
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(string(last)))
	if err != nil {
		return nil, err
	}
	cutoff := end.Add(-since).Format(time.RFC3339)
	out, err := run(r.Dir, "-c", "core.quotepath=off", "log", "--since="+cutoff, "--name-only", "-z", "--format=")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			counts[p]++
		}
	}
	return counts, nil
}

// Worktree checks out rev detached in a temporary directory. Call cleanup to remove it.
func (r *Repo) Worktree(rev string) (dir string, cleanup func() error, err error) {
	dir, err = os.MkdirTemp("", "codemesh-wt-")
	if err != nil {
		return "", nil, err
	}
	if _, err := run(r.Dir, "worktree", "add", "--detach", dir, rev); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, func() error {
		_, err := run(r.Dir, "worktree", "remove", "--force", dir)
		return errors.Join(err, os.RemoveAll(dir))
	}, nil
}
