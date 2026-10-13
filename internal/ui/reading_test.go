package ui_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-via/via/vt"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/notes"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	helperID = "example.com/calc/calc.helper"
	scaleID  = "example.com/calc/calc.Scale"
	mainID   = "example.com/calc.main"
)

// servePR serves a repo whose feat branch is open pull request 7 against
// main: CalcHead plus a wide function, so the PR introduces two smells.
// gh lists it with the real SHAs. It returns the PR's head SHA.
func servePR(t *testing.T) (env, string) {
	t.Helper()
	dir := testrepo.New(t, testrepo.CalcBase, nil)
	g := func(args ...string) { require.NoError(t, testrepo.Git(dir, args...)) }
	git, err := gitx.Open(dir)
	require.NoError(t, err)
	sha := func(rev string) string {
		s, err := git.ResolveRef(rev)
		require.NoError(t, err)
		return s
	}
	g("checkout", "-q", "-b", "feat")
	require.NoError(t, testrepo.Write(dir, testrepo.CalcHead))
	require.NoError(t, testrepo.Write(dir, map[string]string{"calc/wide.go": manyParams(1)}))
	g("add", "-A")
	g("commit", "-qm", "feat")
	head := sha("feat")
	g("update-ref", "refs/pull/7/head", head)
	g("checkout", "-q", "main")
	g("remote", "add", "origin", dir)

	e := serveDirs(t, dir)
	script := fmt.Sprintf(`#!/bin/sh
cat <<'JSON'
[{"number":7,"title":"Clamp it","author":{"login":"ana"},"headRefName":"feat","baseRefName":"main",
"headRefOid":%q,"baseRefOid":%q,"url":"https://github.com/o/r/pull/7","isDraft":false,
"updatedAt":"2026-05-01T10:30:00Z","changedFiles":3,"additions":40,"deletions":5,"reviewDecision":""}]
JSON
`, head, sha("main"))
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return e, head
}

// thread is the path segment that opens the thread of decl IDs, root first.
func thread(ids ...string) string { return url.PathEscape(strings.Join(ids, "~")) }

// page is a reading page open in a browser: its first render, and a live
// stream the actions run over.
type page struct {
	t    *testing.T
	e    env
	path string
	conn *vt.Conn
	last string // the latest frame seen, when the page has changed since it loaded
}

// openPage connects a stream to path, with the thread segment when there is
// one. The page and its stream are two instances of the same URL.
func (e env) openPage(t *testing.T, path, seg string) *page {
	t.Helper()
	if seg != "" {
		path += "/" + seg
	}
	conn := e.app.ConnectAt(path, "{}")
	t.Cleanup(conn.Close)
	return &page{t: t, e: e, path: path, conn: conn}
}

// seen waits for the stream to draw needle, and reads the page from that frame on.
func (p *page) seen(needle string) {
	p.t.Helper()
	p.last = p.conn.Await(needle)
}

// html is the page as the stream last drew it, or as a fresh load shows it.
func (p *page) html() string {
	p.t.Helper()
	if p.last != "" {
		return p.last
	}
	status, body := p.e.app.Get(p.path)
	require.Equal(p.t, http.StatusOK, status, p.path)
	return body
}

var controlTag = regexp.MustCompile(`(?s)<(?:button|form)\b[^>]*>`)

// press fires the action of the first control whose opening tag holds label,
// posting extra as the page's signals the way the browser does.
func (p *page) press(label string, extra ...string) {
	p.t.Helper()
	var body strings.Builder
	body.WriteString("{")
	for i := 0; i+1 < len(extra); i += 2 {
		if i > 0 {
			body.WriteString(",")
		}
		if v := extra[i+1]; v == "true" || v == "false" {
			fmt.Fprintf(&body, "%q:%s", extra[i], v)
		} else {
			fmt.Fprintf(&body, "%q:%q", extra[i], v)
		}
	}
	body.WriteString("}")
	for _, c := range controlTag.FindAllString(p.html(), -1) {
		m := actionURL.FindStringSubmatch(c)
		if m == nil || !strings.Contains(c, label) {
			continue
		}
		status, _ := p.e.app.ChildAction("r", 1).Over(p.conn).Raw(html.UnescapeString(m[2] + m[1])).Body(body.String()).Fire()
		require.Equal(p.t, http.StatusNoContent, status, label)
		return
	}
	p.t.Fatalf("no control with an action holds %q on %s", label, p.path)
}

