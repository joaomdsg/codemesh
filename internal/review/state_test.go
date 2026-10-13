package review_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_remembersReviewedKeysAcrossOpens(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "codemesh", "reviewed.json")

	s, err := review.OpenState(path)
	require.NoError(t, err)
	assert.False(t, s.Reviewed("a@1"))
	require.NoError(t, s.Set("a@1", true))
	require.NoError(t, s.Set("b@1", true))
	require.NoError(t, s.Set("b@1", false))

	again, err := review.OpenState(path)
	require.NoError(t, err)
	assert.True(t, again.Reviewed("a@1"))
	assert.False(t, again.Reviewed("b@1"))
	assert.False(t, again.Reviewed("a@2"), "a new source hash is a new key")
}

func TestOpenStateFrom_seedsFromTheLegacyFileOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy, path := filepath.Join(dir, "legacy", "reviewed.json"), filepath.Join(dir, "state", "reviewed.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.WriteFile(legacy, []byte(`["a@1","b@1"]`), 0o644))

	s, err := review.OpenStateFrom(path, legacy)
	require.NoError(t, err)
	assert.True(t, s.Reviewed("a@1"))
	require.NoError(t, s.Set("b@1", false))

	again, err := review.OpenStateFrom(path, legacy)
	require.NoError(t, err)
	assert.True(t, again.Reviewed("a@1"))
	assert.False(t, again.Reviewed("b@1"), "the new file wins once it exists")
}
