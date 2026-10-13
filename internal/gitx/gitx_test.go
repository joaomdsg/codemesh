package gitx_test

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestRepo_mergeBaseOfFindsTheForkPointOfTwoRevsNotOfHEAD(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "z.txt", "z\n")
	commit(t, dir, "zero")
	zero := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")
	fork := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "b.txt", "b\n")
	commit(t, dir, "feature work")
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "c.txt", "c\n")
	commit(t, dir, "main work")
	git(t, dir, "checkout", "-q", "--detach", zero)

	got, err := open(t, dir).MergeBaseOf("main", "feature")
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
	assert.Equal(t, []span{{OldStart: 3, OldLines: 1, NewStart: 3, NewLines: 1}}, spans(fds[0].Hunks))
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
	assert.Equal(t, []span{{OldStart: 1, OldLines: 3, NewStart: 0, NewLines: 0}}, spans(fds[0].Hunks))
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
	assert.NotContains(t, []any{gitx.Added, gitx.Deleted, gitx.Renamed}, fds[0].Status, "modified")
	assert.Equal(t, []span{{OldStart: 1, OldLines: 0, NewStart: 2, NewLines: 2}}, spans(fds[0].Hunks))
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
	assert.Equal(t, []span{{OldStart: 2, OldLines: 1, NewStart: 1, NewLines: 0}}, spans(fds[0].Hunks))
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
		hunks   []span
	}{
		{"trailing newline", "a\nb\nc\n", []span{{NewStart: 1, NewLines: 3}}},
		{"no trailing newline", "a\nb\nc", []span{{NewStart: 1, NewLines: 3}}},
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
			assert.Equal(t, tt.hunks, spans(fd.Hunks))
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

func TestRepo_diffListsUntrackedLinksWithoutReadingThem(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "tracked.txt", "t\n")
	commit(t, dir, "one")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	write(t, dir, "sub/x.txt", "x\n")
	require.NoError(t, os.Symlink("missing", filepath.Join(dir, "dangling")))
	require.NoError(t, os.Symlink("sub", filepath.Join(dir, "dirlink")))
	nested := filepath.Join(dir, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))
	require.NoError(t, exec.Command("git", "-C", nested, "init", "--quiet").Run())

	got := diffByPath(t, open(t, dir), "HEAD")
	for _, p := range []string{"dangling", "dirlink"} {
		require.Contains(t, got, p)
		assert.Equal(t, gitx.Added, got[p].Status, p)
		assert.Empty(t, got[p].Hunks, p)
	}
	assert.NotContains(t, got, "nested/", "a nested repository has no lines of its own")
}

func TestRepo_ignoresAGitDirFromTheEnvironment(t *testing.T) {
	a, b := newRepo(t), newRepo(t)
	for _, dir := range []string{a, b} {
		write(t, dir, "f.txt", dir)
		commit(t, dir, "one")
	}
	want := git(t, a, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(b, ".git"))
	t.Setenv("GIT_WORK_TREE", b)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(b, ".git", "objects"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(b, ".git"))

	got, err := (&gitx.Repo{Dir: a}).Head()
	require.NoError(t, err)
	assert.Equal(t, want, got, "git runs on the repository it was given")
}

func TestRepo_diffMarksTrackedBinary(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "blob.bin", "ab\x00cd\n")
	commit(t, dir, "one")
	write(t, dir, "blob.bin", "ab\x00ef\n")

	fd := diffByPath(t, open(t, dir), "HEAD")["blob.bin"]
	assert.NotContains(t, []any{gitx.Added, gitx.Deleted, gitx.Renamed}, fd.Status, "modified")
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

func TestClone_clonesLocalBareRepoPartially(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	write(t, src, "f.txt", "v1\n")
	commit(t, src, "one")
	bare := filepath.Join(t.TempDir(), "origin.git")
	git(t, src, "clone", "-q", "--bare", src, bare)
	git(t, bare, "config", "uploadpack.allowFilter", "true")
	dest := filepath.Join(t.TempDir(), "clones", "host", "owner", "name")

	r, err := gitx.Clone("file://"+bare, dest)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dest, "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got))
	assert.Equal(t, "blob:none", git(t, dest, "config", "remote.origin.partialclonefilter"))
	head, err := r.Head()
	require.NoError(t, err)
	assert.Equal(t, git(t, src, "rev-parse", "HEAD"), head)
	assert.False(t, strings.Contains(git(t, dest, "rev-parse", "--is-shallow-repository"), "true"))
}