// urlAfter waits for the address the page tells the browser to show, and
// returns the pushed line.
func (p *page) urlAfter(parts ...string) string {
	p.t.Helper()
	line := p.conn.Await(`"url":"`)
	for _, part := range parts {
		require.Contains(p.t, line, part)
	}
	return line
}

func TestReading_prPageReadsTheChangeAsChaptersWithItsGraphDiff(t *testing.T) {
	e, head := servePR(t)

	status, body := e.app.Get(e.base() + "/pr/7")
	require.Equal(t, http.StatusOK, status)

	assert.Contains(t, body, "Clamp it", "the PR's title")
	assert.Contains(t, body, head[:7], "the head SHA, abbreviated")
	assert.Regexp(t, `decls \+\d+`, body, "graph diff: declarations added")
	assert.Regexp(t, `edges \+\d+`, body, "graph diff: edges added")
	assert.Contains(t, body, "Smells: 2 new", "tangle and F0")
	for _, lane := range []string{"Contract", "Logic", "Tests"} {
		assert.Contains(t, body, lane)
	}
	assert.Contains(t, body, ">Clamp<", "a unit's name")
	assert.Regexp(t, `class="why"[^>]*>Read first: `, body, "the why-read-first line")
	assert.Regexp(t, `class="chip[^"]*">\d+ callers?( \(\d+ in tests\))?<`, body, "the callers chip")
}

func TestReading_workingTreeAndPRShareOnePage(t *testing.T) {
	e := serve(t)

	status, body := e.app.Get(e.base() + "/review")
	require.Equal(t, http.StatusOK, status)

	assert.Contains(t, body, "Working tree against main")
	assert.Contains(t, body, `class="thread-url"`, "the page carries its URL state")
	assert.Contains(t, body, "0 of 10 reviewed")
}

func TestReading_theThreadInTheURLOpensItsHops(t *testing.T) {
	e, _ := servePR(t)

	status, body := e.app.Get(e.base() + "/pr/7/" + thread(helperID, scaleID))
	require.Equal(t, http.StatusOK, status)

	bar := regexp.MustCompile(`(?s)<nav class="thread-bar".*?</nav>`).FindString(body)
	require.NotEmpty(t, bar, "the thread bar")
	assert.Contains(t, bar, ">helper<")
	assert.Contains(t, bar, ">Scale<")
	assert.Regexp(t, `(?s)class="dl dl-call".*?return helper\(x, 1\) \* k`, body, "Scale calls helper on this line, lit")
	assert.Regexp(t, `(?s)class="dl dl-call".*?calc\.Scale\(2, 3\)`, body, "main calls Scale, one hop deeper")
	assert.Regexp(t, `class="hop-pr" href="#u[0-9a-f]+"`, body, "Scale is itself a unit of this PR")
	assert.Contains(t, body, "main.go:5", "file:line of the call")
	assert.Contains(t, body, `"url":"/r/`, "the address signal carries the page's address")
}

func TestReading_expandingCallersStartsAThreadAtTheUnit(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", "")

	p.press("Show callers of helper")

	p.urlAfter("/pr/7/" + url.PathEscape(helperID))
}

func TestReading_followingAHopExtendsTheThread(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", thread(helperID))

	p.press("Follow Scale")

	p.urlAfter(url.PathEscape(helperID + "~" + scaleID))
}

func TestReading_aThreadSurvivesATildeInTheModulePath(t *testing.T) {
	e := serveRepo(t,
		map[string]string{"go.mod": "module example.com/box~v2\n\ngo 1.27\n", "box.go": "package box\n\nfunc Size() int { return 1 }\n\nfunc Use() int { return Size() }\n"},
		map[string]string{"box.go": "package box\n\nfunc Size() int { return 2 }\n\nfunc Use() int { return Size() }\n"})
	p := e.openPage(t, e.base()+"/review", "")

	p.press("Show callers of Size")

	m := regexp.MustCompile(`"url":"([^"]+)"`).FindStringSubmatch(p.urlAfter())
	require.NotNil(t, m)
	status, body := e.app.Get(m[1])
	require.Equal(t, http.StatusOK, status, m[1])
	assert.Contains(t, body, "Trim the thread to Size", "the address opens the thread it was made for")
}

