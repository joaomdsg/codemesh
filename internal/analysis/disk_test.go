package analysis

import (
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func saved(basis string) *Saved {
	return &Saved{Basis: basis, Snap: code.NewBuilder(code.Go, "calc", "/x").Finish()}
}

func TestDisk_pruneKeepsTheCommitSnapshotsReadLast(t *testing.T) {
	d := &Disk{dir: t.TempDir(), version: "v"}
	key := func(rev string) Key { return Key{Repo: "calc", Rev: rev, Root: "."} }
	old := time.Now().Add(-time.Hour)
	for i, rev := range []string{"c1", "c2", "c3", "c4"} {
		require.NoError(t, d.Save(key(rev), saved(rev)))
		at := old.Add(time.Duration(i) * time.Minute)
		require.NoError(t, os.Chtimes(d.path(key(rev)), at, at))
	}
	require.NoError(t, d.Save(key(Worktree), saved("wt")))

	_, err := d.Load(key("c1"))
	require.NoError(t, err)
	require.NoError(t, d.Save(key("c5"), saved("c5")))

	for _, rev := range []string{"c1", "c3", "c4", "c5", Worktree} {
		_, err := d.Load(key(rev))
		assert.NoError(t, err, rev)
	}
	_, err = d.Load(key("c2"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "the snapshot read longest ago goes")
}

func TestDisk_aSnapshotOfAnotherOrAnUnknownBuildReadsAsNone(t *testing.T) {
	dir := t.TempDir()
	k := Key{Repo: "calc", Rev: Worktree, Root: "."}
	require.NoError(t, (&Disk{dir: dir, version: "a"}).Save(k, saved("x")))

	_, err := (&Disk{dir: dir, version: "b"}).Load(k)
	assert.ErrorIs(t, err, fs.ErrNotExist)

	unknown := &Disk{dir: t.TempDir(), version: ""}
	require.NoError(t, unknown.Save(k, saved("x")))
	_, err = unknown.Load(k)
	assert.ErrorIs(t, err, fs.ErrNotExist, "a build that cannot name itself trusts no snapshot")
}

func TestDisk_nestedRootsKeepTheirOwnFiles(t *testing.T) {
	d := &Disk{dir: t.TempDir(), version: "v"}
	for _, root := range []string{"a/b", "a%2Fb", "a"} {
		require.NoError(t, d.Save(Key{Repo: "calc", Rev: Worktree, Root: root}, saved(root)))
	}
	for _, root := range []string{"a/b", "a%2Fb", "a"} {
		s, err := d.Load(Key{Repo: "calc", Rev: Worktree, Root: root})
		require.NoError(t, err)
		assert.Equal(t, root, s.Basis)
	}
}

func TestDisk_forgetRemovesTheReposSnapshots(t *testing.T) {
	d := &Disk{dir: t.TempDir(), version: "v"}
	k := Key{Repo: "calc", Rev: Worktree, Root: "."}
	other := Key{Repo: "other", Rev: Worktree, Root: "."}
	require.NoError(t, d.Save(k, saved("x")))
	require.NoError(t, d.Save(other, saved("y")))

	require.NoError(t, d.Forget("calc"))
	_, err := d.Load(k)
	assert.ErrorIs(t, err, fs.ErrNotExist)
	_, err = d.Load(other)
	assert.NoError(t, err)
}
