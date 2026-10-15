package ui_test

import (
	"bufio"
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/go-via/via/vt"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/joaomdsg/codemesh/internal/ui"
	"github.com/joaomdsg/codemesh/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actionURL matches an action binding in rendered markup, the way vt does:
// a bound argument rides in the data-via-q attribute before the data-on
// (group 1), the path is group 2. vt reads actions off "/" only, and the
// repo pages live elsewhere.
var actionURL = regexp.MustCompile(`(?:data-via-q-[^=\s]+="([^"]*)" data-on:[^=\s]+="@post\('|@post\(')([^']*_via/a/r/[A-Za-z0-9_-]+(?:[?&][^']*)?)'`)

type env struct {
	app   *vt.App
	a     *app.App
	repo  workspace.Repo // the first repo added
	repos []workspace.Repo
}

// base is the URL prefix of the first repo's pages.
func (e env) base() string { return "/r/" + e.repo.ID }

// mapView is the map page's path for a view, given as key, value pairs.
func mapView(pairs ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		q.Set(pairs[i], pairs[i+1])
	}
	return "/map/" + url.PathEscape(q.Encode())
}

func TestHost_aNameThatIsNotTheOriginsIsRefused(t *testing.T) {
	h := ui.New(newApp(t), "http://localhost:7777")
	get := func(host string) int {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		req.Header.Set("Origin", "http://"+host)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusForbidden, get("evil.example:7777"), "a name pointed at this machine, as in DNS rebinding")
	assert.Equal(t, http.StatusOK, get("localhost:7777"))
	assert.Equal(t, http.StatusOK, get("LOCALHOST:7777"))
	assert.Equal(t, http.StatusOK, get("127.0.0.1:7777"))
	assert.Equal(t, http.StatusOK, get("[::1]:7777"))
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPage_isBrotliCompressedWhenAccepted(t *testing.T) {
	h := ui.New(newApp(t), "http://localhost:7777")
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "localhost:7777"
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "br", rec.Header().Get("Content-Encoding"))
	body, err := io.ReadAll(brotli.NewReader(rec.Body))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(body), "<!doctype html>"))
}

func TestStream_isBrotliCompressedFrameByFrame(t *testing.T) {
	e := serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.app.URL()+"/_via/sse", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept-Encoding", "br")
	resp, err := e.app.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	require.Equal(t, "br", resp.Header.Get("Content-Encoding"))
	// The stream stays open, so the first frame must decode before it ends.
	line, err := bufio.NewReader(brotli.NewReader(resp.Body)).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "event: datastar-patch-signals\n", line)
}

// newApp is an app in its own XDG directories, with a gh that fails.
// t.Setenv rules out t.Parallel, so tests that use it run one at a time.
func newApp(t *testing.T) *app.App {
	t.Helper()
	// XDG_CACHE_HOME moves Go's build cache too, which would start every test cold.
	goCache, err := exec.Command("go", "env", "GOCACHE").Output()
	require.NoError(t, err)
	t.Setenv("GOCACHE", strings.TrimSpace(string(goCache)))
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	testrepo.QuietGo(t)
	fake, err := filepath.Abs("../ghx/testdata/fake-gh")
	require.NoError(t, err)
	bin := t.TempDir()
	require.NoError(t, os.Symlink(fake, filepath.Join(bin, "gh")))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_EXIT", "1")
	a, err := app.New(t.Context(), app.Config{Poll: time.Hour, Log: quiet()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, a.Close(context.Background())) })
	return a
}

func serve(t *testing.T) env { return serveRepo(t, testrepo.CalcBase, testrepo.CalcHead) }

func serveRepo(t *testing.T, base, head map[string]string) env {
	t.Helper()
	return serveDirs(t, testrepo.New(t, base, head))
}