func TestReading_clickingAPillTrimsTheThreadToIt(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", thread(helperID, scaleID, mainID))

	p.press("Trim the thread to Scale")

	line := p.urlAfter(url.PathEscape(helperID + "~" + scaleID))
	assert.NotContains(t, line, url.PathEscape(mainID))
}

func TestReading_keepingAThreadPutsItsPathInTheBasket(t *testing.T) {
	e, head := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", thread(helperID, scaleID))

	p.press("Keep this thread")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, handoff.Thread, items[0].Kind)
	assert.Equal(t, e.repo.ID, items[0].Repo)
	assert.Equal(t, "pr:7@"+head, items[0].Scope)
	assert.Contains(t, items[0].Title, "helper")
	assert.Regexp(t, `^calc\.helper ← calc\.Scale · calc/calc\.go:\d+$`, strings.Split(items[0].Body, "\n")[0], "each hop, by name, with the line it calls from")
	assert.Contains(t, items[0].Where, "calc/calc.go:")
	assert.Equal(t, "Blast radius: calc.helper → calc.Scale", items[0].Title, "the whole path, so threads from one root differ")
	assert.NotEmpty(t, items[0].Code, "the root's source")
	assert.NotEmpty(t, items[0].Steps, "what to do with the hops")
	assert.NotEmpty(t, items[0].Check)
}

func TestReading_aThreadKeptBeforeAnyHopStillSaysWhoCallsTheRoot(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", thread(helperID))

	p.press("Keep this thread")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, "Blast radius: calc.helper", items[0].Title)
	assert.Contains(t, items[0].Body, "1 caller", "no hops yet, so the root's own callers")
}

func TestReading_aThreadKeptOnTheWorkingTreeHasThatScope(t *testing.T) {
	e := serve(t)
	p := e.openPage(t, e.base()+"/review", thread(helperID))

	p.press("Keep this thread")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, "wt", items[0].Scope)
}

// lineToken finds the note anchor of the diff line holding text.
func lineToken(t *testing.T, body, text string) string {
	t.Helper()
	m := regexp.MustCompile(`data-ln="([^"]+)"[^>]*>\d+</span><span class="op">\+</span><span class="tx">` + regexp.QuoteMeta(text)).FindStringSubmatch(body)
	require.NotNil(t, m, "no added line %q with a note anchor", text)
	return html.UnescapeString(m[1])
}

func TestReading_aMarginNoteIsSavedAndJoinsTheHandoff(t *testing.T) {
	e, head := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", "")
	token := lineToken(t, p.html(), "\treturn helper(x, 1) * k")

	p.press("Start a note on a line", "anchor", token)
	p.seen(`class="note-form"`)
	p.press("Save the note", "noteText", "Why 1 and not a parameter?", "inHandoff", "true")

	ns, err := e.a.Notes(e.repo.ID)
	require.NoError(t, err)
	all := ns.All()
	require.Len(t, all, 1)
	assert.Equal(t, "calc/calc.go", all[0].File)
	assert.Equal(t, "+", all[0].Side)
	assert.Equal(t, "pr:7@"+head, all[0].Scope)
	assert.Equal(t, "Why 1 and not a parameter?", all[0].Body)
	assert.Equal(t, 13, all[0].Line)
	assert.NotEmpty(t, all[0].UnitKey)
	items := e.a.Basket().Items
	require.Len(t, items, 1, "a note starts in the handoff")
	assert.Equal(t, handoff.Note, items[0].Kind)
	assert.Equal(t, all[0].ID, items[0].Key)
	assert.Equal(t, "calc/calc.go:13", items[0].Where)
	assert.Equal(t, []string{"\treturn helper(x, 1) * k"}, items[0].Code)
	assert.Empty(t, items[0].Body, "a one-line note is its title; the card and the prompt need not say it twice")
	p.seen("Why 1 and not a parameter?")
}

