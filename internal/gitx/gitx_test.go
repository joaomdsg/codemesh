package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_COUNT":    "2",
		"GIT_CONFIG_KEY_0":    "init.defaultBranch",
		"GIT_CONFIG_VALUE_0":  "main",
		"GIT_CONFIG_KEY_1":    "commit.gpgsign",
		"GIT_CONFIG_VALUE_1":  "false",
	} {
		os.Setenv(k, v)
	}
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
}

func open(t *testing.T, dir string) *gitx.Repo {
	t.Helper()
	r, err := gitx.Open(dir)
	require.NoError(t, err)
	return r
}

func diffByPath(t *testing.T, r *gitx.Repo, base string) map[string]gitx.FileDiff {
	t.Helper()
	fds, err := r.Diff(base)
	require.NoError(t, err)
	m := map[string]gitx.FileDiff{}
	for _, fd := range fds {
		p := fd.NewPath
		if p == "" {
			p = fd.OldPath
		}
		m[p] = fd
	}
	return m
}

func TestOpen_resolvesTopLevelFromSubdir(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a/b/f.txt", "x\n")

	r, err := gitx.Open(filepath.Join(dir, "a", "b"))
	require.NoError(t, err)

	want, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	got, err := filepath.EvalSymlinks(r.Dir)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOpen_errorsOutsideRepo(t *testing.T) {
	t.Parallel()
	_, err := gitx.Open(t.TempDir())
	assert.Error(t, err)
}

func TestRepo_headIsEmptyWithoutCommits(t *testing.T) {
	t.Parallel()
	r := open(t, newRepo(t))

	sha, err := r.Head()
	require.NoError(t, err)
	assert.Empty(t, sha)
}

func TestRepo_headReturnsFullSha(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")

	sha, err := open(t, dir).Head()
	require.NoError(t, err)
	assert.Equal(t, git(t, dir, "rev-parse", "HEAD"), sha)
	assert.Len(t, sha, 40)
}

func TestRepo_mergeBaseFindsForkPoint(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")
	fork := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "b.txt", "b\n")
	commit(t, dir, "feature work")
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "c.txt", "c\n")
	commit(t, dir, "main work")

	got, err := open(t, dir).MergeBase("feature")
	require.NoError(t, err)
	assert.Equal(t, fork, got)
}

func TestRepo_mergeBaseErrorsOnUnknownRef(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")

	_, err := open(t, dir).MergeBase("nope")
	assert.Error(t, err)
}

func TestRepo_defaultBase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{"prefers main", "main", "main"},
		{"falls back to master", "master", "master"},
		{"falls back to HEAD", "trunk", "HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newRepo(t)
			git(t, dir, "checkout", "-q", "-b", tt.branch)
			write(t, dir, "a.txt", "a\n")
			commit(t, dir, "one")
			git(t, dir, "checkout", "-q", "-b", "work")

			assert.Equal(t, tt.want, open(t, dir).DefaultBase())
		})
	}
}

func TestRepo_defaultBasePrefersOriginHead(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	write(t, src, "a.txt", "a\n")
	commit(t, src, "one")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, src, "clone", "-q", src, clone)

	assert.Equal(t, "origin/HEAD", open(t, clone).DefaultBase())
}

func TestRepo_diffDetectsRename(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	git(t, dir, "config", "diff.renames", "false")
	write(t, dir, "old.txt", "one\ntwo\nthree\nfour\n")
	commit(t, dir, "one")
	git(t, dir, "mv", "old.txt", "new.txt")

	fds, err := open(t, dir).Diff("HEAD")
	require.NoError(t, err)
	require.Len(t, fds, 1)
	assert.Equal(t, "old.txt", fds[0].OldPath)
	assert.Equal(t, "new.txt", fds[0].NewPath)
	assert.Equal(t, gitx.Renamed, fds[0].Status)
}

func TestRepo_diffRenameWithEditKeepsHunk(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "old.txt", "one\ntwo\nthree\nfour\nfive\nsix\n")
	commit(t, dir, "one")
	write(t, dir, "sub/new.txt", "one\ntwo\nTHREE\nfour\nfive\nsix\n")
	git(t, dir, "rm", "-q", "old.txt")
	git(t, dir, "add", "sub/new.txt")

	fds, err := open(t, dir).Diff("HEAD")
	require.NoError(t, err)
	require.Len(t, fds, 1)
	assert.Equal(t, gitx.Renamed, fds[0].Status)
	assert.Equal(t, "old.txt", fds[0].OldPath)
	assert.Equal(t, "sub/new.txt", fds[0].NewPath)
	assert.Equal(t, []gitx.Hunk{{OldStart: 3, OldLines: 1, NewStart: 3, NewLines: 1}}, fds[0].Hunks)
}