// serveDirs adds each repo directory to a fresh workspace and serves it.
func serveDirs(t *testing.T, dirs ...string) env {
	t.Helper()
	a := newApp(t)
	// Cleanups run last-in first-out: waiting here, after the repo directories
	// exist, lets background jobs finish before those directories are removed.
	t.Cleanup(a.Wait)
	for _, d := range dirs {
		require.NoError(t, a.AddRepo(d))
	}
	repos, err := a.Repos()
	require.NoError(t, err)
	e := env{app: vt.Serve(t, settled(a, ui.New(a, "http://example.com"))), a: a, repos: repos}
	if len(repos) > 0 {
		e.repo = repos[0]
	}
	return e
}

// settled serves a page once what it shows has loaded: while a GET renders
// the loading marker, it waits for the app's loads and asks again. Tests of
// the loading state itself serve ui.New directly.
func settled(a *app.App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || strings.Contains(r.URL.Path, "/_via/") {
			next.ServeHTTP(w, r)
			return
		}
		// The loop reads the body for the marker, so it must come back plain.
		plain := r.Clone(r.Context())
		plain.Header.Del("Accept-Encoding")
		var rec *httptest.ResponseRecorder
		for range 50 {
			rec = httptest.NewRecorder()
			next.ServeHTTP(rec, plain.Clone(plain.Context()))
			if !strings.Contains(rec.Body.String(), `class="analysing"`) {
				break
			}
			a.Wait()
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// ghExits sets the fake gh's exit code and has every repo list its PRs again:
// serving already rendered "/" once, which cached the failing gh's answer.
func (e env) ghExits(t *testing.T, code string) {
	t.Helper()
	t.Setenv("FAKE_GH_EXIT", code)
	e.a.Wait()
	for _, r := range e.repos {
		e.a.RefreshPRs(r.ID)
	}
}

// reviewUnits lists the first repo's working-tree review units.
func (e env) reviewUnits(t *testing.T) []*review.Unit {
	t.Helper()
	wr := e.a.WorkingReview(e.repo.ID, "")
	defer wr.Release()
	require.NoError(t, wr.Err)
	require.NotNil(t, wr.Review, wr.Note)
	return wr.Review.Units
}

func (e env) reviewed(t *testing.T) *review.State {
	t.Helper()
	st, err := e.a.Reviewed(e.repo.ID)
	require.NoError(t, err)
	return st
}

func TestMap_showsEveryPackageWithItsFindings(t *testing.T) {
	e := serve(t)

	status, body := e.app.Get(e.base() + "/")
	require.Equal(t, http.StatusOK, status)
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Regexp(t, `\["p",[^\]]*"calc","",0,"example.com/calc/calc",0\]`, body, "the calc package tile")
	assert.Regexp(t, `\["f",[^\]]*"calc.go","",0,"calc/calc.go",0\]`, body, "its file tile")
	assert.Regexp(t, `data-effect="codemesh.atlas\(el, \$_atlas, \{focus: \$_selected\}\)"`, body)
	assert.Contains(t, body, "Smells · 1")
	assert.Contains(t, body, "Complex function")
}

func TestMap_opensADeclarationWithItsSourceAndCallers(t *testing.T) {
	e := serve(t)

	status, body := e.app.Get(e.base() + mapView("d", "example.com/calc/calc.Scale", "in", "calc/calc.go"))
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "func Scale(x, k int) int {")
	assert.Contains(t, body, "Called by")
	assert.Contains(t, body, ">main<")
}

func TestDeps_marksEachImportWithItsReferenceCount(t *testing.T) {
	e := serve(t)

	status, body := e.app.Get(e.base() + "/deps")
	require.Equal(t, http.StatusOK, status)
	// The root package shows as the module's last element.
	assert.Contains(t, body, "calc → calc: 3 references", "main calls Add, Scale and Moved")
}

func TestReview_listsUnitsByLaneWithTheirReasons(t *testing.T) {
	e := serve(t)

	status, body := e.app.Get(e.base() + "/review")
	require.Equal(t, http.StatusOK, status)
	for _, want := range []string{"Contract", "Logic", "Tests", "Noise", "Clamp", "no direct test", "0 of 10 reviewed"} {
		assert.Contains(t, body, want)
	}
}

func TestReview_markRecordsTheUnitAsReviewed(t *testing.T) {
	e := serve(t)
	first := e.reviewUnits(t)[0]
	conn := e.app.ConnectAt(e.base()+"/review", "{}")
	t.Cleanup(conn.Close)
	_, page := e.app.Get(e.base() + "/review")
	mark := regexp.MustCompile(`<button\b[^>]*title="Mark reviewed"[^>]*>`).FindString(page)
	action := actionURL.FindStringSubmatch(mark)
	require.NotNil(t, action, "the first unit's mark")

	status, _ := e.app.ChildAction("r", 1).Over(conn).Raw(html.UnescapeString(action[2] + action[1])).Fire()
	// A live page answers 204 and pushes the re-render over its stream.
	require.Equal(t, http.StatusNoContent, status)

	assert.True(t, e.reviewed(t).Reviewed(first.Key))
	_, body := e.app.Get(e.base() + "/review")
	assert.Contains(t, body, "1 of 10 reviewed")
}

func TestLinks_carryScopeLensAndDeclarationToTheMap(t *testing.T) {
	e := serve(t)
	b := e.base()
	scale := "example.com/calc/calc.Scale"

	_, home := e.app.Get(b + "/")
	_, decl := e.app.Get(b + mapView("d", scale, "in", "calc/calc.go", "lens", "churn"))
	_, deps := e.app.Get(b + "/deps")
	_, rev := e.app.Get(b + "/review")
	_, thread := e.app.Get(b + "/review/" + url.PathEscape("example.com/calc/calc.Scale"))

	link := func(pairs ...string) string { return `href="` + html.EscapeString(b+mapView(pairs...)) + `"` }
	assert.Contains(t, home, link("lens", "smells"), "crumb to the module")
	assert.Contains(t, decl, link("d", scale, "in", "calc/calc.go", "lens", "complexity"), "lens keeps the scope")
	assert.Contains(t, decl, link("in", "calc/calc.go", "lens", "churn"), "closing the panel keeps the lens")
	assert.Contains(t, decl, link("d", "example.com/calc.main", "in", "main.go", "lens", "churn"), "caller link")
	assert.Contains(t, deps, link("in", "example.com/calc/calc"), "matrix row")
	assert.Contains(t, rev, link("d", "example.com/calc/calc.Clamp", "in", "calc/calc.go"), "unit location")
	assert.Contains(t, thread, link("d", "example.com/calc.main", "in", "main.go"), "caller hop")
}

func TestFavicon_answersWithoutContent(t *testing.T) {
	e := serve(t)

	status, _ := e.app.Get("/favicon.ico")
	assert.Equal(t, http.StatusNoContent, status)
}

func TestReview_linksCallersChangedInTheSameDiffToTheirCard(t *testing.T) {
	e := serve(t)

	_, body := e.app.Get(e.base() + "/review/" + url.PathEscape("example.com/calc/calc.Clamp"))
	assert.Regexp(t, `(?s)class="hop-pr" href="#u[0-9a-f]+">in this change</a>.{0,300}?calc\.TestClamp`, body)
}

func TestReview_foldsLanesThatRarelyNeedReading(t *testing.T) {
	e := serve(t)

	_, body := e.app.Get(e.base() + "/review")
	assert.Contains(t, body, "Show diff · ", "Tests, Other and Noise cards wait to be opened")
	assert.Contains(t, body, ">\treturn helper(x, 1) * k<", "a Logic diff renders unasked")
}

func TestReview_capsLongLanesAtTheRiskiestUnits(t *testing.T) {
	var many strings.Builder
	many.WriteString("package calc\n")
	for i := range 70 {
		fmt.Fprintf(&many, "\nfunc f%d() int { return %d }\n", i, i)
	}
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": many.String()})

	_, body := e.app.Get(e.base() + "/review")
	assert.Contains(t, body, "Show 10 more units (10 left)")
	assert.Equal(t, 60, strings.Count(body, `<article class="unit"`))
}

func TestReview_offersALongLaneOnePageAtATime(t *testing.T) {
	var many strings.Builder
	many.WriteString("package calc\n")
	for i := range 130 {
		fmt.Fprintf(&many, "\nfunc f%d() int { return %d }\n", i, i)
	}
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": many.String()})

	// vt drops SSE lines over 64 KB, so the next page's render is checked
	// in a browser, not here.
	_, body := e.app.Get(e.base() + "/review")
	assert.Contains(t, body, "Show 60 more units (70 left)", "70 more wait, a page at a time")
}

func TestFrame_warnsWhenAPackageDoesNotTypeCheck(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/broken.go": "package calc\n\nfunc Broken() int { return undefined }\n"})

	_, body := e.app.Get(e.base() + "/")
	assert.Contains(t, body, "1 package did not type-check")
	assert.Contains(t, body, "undefined: undefined")
}

func TestFrame_blamesTheParserNotTheCodeForASyntaxError(t *testing.T) {
	e := serveRepo(t, map[string]string{
		"package.json": `{"name":"bad"}`,
		"src/bad.ts":   "export const a = 1;\n\nexport function ok() { return 1; }\nexport function broken( {\n",
	}, nil)

	_, body := e.app.Get(e.base() + "/")

	assert.Contains(t, body, "codemesh could not parse src/bad.ts (line 4), so its callers and smells are missing. The rest of the repo is analysed.")
	assert.NotContains(t, body, "did not type-check")
}

func TestMap_givesTheHottestDeclarationTheHottestColour(t *testing.T) {
	dir := testrepo.New(t, map[string]string{
		"go.mod":  "module example.com/hot\n\ngo 1.27\n",
		"p/a.go":  "package p\n\nfunc A(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		"p/b.go":  "package p\n\nfunc B() int { return 2 }\n",
		"main.go": "package main\n\nimport \"example.com/hot/p\"\n\nfunc main() { println(p.A(1), p.B()) }\n",
	}, nil)
	// b.go churns, a.go is complex: no single file is both.
	for i := range 3 {
		require.NoError(t, testrepo.Write(dir, map[string]string{"p/b.go": fmt.Sprintf("package p\n\nfunc B() int { return %d }\n", i)}))
		require.NoError(t, testrepo.Git(dir, "commit", "-qam", "churn"))
	}
	e := serveDirs(t, dir)

	_, body := e.app.Get(e.base() + mapView("lens", "hotspot"))
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Regexp(t, `\["d",[^\]]*"B","",5,"example.com/hot/p.B",1\]`, body, "the hottest declaration sets the scale")
}

func TestReview_feedsTheAtlasIslandItsLayoutAndMarks(t *testing.T) {
	e := serve(t)
	scale := ""
	for _, u := range e.reviewUnits(t) {
		if u.Name == "Scale" {
			scale = u.Key
		}
	}
	require.NoError(t, e.reviewed(t).Set(scale, true))

	_, body := e.app.Get(e.base() + "/review")
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Contains(t, body, `data-ignore-morph`)
	assert.Regexp(t, `data-effect="codemesh.atlas\(el, \$_atlas, \{reviewed: \$_reviewed, focus: \$_focus, thread: \$_path, side: \$_pane\}\)"`, body)
	assert.Regexp(t, `\["p",[^\]]*"calc","",0,"",0\]`, body, "a package tile")
	assert.Regexp(t, `\["d",[^\]]*"Scale","u[0-9a-f]+",0,"",1\]`, body, "Scale is lit with its card id")
	assert.Regexp(t, `data-signals:_reviewed="\["u[0-9a-f]+"\]"`, body, "the reviewed card")
	assert.Contains(t, body, `src="/_codemesh/d3.min.js?v=`)
}

func TestAssets_serveD3WithAYearOfCaching(t *testing.T) {
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

func TestMap_offersSixtyMoreSmellsAtATime(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": manyParams(130)})

	_, body := e.app.Get(e.base() + mapView("in", "example.com/calc/calc"))

	assert.Equal(t, 60, strings.Count(body, `<li class="finding">`))
	assert.Contains(t, body, "Show 60 more smells (")
}

// The page rewrites the whole list in one stream line, which vt reads only up
// to 64 KiB: with a handoff button on each row, that is about 70 rows.
func TestMap_pagesASmellsListPastSixty(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": manyParams(70)})
	_, body := e.app.Get(e.base() + mapView("in", "example.com/calc/calc"))
	conn := e.app.ConnectAt(e.base()+mapView("in", "example.com/calc/calc"), "{}")
	t.Cleanup(conn.Close)
	conn.Await("data-live") // the frame every connect pushes
	assert.Equal(t, 60, strings.Count(body, `<li class="finding">`))
	require.Contains(t, body, "Show 11 more smells (11 left)")

	actions := actionURL.FindAllStringSubmatch(body, -1)
	require.NotEmpty(t, actions)
	more := actions[len(actions)-1] // after each row's handoff button
	status, _ := e.app.Action(0).Over(conn).Raw(html.UnescapeString(more[2] + more[1])).Fire()
	require.Equal(t, http.StatusNoContent, status)
	assert.NotContains(t, conn.Await("Smells · "), "more smells (", "the second page lists the rest")
}

func TestMap_namesWhereEachDeclarationSmellIs(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/many.go": manyParams(1)})

	_, body := e.app.Get(e.base() + "/")
	assert.Contains(t, body, ", limit 5 · calc/many.go:3<")
}

func TestMap_scopesToAPackageByItsDirectory(t *testing.T) {
	e := serve(t)
	b := e.base()

	_, byPath := e.app.Get(b + mapView("in", "example.com/calc/calc"))
	_, byDir := e.app.Get(b + mapView("in", "calc"))
	assert.Contains(t, byDir, `<span class="sep">/</span><a href="`+html.EscapeString(b+mapView("in", "example.com/calc/calc", "lens", "smells"))+`">calc</a>`)
	smells := regexp.MustCompile(`Smells · \d+`)
	assert.Equal(t, smells.FindString(byPath), smells.FindString(byDir))
}

func TestFrame_offersANestedModuleAsItsOwnRoot(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{
		"tools/go.mod": "module example.com/tools\n\ngo 1.27\n",
		"tools/t.go":   "package tools\n\nfunc T() {}\n",
	})
	b := e.base()

	_, home := e.app.Get(b + "/")
	_, tools := e.app.Get(b + "/in/tools/")
	assert.Contains(t, home, `href="`+b+`/in/tools/"`, "the root switcher")
	assert.Contains(t, home, "Nested module tools/ is a separate root", "the root's map says where the nested module went")
	assert.Contains(t, tools, "example.com/tools")
	assert.Contains(t, tools, `href="`+b+`/in/tools/deps"`, "tabs keep the root")
}

func TestMap_keepsADeclarationWithOnlyInfoSmellsLukewarm(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{
		"internal/x/x.go": "package x\n\nfunc Unused() {}\n",
	})

	_, body := e.app.Get(e.base() + "/")
	body = html.UnescapeString(body) // the feed attribute escapes its JSON
	assert.Regexp(t, `\["d",[^\]]*"Unused","",2,"example.com/calc/internal/x.Unused",1\]`, body,
		"one info smell on one line is dense, yet only info")
}