// noteOnScale writes a note on the PR page's changed line and returns it, as
// stored.
func noteOnScale(t *testing.T, e env, body string) notes.Note {
	t.Helper()
	p := e.openPage(t, e.base()+"/pr/7", "")
	p.press("Start a note on a line", "anchor", lineToken(t, p.html(), "\treturn helper(x, 1) * k"))
	p.seen(`class="note-form"`)
	p.press("Save the note", "noteText", body, "inHandoff", "false")
	ns, err := e.a.Notes(e.repo.ID)
	require.NoError(t, err)
	all := ns.All()
	return all[len(all)-1]
}

// movedTo stores n again under scope, as if it had been written there, and
// drops the original.
func movedTo(t *testing.T, e env, n notes.Note, scope string, edit func(*notes.Note)) {
	t.Helper()
	ns, err := e.a.Notes(e.repo.ID)
	require.NoError(t, err)
	require.NoError(t, ns.Delete(n.ID))
	n.Scope = scope
	edit(&n)
	_, err = ns.Add(n)
	require.NoError(t, err)
}

func TestReading_aPRNoteSurvivesANewPush(t *testing.T) {
	e, _ := servePR(t)
	n := noteOnScale(t, e, "SURVIVOR")
	movedTo(t, e, n, "pr:7@"+strings.Repeat("a", 40), func(*notes.Note) {})

	_, body := e.app.Get(e.base() + "/pr/7")

	assert.Contains(t, body, "SURVIVOR", "written on an earlier head of this PR, its unit has not changed")
	assert.NotContains(t, body, "outdated")
}

func TestReading_aPRNoteOfAnotherPRIsNotShown(t *testing.T) {
	e, _ := servePR(t)
	n := noteOnScale(t, e, "NEIGHBOUR")
	movedTo(t, e, n, "pr:70@"+strings.Repeat("a", 40), func(*notes.Note) {})

	_, body := e.app.Get(e.base() + "/pr/7")

	assert.NotContains(t, body, "NEIGHBOUR")
}

func TestReading_aNoteOnAChangedUnitIsOutdatedAfterANewPush(t *testing.T) {
	e, _ := servePR(t)
	n := noteOnScale(t, e, "STALE")
	movedTo(t, e, n, "pr:7@"+strings.Repeat("a", 40), func(n *notes.Note) { n.UnitKey = "an older version of Scale" })

	_, body := e.app.Get(e.base() + "/pr/7")

	assert.Contains(t, body, "STALE")
	assert.Contains(t, body, "outdated")
}

func TestReading_aNoteSavedOnALineKeepsItsText(t *testing.T) {
	e, _ := servePR(t)

	n := noteOnScale(t, e, "text kept")

	assert.Equal(t, "\treturn helper(x, 1) * k", n.Text)
}

func TestReading_aNoteOnACallerHopIsOutdatedOnceItsLineChanges(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", thread(helperID, scaleID, mainID))
	p.press("Note on the last hop")
	p.seen(`class="note-form"`)
	p.press("Save the note", "noteText", "HOP-NOTE", "inHandoff", "false")
	ns, err := e.a.Notes(e.repo.ID)
	require.NoError(t, err)
	n := ns.All()[0]
	require.Contains(t, n.Text, "Scale", "the call line, as it was")

	_, fresh := e.app.Get(e.base() + "/pr/7/" + thread(helperID, scaleID, mainID))
	assert.Contains(t, fresh, "HOP-NOTE")
	assert.NotContains(t, fresh, "outdated")

	movedTo(t, e, n, n.Scope, func(n *notes.Note) { n.Text = "the line before the push" })
	_, moved := e.app.Get(e.base() + "/pr/7/" + thread(helperID, scaleID, mainID))
	assert.Contains(t, moved, "outdated", "line 5 reads differently than when the note was written")
}

func TestReading_aNoteInTheHandoffKeepsTheLineItWasWrittenOn(t *testing.T) {
	e, _ := servePR(t)
	n := noteOnScale(t, e, "look here")
	movedTo(t, e, n, n.Scope, func(n *notes.Note) { n.Text = "the line then" })
	p := e.openPage(t, e.base()+"/pr/7", "")

	p.press("Add this note to the handoff")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, []string{"the line then"}, items[0].Code, "as written, not as the file reads now")
}

