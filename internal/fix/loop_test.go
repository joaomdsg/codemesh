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
		{2, 3, 4, false, false, false, undone},
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
	worse  = `printf 'package m\n\nfunc C(a, b, c, d, e, f int) int { return a }\n\nfunc D(a, b, c, d, e, f int) int { return a }\n' > c.go`
)

func loopRun(t *testing.T, agent string) Snapshot {
	t.Helper()
	return loopStart(t, agent).Snapshot()
}

func loopStart(t *testing.T, agent string) *Run {
	t.Helper()
	r := loopBegin(t, agent, nil)
	require.Eventually(t, func() bool { s := r.Snapshot(); return !s.Live() }, 2*time.Minute, 50*time.Millisecond)
	return r
}

// loopBegin starts a run on a small module with extra files and returns
// without waiting for it.
func loopBegin(t *testing.T, agent string, extra map[string]string) *Run {
	t.Helper()
	files := map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n\nfunc A() int { return 1 }\n"}
	for k, v := range extra {
		files[k] = v
	}
	repo := testrepo.New(t, files, nil)
	rs := NewRuns(repo)
	rs.Agent = agent
	t.Cleanup(rs.Close)
	return rs.Start(prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"})
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

func TestRun_undoesARoundThatLeavesMore(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, worse)
	r := loopStart(t, agent)
	s := r.Snapshot()

	assert.Equal(t, Done, s.State)
	assert.Equal(t, []int{1, 2}, s.rounds)
	assert.Equal(t, undone, s.ended)
	require.NotNil(t, s.After)
	assert.Nil(t, s.After.Snap.Decl("m.D"), "the result is round 1's")
	c, err := os.ReadFile(filepath.Join(r.mod, "c.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(c), "func D", "and so is the worktree a pull request would commit")
	assert.Len(t, s.Review.Introduced, 1)
	assert.Equal(t, "2 rounds, left after each: 1 → 2. Round 2 left more, so its changes were undone.", s.RoundsNote())
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
	assert.Equal(t, "2 rounds, left after each: 2 → 2. Stopped: as many left as the round before.",
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

func TestRun_stopsAtTheRoundCap(t *testing.T) {
	t.Parallel()
	// Each round leaves one function with too many parameters fewer: five,
	// then four, down to one when the cap ends it.
	calls := t.TempDir()
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\n"+
		`echo '{"type":"system","subtype":"init","session_id":"s1","model":"m","claude_code_version":"t"}'`+"\n"+
		"n=$(( $(cat "+calls+"/n 2>/dev/null || echo 0) + 1 )); echo $n > "+calls+"/n\n"+
		"printf 'package m\\n' > c.go\n"+
		"i=$n; while [ $i -le 5 ]; do printf 'func C%d(a, b, c, d, e, f int) int { return a }\\n' $i >> c.go; i=$((i+1)); done\n"), 0o755))
	s := loopRun(t, agent)

	assert.Equal(t, []int{5, 4, 3, 2, 1}, s.rounds)
	assert.Equal(t, capped, s.ended)
}

func TestRun_stopInALaterRoundKeepsWhatItHas(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, "exec sleep 30")
	r := loopBegin(t, agent, nil)
	require.Eventually(t, func() bool { s := r.Snapshot(); return s.Round == 2 && s.State == Working }, time.Minute, 20*time.Millisecond)
	r.Stop()
	// A stopped run still analyses what it has.
	require.Eventually(t, func() bool { return r.Snapshot().After != nil }, time.Minute, 20*time.Millisecond)

	s := r.Snapshot()
	assert.Equal(t, Stopped, s.State)
	assert.Equal(t, []int{1, 1}, s.rounds)
	assert.Empty(t, s.ended)
	require.NotNil(t, s.After, "what Claude changed so far is analysed")
}

func TestRun_stopWhileCheckingEndsTheLoop(t *testing.T) {
	t.Parallel()
	agent, calls := loopAgent(t, true, fixes)
	r := loopBegin(t, agent, map[string]string{"ci.sh": "#!/bin/sh\nsleep 2\n"})
	require.Eventually(t, func() bool { return r.Snapshot().State == Checking }, time.Minute, 20*time.Millisecond)
	r.Stop()
	require.Eventually(t, func() bool { return !r.Snapshot().Live() }, time.Minute, 20*time.Millisecond)

	s := r.Snapshot()
	assert.Equal(t, Done, s.State, "the round finished; its result can still become a pull request")
	assert.Equal(t, []int{1}, s.rounds)
	assert.NoFileExists(t, filepath.Join(calls, "followup"), "Claude does not go again")
	assert.Equal(t, "Stopped, so Claude does not go again.", s.Events[len(s.Events)-1].Text)
}
