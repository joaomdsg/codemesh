package fix

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushed is a repository whose main branch is on a bare origin, with an
// agent that adds a file and a gh that records how it was called.
func pushed(t *testing.T) (repo, origin, calls string, rs *Runs) {
	t.Helper()
	repo = testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n\nfunc A() int { return 1 }\n"}, nil)
	origin, calls = t.TempDir(), t.TempDir()
	require.NoError(t, testrepo.Git(origin, "init", "--quiet", "--bare"))
	for _, args := range [][]string{
		{"remote", "add", "origin", origin}, {"push", "--quiet", "origin", "main"},
		{"config", "user.name", "t"}, {"config", "user.email", "t@example.com"},
	} {
		require.NoError(t, testrepo.Git(repo, args...))
	}
	bin := t.TempDir()
	agent, gh := filepath.Join(bin, "agent"), filepath.Join(bin, "gh")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\nprintf 'package m\\n\\nfunc B() int { return 2 }\\n' > b.go\n"), 0o755))
	require.NoError(t, os.WriteFile(gh, []byte("#!/bin/sh\ncat > "+calls+"/body\necho \"$@\" > "+calls+"/args\necho https://github.com/o/r/pull/7\n"), 0o755))
	rs = NewRuns(repo)
	rs.Agent, rs.Gh = agent, gh
	t.Cleanup(rs.Close)
	return repo, origin, calls, rs
}

func finish(t *testing.T, rs *Runs) *Run {
	t.Helper()
	r := rs.Start(prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A", Summary: "A is hard to follow."})
	require.Eventually(t, func() bool { s := r.Snapshot(); return s.State == Done || s.State == Failed }, time.Minute, 50*time.Millisecond)
	require.True(t, r.Snapshot().CanPR(), "the run ended Done with a passing check on branch main")
	r.OpenPR()
	require.Eventually(t, func() bool { return !r.Snapshot().PR.Opening }, time.Minute, 50*time.Millisecond)
	return r
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestOpenPR_pushesTheChangeAndOpensADraftAgainstTheStartingBranch(t *testing.T) {
	t.Parallel()
	repo, origin, calls, rs := pushed(t)
	r := finish(t, rs)

	pr := r.Snapshot().PR
	require.NoError(t, pr.Err)
	assert.Equal(t, "https://github.com/o/r/pull/7", pr.URL)
	args, _ := os.ReadFile(filepath.Join(calls, "args"))
	assert.Regexp(t, `^codemesh/complex-a-\d{4}-\d{4}$`, pr.Branch)
	assert.Contains(t, string(args), "pr create --draft --base main --head "+pr.Branch)
	body, _ := os.ReadFile(filepath.Join(calls, "body"))
	assert.Contains(t, string(body), "A is hard to follow.")
	assert.Contains(t, gitOut(t, origin, "show", pr.Branch+":b.go"), "func B()", "the branch on origin holds the change")
	assert.Equal(t, "Complex function: A", gitOut(t, origin, "log", "-1", "--format=%s", pr.Branch))
	assert.Equal(t, "t", gitOut(t, origin, "log", "-1", "--format=%an", pr.Branch), "the repository's own identity authors it")

	rs.Dismiss(r.Key)
	assert.Empty(t, gitOut(t, repo, "branch", "--list", "codemesh/*"), "the local branch goes with the worktree")
}

func TestOpenPR_commitsInARepositoryWithNoIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("EMAIL", "")
	_, origin, _, rs := pushed(t)
	for _, key := range []string{"user.name", "user.email"} {
		require.NoError(t, testrepo.Git(rs.Dir, "config", "--unset", key))
	}
	r := finish(t, rs)

	pr := r.Snapshot().PR
	require.NoError(t, pr.Err)
	assert.Equal(t, "codemesh", gitOut(t, origin, "log", "-1", "--format=%an", pr.Branch))
}

func TestOpenPR_doesNothingOnceTheRunIsClosed(t *testing.T) {
	t.Parallel()
	_, _, calls, rs := pushed(t)
	r := rs.Start(prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"})
	require.Eventually(t, func() bool { return r.Snapshot().CanPR() }, time.Minute, 50*time.Millisecond)
	rs.Dismiss(r.Key)
	r.OpenPR()

	assert.Equal(t, PR{}, r.Snapshot().PR)
	assert.NoFileExists(t, filepath.Join(calls, "args"))
}

func TestOpenPR_pushesABaseBranchOriginLacksFirst(t *testing.T) {
	t.Parallel()
	repo, origin, calls, rs := pushed(t)
	require.NoError(t, testrepo.Git(repo, "switch", "--quiet", "-c", "local"))
	r := finish(t, rs)

	require.NoError(t, r.Snapshot().PR.Err)
	assert.Equal(t, gitOut(t, repo, "rev-parse", "HEAD"), gitOut(t, origin, "rev-parse", "local"), "origin gets the branch at the starting commit")
	args, _ := os.ReadFile(filepath.Join(calls, "args"))
	assert.Contains(t, string(args), "--base local")
}

func TestOpenPR_bringsOriginUpToTheStartingCommit(t *testing.T) {
	t.Parallel()
	repo, origin, _, rs := pushed(t)
	commit(t, repo, "c.go", "local only")
	r := finish(t, rs)

	require.NoError(t, r.Snapshot().PR.Err)
	assert.Equal(t, gitOut(t, repo, "rev-parse", "HEAD"), gitOut(t, origin, "rev-parse", "main"))
}

func TestOpenPR_refusesWhenOriginsBranchMovedOnElsewhere(t *testing.T) {
	t.Parallel()
	repo, origin, calls, rs := pushed(t)
	commit(t, repo, "x.go", "someone else's")
	require.NoError(t, testrepo.Git(repo, "push", "--quiet", "origin", "main"))
	require.NoError(t, testrepo.Git(repo, "reset", "--quiet", "--hard", "HEAD~1"))
	commit(t, repo, "y.go", "mine")
	theirs := gitOut(t, origin, "rev-parse", "main")
	r := finish(t, rs)

	require.Error(t, r.Snapshot().PR.Err)
	assert.Contains(t, r.Snapshot().PR.Err.Error(), "origin's main has commits the run did not start from")
	assert.Equal(t, theirs, gitOut(t, origin, "rev-parse", "main"), "never forced")
	assert.NoFileExists(t, filepath.Join(calls, "args"), "gh is never called")
}

func commit(t *testing.T, repo, file, msg string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, file), []byte("package m\n"), 0o644))
	require.NoError(t, testrepo.Git(repo, "add", file))
	require.NoError(t, testrepo.Git(repo, "commit", "--quiet", "-m", msg))
}