func TestReading_aNoteOnTheOldSideSaysSo(t *testing.T) {
	e, _ := servePR(t)
	n := noteOnScale(t, e, "was this needed")
	movedTo(t, e, n, n.Scope, func(n *notes.Note) { n.Side, n.Text = "-", "the old line" })
	p := e.openPage(t, e.base()+"/pr/7", "")

	p.press("Add this note to the handoff")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, []string{"the old line"}, items[0].Code)
	assert.Contains(t, items[0].Body, "before the change", "the line number counts the old file")
}

func TestReading_theInHandoffToggleAddsAndDropsANotesItem(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", "")
	p.press("Start a note on a line", "anchor", lineToken(t, p.html(), "\treturn helper(x, 1) * k"))
	p.seen(`class="note-form"`)
	p.press("Save the note", "noteText", "Check the bound", "inHandoff", "false")
	require.Empty(t, e.a.Basket().Items, "left out of the handoff when the box was unticked")
	p.seen("Add this note to the handoff")

	p.press("Add this note to the handoff")
	assert.Len(t, e.a.Basket().Items, 1)
	p.seen("Take this note out of the handoff")

	p.press("Take this note out of the handoff")
	assert.Empty(t, e.a.Basket().Items)
}

func TestReading_aNoteAnchorsToTheCallLineOfAHop(t *testing.T) {
	e, head := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", thread(helperID, scaleID, mainID))

	p.press("Note on the last hop")
	p.seen(`class="note-form"`)
	p.press("Save the note", "noteText", "main ignores the result", "inHandoff", "true")

	ns, err := e.a.Notes(e.repo.ID)
	require.NoError(t, err)
	all := ns.All()
	require.Len(t, all, 1)
	assert.Equal(t, "main.go", all[0].File, "the last hop is main, calling Scale")
	assert.Equal(t, 5, all[0].Line, "on the line that makes the call")
	assert.Equal(t, mainID, all[0].DeclID)
	assert.Equal(t, "pr:7@"+head, all[0].Scope)
}

func TestReading_introducedSmellsGoToTheHandoffOneByOneOrAllAtOnce(t *testing.T) {
	e, head := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", "")

	p.press("Add tangle to the handoff")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, handoff.Introduced, items[0].Kind)
	assert.Equal(t, "pr:7@"+head, items[0].Scope)
	assert.Contains(t, items[0].Title, "tangle")
	assert.Regexp(t, `^calc/calc\.go:\d+$`, items[0].Where)
	assert.NotEmpty(t, items[0].Code, "the smell's declaration, as a finding's item carries it")
	assert.NotContains(t, items[0].Check, "analysed again", "an agent cannot run codemesh")

	p.seen("In handoff")
	p.press("Add every introduced smell to the handoff")

	titles := map[string]bool{}
	for _, it := range e.a.Basket().Items {
		titles[it.Title] = true
	}
	assert.Len(t, e.a.Basket().Items, 2, "tangle was already in, so only F0 joins")
	assert.Len(t, titles, 2, "tangle and F0, each once")
}

func TestReading_theAtlasPaneTogglesBetweenBaseAndHead(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", "")
	headAtlas := html.UnescapeString(p.html())
	require.Regexp(t, `\["d",[^\]]*"Clamp","u[0-9a-f]+"`, headAtlas, "head: Clamp is lit")
	require.NotRegexp(t, `\["d",[^\]]*"Old"`, headAtlas, "head: Old is gone")

	p.press("Show the base side of the atlas")

	base := html.UnescapeString(p.conn.Await(`&#34;Old&#34;`))
	assert.Regexp(t, `\["d",[^\]]*"Old","u[0-9a-f]+"`, base, "base: the removed Old is lit")
}

func TestReading_theThreadDrawsAPathAcrossTheAtlas(t *testing.T) {
	e, _ := servePR(t)

	_, body := e.app.Get(e.base() + "/pr/7/" + thread(helperID, scaleID))

	body = html.UnescapeString(body)
	assert.Regexp(t, `data-signals:_path="\[\[[\d.]+,[\d.]+\],\[[\d.]+,[\d.]+\]\]"`, body, "two tile centres, helper then Scale")
	assert.Regexp(t, `codemesh\.atlas\(el, \$_atlas, \{[^}]*thread: \$_path`, body)
}

