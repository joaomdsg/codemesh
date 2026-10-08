package review_test

import (
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
