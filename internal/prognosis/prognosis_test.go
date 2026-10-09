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

func TestPrompt_asksForTheWholeFixCheckedByCodemesh(t *testing.T) {
	gs := sick(t)
	g := gs["hotspot:"+mod+".Tangled"]

	p := g.Prompt(prognosis.Brief{Check: "./ci.sh", Where: "the repository root", List: "/bin/codemesh prognoses /wt", Nearby: []prognosis.Prognosis{gs["complex:"+mod+".Knotty"]}})
	assert.Contains(t, p, "move code to other files of the package or to new files", "restructuring is in scope")
	assert.NotContains(t, p, "Touch only what this problem needs", "a narrow scope produced token fixes")
	assert.Contains(t, p, "Run `/bin/codemesh prognoses /wt`. hotspot:"+mod+".Tangled must be gone", "the agent checks against codemesh's verdict")
	assert.Contains(t, p, "complex:"+mod+".Knotty", "the problems around it are in view")
	assert.Contains(t, p, "Run `./ci.sh` from the repository root")
	assert.Contains(t, p, "Do not commit and do not push.")
	assert.Contains(t, p, g.Do[0])
}

func TestNearby_isTheRestOfTheFile(t *testing.T) {
	var all []prognosis.Prognosis
	for _, g := range sick(t) {
		all = append(all, g)
	}
	g := sick(t)["hotspot:"+mod+".Tangled"]
	keys := map[string]bool{}
	for _, o := range prognosis.Nearby(all, g) {
		keys[o.Key] = true
	}
	assert.True(t, keys["untested:"+mod+".Tangled"], "its own missing tests, in busy.go")
	assert.False(t, keys["complex:"+mod+".Knotty"], "quiet.go is another file")
	assert.False(t, keys[g.Key], "not itself")
}

func TestFind_flagsAnImportedPackageThatExportsMostOfWhatItHolds(t *testing.T) {
	s, err := code.Load("testdata/wide")
	require.NoError(t, err)
	gs := map[string]prognosis.Prognosis{}
	for _, g := range prognosis.Find(s, smell.Find(s)) {
		gs[g.Key] = g
	}

	g, ok := gs["wide:example.com/wide/api"]
	require.True(t, ok, "api exports 7 of its 8 declarations")
	assert.NotEqual(t, "Open", g.Related[0].Name, "names no other package uses come first")
	assert.Equal(t, "Open", g.Related[len(g.Related)-1].Name)
	assert.NotContains(t, gs, "wide:example.com/wide/pub", "nothing imports pub, so its exports serve outsiders")
}