func TestRepo_diffReportsDeletedFile(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "gone.txt", "a\nb\nc\n")
	commit(t, dir, "one")
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))

	fds, err := open(t, dir).Diff("HEAD")
	require.NoError(t, err)
	require.Len(t, fds, 1)
	assert.Equal(t, "gone.txt", fds[0].OldPath)
	assert.Empty(t, fds[0].NewPath)
	assert.Equal(t, gitx.Deleted, fds[0].Status)
	assert.Equal(t, []gitx.Hunk{{OldStart: 1, OldLines: 3, NewStart: 0, NewLines: 0}}, fds[0].Hunks)
}

func TestRepo_diffReportsPointInsertionWithZeroOldLines(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "a\nb\nc\n")
	commit(t, dir, "one")
	write(t, dir, "f.txt", "a\nX\nY\nb\nc\n")

	fds, err := open(t, dir).Diff("HEAD")
	require.NoError(t, err)
	require.Len(t, fds, 1)
	assert.Equal(t, gitx.Modified, fds[0].Status)
	assert.Equal(t, []gitx.Hunk{{OldStart: 1, OldLines: 0, NewStart: 2, NewLines: 2}}, fds[0].Hunks)
}

func TestRepo_diffReportsPointDeletionWithZeroNewLines(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "a\nb\nc\n")
	commit(t, dir, "one")
	write(t, dir, "f.txt", "a\nc\n")

	fds, err := open(t, dir).Diff("HEAD")
	require.NoError(t, err)
	require.Len(t, fds, 1)
	assert.Equal(t, []gitx.Hunk{{OldStart: 2, OldLines: 1, NewStart: 1, NewLines: 0}}, fds[0].Hunks)
}

func TestRepo_diffIncludesStagedAndUnstaged(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "staged.txt", "a\n")
	write(t, dir, "unstaged.txt", "a\n")
	commit(t, dir, "one")
	write(t, dir, "staged.txt", "b\n")
	git(t, dir, "add", "staged.txt")
	write(t, dir, "unstaged.txt", "b\n")

	got := diffByPath(t, open(t, dir), "HEAD")
	assert.Contains(t, got, "staged.txt")
	assert.Contains(t, got, "unstaged.txt")
}

func TestRepo_diffCountsUntrackedFileAsAdded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		hunks   []gitx.Hunk
	}{
		{"trailing newline", "a\nb\nc\n", []gitx.Hunk{{NewStart: 1, NewLines: 3}}},
		{"no trailing newline", "a\nb\nc", []gitx.Hunk{{NewStart: 1, NewLines: 3}}},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newRepo(t)
			write(t, dir, "tracked.txt", "t\n")
			commit(t, dir, "one")
			write(t, dir, "pkg/new.txt", tt.content)

			got := diffByPath(t, open(t, dir), "HEAD")
			require.Contains(t, got, "pkg/new.txt")
			fd := got["pkg/new.txt"]
			assert.Equal(t, gitx.Added, fd.Status)
			assert.Empty(t, fd.OldPath)
			assert.Equal(t, "pkg/new.txt", fd.NewPath)
			assert.False(t, fd.Binary)
			assert.Equal(t, tt.hunks, fd.Hunks)
		})
	}
}

func TestRepo_diffMarksUntrackedBinary(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "tracked.txt", "t\n")
	commit(t, dir, "one")
	write(t, dir, "blob.bin", "ab\x00cd\n")

	fd := diffByPath(t, open(t, dir), "HEAD")["blob.bin"]
	assert.Equal(t, gitx.Added, fd.Status)
	assert.True(t, fd.Binary)
	assert.Empty(t, fd.Hunks)
}

func TestRepo_diffMarksTrackedBinary(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "blob.bin", "ab\x00cd\n")
	commit(t, dir, "one")
	write(t, dir, "blob.bin", "ab\x00ef\n")

	fd := diffByPath(t, open(t, dir), "HEAD")["blob.bin"]
	assert.Equal(t, gitx.Modified, fd.Status)
	assert.True(t, fd.Binary)
	assert.Empty(t, fd.Hunks)
}

