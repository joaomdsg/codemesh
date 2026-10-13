// Package gitx wraps the git CLI for the few operations codemesh needs.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// Repo is a git repository identified by its top-level directory.
type Repo struct{ Dir string }

// status is how a file changed.
type status int

const (
	Added status = iota
	Deleted
	modified
	Renamed
)

// hunk is a changed line range. With -U0, a zero Lines count means a point
// insertion or deletion after Start.
type hunk struct{ OldStart, OldLines, NewStart, NewLines int }

// FileDiff is one file's change. A path is "" when the file is absent on that side.
type FileDiff struct {
	OldPath, NewPath string
	Status           status
	Binary           bool
	Hunks            []hunk
}

// env is the process's environment less the variables that point git at a
// repository other than the one it runs in, as a git hook's environment does.
func env() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(elsewhere, k)
	})
}

// elsewhere are the variables of `git rev-parse --local-env-vars` that locate
// a repository; the config ones stay.
var elsewhere = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE", "GIT_PREFIX"}

func run(dir string, args ...string) ([]byte, error) {
	return runContext(context.Background(), dir, args...)
}

// networkTimeout is how long a command that talks to a remote may take. A
// stalled server would otherwise hold its caller, and whatever lock it holds,
// for good.
var networkTimeout = 10 * time.Minute

func network(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	return runContext(ctx, dir, args...)
}

func runContext(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Without GIT_TERMINAL_PROMPT=0 a remote that wants credentials makes git
	// ask on the server's terminal and wait.
	cmd.Env = append(env(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	// A killed git leaves its helpers holding the pipes; do not wait for them.
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s", networkTimeout)
		}
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
func (r *Repo) MergeBase(ref string) (string, error) { return r.MergeBaseOf(ref, "HEAD") }

// MergeBaseOf returns the full sha of the merge base of two revs.
func (r *Repo) MergeBaseOf(a, b string) (string, error) {
	out, err := run(r.Dir, "merge-base", a, b)
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
	fds, err := r.diff(base)
	if err != nil {
		return nil, err
	}
	added, err := r.untrackedDiffs()
	if err != nil {
		return nil, err
	}
	return append(fds, added...), nil
}

// DiffRefs compares two commits.
func (r *Repo) DiffRefs(base, head string) ([]FileDiff, error) {
	return r.diff(base, head)
}

func (r *Repo) diff(revs ...string) ([]FileDiff, error) {
	// Explicit prefixes keep diff.noprefix and diff.mnemonicPrefix from changing the header format.
	args := append([]string{"-c", "core.quotepath=off", "diff", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", "-U0", "-M"}, revs...)
	out, err := run(r.Dir, args...)
	if err != nil {
		return nil, err
	}
	files, _, err := gitdiff.Parse(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}
	fds := make([]FileDiff, 0, len(files))
	for _, f := range files {
		fds = append(fds, fileDiff(f))
	}
	return fds, nil
}

func fileDiff(f *gitdiff.File) FileDiff {
	fd := FileDiff{OldPath: f.OldName, NewPath: f.NewName, Status: modified, Binary: f.IsBinary}
	switch {
	case f.IsNew:
		fd.Status = Added
	case f.IsDelete:
		fd.Status = Deleted
	case f.IsRename:
		fd.Status = Renamed
	}
	for _, tf := range f.TextFragments {
		fd.Hunks = append(fd.Hunks, hunk{int(tf.OldPosition), int(tf.OldLines), int(tf.NewPosition), int(tf.NewLines)})
	}
	return fd
}

// untrackedDiffs lists the untracked files git does not ignore as added.
func (r *Repo) untrackedDiffs() ([]FileDiff, error) {
	out, err := run(r.Dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var fds []FileDiff
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		fd, ok, err := r.untracked(p)
		if err != nil {
			return nil, err
		}
		if ok {
			fds = append(fds, fd)
		}
	}
	return fds, nil
}

// untracked describes an untracked entry. A link is listed but not
// followed: its target may be missing, a directory or outside the tree. A
// directory, such as a nested repository, has no lines of its own.
func (r *Repo) untracked(path string) (fd FileDiff, ok bool, err error) {
	fd = FileDiff{NewPath: path, Status: Added}
	full := filepath.Join(r.Dir, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if err != nil || info.IsDir() {
		return fd, false, err
	}
	if !info.Mode().IsRegular() {
		return fd, true, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return fd, false, err
	}
	// Same heuristic git uses: a NUL byte in the first 8000 bytes.
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		fd.Binary = true
		return fd, true, nil
	}
	if n := lines(data); n > 0 {
		fd.Hunks = []hunk{{NewStart: 1, NewLines: n}}
	}
	return fd, true, nil
}

// lines counts data's lines, the last one counted without its newline.
func lines(data []byte) int {
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// Churn counts commits per path in the since-long window ending at the last
// commit. Renames are not followed.
func (r *Repo) Churn(since time.Duration) (map[string]int, error) {
	if head, err := r.Head(); err != nil || head == "" {
		return map[string]int{}, err
	}
	return r.ChurnAt("HEAD", since)
}

// ChurnAt is Churn for the history that ends at rev.
func (r *Repo) ChurnAt(rev string, since time.Duration) (map[string]int, error) {
	sha, err := r.ResolveRef(rev)
	if err != nil {
		return nil, err
	}
	// The window ends at the last commit, not now, so a repo nobody touched
	// for months still shows where its work went.
	last, err := run(r.Dir, "log", "-1", "--format=%cI", sha, "--")
	if err != nil {
		return nil, err
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(string(last)))
	if err != nil {
		return nil, err
	}
	cutoff := end.Add(-since).Format(time.RFC3339)
	out, err := run(r.Dir, "-c", "core.quotepath=off", "log", "--since="+cutoff, "--name-only", "-z", "--format=", sha, "--")
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

// lockedBy prefixes the lock reason on a worktree this process owns, so a
// prune leaves it alone and a reader can tell whose it is.
const lockedBy = "codemesh pid "

// Worktree checks out rev detached in a temporary directory. Call cleanup to remove it.
func (r *Repo) Worktree(rev string) (dir string, cleanup func() error, err error) {
	dir, err = os.MkdirTemp("", "codemesh-wt-")
	if err != nil {
		return "", nil, err
	}
	if _, err := run(r.Dir, "worktree", "add", "--detach", "--lock", "--reason", lockedBy+strconv.Itoa(os.Getpid()), dir, rev); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, func() error {
		// A locked worktree needs --force twice.
		_, err := run(r.Dir, "worktree", "remove", "--force", "--force", dir)
		return errors.Join(err, os.RemoveAll(dir))
	}, nil
}