func TestHealth_marksAPlaceAndOpensItsPrognosis(t *testing.T) {
	e := serve(t)

	status, body := e.app.Get(e.base() + "/health")
	require.Equal(t, http.StatusOK, status)
	body = html.UnescapeString(body)
	assert.Contains(t, body, `"key":"complex:example.com/calc/calc.tangle"`, "tangle is past the complexity limit")
	assert.Contains(t, body, "Hard to follow")

	status, body = e.app.Get(e.base() + "/prognosis/" + url.PathEscape("complex:example.com/calc/calc.tangle"))
	require.Equal(t, http.StatusOK, status)
	for _, want := range []string{"Why it matters", "What to do", "How to check"} {
		assert.Contains(t, body, want)
	}
}

func TestRepoPages_answerNotFoundForAnUnknownRepo(t *testing.T) {
	e := serve(t)

	for _, p := range []string{"/", "/deps", "/health", "/review", "/prs"} {
		status, _ := e.app.Get("/r/nope" + p)
		assert.Equal(t, http.StatusNotFound, status, p)
	}
}

func TestFrame_switchesRepoAndCountsTheBasket(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, nil), testrepo.New(t, testrepo.CalcBase, nil))
	_, empty := e.app.Get(e.base() + "/")
	_, err := e.a.BasketAdd(handoff.Item{Kind: handoff.Finding, Repo: e.repo.ID, Title: "Split Clamp"})
	require.NoError(t, err)

	_, body := e.app.Get(e.base() + "/")

	assert.Contains(t, body, `href="/r/`+e.repos[1].ID+`/"`, "the other repo is one click away")
	assert.Regexp(t, `href="/basket"[^>]*>Basket<span class="count">0</span>`, empty)
	assert.Regexp(t, `href="/basket"[^>]*>Basket<span class="count">1</span>`, body)
}

