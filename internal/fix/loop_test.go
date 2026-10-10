package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
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
		"  "+resumed+"\n"+
		`  echo '{"type":"result","result":"second"}' ;;`+"\n"+
		`*) printf 'package m\n\nfunc C(a, b, c, d, e, f int) int { return a }\n' > c.go`+"\n"+
		`  echo '{"type":"result","result":"first"}' ;;`+"\n"+
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
	if _, ok := extra["ci.sh"]; ok {
		// checkOf runs ci.sh only when it is executable.
		require.NoError(t, os.Chmod(filepath.Join(repo, "ci.sh"), 0o755))
		require.NoError(t, testrepo.Git(repo, "update-index", "--chmod=+x", "ci.sh"))
		require.NoError(t, testrepo.Git(repo, "commit", "--quiet", "--amend", "--no-edit"))
	}
	rs := NewRuns(repo)
	rs.Agent = agent
	t.Cleanup(rs.Close)
	return rs.Start(prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"})
}

// modOf reads the run's module directory, which prepare sets while the
// run goes.
func modOf(r *Run) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mod
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
	assert.Equal(t, "second", s.Summary)
	assert.False(t, s.CanPR(), "no origin to push to")
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
	assert.Equal(t, "first", s.Summary, "the summary of the round kept")
	assert.Equal(t, "Claude's summary", s.SummaryTitle())
	assert.Equal(t, 2, s.Undone)
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
	assert.Equal(t, "2 rounds, left after each: 1 → 1. Stopped: round 2 ended with an error: claude: exit status 1.", s.RoundsNote())
	assert.Equal(t, "first", s.Summary)
	assert.Equal(t, "Claude's summary, from round 1", s.SummaryTitle(), "round 2 left no summary of its own")
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
	assert.Equal(t, "2 rounds, left after each: 2 → 1. Stopped by you; Claude did not go again.",
		Snapshot{rounds: []int{2, 1}, ended: halted}.RoundsNote())
	assert.Equal(t, "2 rounds, left after each: 2 → 2 (stopped). Stopped by you during round 2.",
		Snapshot{rounds: []int{2, 2}, ended: cut}.RoundsNote())
	assert.Equal(t, "2 rounds, left after each: 2 → 2. Stopped: round 2 ended with an error: boom.",
		Snapshot{rounds: []int{2, 2}, ended: failed, roundErr: "boom\nmore"}.RoundsNote(), "the first line of the error")
	assert.Empty(t, Snapshot{rounds: []int{0}, ended: clean}.RoundsNote())
	assert.Equal(t, "round 2 of up to 5", Snapshot{State: Working, Round: 2}.RoundsLabel())
	assert.Equal(t, "2 rounds", Snapshot{State: Done, rounds: []int{2, 2}}.RoundsLabel())
	assert.Empty(t, Snapshot{State: Done, rounds: []int{1}}.RoundsLabel())
}

func TestLeftover_namesTheProblemStillThereAndACheckTheChangeBroke(t *testing.T) {
	p := prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"}
	still := &Side{Prognoses: []prognosis.Prognosis{p}, Output: "FAIL m"}
	list, total := leftover(p, "go test", &Side{CheckOK: true}, still, &review.Review{})
	assert.Equal(t, []string{
		"The problem is still there: Complex function, A [complex:m.A]",
		"`go test` now fails. Its output ends:\nFAIL m",
	}, list)
	assert.Equal(t, 2, total)
	list, _ = leftover(p, "go test", &Side{}, still, &review.Review{})
	assert.Equal(t, []string{"The problem is still there: Complex function, A [complex:m.A]"}, list, "a check that failed at the start is not the run's to fix")
}

func TestLeftover_countsEverySmellButListsAFew(t *testing.T) {
	rev := &review.Review{Introduced: make([]smell.Finding, maxListed+5)}
	list, total := leftover(prognosis.Prognosis{}, "go test", &Side{}, &Side{}, rev)
	assert.Len(t, list, maxListed+1)
	assert.Equal(t, "5 more new smells", list[maxListed])
	assert.Equal(t, maxListed+5, total)
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
	assert.Equal(t, cut, s.ended)
	assert.Equal(t, "2 rounds, left after each: 1 → 1 (stopped). Stopped by you during round 2.", s.RoundsNote())
	require.NotNil(t, s.After, "what Claude changed so far is analysed")
}

func TestRun_stopEndsWhatClaudeStarted(t *testing.T) {
	t.Parallel()
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\nsleep 30; sleep 30\n"), 0o755))
	r := loopBegin(t, agent, nil)
	require.Eventually(t, func() bool { return r.Snapshot().State == Working }, time.Minute, 20*time.Millisecond)
	stopped := time.Now()
	r.Stop()
	require.Eventually(t, func() bool { return r.Snapshot().After != nil }, time.Minute, 20*time.Millisecond)
	assert.Less(t, time.Since(stopped), 15*time.Second, "the agent's children do not hold the run")
}

func TestRun_stopWhilePreparingKeepsTheStartingCheck(t *testing.T) {
	t.Parallel()
	agent, calls := loopAgent(t, true, fixes)
	r := loopBegin(t, agent, map[string]string{"ci.sh": "#!/bin/sh\nsleep 1\n"})
	r.Stop()
	require.Eventually(t, func() bool { return r.Snapshot().After != nil }, time.Minute, 20*time.Millisecond)

	s := r.Snapshot()
	assert.Equal(t, Stopped, s.State)
	assert.True(t, s.Before.CheckOK, "a stop is not a failed check")
	assert.NoFileExists(t, filepath.Join(calls, "runs"), "Claude never starts")
}

// lock leaves an index.lock in the worktree's git directory, so the next git
// write there fails.
const lock = `touch "$(git rev-parse --git-dir)/index.lock"`

