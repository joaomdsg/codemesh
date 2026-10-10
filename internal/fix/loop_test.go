package fix

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextRound_stopsCleanAtTheCapOrWhenStuck(t *testing.T) {
	for _, c := range []struct {
		round, prev, left int
		stopped, broke    bool
		more              bool
		ended             string
	}{
		{1, 0, 0, false, false, false, clean},
		{1, 0, 3, false, false, true, ""},
		{2, 3, 2, false, false, true, ""},
		{2, 3, 3, false, false, false, stalled},
		{2, 3, 4, false, false, false, stalled},
		{maxRounds, 5, 1, false, false, false, capped},
		{1, 0, 3, true, false, false, ""},
		{2, 3, 2, false, true, false, failed},
	} {
		more, ended := nextRound(c.round, c.prev, c.left, c.stopped, c.broke)
		assert.Equal(t, c.more, more, "%+v", c)
		assert.Equal(t, c.ended, ended, "%+v", c)
	}
}

// loopAgent writes a function with too many parameters into c.go; resumed,
// it records the prompt and does what resumed says.
func loopAgent(t *testing.T, init bool, resumed string) (agent, calls string) {
	t.Helper()
	calls = t.TempDir()
	agent = filepath.Join(t.TempDir(), "agent")
	start := ""
	if init {
		start = `echo '{"type":"system","subtype":"init","session_id":"s1","model":"m","claude_code_version":"t"}'` + "\n"
	}
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\n"+start+
		"echo x >> "+calls+"/runs\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t'$$'","name":"Bash","input":{"command":"true"}}]}}'`+"\n"+
		`case "$*" in *"--resume s1"*)`+"\n"+
		`  printf '%s' "$2" > `+calls+"/followup\n"+
		"  "+resumed+" ;;\n"+
		`*) printf 'package m\n\nfunc C(a, b, c, d, e, f int) int { return a }\n' > c.go ;;`+"\n"+
		"esac\n"), 0o755))
	return agent, calls
}

const (
	fixes  = `printf 'package m\n\nfunc C(a int) int { return a }\n' > c.go`
	stuck  = "true"
	breaks = "exit 1"
)

func loopRun(t *testing.T, agent string) Snapshot {
	t.Helper()
	repo := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n\nfunc A() int { return 1 }\n"}, nil)
	rs := NewRuns(repo)
	rs.Agent = agent
	t.Cleanup(rs.Close)
	r := rs.Start(prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"})
	require.Eventually(t, func() bool { s := r.Snapshot(); return s.State == Done || s.State == Failed }, 2*time.Minute, 50*time.Millisecond)
	return r.Snapshot()
}

func TestRun_feedsBackWhatTheChangeLeftUntilItIsClean(t *testing.T) {
	t.Parallel()
	agent, calls := loopAgent(t, true, fixes)
	s := loopRun(t, agent)

	require.NoError(t, s.Err)
	assert.Equal(t, []int{1, 0}, s.rounds)
	assert.Equal(t, clean, s.ended)
	followup, err := os.ReadFile(filepath.Join(calls, "followup"))
	require.NoError(t, err, "the second round resumes Claude's session")
	assert.Contains(t, string(followup), "many-params C", "it is told what the first round left")
	assert.Empty(t, s.Review.Introduced)
	require.Len(t, s.Steps, 2)
	assert.Equal(t, []int{1, 2}, []int{s.Steps[0].Round, s.Steps[1].Round})
}

func TestRun_stopsWhenARoundMakesNoProgress(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, stuck)
	s := loopRun(t, agent)

	assert.Equal(t, []int{1, 1}, s.rounds)
	assert.Equal(t, stalled, s.ended)
}

func TestRun_keepsTheChangeWhenALaterRoundFails(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, breaks)
	s := loopRun(t, agent)

	assert.Equal(t, Done, s.State, "round 1's change is still there to read")
	assert.NoError(t, s.Err)
	require.NotNil(t, s.After)
	assert.Len(t, s.Review.Introduced, 1)
	assert.Equal(t, failed, s.ended)
	assert.Equal(t, "2 rounds, left after each: 1 → 1. Stopped: round 2 ended with an error.", s.RoundsNote())
}

func TestRun_endsAfterOneRoundWithoutASessionToResume(t *testing.T) {
	t.Parallel()
	agent, calls := loopAgent(t, false, fixes)
	s := loopRun(t, agent)

	assert.Equal(t, Done, s.State)
	assert.Equal(t, []int{1}, s.rounds)
	runs, _ := os.ReadFile(filepath.Join(calls, "runs"))
	assert.Equal(t, "x\n", string(runs), "no fresh session is started with the follow-up")
}

func TestSnapshot_roundsSayWhatEachLeftAndWhyTheLoopStopped(t *testing.T) {
	assert.Equal(t, "2 rounds, left after each: 2 → 2. Stopped: no fewer left than the round before.",
		Snapshot{rounds: []int{2, 2}, ended: stalled}.RoundsNote())
	assert.Equal(t, "2 rounds, left after each: 2 → 0.", Snapshot{rounds: []int{2, 0}, ended: clean}.RoundsNote())
	assert.Equal(t, "5 rounds, left after each: 5 → 4 → 3 → 2 → 1. Stopped at the 5-round limit.",
		Snapshot{rounds: []int{5, 4, 3, 2, 1}, ended: capped}.RoundsNote())
	assert.Empty(t, Snapshot{rounds: []int{0}, ended: clean}.RoundsNote())
	assert.Equal(t, "round 2 of up to 5", Snapshot{State: Working, Round: 2}.RoundsLabel())
	assert.Equal(t, "2 rounds", Snapshot{State: Done, rounds: []int{2, 2}}.RoundsLabel())
	assert.Empty(t, Snapshot{State: Done, rounds: []int{1}}.RoundsLabel())
}

func TestLeftover_namesTheProblemStillThereAndACheckTheChangeBroke(t *testing.T) {
	p := prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"}
	still := &Side{Prognoses: []prognosis.Prognosis{p}, Output: "FAIL m"}
	assert.Equal(t, []string{
		"The problem is still there: Complex function, A [complex:m.A]",
		"`go test` now fails. Its output ends:\nFAIL m",
	}, leftover(p, "go test", &Side{CheckOK: true}, still, &review.Review{}))
	assert.Equal(t, []string{"The problem is still there: Complex function, A [complex:m.A]"},
		leftover(p, "go test", &Side{}, still, &review.Review{}), "a check that failed at the start is not the run's to fix")
}