func TestWorkspace_asksForARepoWhenEmpty(t *testing.T) {
	e := serveDirs(t)

	status, body := e.app.Get("/")
	require.Equal(t, http.StatusOK, status)
	assert.Regexp(t, `(?s)<form class="add".*?</form>[^<]*<p class="hint">Add a repo by path or git URL, e\.g\. ~/src/app or https://github\.com/org/repo</p>`, body, "the hint sits right under the input")
	assert.NotContains(t, body, `class="empty"`, "the hint is not a second, distant line")
}

// pageWith polls path until its body holds every want.
func pageWith(t *testing.T, e env, path string, want ...string) string {
	t.Helper()
	var body string
	require.Eventually(t, func() bool {
		_, body = e.app.Get(path)
		for _, w := range want {
			if !strings.Contains(body, w) {
				return false
			}
		}
		return true
	}, time.Minute, 100*time.Millisecond, "%s never showed %q; last body:\n%s", path, want, body)
	return body
}

func TestWorkspace_listsEachRepoWithItsHealthAndLanguage(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead), testrepo.New(t, testrepo.CalcBase, nil))

	body := pageWith(t, e, "/", "typed", `class="dx-census"`)
	for _, r := range e.repos {
		assert.Contains(t, body, `href="/r/`+r.ID+`/"`, r.Name)
		assert.Contains(t, body, r.Name)
	}
	assert.Regexp(t, `\d+ (fix first|fix soon|when convenient)`, body, "the head tree's complex function")
}