// gitLines is git's own count of what the PR changes, summed over its files.
func gitLines(t *testing.T, dir, base, head string) (added, deleted int) {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "diff", "--numstat", base, head).Output()
	require.NoError(t, err)
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var a, d int
		_, err := fmt.Sscan(l, &a, &d)
		require.NoError(t, err)
		added, deleted = added+a, deleted+d
	}
	return added, deleted
}

func TestReading_theInboxAndThePRPageCountTheChangeTheSameWay(t *testing.T) {
	e, head := servePR(t)
	added, deleted := gitLines(t, e.repo.Path, "main", head)
	require.NotEqual(t, 40, added, "gh's stub count must differ from git's for this test to tell them apart")
	e.a.Reading(e.repo.ID, 7)
	e.a.Wait()
	want := fmt.Sprintf(`>+%d</span><span class="del">−%d<`, added, deleted)

	_, inbox := e.app.Get("/")
	_, page := e.app.Get(e.base() + "/pr/7")

	assert.Contains(t, inbox, want, "the inbox row")
	assert.Contains(t, page, want, "the PR page header")
}

func TestReading_theHeaderSaysWhichRootItCountsAndLinksTheOthers(t *testing.T) {
	dir := testrepo.New(t, map[string]string{
		"go.mod":       "module example.com/calc\n\ngo 1.27\n",
		"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
		"tools/go.mod": "module example.com/tools\n\ngo 1.27\n",
		"tools/t.go":   "package tools\n\nfunc T() int { return 1 }\n",
	}, map[string]string{
		"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return b + a }\n",
		"tools/t.go":   "package tools\n\nfunc T() int { return 2 }\n",
	})
	e := serveDirs(t, dir)
	e.a.Reading(e.repo.ID, 0)
	e.a.Wait()

	_, body := e.app.Get(e.base() + "/review")

	assert.Regexp(t, `\d+ of \d+ units in calc · \d+ in <a href="`+e.base()+`/in/tools/review">tools</a> →`, body)
	assert.Regexp(t, `class="seg on"[^>]*>calc</a>`, body, "the pill names the module, not (root)")
	assert.NotContains(t, body, "(root)")
}

func TestReading_oneSmellPerLineWithTheMapsSmallButtonAndALinkToItsUnit(t *testing.T) {
	e, _ := servePR(t)
	p := e.openPage(t, e.base()+"/pr/7", "")

	body := p.html()

	assert.Regexp(t, `(?s)<li class="finding smell">.*?Complex function · <a class="smell-name" href="#u[0-9a-f]+">tangle</a> —[^<]*</span><button class="handoff"[^>]*>Add to handoff</button></li>`, body)
	assert.NotContains(t, body, `class="btn small" title="Add`, "the full-width bar is gone")
	assert.Regexp(t, `<details class="smells" open>`, body, "open while something waits to be added")

	p.press("Add every introduced smell to the handoff")
	p.seen("all in handoff")

	assert.Regexp(t, `<details class="smells">`, p.html(), "folded once every smell is in the handoff")
	assert.Contains(t, p.html(), "Smells: 2 new, 1 fixed", "the summary line stays")
}

func TestReading_oneWordingForTheCallersOfAUnit(t *testing.T) {
	e, _ := servePR(t)

	_, body := e.app.Get(e.base() + "/pr/7")

	// TestClamp calls Clamp: one caller, and it is a test.
	assert.Regexp(t, `class="chip[^"]*">1 caller \(1 in tests\)<`, body, "the chip")
	assert.Regexp(t, `class="why">Read first: [^<]*1 caller \(1 in tests\)`, body, "the read-first line")
	assert.Regexp(t, `class="calls-toggle"[^>]*>▸ 1 caller \(1 in tests\)<`, body, "the disclosure")
	assert.NotContains(t, body, "test caller")
	assert.NotContains(t, body, "Callers (")
}

const (
	boxID    = "example.com/box/box.Box"
	boxSize  = "example.com/box/box.Box.Size"
	boxWeigh = "example.com/box/box.Box.Weigh"
)