func TestRepo_diffSkipsIgnoredFiles(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, ".gitignore", "*.log\n")
	commit(t, dir, "one")
	write(t, dir, "debug.log", "x\n")

	assert.NotContains(t, diffByPath(t, open(t, dir), "HEAD"), "debug.log")
}

func TestRepo_diffAgainstEarlierBaseCoversCommittedChanges(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "b.txt", "b\n")
	commit(t, dir, "two")

	got := diffByPath(t, open(t, dir), base)
	assert.Equal(t, gitx.Added, got["b.txt"].Status)
	assert.NotContains(t, got, "a.txt")
}

func TestRepo_diffIgnoresDiffConfigAndUnicodeNames(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	git(t, dir, "config", "diff.noprefix", "true")
	write(t, dir, "pkg/café.txt", "a\n")
	commit(t, dir, "one")
	write(t, dir, "pkg/café.txt", "b\n")

	fds, err := open(t, dir).Diff("HEAD")
	require.NoError(t, err)
	require.Len(t, fds, 1)
	assert.Equal(t, "pkg/café.txt", fds[0].OldPath)
	assert.Equal(t, "pkg/café.txt", fds[0].NewPath)
}

func TestRepo_showReturnsFileAtRev(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "pkg/f.txt", "v1\n")
	commit(t, dir, "one")
	write(t, dir, "pkg/f.txt", "v2\n")
	commit(t, dir, "two")

	got, err := open(t, dir).Show("HEAD~1", "pkg/f.txt")
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got))
}

func TestRepo_showErrorsOnMissingPath(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")

	_, err := open(t, dir).Show("HEAD", "missing.txt")
	assert.Error(t, err)
}

func TestRepo_churnCountsCommitsPerPath(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "hot.txt", "1\n")
	write(t, dir, "cold.txt", "1\n")
	commit(t, dir, "one")
	write(t, dir, "hot.txt", "2\n")
	commit(t, dir, "two")
	write(t, dir, "hot.txt", "3\n")
	write(t, dir, "sub/new.txt", "1\n")
	commit(t, dir, "three")

	got, err := open(t, dir).Churn(90 * 24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"hot.txt": 3, "cold.txt": 1, "sub/new.txt": 1}, got)
}

func TestRepo_churnExcludesCommitsOlderThanWindow(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "old.txt", "1\n")
	git(t, dir, "add", "-A")
	old := time.Now().Add(-200 * 24 * time.Hour).Format(time.RFC3339)
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "-m", "old")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+old, "GIT_COMMITTER_DATE="+old)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	write(t, dir, "new.txt", "1\n")
	commit(t, dir, "new")

	got, err := open(t, dir).Churn(90 * 24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"new.txt": 1}, got)
}

func TestRepo_churnCountsBackFromTheLastCommit(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	at := func(days int, file string) {
		write(t, dir, file, "1\n")
		git(t, dir, "add", "-A")
		when := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
		cmd := exec.Command("git", "-C", dir, "commit", "-q", "-m", file)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	at(400, "older.txt")
	at(300, "old.txt")
	at(250, "last.txt")

	got, err := open(t, dir).Churn(90 * 24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"old.txt": 1, "last.txt": 1}, got, "a repo quiet for months still has churn")
}

func TestRepo_churnIsEmptyWithoutCommits(t *testing.T) {
	t.Parallel()
	got, err := open(t, newRepo(t)).Churn(time.Hour)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRepo_worktreeChecksOutRevAndCleansUp(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "v1\n")
	commit(t, dir, "one")
	write(t, dir, "f.txt", "v2\n")
	commit(t, dir, "two")
	r := open(t, dir)

	wt, cleanup, err := r.Worktree("HEAD~1")
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(wt, "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got))
	assert.Contains(t, git(t, dir, "worktree", "list"), filepath.Base(wt))

	require.NoError(t, cleanup())
	assert.NoDirExists(t, wt)
	assert.NotContains(t, git(t, dir, "worktree", "list"), filepath.Base(wt))
	assert.Len(t, strings.Split(git(t, dir, "worktree", "list"), "\n"), 1)
}

func TestRepo_worktreeErrorsOnUnknownRev(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "v1\n")
	commit(t, dir, "one")

	_, _, err := open(t, dir).Worktree("nope")
	assert.Error(t, err)
}