func TestWorkspace_inboxListsADirtyTreeAndOpenPRsAndSkipsACleanTree(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead), testrepo.New(t, testrepo.CalcBase, nil))
	e.ghExits(t, "")

	body := pageWith(t, e, "/", "Needs reading", "Add cache")
	assert.Contains(t, body, `href="/r/`+e.repos[0].ID+`/review"`, "the dirty tree reads as a review")
	assert.NotContains(t, body, `href="/r/`+e.repos[1].ID+`/review"`, "a clean tree has nothing to read")
	assert.Contains(t, body, `href="/r/`+e.repos[0].ID+`/pr/7"`, "a PR row opens its review")
}

func TestWorkspace_addsARepoByPath(t *testing.T) {
	e := serveDirs(t)
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	conn := e.app.Connect()
	t.Cleanup(conn.Close)

	status, _ := e.app.Action(0).Over(conn).Body(`{"input":` + fmt.Sprintf("%q", dir) + `}`).Fire()
	require.Equal(t, http.StatusNoContent, status)

	repos, err := e.a.Repos()
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, filepath.Base(dir), repos[0].Name)
}

func TestWorkspace_clonesAGitURLAndMapsIt(t *testing.T) {
	src := testrepo.New(t, testrepo.CalcBase, nil)
	bare := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, testrepo.Git(src, "clone", "-q", "--bare", src, bare))
	e := serveDirs(t)
	conn := e.app.Connect()
	t.Cleanup(conn.Close)

	status, _ := e.app.Action(0).Over(conn).Body(`{"input":"file://` + bare + `"}`).Fire()
	require.Equal(t, http.StatusNoContent, status)

	require.Eventually(t, func() bool { r, _ := e.a.Repos(); return len(r) == 1 }, time.Minute, 50*time.Millisecond, "the clone never registered")
	repos, _ := e.a.Repos()
	_, body := e.app.Get("/r/" + repos[0].ID + "/")
	assert.Contains(t, html.UnescapeString(body), `"example.com/calc/calc"`, "a clone maps its default branch")
}

