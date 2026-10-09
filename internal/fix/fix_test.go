package fix

import (
	"os"
	"path/filepath"
	"strings"
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
	assert.Equal(t, Step{Kind: Think, Title: "Grouping the shared parameters.", Text: "Grouping the shared parameters.\nThen tests."}, *thought[0].step)
	assert.Equal(t, Step{ID: "t1", Kind: Edit, Tool: "Edit", Title: "Edit a.go", Path: "/wt/m/a.go", Old: "f(a, b)", New: "f(p)"}, *thought[1].step)

	run := parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./...\necho done"}}]}}`))
	assert.Equal(t, Step{ID: "t2", Kind: Exec, Tool: "Bash", Title: "Bash go test ./...", Command: "go test ./...\necho done"}, *run[0].step)

	res := parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"FAIL a"}],"is_error":true}]}}`))
	require.Len(t, res, 1)
	assert.Equal(t, "t2", res[0].result.id)
	assert.Equal(t, "FAIL a", res[0].result.output)
	assert.True(t, res[0].result.failed)

	final := parse([]byte(`{"type":"result","result":"Grouped them.","total_cost_usd":0.42}`))
	assert.Equal(t, "Grouped them.", final[0].final.summary)
	assert.Equal(t, 0.42, final[0].final.cost)

	assert.Empty(t, parse([]byte(`{"type":"system","subtype":"init"}`)), "setup lines are not steps")
	assert.Empty(t, parse([]byte(`not json`)))
}

func TestRead_timesStepsAndPlacesTheirFilesInTheModule(t *testing.T) {
	r := &Run{Started: time.Now(), updates: topic.New[int64](), wt: "/wt", mod: "/wt/m"}
	r.read(strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/wt/m/sub/a.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/wt/README.md"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"package sub"}]}}
`))
	require.Len(t, r.steps, 2)
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

func TestTree_namesTheModuleFilesAShellCommandReads(t *testing.T) {
	wt := testrepo.New(t, map[string]string{"go.mod": "module m\n", "sub/a.go": "package sub\n", "b.go": "package m\n"}, nil)
	tr := newTree(wt, wt)
	reads := tr.named(`sed -n 1,5p sub/a.go && cat "b.go" missing.go | head`)
	assert.Equal(t, []string{"sub/a.go", "b.go"}, reads)
	assert.Equal(t, Read, shellKind("sed -n 1,5p sub/a.go", reads))
	assert.Equal(t, Search, shellKind(`grep -rn "A" .`, nil))
	assert.Equal(t, Exec, shellKind("go test ./...", nil))
}
