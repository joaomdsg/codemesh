package store_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/joaomdsg/codemesh/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doc struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

func TestJSON_roundTripsAndCreatesParents(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a", "b", "doc.json")

	require.NoError(t, store.WriteJSON(path, doc{"x", 7}))

	var got doc
	require.NoError(t, store.ReadJSON(path, &got))
	assert.Equal(t, doc{"x", 7}, got)
}

func TestReadJSON_missingFileIsErrNotExist(t *testing.T) {
	t.Parallel()
	var got doc
	err := store.ReadJSON(filepath.Join(t.TempDir(), "none.json"), &got)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestWriteJSON_failedWriteLeavesOldFileAndNoTemp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.json")
	require.NoError(t, store.WriteJSON(path, doc{"old", 1}))

	// A channel cannot be encoded, so the write dies before it finishes.
	require.Error(t, store.WriteJSON(path, make(chan int)))

	var got doc
	require.NoError(t, store.ReadJSON(path, &got))
	assert.Equal(t, doc{"old", 1}, got, "the old contents survive")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file is left behind")
}

func TestWriteJSON_failedRenameLeavesNoTemp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.json")
	require.NoError(t, os.Mkdir(path, 0o755)) // rename onto a directory fails

	require.Error(t, store.WriteJSON(path, doc{"x", 1}))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "only the directory remains")
}

func TestWriteJSON_concurrentWritersLeaveWholeJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "doc.json")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, store.WriteJSON(path, doc{"w", i}))
		}()
	}
	wg.Wait()

	var got doc
	require.NoError(t, store.ReadJSON(path, &got))
	assert.Equal(t, "w", got.Name)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}
