package prognosis_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeats_bucketsEachLensZeroToFive(t *testing.T) {
	t.Parallel()
	s, err := code.Load("testdata/sick")
	require.NoError(t, err)
	decl := func(name string) *code.Decl {
		d := s.Decl(mod + "." + name)
		require.NotNil(t, d, name)
		return d
	}
	const busy = 2

	// Order: health, reach, structure, tests.
	assert.Equal(t, [4]int{3, 0, 3, 3}, prognosis.Heats(s, decl("Tangled"), 0, busy, 0.5), "complexity 16, quiet file, no test calls it")
	assert.Equal(t, [4]int{4, 0, 3, 3}, prognosis.Heats(s, decl("Tangled"), busy, busy, 0.5), "a busy file adds one to health")
	assert.Equal(t, [4]int{3, 0, 5, 0}, prognosis.Heats(s, decl("Knotty"), 0, busy, 1), "a test calls Knotty; instability 1 is the hottest structure")
	assert.Equal(t, [4]int{0, 1, 1, 3}, prognosis.Heats(s, decl("Six"), 0, busy, 0), "simple, one caller, but six parameters")
}
