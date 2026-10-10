package fix

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
)

func TestParse_turnsClaudesStreamIntoSteps(t *testing.T) {
	thought := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Grouping the shared parameters.\nThen tests."},{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/wt/m/a.go","old_string":"f(a, b)","new_string":"f(p)"}}]}}`))
	require.Len(t, thought, 2)
	assert.Equal(t, Step{Kind: kindThink, Title: "Grouping the shared parameters.", Text: "Grouping the shared parameters.\nThen tests."}, *thought[0].step)
	assert.Equal(t, Step{ID: "t1", Kind: kindEdit, Title: "Edit a.go", Path: "/wt/m/a.go", Old: "f(a, b)", New: "f(p)"}, *thought[1].step)

	run := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./...\necho done"}}]}}`))
	assert.Equal(t, Step{ID: "t2", Kind: kindExec, Title: "Bash go test ./...", Command: "go test ./...\necho done"}, *run[0].step)

	res := parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"FAIL a"}],"is_error":true}]}}`))
	require.Len(t, res, 1)
	assert.Equal(t, "t2", res[0].result.id)
	assert.Equal(t, "FAIL a", res[0].result.output)
	assert.True(t, res[0].result.failed)

	final := parse([]byte(`{"type":"result","result":"Grouped them.","modelUsage":{"claude-haiku-5-5":{"inputTokens":2,"costUSD":0.42}}}`))
	assert.Equal(t, "Grouped them.", final[0].final.summary)
	assert.Equal(t, 0.42, final[0].final.by["claude-haiku-5-5"].USD)

	init := parse([]byte(`{"type":"system","subtype":"init","model":"claude-haiku-5-5","claude_code_version":"2.1.294","session_id":"s1"}`))
	require.Len(t, init, 1)
	assert.Nil(t, init[0].step, "setup lines are not steps")
	assert.Equal(t, "claude-haiku-5-5", init[0].init.model)
	assert.Empty(t, parse([]byte(`not json`)))
}

// The numbers are a real one-reply run, whose result reported
// total_cost_usd 0.0038114.
func TestUsage_estimatesEachReplyOnceThenTakesClaudesTally(t *testing.T) {
	reply := []byte(`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":19046,"cache_read_input_tokens":0,"output_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":19046}},"content":[{"type":"text","text":"ok"}]}}`)
	var u Usage
	for range 2 { // stream-json repeats a reply per content block
		for _, e := range parse(reply) {
			if e.use != nil {
				u.add(e.use.id, e.use.model, e.use.t)
			}
		}
	}
	usd, all := u.USD()
	assert.InDelta(t, 0.0038114, usd, 1e-9)
	assert.True(t, all)
	assert.Equal(t, 1, u.Calls())

	u.add("m2", "claude-unknown-9", tokens{Input: 10})
	_, all = u.USD()
	assert.False(t, all, "a model with no price leaves the estimate open")

	u.settle(map[string]tally{"claude-haiku-5-5": {USD: 0.5}, "claude-unknown-9": {USD: 0.25}})
	usd, all = u.USD()
	assert.InDelta(t, 0.75, usd, 1e-9)
	assert.True(t, all && u.Exact)
	assert.Equal(t, 1, u.Models["claude-haiku-5-5"].Calls, "the tally keeps the counted calls")

	long := tokens{CacheRead: 200_000}
	p, _ := priceOf("claude-haiku-5-5")
	assert.InDelta(t, 5*200_000*0.01/1e6, p.of(long), 1e-12, "past 100k tokens Haiku 5.5 costs five times as much")
	_, ok := priceOf("claude-haiku-4-5-20251001")
	assert.True(t, ok, "a dated ID takes its model's price")
}

