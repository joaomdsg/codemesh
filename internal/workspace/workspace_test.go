package workspace_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/joaomdsg/codemesh/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// open points every XDG directory at a temp dir and opens a workspace there.
func open(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	w, err := workspace.Open()
	require.NoError(t, err)
	return w, root
}

func repos(t *testing.T) []workspace.Repo {
	t.Helper()
	w, err := workspace.Open()
	require.NoError(t, err)
	got, err := w.Repos()
	require.NoError(t, err)
	return got
}

// bare makes a bare origin with one commit and returns its file:// URL.
func bare(t *testing.T) (url, dir string) {
	t.Helper()
	src := testrepo.New(t, map[string]string{"f.txt": "v1\n"}, nil)
	dir = filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, testrepo.Git(src, "clone", "-q", "--bare", src, dir))
	require.NoError(t, testrepo.Git(dir, "config", "uploadpack.allowFilter", "true"))
	return "file://" + dir, dir
}

func TestAdd_pathPersistsAndReloads(t *testing.T) {
	w, _ := open(t)
	dir := testrepo.New(t, map[string]string{"a.go": "package a\n"}, nil)

	r, err := w.Add(dir)
	require.NoError(t, err)

	assert.Regexp(t, regexp.MustCompile(`^`+regexp.QuoteMeta(filepath.Base(dir))+`-[0-9a-f]{6}$`), r.ID)
	assert.Equal(t, filepath.Base(dir), r.Name)
	assert.Equal(t, dir, r.Path)
	assert.Empty(t, r.URL)
	assert.Equal(t, dir, r.Dir())
	assert.Equal(t, []workspace.Repo{r}, repos(t))
}

func TestAdd_urlClonesIntoCacheAndReloads(t *testing.T) {
	w, root := open(t)
	url, origin := bare(t)

	r, err := w.Add(url)
	require.NoError(t, err)

	assert.Equal(t, url, r.URL)
	assert.Empty(t, r.Path)
	assert.Equal(t, "origin", r.Name)
	assert.True(t, filepath.IsLocal(mustRel(t, filepath.Join(root, "cache", "codemesh", "clones"), r.ClonePath)), r.ClonePath)
	assert.Equal(t, r.ClonePath, r.Dir())
	got, err := os.ReadFile(filepath.Join(r.Dir(), "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got))
	assert.Equal(t, []workspace.Repo{r}, repos(t))
	assert.DirExists(t, origin, "the origin is untouched")
}

func mustRel(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	require.NoError(t, err)
	return rel
}

func TestAdd_duplicateIsNoOp(t *testing.T) {
	w, _ := open(t)
	dir := testrepo.New(t, map[string]string{"a.go": "package a\n"}, nil)
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	url, origin := bare(t)

	first, err := w.Add(dir)
	require.NoError(t, err)
	again, err := w.Add(sub) // a subdirectory is the same repository
	require.NoError(t, err)
	assert.Equal(t, first, again)

	cloned, err := w.Add(url)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(origin)) // a second clone would now fail
	cloneAgain, err := w.Add(url)
	require.NoError(t, err)
	assert.Equal(t, cloned, cloneAgain)

	assert.Equal(t, []workspace.Repo{first, cloned}, repos(t))
}

func TestAdd_anotherSpellingOfAClonedURLIsTheSameRepo(t *testing.T) {
	w, _ := open(t)
	url, origin := bare(t)
	first, err := w.Add(url)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(origin)) // a second clone would now fail

	again, err := w.Add(url + "/")

	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.Len(t, repos(t), 1)
}

func TestAdd_urlsWithParentElementsAreRefused(t *testing.T) {
	w, root := open(t)

	for _, arg := range []string{"https://127.0.0.1:1/o/..", "https://127.0.0.1:1/../r/..", "https://..:1/o/r"} {
		_, err := w.Add(arg)
		assert.ErrorContains(t, err, "not a usable git URL", arg)
	}

	assert.Empty(t, repos(t))
	assert.NoDirExists(t, filepath.Join(root, "cache", "codemesh", "clones"))
}

func TestRemove_neverDeletesOutsideTheClonesDir(t *testing.T) {
	w, root := open(t)
	victim := t.TempDir()
	reg := `{"version":1,"repos":[{"id":"x-000000","name":"x","url":"https://h/o/x","clonePath":` + strconv.Quote(victim) + `}]}`
	require.NoError(t, os.MkdirAll(filepath.Join(root, "config", "codemesh"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config", "codemesh", "workspace.json"), []byte(reg), 0o644))

	assert.Error(t, w.Remove("x-000000"))

	assert.DirExists(t, victim)
}

func TestAdd_sameNameDifferentPathGetsDifferentIDs(t *testing.T) {
	w, _ := open(t)
	a := filepath.Join(t.TempDir(), "app")
	b := filepath.Join(t.TempDir(), "app")
	for _, d := range []string{a, b} {
		require.NoError(t, os.Mkdir(d, 0o755))
		require.NoError(t, testrepo.Git(d, "init", "-q"))
	}

	ra, err := w.Add(a)
	require.NoError(t, err)
	rb, err := w.Add(b)
	require.NoError(t, err)

	assert.NotEqual(t, ra.ID, rb.ID)
	assert.Len(t, repos(t), 2)
}

func TestAdd_failuresRegisterNothing(t *testing.T) {
	w, _ := open(t)

	_, err := w.Add(t.TempDir()) // not a git repository
	assert.Error(t, err)
	_, err = w.Add("file://" + filepath.Join(t.TempDir(), "missing.git"))
	assert.Error(t, err)

	assert.Empty(t, repos(t))
}

func TestRemove_forgetsBothKindsAndDeletesOnlyTheClone(t *testing.T) {
	w, _ := open(t)
	dir := testrepo.New(t, map[string]string{"a.go": "package a\n"}, nil)
	url, _ := bare(t)
	local, err := w.Add(dir)
	require.NoError(t, err)
	cloned, err := w.Add(url)
	require.NoError(t, err)

	require.NoError(t, w.Remove(local.ID))
	require.NoError(t, w.Remove(cloned.ID))
	require.NoError(t, w.Remove(cloned.ID), "removing a missing repo is a no-op")

	assert.Empty(t, repos(t))
	assert.DirExists(t, dir, "the user's checkout stays")
	assert.NoDirExists(t, cloned.ClonePath)
}

func TestAdd_concurrentAddsOfOneRepoRegisterOnce(t *testing.T) {
	w, _ := open(t)
	dir := testrepo.New(t, map[string]string{"a.go": "package a\n"}, nil)

	done := make(chan error, 8)
	for range cap(done) {
		go func() { _, err := w.Add(dir); done <- err }()
	}
	for range cap(done) {
		require.NoError(t, <-done)
	}

	assert.Len(t, repos(t), 1)
}
