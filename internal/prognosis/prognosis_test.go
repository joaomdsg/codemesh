package prognosis_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mod = "example.com/sick"

// sick loads the fixture with busy.go marked as the file that keeps changing.
func sick(t *testing.T) map[string]prognosis.Prognosis {
	t.Helper()
	s, err := code.Load("testdata/sick")
	require.NoError(t, err)
	for _, f := range s.Package(mod).Files {
		if f.Path == "busy.go" {
			f.Churn = 9
		}
	}
	out := map[string]prognosis.Prognosis{}
	for _, g := range prognosis.Find(s, smell.Find(s)) {
		out[g.Key] = g
	}
	return out
}

func TestFind_callsComplexCodeInABusyFileAHotspotAndQuietComplexCodeLessUrgent(t *testing.T) {
	gs := sick(t)

	hot, ok := gs["hotspot:"+mod+".Tangled"]
	require.True(t, ok, "Tangled is complex and its file keeps changing")
	assert.Equal(t, prognosis.Level(2), hot.Level, "complexity 17 is past the hot limit, short of the high one")

	quiet, ok := gs["complex:"+mod+".Knotty"]
	require.True(t, ok, "Knotty is complex in a file nobody changes")
	assert.Equal(t, prognosis.Level(1), quiet.Level)
}

func TestFind_flagsComplexCodeOnlyWhenNoTestCallsIt(t *testing.T) {
	gs := sick(t)

	assert.Contains(t, gs, "untested:"+mod+".Tangled")
	assert.NotContains(t, gs, "untested:"+mod+".Knotty", "TestKnotty calls it")
}

func TestFind_groupsAPackagesLongParameterListsIntoOneProblem(t *testing.T) {
	gs := sick(t)

	g, ok := gs["params:"+mod]
	require.True(t, ok)
	assert.Equal(t, mod+".Eight", g.Target, "the longest list stands for the group")
	assert.Len(t, g.Related, 2)
	assert.NotContains(t, gs, "params:"+mod+".Six", "grouped, not listed one by one")
}

func TestPrompt_forbidsCommittingAndPushing(t *testing.T) {
	g := sick(t)["hotspot:"+mod+".Tangled"]

	p := g.Prompt("./ci.sh", "the repository root")
	assert.Contains(t, p, "Do not commit and do not push.")
	assert.Contains(t, p, "Run `./ci.sh` from the repository root", "the repo's own gate, not a narrower one")
	assert.Contains(t, p, "busy.go:")
	assert.Contains(t, p, g.Do[0])
}