func TestWorkspace_removesARepoAndLeavesItsFiles(t *testing.T) {
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	e := serveDirs(t, dir)
	conn := e.app.Connect()
	t.Cleanup(conn.Close)
	_, page := e.app.Get("/")
	actions := actionURL.FindAllStringSubmatch(page, -1)
	require.Len(t, actions, 2, "add, then remove")

	status, _ := e.app.ChildAction("r", 1).Over(conn).Raw(html.UnescapeString(actions[1][2] + actions[1][1])).Fire()
	require.Equal(t, http.StatusNoContent, status)

	repos, err := e.a.Repos()
	require.NoError(t, err)
	assert.Empty(t, repos)
	assert.FileExists(t, filepath.Join(dir, "go.mod"))
}

func TestPRs_listsOpenPullRequestsFromGH(t *testing.T) {
	e := serve(t)
	e.ghExits(t, "")

	body := pageWith(t, e, e.base()+"/prs", "Add cache")
	assert.Contains(t, body, "Fix typo")
	assert.Contains(t, body, `href="`+e.base()+`/pr/7"`)
}

func TestPRs_saysHowToLogInWhenGHIsNotAuthenticated(t *testing.T) {
	e := serve(t)
	e.ghExits(t, "4")

	pageWith(t, e, e.base()+"/prs", "gh auth login")
}

