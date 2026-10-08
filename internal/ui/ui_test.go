package ui_test

import (
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
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

func serve(t *testing.T) env {
	t.Helper()
	dir := testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead)
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
	assert.Contains(t, body, ">calc<", "the calc package frame")
	assert.Contains(t, body, ">calc.go<", "its file inside the frame")
	assert.Contains(t, body, "Findings")
	assert.Contains(t, body, "Unused export")
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
	for _, want := range []string{"Contract", "Logic", "Tests", "Noise", "Clamp", "no direct test", "0 of 9 reviewed"} {
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
	assert.Contains(t, body, "1 of 9 reviewed")
}