func TestRun_reanalysesTheWorktreeWhileClaudeWritesANewFile(t *testing.T) {
	t.Parallel()
	repo := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n\nfunc A() int { return 1 }\n"}, nil)
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"write b.go"}}]}}'`+"\n"+
		"printf 'package m\\n\\nfunc B() int { return 2 }\\n' > b.go\n"+
		`echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}'`+"\n"+
		"exec sleep 30\n"), 0o755))
	rs := NewRuns(repo)
	rs.Agent = agent
	t.Cleanup(rs.Close)
	r := rs.Start(prognosis.Prognosis{Key: "complex:m.A", Title: "Complex function", Name: "A"})

	require.Eventually(t, func() bool { return r.Snapshot().Now != nil }, 20*time.Second, 50*time.Millisecond)
	s := r.Snapshot()
	assert.Equal(t, Working, s.State, "the map grows while Claude is still at work")
	assert.True(t, hasFile(s.Now, "b.go"))
	assert.NotNil(t, s.Now.Decl("m.B"))
}

// The starting point is read from its own worktree: Claude's copy has moved
// on by the time the review reads the source.
func TestRun_reviewsAChangeAgainstTheTreeItStartedFrom(t *testing.T) {
	t.Parallel()
	repo := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n",
		"m.go": "package m\n\nfunc First() int { return 1 }\n\nfunc Parse(x string) string {\n\treturn x\n}\n"}, nil)
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\ncat > m.go <<'GO'\npackage m\n\nfunc First() int { return 1 }\n\n"+
		"func noop(s string) string { return s }\n\nfunc Parse(x string) string {\n\treturn noop(x)\n}\nGO\n"), 0o755))
	rs := NewRuns(repo)
	rs.Agent = agent
	t.Cleanup(rs.Close)
	r := rs.Start(prognosis.Prognosis{Key: "complex:m.Parse", Title: "Complex function", Name: "Parse"})
	require.Eventually(t, func() bool { s := r.Snapshot(); return s.State == Done || s.State == Failed }, time.Minute, 50*time.Millisecond)

	s := r.Snapshot()
	require.NotNil(t, s.Review)
	for _, u := range s.Review.Units {
		if u.ID == "m.Parse" {
			assert.Equal(t, 1, u.Added)
			assert.Equal(t, 1, u.Deleted, "only return x changed")
			return
		}
	}
	t.Fatal("no unit for Parse")
}

// Rounds are committed in the worktree; a pull request still carries one
// commit with the whole change.
func TestOpenPR_foldsEveryRoundIntoOneCommit(t *testing.T) {
	t.Parallel()
	_, origin, _, rs := pushed(t)
	rs.Agent, _ = loopAgent(t, true, fixes)
	r := finish(t, rs)

	s := r.Snapshot()
	require.NoError(t, s.PR.Err)
	assert.Equal(t, []int{1, 0}, s.rounds, "two rounds ran")
	assert.Contains(t, gitOut(t, origin, "show", s.PR.Branch+":c.go"), "func C(a int)", "the branch holds the last round")
	assert.Equal(t, "1", gitOut(t, origin, "rev-list", "--count", "main.."+s.PR.Branch))
}