func TestPRs_saysWhyThereAreNoneWhenGHFails(t *testing.T) {
	e := serve(t)

	pageWith(t, e, e.base()+"/prs", "Pull requests are not available")
}

func complexFuncs(n int) string {
	var b strings.Builder
	b.WriteString("package calc\n")
	for i := range n {
		fmt.Fprintf(&b, "\nfunc Twist%d(x int) int {\n", i)
		for j := range 11 {
			fmt.Fprintf(&b, "\tif x == %d {\n\t\treturn %d\n\t}\n", j+2, j)
		}
		b.WriteString("\treturn 0\n}\n")
	}
	return b.String()
}

func TestHealth_titlesEachRowWithItsSymbolAndSaysASharedReasonOnce(t *testing.T) {
	e := serveRepo(t, testrepo.CalcBase, map[string]string{"calc/twist.go": complexFuncs(3)})

	_, body := e.app.Get(e.base() + "/health")

	list := regexp.MustCompile(`(?s)<aside class="dx-side".*?</aside>`).FindString(body)
	for _, name := range []string{"Twist0", "Twist1", "Twist2"} {
		assert.Contains(t, list, `class="dx-row-title">`+name+`<`, "the symbol is the title")
	}
	assert.Equal(t, 1, strings.Count(list, "Hard to follow"), "three rows share one reason, said once above the list")
	assert.Regexp(t, `<h2>Start here</h2><p class="hint dx-why">Hard to follow</p><ul`, list)
}

