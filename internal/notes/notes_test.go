package notes_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/notes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_roundTripsResolvesAndDeletes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "repos", "r", "notes.json")
	s, err := notes.Open(path)
	require.NoError(t, err)
	assert.Empty(t, s.All())

	n, err := s.Add(notes.Note{
		Repo: "r", Scope: "wt", File: "a.go", DeclID: "m.f", UnitKey: "m.f@abc",
		Side: "+", Line: 12, Body: "check the error path",
		Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, n.ID)
	second, err := s.Add(notes.Note{Body: "other"})
	require.NoError(t, err)
	assert.NotEqual(t, n.ID, second.ID)

	require.NoError(t, s.Resolve(n.ID, true))
	n.Resolved = true

	re, err := notes.Open(path)
	require.NoError(t, err)
	assert.Equal(t, []notes.Note{n, second}, re.All(), "insertion order survives the file")

	require.NoError(t, re.Delete(n.ID))
	assert.Equal(t, []notes.Note{second}, re.All())
	assert.Error(t, re.Resolve("nope", true))
}

func TestOutdated_isTrueWhenTheUnitKeyLeftTheReview(t *testing.T) {
	t.Parallel()
	keys := map[string]bool{"m.f@abc": true}

	assert.False(t, notes.Outdated(notes.Note{UnitKey: "m.f@abc"}, keys))
	assert.True(t, notes.Outdated(notes.Note{UnitKey: "m.f@def"}, keys), "source changed, so the key did")
	assert.False(t, notes.Outdated(notes.Note{}, keys), "a note on no unit cannot go stale")
}