func TestRun_failsWhenARoundCannotBeKept(t *testing.T) {
	t.Parallel()
	agent, calls := loopAgent(t, true, fixes)
	first := `echo '{"type":"result","result":"first"}'`
	script, err := os.ReadFile(agent)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(agent, []byte(strings.Replace(string(script), first, lock+"; "+first, 1)), 0o755))
	s := loopRun(t, agent)

	assert.Equal(t, Failed, s.State)
	require.Error(t, s.Err)
	assert.Contains(t, s.Err.Error(), "index.lock")
	assert.NoFileExists(t, filepath.Join(calls, "followup"), "Claude does not go again")
}

func TestRun_failsWhenAWorseRoundCannotBeUndone(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, worse+"; "+lock)
	s := loopRun(t, agent)

	assert.Equal(t, Failed, s.State)
	require.Error(t, s.Err)
	assert.Contains(t, s.Err.Error(), "index.lock")
	assert.Equal(t, 0, s.Undone)
}

func TestRun_stopWhileCheckingEndsTheLoop(t *testing.T) {
	t.Parallel()
	agent, calls := loopAgent(t, true, fixes)
	r := loopBegin(t, agent, map[string]string{"ci.sh": "#!/bin/sh\nsleep 2\n"})
	require.Eventually(t, func() bool { return r.Snapshot().State == Checking }, time.Minute, 20*time.Millisecond)
	r.Stop()
	assert.True(t, r.Snapshot().Stopping, "the page can say the stop is coming")
	require.Eventually(t, func() bool { return !r.Snapshot().Live() }, time.Minute, 20*time.Millisecond)

	s := r.Snapshot()
	assert.Equal(t, Done, s.State, "the round finished; its result can still become a pull request")
	assert.Equal(t, []int{1}, s.rounds)
	assert.NoFileExists(t, filepath.Join(calls, "followup"), "Claude does not go again")
	assert.Equal(t, halted, s.ended)
	assert.Equal(t, "Stopped by you; Claude did not go again.", s.RoundsNote())
	assert.Equal(t, "./ci.sh", s.Check.Name)
}

func TestRun_undoesAWorseRoundThatFailed(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, worse+"\n  exit 1")
	r := loopStart(t, agent)
	s := r.Snapshot()

	assert.Equal(t, []int{1, 2}, s.rounds)
	assert.Equal(t, failed, s.ended)
	assert.Equal(t, 2, s.Undone)
	assert.Nil(t, s.After.Snap.Decl("m.D"), "the result is round 1's")
	assert.Equal(t, "2 rounds, left after each: 1 → 2. Stopped: round 2 ended with an error: claude: exit status 1. Round 2 left more, so its changes were undone.", s.RoundsNote())
}

func TestRun_undoesAWorseRoundStoppedDuringItsChecks(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, worse)
	r := loopBegin(t, agent, map[string]string{"ci.sh": "#!/bin/sh\nsleep 2\n"})
	// Round already reads 2 while round 1's commit is made; the worse code
	// marks round 2's checks.
	require.Eventually(t, func() bool {
		c, _ := os.ReadFile(filepath.Join(modOf(r), "c.go"))
		return r.Snapshot().State == Checking && strings.Contains(string(c), "func D")
	}, time.Minute, 20*time.Millisecond)
	r.Stop()
	require.Eventually(t, func() bool { return !r.Snapshot().Live() }, time.Minute, 20*time.Millisecond)

	s := r.Snapshot()
	assert.Equal(t, Done, s.State)
	assert.Equal(t, []int{1, 2}, s.rounds)
	assert.Equal(t, halted, s.ended)
	assert.Equal(t, 2, s.Undone)
	assert.Nil(t, s.After.Snap.Decl("m.D"), "a pull request would carry round 1")
	c, err := os.ReadFile(filepath.Join(r.mod, "c.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(c), "func D")
}

func TestRun_undoesAWorseRoundStoppedWhileClaudeWorked(t *testing.T) {
	t.Parallel()
	agent, _ := loopAgent(t, true, worse+"\n  exec sleep 30")
	r := loopBegin(t, agent, nil)
	require.Eventually(t, func() bool {
		c, _ := os.ReadFile(filepath.Join(modOf(r), "c.go"))
		return r.Snapshot().Round == 2 && strings.Contains(string(c), "func D")
	}, time.Minute, 20*time.Millisecond)
	r.Stop()
	require.Eventually(t, func() bool { return r.Snapshot().After != nil }, time.Minute, 20*time.Millisecond)

	s := r.Snapshot()
	assert.Equal(t, Stopped, s.State)
	assert.Equal(t, cut, s.ended)
	assert.Equal(t, 2, s.Undone)
	assert.Nil(t, s.After.Snap.Decl("m.D"))
}

func TestRun_undoesARoundThatLeavesMoreThanTheListShows(t *testing.T) {
	t.Parallel()
	// Round 1 leaves 25 functions with too many parameters, round 2 leaves
	// 100: both lists stop at maxListed, the counts do not.
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\n"+
		`echo '{"type":"system","subtype":"init","session_id":"s1","model":"m","claude_code_version":"t"}'`+"\n"+
		`case "$*" in *"--resume s1"*) n=100 ;; *) n=25 ;; esac`+"\n"+
		"printf 'package m\\n' > c.go\n"+
		"i=1; while [ $i -le $n ]; do printf 'func C%d(a, b, c, d, e, f int) int { return a }\\n' $i >> c.go; i=$((i+1)); done\n"), 0o755))
	s := loopRun(t, agent)

	assert.Equal(t, []int{25, 100}, s.rounds)
	assert.Equal(t, undone, s.ended)
	assert.Len(t, s.Review.Introduced, 25)
}
