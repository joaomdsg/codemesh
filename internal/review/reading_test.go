package review_test

import (
	"path/filepath"
	"testing"

	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReview_SharedName_ignoresCaseAndSpotsOnlyRepeats(t *testing.T) {
	t.Parallel()
	a, b, c := &review.Unit{Name: "Build"}, &review.Unit{Name: "build"}, &review.Unit{Name: "Load"}
	shared := (&review.Review{Units: []*review.Unit{a, b, c}}).SharedName()

	assert.True(t, shared(a))
	assert.True(t, shared(b))
	assert.False(t, shared(c))
}

func TestReview_Section_holdsOneLanesUnitsAndHowManyAreReviewed(t *testing.T) {
	t.Parallel()
	s, err := review.OpenState(filepath.Join(t.TempDir(), "reviewed.json"))
	require.NoError(t, err)
	require.NoError(t, s.Set("a@1", true))
	a, b := &review.Unit{Key: "a@1", Lane: review.Contract}, &review.Unit{Key: "b@1", Lane: review.Contract}
	c := &review.Unit{Key: "c@1", Lane: review.Noise}
	rev := &review.Review{Units: []*review.Unit{c, a, b}}

	assert.Equal(t, review.Section{Lane: review.Contract, Units: []*review.Unit{a, b}, Done: 1}, rev.Section(review.Contract, s))
	assert.Equal(t, review.Section{Lane: review.Noise, Units: []*review.Unit{c}}, rev.Section(review.Noise, s))
	assert.Empty(t, rev.Section(review.Logic, s).Units)
}

func TestState_NeedsReading_skipsReviewedUnitsAndLanesAfterLogic(t *testing.T) {
	t.Parallel()
	s, err := review.OpenState(filepath.Join(t.TempDir(), "reviewed.json"))
	require.NoError(t, err)
	require.NoError(t, s.Set("done@1", true))
	fresh, done := &review.Unit{Key: "fresh@1"}, &review.Unit{Key: "done@1"}

	assert.True(t, s.NeedsReading(fresh, review.Contract))
	assert.True(t, s.NeedsReading(fresh, review.Logic))
	assert.False(t, s.NeedsReading(fresh, review.Tests), "test code rarely needs reading")
	assert.False(t, s.NeedsReading(fresh, review.Noise))
	assert.False(t, s.NeedsReading(done, review.Logic), "a reviewed unit is settled")
}