func TestHealth_keepsALoneRowsReasonOnTheRow(t *testing.T) {
	e := serve(t)

	_, body := e.app.Get(e.base() + "/health")

	assert.Contains(t, body, `class="dx-row-title">tangle<`)
	assert.Contains(t, body, `class="dx-row-why">Hard to follow<`, "the reason is the dim line under the symbol")
	assert.NotContains(t, body, `dx-why"`, "one row has no reason to share")
}

func TestWorkspace_aPRRowIsOneScannableLine(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, nil))
	e.ghExits(t, "")

	body := pageWith(t, e, "/", "Add cache")

	assert.Regexp(t, `<a class="title-link" href="/r/`+e.repo.ID+`/pr/7">Add cache</a><span class="by"> · ana</span>`, body, "the title is the link, with its author")
	assert.Regexp(t, `class="inbox-lines">(?:<span class="(?:add|del)">[^<]*</span>)+</span>`, body, "the lines sit on one nowrap line, with no unit count until the change is read")
	assert.NotContains(t, body, "0 units", "a change not read yet has no unit count to show")
	assert.NotContains(t, body, `<a class="inbox-row"`, "a row is not an anchor around anchors")
}

func TestWorkspace_showsReviewProgressAsABarAndPlainText(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead))

	body := pageWith(t, e, "/", `class="progress"`)

	assert.Regexp(t, `<meter class="progress"[^>]*></meter><span class="hint">0/10</span>`, body)
}

func TestWorkspace_putsTheReposBesideTheInbox(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead))

	body := pageWith(t, e, "/", `class="ws-grid"`)

	assert.Regexp(t, `(?s)<div class="ws-grid"><section>.*?Inbox.*?</section><aside[^>]*>.*?Repos.*?</aside></div>`, body)
}

func TestLoading_aColdPageShowsASkeletonAndPushesTheMapWhenItLands(t *testing.T) {
	e := serve(t)
	cold := vt.Serve(t, ui.New(e.a, "http://example.com"))

	_, body := cold.Get(e.base() + "/")
	assert.Contains(t, body, `class="map-layout skeleton"`)
	assert.Contains(t, body, "Analysing…")
	conn := cold.ConnectAt(e.base()+"/", "{}")
	t.Cleanup(conn.Close)
	e.a.Wait()

	assert.NotContains(t, conn.Await(`class="map-pane"`), "Analysing…")
}

func TestLoading_aPageConnectingAfterItsAnalysisLandedGetsTheMap(t *testing.T) {
	e := serve(t)
	cold := vt.Serve(t, ui.New(e.a, "http://example.com"))

	_, body := cold.Get(e.base() + "/")
	require.Contains(t, body, "skeleton")
	e.a.Wait()
	conn := cold.ConnectAt(e.base()+"/", "{}")
	t.Cleanup(conn.Close)

	conn.Await(`class="map-pane"`)
}

func TestWorkspace_aLocalRepoGetsItsHealthFromTheInboxWithoutBeingOpened(t *testing.T) {
	e := serve(t)

	_, body := e.app.Get("/")
	assert.Contains(t, body, "Not analysed yet.")
	assert.NotContains(t, body, "Open it to analyse.", "the inbox analyses a local repo's working tree")

	e.a.Wait()
	_, body = e.app.Get("/")
	assert.NotContains(t, body, "Not analysed yet.")
}

func TestShell_theStampSaysWhenTheShownSnapshotWasAnalysed(t *testing.T) {
	e := serve(t)
	cold := vt.Serve(t, ui.New(e.a, "http://example.com"))

	_, body := cold.Get(e.base() + "/")
	require.Contains(t, body, "skeleton")
	assert.NotContains(t, body, "analysed ", "nothing is analysed yet")

	e.a.Wait()
	an := e.a.Cached(e.repo.ID, "", "")
	defer an.Release()
	require.NotNil(t, an.Snap)
	_, body = cold.Get(e.base() + "/")
	assert.Contains(t, body, "analysed "+an.At.Format("15:04:05"))
}
