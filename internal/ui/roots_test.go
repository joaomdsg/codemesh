package ui_test

import (
	"strings"
	"testing"

	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoRoots is a repo with the module "example.com/calc" at its top and
// "example.com/tools" in tools/, both changed, and both with a t.go.
func twoRoots(t *testing.T) env {
	t.Helper()
	base := map[string]string{
		"go.mod":       "module example.com/calc\n\ngo 1.27\n",
		"t.go":         "package calc\n\nfunc A() int { return 1 }\n",
		"tools/go.mod": "module example.com/tools\n\ngo 1.27\n",
		"tools/t.go":   "package tools\n\nfunc U() int { return 1 }\n",
	}
	head := map[string]string{
		"t.go":       "package calc\n\nfunc A() int { return 2 }\n",
		"tools/t.go": "package tools\n\nfunc U() int { return 2 }\n",
		"tools/w.go": "package tools\n\nfunc W(a, b, c, d, e, f int) int {\n" + strings.Repeat("\tif a == 1 {\n\t\treturn b\n\t}\n", 11) + "\treturn c\n}\n",
	}
	e := serveDirs(t, testrepo.New(t, base, head))
	e.a.Reading(e.repo.ID, 0)
	e.a.Wait()
	return e
}

func TestRoots_aLivePageStaysInTheRootItWasOpenedIn(t *testing.T) {
	e := twoRoots(t)
	p := e.openPage(t, e.base()+"/in/tools/review", "")
	require.Contains(t, p.html(), "example.com/tools")

	p.press("Mark reviewed")

	wr := e.a.WorkingReview(e.repo.ID, "tools")
	defer wr.Release()
	require.NotNil(t, wr.Review)
	marked := 0
	for _, u := range wr.Review.Units {
		if e.reviewed(t).Reviewed(u.Key) {
			marked++
		}
	}
	assert.Equal(t, 1, marked, "the mark landed on a unit of tools")
	assert.NotContains(t, p.conn.Await("reviewed"), "example.com/calc", "the re-render is still tools")
}

func TestRoots_aNoteStaysInTheRootItWasWrittenIn(t *testing.T) {
	e := twoRoots(t)
	top := e.openPage(t, e.base()+"/review", "")
	top.press("Start a note on a line", "anchor", lineToken(t, top.html(), "func A() int { return 2 }"))
	top.seen(`class="note-form"`)
	top.press("Save the note", "noteText", "ROOT-A-NOTE", "inHandoff", "true")

	_, tools := e.app.Get(e.base() + "/in/tools/review")

	assert.NotContains(t, tools, "ROOT-A-NOTE", "tools has its own t.go; the note is on the top module's")
	require.Len(t, e.a.Basket().Items, 1)
	assert.Equal(t, "t.go:3", e.a.Basket().Items[0].Where)

	inTools := e.openPage(t, e.base()+"/in/tools/review", "")
	inTools.press("Start a note on a line", "anchor", lineToken(t, inTools.html(), "func U() int { return 2 }"))
	inTools.seen(`class="note-form"`)
	inTools.press("Save the note", "noteText", "TOOLS-NOTE", "inHandoff", "true")

	assert.Equal(t, "tools/t.go:3", e.a.Basket().Items[1].Where, "the prompt names the file from the repo's top")
	_, again := e.app.Get(e.base() + "/review")
	assert.NotContains(t, again, "TOOLS-NOTE")
	assert.Contains(t, again, "ROOT-A-NOTE")
}

func TestRoots_everyHandoffItemNamesItsFileFromTheRepoTop(t *testing.T) {
	e := twoRoots(t)

	e.basketPress(t, e.base()+"/in/tools/health", "Add to handoff")
	e.basketPress(t, e.base()+"/in/tools/", "Add to handoff")
	p := e.openPage(t, e.base()+"/in/tools/review", "")
	p.press("Add every introduced smell to the handoff")

	items := e.a.Basket().Items
	require.NotEmpty(t, items)
	for _, it := range items {
		if strings.Contains(it.Where, ":") {
			assert.True(t, strings.HasPrefix(it.Where, "tools/"), "%s item %q is at %q", it.Kind, it.Title, it.Where)
		}
	}
	kinds := map[handoff.Kind]bool{}
	for _, it := range items {
		kinds[it.Kind] = true
	}
	assert.True(t, kinds[handoff.Prognosis] && kinds[handoff.Finding], "the health list and the map each added one: %v", kinds)
}