// serveBox is a type with two methods, the type and one method changed.
func serveBox(t *testing.T) env {
	t.Helper()
	return serveRepo(t, map[string]string{
		"go.mod":     "module example.com/box\n\ngo 1.27\n",
		"box/box.go": "package box\n\ntype Box struct{}\n\nfunc (b *Box) Size() int { return 1 }\n\nfunc (b *Box) Weigh() int { return 2 }\n\nfunc Heavy(b *Box) int { return b.Weigh() }\n",
	}, map[string]string{
		"box/box.go": "package box\n\ntype Box struct{ W int }\n\nfunc (b *Box) Size() int { return 3 }\n\nfunc (b *Box) Weigh() int { return 2 }\n\nfunc Heavy(b *Box) int { return b.Weigh() }\n",
	})
}

func TestReading_aHopThatOnlyNamesTheTypeInItsReceiverLightsThatLine(t *testing.T) {
	e := serveBox(t)

	_, body := e.app.Get(e.base() + "/review/" + thread(boxID))

	assert.Regexp(t, `class="dl dl-call"[^>]*>(?:<span[^>]*></span>|<span[^>]*>\d+</span>)*<span class="op"> </span><span class="tx">func \(b \*Box\) Weigh\(\)`, body, "Weigh's receiver is its reference to Box")
}

func TestReading_closeOnAnOpenHopIsASecondaryButtonLikeCallers(t *testing.T) {
	e := serveBox(t)

	_, body := e.app.Get(e.base() + "/review/" + thread(boxID, boxWeigh))

	assert.NotRegexp(t, `class="tool on hop-follow"`, body)
	assert.Regexp(t, `class="tool hop-follow"[^>]*>Close<`, body)
}

func TestReading_theSpineLeadsWithTheMethodAndDimsItsReceiver(t *testing.T) {
	e := serveBox(t)

	_, body := e.app.Get(e.base() + "/review")

	assert.Contains(t, body, `class="ol-name">Size<span class="ol-recv">Box</span>`, "method first, receiver dim after it")
	assert.Regexp(t, `<li class="ol-head"><span>name</span><span>risk ±</span></li>`, body, "a header row per chapter")
	assert.Regexp(t, `class="ol-risk" title="risk \d+: [^"]*">\d+</span>`, body, "the number alone, its reasons on hover")
	assert.NotRegexp(t, `>r\d+<`, body, "no bare r52")
}

func TestReading_theAtlasPaneSaysWhatItsMarksMean(t *testing.T) {
	e, _ := servePR(t)

	_, body := e.app.Get(e.base() + "/pr/7")

	assert.Contains(t, body, `class="atlas-legend"`)
	assert.Contains(t, body, "changed in this PR")
	assert.Contains(t, body, "thread (Head only)")
}

func TestReading_theHeaderKeepsTheKeysBehindAQuestionMark(t *testing.T) {
	e, _ := servePR(t)

	_, body := e.app.Get(e.base() + "/pr/7")

	assert.NotContains(t, body, `class="keys"`, "no always-visible legend")
	assert.Regexp(t, `<button class="btn small ghost keys-open"[^>]*>\? keys</button>`, body)
	assert.Regexp(t, `<button class="btn small ghost"[^>]*>Refetch</button>`, body, "a small secondary button")
	assert.Regexp(t, `<button class="btn small ghost"[^>]*>Hide atlas</button>`, body)
}

func TestReading_theKeysListIsNotHiddenByTheServer(t *testing.T) {
	e, _ := servePR(t)

	_, body := e.app.Get(e.base() + "/pr/7")

	assert.Contains(t, body, `class="keys-help"`)
	assert.NotRegexp(t, `class="keys-help"[^>]*hidden`, body, "a re-render would close the list the reader opened; the page's own class shows it")
}

func TestReading_theNewCallersChipCountsProductionCallersOnce(t *testing.T) {
	e := serveRepo(t,
		map[string]string{"go.mod": "module example.com/nc\n\ngo 1.27\n", "nc.go": "package nc\n\nfunc Target() int { return 1 }\n"},
		map[string]string{
			"nc.go":      "package nc\n\nfunc Target() int { return 2 }\n\nfunc A() int { return Target() }\n\nfunc B() int { return Target() + Target() }\n",
			"nc_test.go": "package nc\n\nimport \"testing\"\n\nfunc TestTarget(t *testing.T) { Target() }\n",
		})

	_, body := e.app.Get(e.base() + "/review")

	assert.Contains(t, body, `class="chip chip-new">+2 new<`, "A and B; the test and B's second call are not new callers")
}
