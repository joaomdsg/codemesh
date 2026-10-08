package ui_test

import (
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-via/via/vt"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/joaomdsg/codemesh/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actionURL matches an action binding in rendered markup, the way vt does:
// a bound argument rides in the data-via-q attribute before the data-on
// (group 1), the path is group 2. vt reads actions off "/" only, and the
// review page lives elsewhere.
var actionURL = regexp.MustCompile(`(?:data-via-q-[^=\s]+="([^"]*)" data-on:[^=\s]+="@post\('|@post\(')([^']*_via/a/r/[A-Za-z0-9_-]+(?:[?&][^']*)?)'`)

type env struct {
	app   *vt.App
	src   *live.Source
	state *review.State
}

func serve(t *testing.T) env { return serveRepo(t, testrepo.CalcBase, testrepo.CalcHead) }

func serveRepo(t *testing.T, base, head map[string]string) env {
	t.Helper()
	dir := testrepo.New(t, base, head)
	src := live.New(dir, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(src.Close)
	require.NoError(t, src.Refresh().Err)
	state, err := review.OpenState(filepath.Join(t.TempDir(), "reviewed.json"))
	require.NoError(t, err)
	return env{vt.Serve(t, ui.New(src, state, "http://localhost:7777")), src, state}
}

func TestMap_showsEveryPackageWithItsFindings(t *testing.T) {
	t.Parallel()
	e := serve(t)

	status, body := e.app.Get("/")
	require.Equal(t, http.StatusOK, status)
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Regexp(t, `\["p",[^\]]*"calc","",0,"example.com/calc/calc"\]`, body, "the calc package tile")
	assert.Regexp(t, `\["f",[^\]]*"calc.go","",0,"calc/calc.go"\]`, body, "its file tile")
	assert.Regexp(t, `data-effect="codemesh.atlas\(el, \$_atlas, \{focus: \$_selected\}\)"`, body)
	assert.Contains(t, body, "Smells · 1")
	assert.Contains(t, body, "Complex function")
}

func TestMap_opensADeclarationWithItsSourceAndCallers(t *testing.T) {
	t.Parallel()
	e := serve(t)

	status, body := e.app.Get("/?in=calc/calc.go&d=" + url.QueryEscape("example.com/calc/calc.Scale"))
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "func Scale(x, k int) int {")
	assert.Contains(t, body, "Called by")
	assert.Contains(t, body, ">main<")
}

func TestDeps_marksEachImportWithItsReferenceCount(t *testing.T) {
	t.Parallel()
	e := serve(t)

	status, body := e.app.Get("/deps")
	require.Equal(t, http.StatusOK, status)
	// The root package shows as the module's last element.
	assert.Contains(t, body, "calc → calc: 3 references", "main calls Add, Scale and Moved")
}

func TestReview_listsUnitsByLaneWithTheirReasons(t *testing.T) {
	t.Parallel()
	e := serve(t)

	status, body := e.app.Get("/review")
	require.Equal(t, http.StatusOK, status)
	for _, want := range []string{"Contract", "Logic", "Tests", "Noise", "Clamp", "no direct test", "0 of 10 reviewed"} {
		assert.Contains(t, body, want)
	}
}

func TestReview_markRecordsTheUnitAsReviewed(t *testing.T) {
	t.Parallel()
	e := serve(t)
	first := e.src.Current().Review.Units[0]
	conn := e.app.ConnectAt("/review", "{}")
	t.Cleanup(conn.Close)
	_, page := e.app.Get("/review")
	actions := actionURL.FindAllStringSubmatch(page, -1)
	require.Greater(t, len(actions), 1)

	// Action 0 is Rescan; the first unit's mark follows.
	status, _ := e.app.ChildAction("r", 1).Over(conn).Raw(html.UnescapeString(actions[1][2] + actions[1][1])).Fire()
	// A live page answers 204 and pushes the re-render over its stream.
	require.Equal(t, http.StatusNoContent, status)

	assert.True(t, e.state.Reviewed(first.Key))
	_, body := e.app.Get("/review")
	assert.Contains(t, body, "1 of 10 reviewed")
}

func TestLinks_carryScopeLensAndDeclarationToTheMap(t *testing.T) {
	t.Parallel()
	e := serve(t)
	scale := url.QueryEscape("example.com/calc/calc.Scale")

	_, home := e.app.Get("/")
	_, decl := e.app.Get("/?lens=churn&in=calc/calc.go&d=" + scale)
	_, deps := e.app.Get("/deps")
	_, rev := e.app.Get("/review")

	assert.Contains(t, home, `href="/?lens=smells"`, "crumb to the module")
	assert.Contains(t, decl, `href="/?lens=complexity&amp;in=calc%2Fcalc.go&amp;d=`+scale+`"`, "lens keeps the scope")
	assert.Contains(t, decl, `href="/?lens=churn&amp;in=calc%2Fcalc.go"`, "closing the panel keeps the lens")
	assert.Contains(t, decl, `href="/?lens=churn&amp;in=main.go&amp;d=example.com%2Fcalc.main"`, "caller link")
	assert.Contains(t, deps, `href="/?in=example.com%2Fcalc%2Fcalc"`, "matrix row")
	assert.Contains(t, rev, `href="/?in=calc%2Fcalc.go&amp;d=example.com%2Fcalc%2Fcalc.Clamp"`, "unit location")
	assert.Contains(t, rev, `href="/?in=main.go&amp;d=example.com%2Fcalc.main"`, "unit caller")
}

func TestFavicon_answersWithoutContent(t *testing.T) {
	t.Parallel()
	e := serve(t)

	status, _ := e.app.Get("/favicon.ico")
	assert.Equal(t, http.StatusNoContent, status)
}

func TestReview_linksCallersChangedInTheSameDiffToTheirCard(t *testing.T) {
	t.Parallel()
	e := serve(t)

	_, body := e.app.Get("/review")
	assert.Regexp(t, `<a class="changed" href="#u[0-9a-f]+" title="Changed in this diff">calc.TestClamp`, body)
}

func TestReview_foldsLanesThatRarelyNeedReading(t *testing.T) {
	t.Parallel()
	e := serve(t)

	_, body := e.app.Get("/review")
	assert.Contains(t, body, "Show diff · ", "Tests, Other and Noise cards wait to be opened")
	assert.Contains(t, body, ">\treturn helper(x, 1) * k<", "a Logic diff renders unasked")
}

func TestReview_capsLongLanesAtTheRiskiestUnits(t *testing.T) {
	t.Parallel()
	var many strings.Builder
	many.WriteString("package calc\n")
	for i := range 70 {
		fmt.Fprintf(&many, "\nfunc f%d() int { return %d }\n", i, i)
	}
	app := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": many.String()}).app

	_, body := app.Get("/review")
	assert.Contains(t, body, "Show 10 more logic units")
	assert.Equal(t, 60, strings.Count(body, `<article class="unit"`))
}

func TestFrame_warnsWhenAPackageDoesNotTypeCheck(t *testing.T) {
	t.Parallel()
	app := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/broken.go": "package calc\n\nfunc Broken() int { return undefined }\n"}).app

	_, body := app.Get("/")
	assert.Contains(t, body, "1 package did not type-check")
	assert.Contains(t, body, "undefined: undefined")
}

func TestMap_givesTheHottestDeclarationTheHottestColour(t *testing.T) {
	t.Parallel()
	e := serveRepo(t, map[string]string{
		"go.mod":  "module example.com/hot\n\ngo 1.27\n",
		"p/a.go":  "package p\n\nfunc A(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		"p/b.go":  "package p\n\nfunc B() int { return 2 }\n",
		"main.go": "package main\n\nimport \"example.com/hot/p\"\n\nfunc main() { println(p.A(1), p.B()) }\n",
	}, nil)
	// b.go churns, a.go is complex: no single file is both.
	dir := e.src.Dir
	for i := range 3 {
		require.NoError(t, testrepo.Write(dir, map[string]string{"p/b.go": fmt.Sprintf("package p\n\nfunc B() int { return %d }\n", i)}))
		require.NoError(t, testrepo.Git(dir, "commit", "-qam", "churn"))
	}
	e.src.Refresh()

	_, body := e.app.Get("/?lens=hotspot")
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Regexp(t, `\["d",[^\]]*"B","",5,"example.com/hot/p.B"\]`, body, "the hottest declaration sets the scale")
}

func TestReview_feedsTheAtlasIslandItsLayoutAndMarks(t *testing.T) {
	t.Parallel()
	e := serve(t)
	scale := ""
	for _, u := range e.src.Current().Review.Units {
		if u.Name == "Scale" {
			scale = u.Key
		}
	}
	require.NoError(t, e.state.Set(scale, true))

	_, body := e.app.Get("/review")
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Contains(t, body, `data-ignore-morph`)
	assert.Regexp(t, `data-effect="codemesh.atlas\(el, \$_atlas, \{reviewed: \$_reviewed, focus: \$_focus\}\)"`, body)
	assert.Regexp(t, `\["p",[^\]]*"calc","",0,""\]`, body, "a package tile")
	assert.Regexp(t, `\["d",[^\]]*"Scale","u[0-9a-f]+",0,""\]`, body, "Scale is lit with its card id")
	assert.Regexp(t, `data-signals:_reviewed="\["u[0-9a-f]+"\]"`, body, "the reviewed card")
	assert.Contains(t, body, `src="/_codemesh/d3.min.js?v=`)
}

func TestAssets_serveD3WithAYearOfCaching(t *testing.T) {
	t.Parallel()
	e := serve(t)

	resp, err := e.app.Client().Get(e.app.URL() + "/_codemesh/d3.min.js")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "public, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))
}

func manyParams(n int) string {
	var b strings.Builder
	b.WriteString("package calc\n")
	for i := range n {
		fmt.Fprintf(&b, "\nfunc F%d(a, b, c, d, e, f int) int { return a + b + c + d + e + f }\n", i)
	}
	return b.String()
}

func TestMap_pagesASmellsListPastSixty(t *testing.T) {
	t.Parallel()
	app := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": manyParams(130)}).app
	conn := app.Connect()
	t.Cleanup(conn.Close)

	_, body := app.Get("/?in=example.com/calc/calc")
	assert.Equal(t, 60, strings.Count(body, `<li class="finding">`))
	require.Contains(t, body, "Show 60 more smells")

	status, _ := app.Action(0).Over(conn).Fire()
	require.Equal(t, http.StatusNoContent, status)
	assert.NotContains(t, conn.Await("Smells · "), "Show 60 more smells", "the second page leaves fewer than 60")
}

func TestMap_namesWhereEachDeclarationSmellIs(t *testing.T) {
	t.Parallel()
	app := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": manyParams(1)}).app

	_, body := app.Get("/")
	assert.Contains(t, body, ">calc/many.go:3<")
}

func TestMap_scopesToAPackageByItsDirectory(t *testing.T) {
	t.Parallel()
	e := serve(t)

	_, byPath := e.app.Get("/?in=example.com/calc/calc")
	_, byDir := e.app.Get("/?in=calc")
	assert.Contains(t, byDir, `<span class="sep">/</span><a href="/?lens=smells&amp;in=example.com%2Fcalc%2Fcalc">calc</a>`)
	smells := regexp.MustCompile(`Smells · \d+`)
	assert.Equal(t, smells.FindString(byPath), smells.FindString(byDir))
}

func TestFrame_namesNestedModulesTheMapLeavesOut(t *testing.T) {
	t.Parallel()
	app := serveRepo(t, testrepo.CalcBase, map[string]string{
		"tools/go.mod": "module example.com/tools\n\ngo 1.27\n",
		"tools/t.go":   "package tools\n",
	}).app

	_, home := app.Get("/")
	_, deps := app.Get("/deps")
	_, rev := app.Get("/review")
	for _, body := range []string{home, deps} {
		assert.Contains(t, body, "The nested module tools is not mapped. Run codemesh in tools to map it.")
	}
	assert.NotContains(t, rev, "is not mapped", "the review lists nested module files")
}