func TestRead_timesStepsAndPlacesTheirFilesInTheModule(t *testing.T) {
	r := &Run{Started: time.Now(), updates: topic.New[int64](), wt: "/wt", mod: "/wt/m"}
	r.read(strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/wt/m/sub/a.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/wt/README.md"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"package sub"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"/wt/m/sub/a.go","offset":10,"limit":5}}]}}
`))
	require.Len(t, r.steps, 3)
	assert.Equal(t, []Read{{File: "sub/a.go"}}, r.steps[0].Reads, "no offset or limit: the whole file")
	assert.Empty(t, r.steps[1].Reads)
	assert.Equal(t, []Read{{File: "sub/a.go", From: 10, To: 14}}, r.steps[2].Reads)
	assert.Equal(t, "sub/a.go", r.steps[0].File)
	assert.Equal(t, "m/sub/a.go", r.steps[0].Path)
	assert.Equal(t, "package sub", r.steps[0].Output, "the result lands on the call it answers")
	assert.Equal(t, "", r.steps[1].File, "outside the module: no tile to light")
	assert.Equal(t, "README.md", r.steps[1].Path)
	assert.Empty(t, r.steps[1].Output)
}

func TestCheckOf_prefersTheReposOwnGate(t *testing.T) {
	root := t.TempDir()
	assert.Equal(t, "go build ./... && go test ./...", checkOf(root, root).Name, "no gate: build and test the module")

	assert.NoError(t, os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\n\tgo build\nci: build\n\t./x\n"), 0o644))
	assert.Equal(t, "make ci", checkOf(root, root).Name)

	assert.NoError(t, os.WriteFile(filepath.Join(root, "ci.sh"), []byte("#!/bin/sh\n"), 0o755))
	c := checkOf(root, filepath.Join(root, "sub"))
	assert.Equal(t, "./ci.sh", c.Name)
	assert.Equal(t, root, c.Dir, "a repo's gate runs from its root, not the module's")
}

func TestAnalyse_endsTheChecksChildrenWhenCancelled(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	// Without exec the shell forks each sleep, and the child holds the output.
	s, err := analyse(ctx, dir, Check{Dir: dir, Args: []string{"sh", "-c", "sleep 30; sleep 30"}})
	require.NoError(t, err)
	assert.False(t, s.CheckOK)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestAnalyse_passesACheckThatLeavesAChildBehind(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	s, err := analyse(context.Background(), dir, Check{Dir: dir, Args: []string{"sh", "-c", "sleep 8 &"}})
	require.NoError(t, err)
	assert.True(t, s.CheckOK, "the check itself succeeded")
}

func TestRun_keepsTheCheckAndClaudeInTheWorktree(t *testing.T) {
	dir := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	t.Setenv("GIT_DIR", t.TempDir())
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	clean := []string{"sh", "-c", `[ -z "$GIT_DIR$GIT_WORK_TREE" ]`}
	s, err := analyse(context.Background(), dir, Check{Dir: dir, Args: clean})
	require.NoError(t, err)
	assert.True(t, s.CheckOK, "the check does not see them")

	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\n"+strings.Join(clean[2:], "")+"\n"), 0o755))
	r := &Run{updates: topic.New[int64]()}
	assert.NoError(t, r.claude(context.Background(), agent, dir, "p"), "nor does Claude")
}

func TestClaude_doesNotWaitForAChildHoldingItsOutput(t *testing.T) {
	t.Parallel()
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\nsleep 20 &\necho '{\"type\":\"result\",\"result\":\"done\"}'\n"), 0o755))
	r := &Run{updates: topic.New[int64]()}
	start := time.Now()
	require.NoError(t, r.claude(context.Background(), agent, t.TempDir(), "p"))
	assert.Less(t, time.Since(start), 15*time.Second)
	assert.Equal(t, "done", r.summary, "what it printed is read")
}

func TestAnalyse_endsWhatTheCheckLeftRunning(t *testing.T) {
	t.Parallel()
	dir := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	pid := filepath.Join(t.TempDir(), "pid")
	// The child writes nothing to the check's output, so the check ends at once.
	_, err := analyse(context.Background(), dir, Check{Dir: dir, Args: []string{"sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $! > " + pid}})
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return !running(t, pid) }, 5*time.Second, 50*time.Millisecond)
}

// running reports whether the process whose id is in file still runs. A
// killed process stays a zombie until its parent reaps it, which an init
// that does not reap never does, so a zombie counts as gone.
func running(t *testing.T, file string) bool {
	t.Helper()
	b, err := os.ReadFile(file)
	require.NoError(t, err)
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NoError(t, err)
	if stat, err := os.ReadFile("/proc/" + strconv.Itoa(n) + "/stat"); err == nil {
		// The state follows the command in parentheses, which may hold any
		// character, ")" too.
		i := bytes.LastIndexByte(stat, ')')
		return i+2 < len(stat) && stat[i+2] != 'Z'
	}
	p, err := os.FindProcess(n)
	require.NoError(t, err)
	return p.Signal(syscall.Signal(0)) == nil
}

func TestRunning_takesAZombieAsGone(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	dead := exec.Command("true")
	require.NoError(t, dead.Start())
	t.Cleanup(func() { _ = dead.Wait() })
	pid := filepath.Join(t.TempDir(), "pid")
	require.NoError(t, os.WriteFile(pid, []byte(strconv.Itoa(dead.Process.Pid)), 0o600))
	// Not waited for, it stays a zombie: killed, it is gone all the same.
	assert.Eventually(t, func() bool { return !running(t, pid) }, 5*time.Second, 20*time.Millisecond)
}

func TestUsage_settle_addsATallyThatCoversOnlyItsOwnCall(t *testing.T) {
	var u Usage
	u.settle(map[string]tally{"m": {Input: 100, USD: 1}})
	u.settle(map[string]tally{"m": {Input: 150, USD: 1.5}})
	assert.Equal(t, 150, u.Models["m"].Input, "a resumed session's tally covers the rounds before")

	u.settle(map[string]tally{"m": {Input: 30, USD: 0.3}})
	assert.Equal(t, 180, u.Models["m"].Input, "a smaller one does not, as before Claude Code 2.1.277")
	assert.InDelta(t, 1.8, u.Models["m"].USD, 1e-9)

	old := Usage{Version: "2.1.276"}
	old.settle(map[string]tally{"m": {Input: 100}})
	old.settle(map[string]tally{"m": {Input: 150}})
	assert.Equal(t, 250, old.Models["m"].Input, "before 2.1.277 each call's tally is its own, larger or not")
}

func TestTree_seesEditsWhateverToolMadeThem(t *testing.T) {
	wt := testrepo.New(t, map[string]string{"go.mod": "module m\n", "sub/a.go": "package sub\n\nfunc A() int {\n\treturn 1\n}\n"}, nil)
	tr := newTree(wt, wt)
	assert.Empty(t, tr.changes(), "a clean tree changed nothing")

	write := func(s string) { require.NoError(t, os.WriteFile(filepath.Join(wt, "sub/a.go"), []byte(s), 0o644)) }
	write("package sub\n\nfunc A() int {\n\treturn 2\n}\n")
	cs := tr.changes()
	require.Len(t, cs, 1)
	assert.Equal(t, Change{Path: "sub/a.go", File: "sub/a.go", Start: 2, Old: "\nfunc A() int {\n\treturn 1\n}\n", New: "\nfunc A() int {\n\treturn 2\n}\n"}, cs[0])
	assert.Empty(t, tr.changes(), "the same edit is not reported twice")

	write("package sub\n\nfunc A() int {\n\treturn 1\n}\n")
	assert.Len(t, tr.changes(), 1, "putting the file back is a change too")
}

func TestInside_takesNamesStartingWithDotsAsInside(t *testing.T) {
	for p, want := range map[string]bool{"/r/..x/a.go": true, "/r/a.go": true, "/r": true, "/r/../a.go": false, "/a.go": false} {
		_, ok := inside("/r", p)
		assert.Equal(t, want, ok, p)
	}
}

func TestTree_showsALinkAsItsTarget(t *testing.T) {
	wt := testrepo.New(t, map[string]string{"go.mod": "module m\n"}, nil)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("SECRET\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(wt, "link")))

	cs := newTree(wt, wt).changes()
	require.Len(t, cs, 1)
	assert.Equal(t, "link", cs[0].Path)
	assert.Equal(t, outside, cs[0].New, "the target, as git stores it")
}

func TestTree_readsTheLinesAShellCommandNames(t *testing.T) {
	b := "package m\n" + strings.Repeat("x\n", 9)
	wt := testrepo.New(t, map[string]string{"go.mod": "module m\n", "sub/a.go": "package sub\n", "b.go": b}, nil)
	tr := newTree(wt, wt)
	for cmd, want := range map[string][]Read{
		`cat "b.go" missing.go`:                   {{File: "b.go"}},
		`sed -n '3,5p' sub/a.go`:                  {{File: "sub/a.go", From: 3, To: 5}},
		`sed -n 7p b.go`:                          {{File: "b.go", From: 7, To: 7}},
		`sed -n '4,$p' b.go`:                      {{File: "b.go", From: 4}},
		`head -n 20 b.go`:                         {{File: "b.go", From: 1, To: 20}},
		`head -3 b.go`:                            {{File: "b.go", From: 1, To: 3}},
		`tail -n +4 b.go`:                         {{File: "b.go", From: 4}},
		`tail -n 2 b.go`:                          {{File: "b.go", From: 9, To: 10}},
		`sed -n 1,5p sub/a.go && cat b.go | head`: {{File: "sub/a.go", From: 1, To: 5}, {File: "b.go"}},
		`awk 'NR<5' b.go`:                         {{File: "b.go"}},
		`go test ./...`:                           nil,
	} {
		assert.Equal(t, want, tr.reads(cmd), cmd)
	}
	assert.Equal(t, kindRead, shellKind("sed -n 1,5p sub/a.go", tr.reads("sed -n 1,5p sub/a.go")))
	assert.Equal(t, kindSearch, shellKind(`grep -rn "A" .`, nil))
	assert.Equal(t, kindExec, shellKind("go test ./...", nil))
}

func TestTree_placesGrepMatchesOnTheirLines(t *testing.T) {
	wt := testrepo.New(t, map[string]string{"go.mod": "module m\n", "sub/a.go": "package sub\n", "b.go": "package m\n", "my-2-x.go": "package m\n"}, nil)
	tr := newTree(wt, wt)
	assert.Equal(t, []Read{{File: "sub/a.go", From: 3, To: 4}, {File: "b.go", From: 7, To: 7}},
		tr.matched(`grep -rn A .`, "sub/a.go:3:x\nsub/a.go:4-y\nb.go:7:z\n"), "context lines count; neighbours merge")
	assert.Equal(t, []Read{{File: "b.go", From: 2, To: 2}}, tr.matched("grep -n x b.go", "2:x\n"), "one file: no path in the output")
	assert.Equal(t, []Read{{File: "b.go", From: 5, To: 5}}, tr.matched("rg -n x", "b.go:5:x\n"))
	assert.Equal(t, []Read{{File: "my-2-x.go", From: 9, To: 9}}, tr.matched("rg -n x", "my-2-x.go:9:x\n"), "digits and - in a path")
	assert.Nil(t, tr.matched("grep -r A .", "sub/a.go:3:x\n"), "without -n a number may be text")
	assert.Nil(t, tr.matched("go vet ./...", "b.go:3: unused"))
}

func TestHunks_keepsEditsFarApartSeparate(t *testing.T) {
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	a := strings.Join(lines, "\n") + "\n"
	lines[2], lines[35] = "changed 3", "changed 36"
	hs := hunks(a, strings.Join(lines, "\n")+"\n")
	require.Len(t, hs, 2, "two edits 33 lines apart are two hunks, not one region")
	assert.Equal(t, 1, hs[0].start)
	assert.Equal(t, "line 1\nline 2\nline 3\nline 4\nline 5\n", hs[0].old)
	assert.Equal(t, "line 1\nline 2\nchanged 3\nline 4\nline 5\n", hs[0].new)
	assert.Equal(t, 34, hs[1].start)
}

func TestSnapshot_phases(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		state         State
		live, editing bool
	}{
		{Preparing, true, true},
		{Working, true, true},
		{Checking, true, false},
		{Done, false, false},
		{Failed, false, false},
		{Stopped, false, false},
	} {
		s := Snapshot{State: c.state}
		assert.Equal(t, c.live, s.Live(), "%s live", c.state)
		assert.Equal(t, c.editing, s.Editing(), "%s editing", c.state)
	}
}

func TestUsage_Tokens_sumsEachKindAcrossModels(t *testing.T) {
	t.Parallel()
	u := Usage{Models: map[string]ModelUse{
		"a": {Input: 1, CacheWrite: 10, CacheRead: 100, Output: 1000},
		"b": {Input: 2, CacheWrite: 20, CacheRead: 200, Output: 2000},
	}}

	in, write, read, out := u.Tokens()

	assert.Equal(t, [4]int{3, 30, 300, 3000}, [4]int{in, write, read, out})
}