func TestClone_errorsOnMissingRemote(t *testing.T) {
	t.Parallel()
	_, err := gitx.Clone("file://"+filepath.Join(t.TempDir(), "nope.git"), filepath.Join(t.TempDir(), "dest"))
	assert.Error(t, err)
}

func TestRepo_fetchBringsNewCommits(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	write(t, src, "f.txt", "v1\n")
	commit(t, src, "one")
	r, err := gitx.Clone("file://"+src, filepath.Join(t.TempDir(), "clone"))
	require.NoError(t, err)
	write(t, src, "f.txt", "v2\n")
	commit(t, src, "two")

	require.NoError(t, r.Fetch())

	got, err := r.ResolveRef("origin/main")
	require.NoError(t, err)
	assert.Equal(t, git(t, src, "rev-parse", "HEAD"), got)
}

func TestRepo_advanceMovesAClonesBranchToItsUpstream(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	write(t, src, "f.txt", "v1\n")
	commit(t, src, "one")
	r, err := gitx.Clone("file://"+src, filepath.Join(t.TempDir(), "clone"))
	require.NoError(t, err)
	write(t, src, "f.txt", "v2\n")
	commit(t, src, "two")
	require.NoError(t, r.Fetch())

	require.NoError(t, r.Advance())

	head, err := r.Head()
	require.NoError(t, err)
	assert.Equal(t, git(t, src, "rev-parse", "HEAD"), head)
	got, err := os.ReadFile(filepath.Join(r.Dir, "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(got), "the checkout follows")
}

func TestRepo_fetchPRFetchesHeadAndFollowsForcePushes(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	write(t, src, "f.txt", "v1\n")
	commit(t, src, "one")
	git(t, src, "checkout", "-q", "-b", "pr")
	write(t, src, "f.txt", "pr1\n")
	commit(t, src, "pr first")
	first := git(t, src, "rev-parse", "HEAD")
	git(t, src, "update-ref", "refs/pull/7/head", first)
	git(t, src, "checkout", "-q", "main")
	r, err := gitx.Clone("file://"+src, filepath.Join(t.TempDir(), "clone"))
	require.NoError(t, err)

	got, err := r.FetchPR(7)
	require.NoError(t, err)
	assert.Equal(t, first, got)
	assert.Equal(t, first, git(t, r.Dir, "rev-parse", "refs/codemesh/pr/7"))

	git(t, src, "checkout", "-q", "-B", "pr", "main")
	write(t, src, "f.txt", "rewritten\n")
	commit(t, src, "pr rewritten")
	second := git(t, src, "rev-parse", "HEAD")
	git(t, src, "update-ref", "refs/pull/7/head", second)

	got, err = r.FetchPR(7)
	require.NoError(t, err)
	assert.Equal(t, second, got)
}

func TestRepo_fetchPRErrorsOnUnknownPR(t *testing.T) {
	t.Parallel()
	src := newRepo(t)
	write(t, src, "f.txt", "v1\n")
	commit(t, src, "one")
	r, err := gitx.Clone("file://"+src, filepath.Join(t.TempDir(), "clone"))
	require.NoError(t, err)

	_, err = r.FetchPR(99)
	assert.Error(t, err)
}

func TestRepo_resolveRef(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	commit(t, dir, "one")
	r := open(t, dir)

	got, err := r.ResolveRef("main")
	require.NoError(t, err)
	assert.Equal(t, git(t, dir, "rev-parse", "HEAD"), got)

	_, err = r.ResolveRef("nope")
	assert.Error(t, err)
}

func TestRepo_diffRefsComparesTwoCommitsOnly(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "1\n2\n3\n")
	write(t, dir, "gone.txt", "x\n")
	commit(t, dir, "one")
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "a.txt", "1\nTWO\n3\n")
	write(t, dir, "new.txt", "n\n")
	git(t, dir, "rm", "-q", "gone.txt")
	commit(t, dir, "two")
	head := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "a.txt", "dirty\n")
	write(t, dir, "untracked.txt", "u\n")

	fds, err := open(t, dir).DiffRefs(base, head)
	require.NoError(t, err)

	got := map[string]gitx.FileDiff{}
	for _, fd := range fds {
		got[cmp.Or(fd.NewPath, fd.OldPath)] = fd
	}
	require.Len(t, got, 3)
	assert.Equal(t, []span{{2, 1, 2, 1}}, spans(got["a.txt"].Hunks))
	assert.Equal(t, gitx.Added, got["new.txt"].Status)
	assert.Equal(t, gitx.Deleted, got["gone.txt"].Status)
}

func TestRepo_churnAtCountsCommitsUpToRev(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	for i, v := range []string{"1", "2", "3"} {
		write(t, dir, "f.txt", v+"\n")
		commit(t, dir, "c"+strconv.Itoa(i))
	}
	r := open(t, dir)
	second := git(t, dir, "rev-parse", "HEAD~1")

	got, err := r.ChurnAt(second, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"f.txt": 2}, got)

	head, err := r.Churn(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"f.txt": 3}, head)
}

func TestRepo_churnAtErrorsOnUnknownRev(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "1\n")
	commit(t, dir, "one")

	_, err := open(t, dir).ChurnAt("nope", time.Hour)
	assert.Error(t, err)
}

func TestRepo_worktreeAtIsNamedBySHAAndIdempotent(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "v1\n")
	commit(t, dir, "one")
	sha := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "f.txt", "v2\n")
	commit(t, dir, "two")
	r := open(t, dir)
	root := filepath.Join(t.TempDir(), "worktrees", "repo-abc123")

	wt, err := r.WorktreeAt(root, "HEAD~1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, sha), wt)
	got, err := os.ReadFile(filepath.Join(wt, "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got))

	again, err := r.WorktreeAt(root, sha)
	require.NoError(t, err)
	assert.Equal(t, wt, again)
	assert.Len(t, strings.Split(git(t, dir, "worktree", "list"), "\n"), 2, "main plus one, not two")
}

func TestRepo_worktreeAtRecreatesAWorktreeWhoseDirectoryVanished(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "v1\n")
	commit(t, dir, "one")
	r := open(t, dir)
	root := t.TempDir()
	wt, err := r.WorktreeAt(root, "HEAD")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(wt))

	again, err := r.WorktreeAt(root, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, wt, again)
	assert.FileExists(t, filepath.Join(wt, "f.txt"))
}

func TestRepo_worktreeAtErrorsOnUnknownRev(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "v1\n")
	commit(t, dir, "one")
	root := t.TempDir()

	_, err := open(t, dir).WorktreeAt(root, "nope")
	assert.Error(t, err)
	entries, _ := os.ReadDir(root)
	assert.Empty(t, entries)
}

func TestRepo_removeWorktree(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "f.txt", "v1\n")
	commit(t, dir, "one")
	r := open(t, dir)
	wt, err := r.WorktreeAt(t.TempDir(), "HEAD")
	require.NoError(t, err)

	require.NoError(t, r.RemoveWorktree(wt))
	assert.NoDirExists(t, wt)
	assert.Len(t, strings.Split(git(t, dir, "worktree", "list"), "\n"), 1)
}

func TestRepo_gcWorktreesKeepsNewestAndThoseInUse(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	var shas []string
	for _, v := range []string{"1", "2", "3", "4"} {
		write(t, dir, "f.txt", v+"\n")
		commit(t, dir, "c"+v)
		shas = append(shas, git(t, dir, "rev-parse", "HEAD"))
	}
	r := open(t, dir)
	root := t.TempDir()
	now := time.Now()
	for i, sha := range shas {
		wt, err := r.WorktreeAt(root, sha)
		require.NoError(t, err)
		require.NoError(t, os.Chtimes(wt, now, now.Add(time.Duration(i)*time.Minute)))
	}

	require.NoError(t, r.GCWorktrees(root, 1, map[string]bool{shas[0]: true}))

	assert.DirExists(t, filepath.Join(root, shas[3]), "newest")
	assert.DirExists(t, filepath.Join(root, shas[0]), "in use, though oldest")
	assert.NoDirExists(t, filepath.Join(root, shas[1]))
	assert.NoDirExists(t, filepath.Join(root, shas[2]))
	assert.Len(t, strings.Split(git(t, dir, "worktree", "list"), "\n"), 3)
}

// span mirrors gitx's unexported hunk, so tests can spell expected ranges.
type span struct{ OldStart, OldLines, NewStart, NewLines int }

func spans[H ~struct{ OldStart, OldLines, NewStart, NewLines int }](hs []H) []span {
	var out []span
	for _, h := range hs {
		out = append(out, span(h))
	}
	return out
}

func TestRepo_filesListsTheTreeOfARevWithoutCheckingItOut(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "a.txt", "a\n")
	write(t, dir, "sub/b c.txt", "b\n")
	commit(t, dir, "one")
	sha := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "later.txt", "x\n")
	commit(t, dir, "two")

	got, err := open(t, dir).Files(sha)

	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt", "sub/b c.txt"}, got, "the files of that commit, not the working tree's")
}
