package ui

import (
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"path"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/ghx"
	"github.com/joaomdsg/codemesh/internal/graphdiff"
	"github.com/joaomdsg/codemesh/internal/notes"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/thread"
)

// reviewPage is the reading page: a change read as chapters, with each
// changed declaration's callers open under its diff. It serves the working
// tree at /r/{repo}/review and a pull request at /r/{repo}/pr/{n}.
type reviewPage struct {
	shell
	// The atlas island's inputs, all client-only: the server renders the
	// layout, the reviewed cards and the thread's path into them; the focused
	// card is the browser's own.
	Atlas    via.SignalCS[atlas]
	Reviewed via.SignalCS[[]string]     `via:"init=[]"`
	Focus    via.SignalCS[string]       `via:"init=\"\""`
	Path     via.SignalCS[[][2]float64] `via:"init=[]"`
	Pane     via.SignalCS[string]       `via:"init=\"head\""`

	// Url is the address the page shows once the thread changes; the browser
	// writes it to the address bar. The thread itself is in the path (see
	// path), not the query: an action's authority render does not see the
	// query or any signal the browser sends.
	Url via.Signal[string]
	// Anchor is the line the browser asks to write a note on.
	Anchor    via.Signal[string]
	NoteText  via.Signal[string]
	InHandoff via.Signal[bool] `via:"init=true"`

	pr      int      // pull request number; 0 reads the working tree
	path    []string // the open thread's decl IDs, root first
	live    bool
	loaded  *app.PRReview // kept while a browser is connected
	loadErr error

	base      bool // the atlas shows the base side
	noAtlas   bool
	showNoise bool
	moreHops  map[string]bool
	noting    *noteTarget
	err       string
	opened    map[string]bool     // unit keys whose diff the reviewer asked to see
	whole     map[string]bool     // unit keys whose diff shows past maxDiffLines
	pages     map[review.Lane]int // pages of laneCap units listed past the first

	graphBase, graphHead *code.Snapshot
	graph                *graphdiff.Diff

	// Per render.
	budget   int // diff lines rendered so far
	anchors  map[string]anchor
	notesAt  map[string][]notes.Note
	unitKeys map[string]bool
	stale    map[string]bool // note IDs whose code has changed since
	shown    map[string]bool
	files    map[string][]string
}

// reading is what one render shows: the head and base snapshots of the
// change, its review, and where it came from.
type reading struct {
	head, base       *app.Analysis // base is nil when it did not load
	review           *review.Review
	graph            *graphdiff.Diff
	scope            string // "wt", or "pr:<n>@<headSHA>"
	title            string
	baseName         string
	baseSHA, headSHA string
	pr               *ghx.PR
	at               int64 // identifies the layout, so the atlas redraws only when it changes
	keys             map[string]string
}

func (r *reading) keyOf(declID string) string { return r.keys[declID] }

func (p *reviewPage) OnInit(ctx *via.Ctx) error {
	if err := p.start(ctx); err != nil {
		return err
	}
	if v := ctx.Request().PathValue("n"); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err != nil || n < 1 {
			return via.ErrNotFound
		}
		p.pr = n
	}
	p.path = splitPath(ctx.Request().PathValue("thread"))
	p.Url.Set(p.threadURL())
	ctx.OnConnect(func() { p.live = true })
	ctx.OnDispose(p.dispose)
	return nil
}

func (p *reviewPage) PageMeta() via.Meta {
	if p.pr > 0 {
		return via.Meta{Title: fmt.Sprintf("PR #%d · codemesh", p.pr)}
	}
	return via.Meta{Title: "Review · codemesh"}
}

// threadURL is the page's address with the open thread in its path.
func (p *reviewPage) threadURL() string { return p.address(p.path, p.root) }

// address is this change's page in root, with the thread ids in its path.
func (p *reviewPage) address(ids []string, root string) string {
	base := repoBase(p.repo.ID, root)
	u := base + "review"
	if p.pr > 0 {
		u = fmt.Sprintf("%spr/%d", base, p.pr)
	}
	if len(ids) > 0 {
		u += "/" + url.PathEscape(joinPath(ids))
	}
	return u
}

// A thread's IDs travel as one string, "~" between them. A decl ID can hold
// "~" itself (a Go module path may), so each ID escapes "%" and "~" first.
var idEscaper = strings.NewReplacer("%", "%25", "~", "%7E")
var idUnescaper = strings.NewReplacer("%7E", "~", "%25", "%")

func joinPath(ids []string) string {
	esc := make([]string, len(ids))
	for i, id := range ids {
		esc[i] = idEscaper.Replace(id)
	}
	return strings.Join(esc, "~")
}

func splitPath(s string) []string {
	if s == "" {
		return nil
	}
	ids := strings.Split(s, "~")
	for i, id := range ids {
		ids[i] = idUnescaper.Replace(id)
	}
	return ids
}

// dispose gives back the pull request a closed tab held.
func (p *reviewPage) dispose() {
	if p.loaded != nil {
		p.loaded.Release()
		p.loaded = nil
	}
}

// loadPR reads the pull request. A tab that stays connected keeps what it
// loaded, so a keystroke does not fetch it again; a page that is only read
// once gives its snapshots back when its render ends.
func (p *reviewPage) loadPR() (*app.PRReview, func(), error) {
	if p.loaded != nil {
		return p.loaded, func() {}, nil
	}
	if p.loadErr != nil {
		return nil, func() {}, p.loadErr
	}
	r, err := p.app.PRReview(p.repo.ID, p.root, p.pr)
	switch {
	case err != nil:
		if p.live {
			p.loadErr = err
		}
		return nil, func() {}, err
	case p.live:
		p.loaded = r
		return r, func() {}, nil
	}
	return r, r.Release, nil
}

// read loads what the page shows. When there is nothing to show it returns
// the page to render instead.
func (p *reviewPage) read() (*reading, func(), h.H) {
	p.files = map[string][]string{}
	var rd *reading
	var release func()
	if p.pr > 0 {
		r, rel, err := p.loadPR()
		release = rel
		if err != nil {
			return nil, release, p.frame(tabPRs, nil, p.cannotRead(err))
		}
		if r.Head.Snap == nil || r.Base.Snap == nil {
			bad := r.Head
			if bad.Snap != nil {
				bad = r.Base
			}
			return nil, release, p.unavailable(tabPRs, bad)
		}
		rd = &reading{
			head: r.Head, base: r.Base, review: r.Review, graph: r.Graph, pr: &r.PR,
			scope: fmt.Sprintf("pr:%d@%s", p.pr, r.HeadSHA), baseName: r.PR.BaseRef,
			title: fmt.Sprintf("#%d %s", p.pr, r.PR.Title), baseSHA: r.BaseSHA, headSHA: r.HeadSHA,
			at: int64(fnv32(r.HeadSHA)),
		}
		if len(rd.review.Units) == 0 {
			return nil, release, p.frame(tabPRs, rd.head, h.P(h.Class("empty"), h.Str("This pull request changes no declarations or files.")))
		}
	} else {
		a := p.app.WorkingReview(p.repo.ID, p.root)
		release = a.Release
		switch {
		case a.Snap == nil:
			return nil, release, p.unavailable(tabReview, a)
		case a.Review == nil:
			return nil, release, p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("Nothing to review: "+a.Note+".")))
		case len(a.Review.Units) == 0:
			// Nothing changed: the map, progress and shortcuts would frame nothing.
			return nil, release, p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("No changes against "+a.Base+". Edits show up here as you make them.")))
		}
		rd = &reading{
			head: a, review: a.Review, scope: "wt", baseName: a.Base, baseSHA: a.BaseSHA,
			title: "Working tree against " + a.Base, at: a.At.UnixMilli(),
		}
		if base := p.app.Analyse(p.repo.ID, p.root, a.BaseSHA); base.Snap != nil {
			rd.base = base
			rd.graph = p.graphOf(base, a)
			release = func() { a.Release(); base.Release() }
		} else {
			base.Release()
		}
	}
	rd.keys = map[string]string{}
	for _, u := range rd.review.Units {
		if u.Kind != "" {
			rd.keys[u.ID] = u.Key
		}
	}
	return rd, release, nil
}

// graphOf compares the snapshots once per pair; the working tree is read
// again on every render.
func (p *reviewPage) graphOf(base, head *app.Analysis) *graphdiff.Diff {
	if p.graph == nil || p.graphBase != base.Snap || p.graphHead != head.Snap {
		p.graph = graphdiff.Between(base.Snap, head.Snap, base.Findings, head.Findings)
		p.graphBase, p.graphHead = base.Snap, head.Snap
	}
	return p.graph
}

func (p *reviewPage) cannotRead(err error) h.H {
	text := fmt.Sprintf("Cannot read pull request #%d: %s.", p.pr, strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0]))
	if errors.Is(err, ghx.ErrNoGH) || errors.Is(err, ghx.ErrNotAuthed) {
		text = app.PRList{Err: err}.Hint()
	}
	return h.Div(h.Class("banner err"), h.Role("alert"), h.Str(text))
}

func fnv32(s string) uint32 {
	f := fnv.New32a()
	f.Write([]byte(s))
	return f.Sum32()
}

// unreviewed counts the units not yet marked reviewed.
func (p *reviewPage) unreviewed(rev *review.Review) int {
	n := 0
	for _, u := range rev.Units {
		if !p.state.Reviewed(u.Key) {
			n++
		}
	}
	return n
}

// laneCap is how many units a lane lists at a time: a lane is in risk
// order, so the cut keeps the units most worth reading. Listing a
// release-sized lane at once (864 units) built a 2.8 MB page.
const laneCap = 60

// renderBudget bounds the diff lines one page render shows unasked. Past it,
// and in the lanes that rarely need reading, cards show their header and a
// button that renders the diff on demand: a release-sized change otherwise
// sends megabytes nobody scrolls through.
const renderBudget = 3000

// Mark toggles one unit's reviewed mark.
func (p *reviewPage) Mark(_ *via.Ctx, key string) {
	p.save(key, !p.state.Reviewed(key))
}

// AcceptNoise marks every noise unit reviewed.
func (p *reviewPage) AcceptNoise(_ *via.Ctx) {
	rd, release, _ := p.read()
	defer release()
	if rd == nil {
		return
	}
	for _, u := range rd.review.Lane(review.Noise) {
		if !p.save(u.Key, true) {
			break
		}
	}
}

// ToggleNoise shows or folds the noise units.
func (p *reviewPage) ToggleNoise(_ *via.Ctx) { p.showNoise = !p.showNoise }

// Open shows or hides one unit's diff.
func (p *reviewPage) Open(_ *via.Ctx, key string) {
	if p.opened == nil {
		p.opened = map[string]bool{}
	}
	p.opened[key] = !p.opened[key]
}

// ShowWhole renders a unit's diff past the per-unit cap.
func (p *reviewPage) ShowWhole(_ *via.Ctx, key string) {
	if p.whole == nil {
		p.whole = map[string]bool{}
	}
	p.whole[key] = true
}

// MoreUnits lists the next laneCap units of a lane.
func (p *reviewPage) MoreUnits(_ *via.Ctx, l int) {
	if p.pages == nil {
		p.pages = map[review.Lane]int{}
	}
	p.pages[review.Lane(l)]++
}

// visible is the part of a lane that renders.
func (p *reviewPage) visible(l review.Lane, units []*review.Unit) []*review.Unit {
	return units[:min(len(units), laneCap*(1+p.pages[l]))]
}

// Refresh analyses again now: the working tree without waiting for the
// watcher, a pull request from a new fetch.
func (p *reviewPage) Refresh(_ *via.Ctx) {
	if p.pr > 0 {
		p.dispose()
		p.loadErr = nil
		return
	}
	p.app.Rescan(p.repo.ID)
}

// ShowSide picks which snapshot the atlas draws.
func (p *reviewPage) ShowSide(_ *via.Ctx, base bool) { p.base = base }

// ToggleAtlas shows or hides the atlas pane.
func (p *reviewPage) ToggleAtlas(_ *via.Ctx) { p.noAtlas = !p.noAtlas }

func (p *reviewPage) save(key string, done bool) bool {
	if err := p.state.Set(key, done); err != nil {
		p.err = "Review mark not saved: " + err.Error() + ". Check that the state directory is writable, then try again."
		return false
	}
	p.err = ""
	return true
}

func (p *reviewPage) View() h.H {
	rd, release, empty := p.read()
	defer release()
	if rd == nil {
		return empty
	}
	rev := rd.review
	p.budget = 0
	p.anchors = map[string]anchor{}
	p.shown = map[string]bool{}
	p.unitKeys = map[string]bool{}
	for _, u := range rev.Units {
		p.unitKeys[u.Key] = true
	}
	p.notesAt = p.notesByLine(rd)
	t := p.active(rd)
	done := len(rev.Units) - p.unreviewed(rev)

	var secs []review.Section
	var lanes []h.H
	for _, l := range review.Lanes {
		sec := rev.Section(l, p.state)
		if len(sec.Units) == 0 {
			continue
		}
		secs = append(secs, sec)
		switch {
		case l != review.Noise:
			lanes = append(lanes, p.lane(rd, sec, t))
		default:
			lanes = append(lanes, p.noise(rd, sec, t))
		}
	}
	body := []h.H{p.header(rd, done), p.threadBar(rd, t)}
	if p.err != "" {
		body = append(body, h.Div(h.Class("banner err"), h.Role("alert"), h.Str(p.err)))
	}
	body = append(body, p.smells(rd))
	body = append(body, lanes...)
	body = append(body, p.strayNotes(rd), p.stateInputs(), p.help())

	layout := "reading-layout"
	if p.noAtlas {
		layout += " no-atlas"
	}
	// The focused card becomes $_focus, which the atlas follows.
	track := on.EventCS("focusin", expr.Rawf("%s = evt.target.closest('.unit')?.id || %s", p.Focus.Ref(), p.Focus.Ref()))
	tab := tabReview
	if p.pr > 0 {
		tab = tabPRs
	}
	return p.frame(tab, rd.head, h.Div(h.Class(layout), track,
		p.spine(secs, rd.review.SharedName()),
		h.Div(append([]h.H{h.Class("review reading")}, body...)...),
		p.atlasPane(rd, t),
	))
}

// stateInputs holds the signals the browser posts back with every action, and the
// button its keyboard shortcut and line numbers press to start a note.
func (p *reviewPage) stateInputs() h.H {
	return h.Div(h.Class("reading-state"), h.Hidden(true),
		h.Input(h.Type("hidden"), h.Class("thread-url"), p.Url.Bind(),
			h.DataEffect(expr.Rawf("codemesh.url(%s)", p.Url.Ref()))),
		h.Input(h.Type("hidden"), h.Class("pick-input"), p.Anchor.Bind()),
		h.Button(h.Class("pick-go"), h.Type("button"), h.Title("Start a note on a line"), on.Click(p.Pick)),
	)
}

func (p *reviewPage) header(rd *reading, done int) h.H {
	rev := rd.review
	progress := fmt.Sprintf("%d of %d reviewed", done, len(rev.Units))
	if done == len(rev.Units) && done > 0 {
		progress = "✓ All " + plural(done, "unit") + " reviewed"
	}
	ch := p.app.Reading(p.repo.ID, p.pr)
	lead, refresh, refreshTitle := "merge-base "+short(rd.baseSHA), "Rescan", "Analyse the working tree again"
	if pr := rd.pr; pr != nil {
		lead = fmt.Sprintf("%s · %s@%s → %s@%s", pr.Author, pr.BaseRef, short(rd.baseSHA), pr.HeadRef, short(rd.headSHA))
		refresh, refreshTitle = "Refetch", "Fetch the pull request again"
	}
	sub := []h.H{h.Class("hint"), h.Str(lead + " · "), p.unitCounts(rd, ch)}
	if added, deleted := changeLines(ch, rd.pr); added+deleted > 0 {
		sub = append(sub, h.Str(" · "), delta(added, deleted))
	}
	atlasLabel := "Hide atlas"
	if p.noAtlas {
		atlasLabel = "Show atlas"
	}
	return h.Div(h.Class("review-head"),
		h.Div(
			h.H2(h.Str(rd.title)),
			h.P(sub...),
			graphChips(rd.graph),
		),
		h.Div(h.Class("review-progress"),
			h.Meter(h.RawAttr("min", "0"), h.RawAttr("max", fmt.Sprint(max(len(rev.Units), 1))), h.RawAttr("value", fmt.Sprint(done)), h.Aria("label", "Reviewed")),
			h.Span(h.Class(map[bool]string{true: "ok", false: "hint"}[done == len(rev.Units) && done > 0]), h.Str(progress)),
			h.Button(h.Class("btn small ghost"), h.Title(refreshTitle), on.Click(p.Refresh), h.Str(refresh)),
			h.Button(h.Class("btn small ghost"), h.Title(atlasLabel), on.Click(p.ToggleAtlas), h.Str(atlasLabel)),
			h.Button(h.Class("btn small ghost keys-open"), h.Type("button"), h.Title("Show the keyboard shortcuts"), h.Str("? keys")),
		),
	)
}

// changeLines is the lines a change adds and deletes, as the inbox and the
// reading page both say them: git's count once the change is read, gh's until then.
func changeLines(r app.Reading, pr *ghx.PR) (added, deleted int) {
	if !r.Ready && pr != nil {
		return pr.Additions, pr.Deletions
	}
	return r.Added, r.Deleted
}

// unitCounts says how many units the page lists; with several roots, how
// many of the change's units they are, and where the others are.
func (p *reviewPage) unitCounts(rd *reading, ch app.Reading) h.H {
	n := len(rd.review.Units)
	if !ch.Ready || len(ch.Roots) < 2 {
		return h.Str(plural(n, "unit"))
	}
	kids := []h.H{h.Str(fmt.Sprintf("%d of %d units in %s", n, ch.Units, p.rootLabel(rd.head.Root, rd.head)))}
	for _, r := range ch.Roots {
		if r.Dir != rd.head.Root {
			kids = append(kids, h.Str(fmt.Sprintf(" · %d in ", r.Units)), h.A(h.Href(p.address(nil, r.Dir)), h.Str(p.rootLabel(r.Dir, rd.head))), h.Str(" →"))
		}
	}
	return group(kids)
}

// graphChips sums up the structural change: declarations, references and
// smells between the two snapshots.
func graphChips(g *graphdiff.Diff) h.H {
	if g == nil {
		return nil
	}
	chip := func(text string) h.H { return h.Span(h.Class("chip"), h.Str(text)) }
	return h.Div(h.Class("chips graph"),
		chip(fmt.Sprintf("decls +%d −%d ~%d", len(g.Added), len(g.Removed), len(g.Changed))),
		chip(fmt.Sprintf("edges +%d −%d", len(g.EdgesAdded), len(g.EdgesRemoved))),
		chip(fmt.Sprintf("smells +%d −%d", len(g.Introduced), len(g.Fixed))),
	)
}

// spine lists every chapter's units with their risk and mark, linking to
// their cards. Noise stays out of it.
func (p *reviewPage) spine(secs []review.Section, shared func(*review.Unit) bool) h.H {
	var kids []h.H
	for _, sec := range secs {
		if sec.Lane == review.Noise {
			kids = append(kids, h.P(h.Class("hint"), h.Str(fmt.Sprintf("Noise · %d, folded", len(sec.Units)))))
			continue
		}
		var rows []h.H
		shown := p.visible(sec.Lane, sec.Units)
		for _, u := range shown {
			rows = append(rows, p.spineRow(u, shared(u)))
		}
		if hidden := len(sec.Units) - len(shown); hidden > 0 {
			rows = append(rows, h.Li(h.Class("ol-more"), h.Str(fmt.Sprintf("%d more", hidden))))
		}
		rows = append([]h.H{h.Li(h.Class("ol-head"), h.Span(h.Str("name")), h.Span(h.Str("risk ±")))}, rows...)
		kids = append(kids, h.H3(h.Str(sec.Lane.String()), h.Span(h.Class("ol-count"), h.Str(fmt.Sprintf(" %d/%d", sec.Done, len(sec.Units))))), h.Ul(rows...))
	}
	return h.Nav(append([]h.H{h.Class("outline spine"), h.Aria("label", "Chapters")}, kids...)...)
}

// spineRow leads with a method's own name and dims its receiver after it, so
// the names that a narrow column cuts off still differ. It names the unit's
// package when another unit shares its name.
func (p *reviewPage) spineRow(u *review.Unit, shared bool) h.H {
	cls, glyph := "ol-row", "○"
	if p.state.Reviewed(u.Key) {
		cls, glyph = "ol-row done", "✓"
	}
	name := []h.H{h.Class("ol-name")}
	recv, method, isMethod := strings.Cut(u.Name, ".")
	if isMethod && u.Kind == code.Method {
		name = append(name, h.Str(method), h.Span(h.Class("ol-recv"), h.Str(recv)))
	} else {
		name = append(name, h.Str(u.Name))
	}
	if shared {
		name = append(name, h.Span(h.Class("ol-pkg"), h.Str(" "+path.Base(u.Package))))
	}
	why := fmt.Sprintf("risk %d", u.Risk)
	if len(u.Reasons) > 0 {
		why += ": " + strings.Join(u.Reasons, ", ")
	}
	return h.Li(h.A(h.Class(cls), h.Href("#"+unitID(u.Key)),
		h.Span(h.Class("ol-mark"), h.Str(glyph)),
		h.Span(name...),
		h.Span(h.Class("ol-delta"), h.Span(h.Class("ol-risk"), h.Title(why), h.Str(u.Risk)), delta(u.Added, u.Deleted)),
	))
}

var laneHelp = map[review.Lane]string{
	review.Contract: "exported API and go.mod: read first, others depend on them",
	review.Logic:    "behaviour changes, riskiest first",
	review.Tests:    "test code",
	review.Other:    "non-Go files and long string literals",
	review.Noise:    "no change in meaning: comments, layout, moves, generated code",
}

func (p *reviewPage) lane(rd *reading, sec review.Section, t thread.Thread) h.H {
	l, units := sec.Lane, sec.Units
	head := h.Div(h.Class("lane-head"),
		h.H3(h.Str(l.String())),
		h.Span(h.Class("hint"), h.Str(fmt.Sprintf("%d of %d reviewed · %s", sec.Done, len(units), laneHelp[l]))),
	)
	var cards []h.H
	shown := p.visible(l, units)
	for _, u := range shown {
		cards = append(cards, p.card(rd, u, l, t))
	}
	if hidden := len(units) - len(shown); hidden > 0 {
		cards = append(cards, h.Button(h.Class("btn more"), on.Click(on.Bind(p.MoreUnits, int(l))),
			h.Str(fmt.Sprintf("Show %d more units (%d left)", min(hidden, laneCap), hidden))))
	}
	return h.Section(h.Class("lane lane-"+strings.ToLower(l.String())), h.Div(h.Class("lane-bar"), head), group(cards))
}

// noise folds the units whose meaning did not change into one line.
func (p *reviewPage) noise(rd *reading, sec review.Section, t thread.Thread) h.H {
	var accept h.H
	if sec.Done < len(sec.Units) {
		accept = h.Button(h.Class("btn"), on.Click(p.AcceptNoise), h.Str("Mark all noise reviewed"))
	}
	toggle := "Show"
	if p.showNoise {
		toggle = "Hide"
	}
	fold := h.Div(h.Class("noise-fold"),
		h.Span(h.Str(fmt.Sprintf("Noise · %s · %s", plural(len(sec.Units), "unit"), laneHelp[review.Noise]))),
		accept,
		h.Button(h.Class("expand"), on.Click(p.ToggleNoise), h.Str(toggle)),
	)
	if !p.showNoise {
		return fold
	}
	return group([]h.H{fold, p.lane(rd, sec, t)})
}

// showDiff decides whether a card renders its diff unasked.
func (p *reviewPage) showDiff(u *review.Unit, l review.Lane) bool {
	if p.opened[u.Key] {
		return true
	}
	if p.budget >= renderBudget || !p.state.NeedsReading(u, l) {
		return false
	}
	p.budget += len(u.Lines)
	return true
}

func (p *reviewPage) card(rd *reading, u *review.Unit, l review.Lane, t thread.Thread) h.H {
	done := p.state.Reviewed(u.Key)
	cls := "unit"
	if done {
		cls += " reviewed"
	}
	if t.Root != "" && t.Root == u.ID {
		cls += " threaded"
	}
	label, glyph := "Mark reviewed", ""
	if done {
		label, glyph = "Reviewed. Click to reopen", "✓"
	}
	where := u.File
	if n := firstLine(u); n > 0 {
		where = fmt.Sprintf("%s:%d", u.File, n)
	}
	head := h.Div(h.Class("unit-head"),
		h.Button(h.Class("mark"), h.Aria("label", label), h.Title(label), on.Click(on.Bind(p.Mark, u.Key)), h.Str(glyph)),
		h.Span(h.Class("unit-name"), h.Str(u.Name)),
		h.Span(h.Class("badge change-"+string(u.Change)), h.Str(string(u.Change))),
		p.loc("unit-loc", where, u.File, u.ID, u.Kind != ""),
		h.Span(h.Class("unit-delta"), delta(u.Added, u.Deleted)),
		h.Span(h.Class("chips"), group(p.chips(rd, u))),
	)
	var why h.H
	if len(u.Reasons) > 0 {
		lead := "Read first: "
		if l > review.Logic {
			lead = "Why it is here: "
		}
		why = h.P(h.Class("why"), h.Str(lead+strings.Join(u.Reasons, ", ")+"."))
	}
	var body h.H
	switch {
	case p.showDiff(u, l):
		body = h.Div(h.Class("unit-body"), p.diffView(u))
		if p.opened[u.Key] {
			body = group([]h.H{body, h.Div(h.Class("fold"), h.Button(h.Class("expand"), on.Click(on.Bind(p.Open, u.Key)), h.Str("Hide diff")))})
		}
	case len(u.Lines) > 0:
		body = h.Div(h.Class("fold"), h.Button(h.Class("expand"), on.Click(on.Bind(p.Open, u.Key)), h.Str("Show diff · "+plural(len(u.Lines), "line"))))
	}
	return h.Article(h.Class(cls), h.ID(unitID(u.Key)), h.TabIndex(0), head, why, body, p.callers(rd, u, t))
}

// chips are the unit's facts at a glance: what the change does to its
// callers, and the smells it brings.
func (p *reviewPage) chips(rd *reading, u *review.Unit) []h.H {
	var chips []h.H
	for _, f := range u.Smells {
		chips = append(chips, h.Span(h.Class("chip chip-smell"), h.Title(f.Detail+". "+f.Rule.Why()), h.Str("new: "+strings.ToLower(ruleLabel(f.Rule)))))
	}
	if c := callerCount(rd.head.Snap, u.Callers); c != "" {
		chips = append(chips, h.Span(h.Class("chip"), h.Str(c)))
	}
	if g := rd.graph; g != nil {
		if n := newCallers(g, rd.head.Snap, u.ID); n > 0 {
			chips = append(chips, h.Span(h.Class("chip chip-new"), h.Str(fmt.Sprintf("+%d new", n))))
		}
		for _, c := range g.Changed {
			if c.ID == u.ID && c.Sig {
				chips = append(chips, h.Span(h.Class("chip chip-smell"), h.Str("signature changed")))
			}
		}
	}
	return chips
}

// newCallers counts the production decls that call id in head and did not in
// base, each once.
func newCallers(g *graphdiff.Diff, head *code.Snapshot, id string) int {
	from := map[string]bool{}
	for _, e := range g.EdgesAdded {
		if c := head.Decl(e.From); e.To == id && e.Kind == code.Caller && c != nil && !c.Test {
			from[e.From] = true
		}
	}
	return len(from)
}

// loc is a file:line the browser can follow to the map when the page reads
// the working tree. A pull request's code is not what the map shows.
func (p *reviewPage) loc(class, text, file, declID string, decl bool) h.H {
	if p.pr > 0 {
		return h.Span(h.Class(class), h.Str(text))
	}
	href := p.mapURL("", file, "").String()
	if decl {
		href = p.mapURL("", file, declID).String()
	}
	return h.A(h.Class(class), h.Href(href), h.Str(text))
}

// contextRun is how many unchanged lines show around a change; longer runs
// fold.
const contextRun = 3

// maxDiffLines caps one unit's rendered diff; a fixture or a vendored file
// can run to thousands of lines nobody reads in a review.
const maxDiffLines = 400

func (p *reviewPage) diffView(u *review.Unit) h.H {
	if len(u.Lines) == 0 {
		return nil
	}
	block := unitID(u.Key)
	p.anchors[block] = anchor{file: u.File, decl: u.ID, key: u.Key}
	row := func(l review.Line) h.H { return p.row(block, u.File, l, false) }
	lines, cut := u.Lines, 0
	if len(lines) > maxDiffLines && !p.whole[u.Key] {
		lines, cut = lines[:maxDiffLines], len(lines)-maxDiffLines
	}
	rows := foldRows(lines, row)
	if cut > 0 {
		rows = append(rows, h.Button(h.Class("expand"), on.Click(on.Bind(p.ShowWhole, u.Key)), h.Str(fmt.Sprintf("Show the other %s", plural(cut, "line")))))
	}
	return h.Div(h.Class("diff"), group(rows))
}

// foldRows renders the lines, folding each long run of unchanged ones.
func foldRows(lines []review.Line, row func(review.Line) h.H) []h.H {
	var rows []h.H
	var run []review.Line
	for _, l := range lines {
		if l.Op == ' ' {
			run = append(run, l)
			continue
		}
		rows = append(rows, runRows(run, len(rows) == 0, false, row)...)
		run = nil
		rows = append(rows, row(l))
	}
	return append(rows, runRows(run, len(rows) == 0, true, row)...)
}

// runRows renders a run of unchanged lines. A long one keeps contextRun lines
// beside each change and folds the rest; first and last mean no change lies
// before or after it, so that side keeps none.
func runRows(run []review.Line, first, last bool, row func(review.Line) h.H) []h.H {
	if len(run) <= 2*contextRun+1 {
		return diffRows(run, row)
	}
	lead, tail := run[:contextRun], run[len(run)-contextRun:]
	if first {
		lead = nil
	}
	if last {
		tail = nil
	}
	skipped := run[len(lead) : len(run)-len(tail)]
	rows := diffRows(lead, row)
	rows = append(rows, h.Details(h.Class("skip"), h.Summary(h.Str(fmt.Sprintf("%d unchanged lines", len(skipped)))), group(diffRows(skipped, row))))
	return append(rows, diffRows(tail, row)...)
}

func diffRows(lines []review.Line, row func(review.Line) h.H) []h.H {
	var rows []h.H
	for _, l := range lines {
		rows = append(rows, row(l))
	}
	return rows
}

// row is one code line. Its line number starts a margin note, and the notes
// written on the line show under it. lit marks the line a hop's call is on.
func (p *reviewPage) row(block, file string, l review.Line, lit bool) h.H {
	cls := map[byte]string{' ': "dl", '+': "dl dl-add", '-': "dl dl-del"}[l.Op]
	if lit {
		cls = "dl dl-call"
	}
	side, at := "+", l.New
	if l.Op == '-' {
		side, at = "-", l.Old
	}
	no := func(n int, notable bool) h.H {
		switch {
		case n == 0:
			return h.Span(h.Class("no"))
		case notable:
			return h.Span(h.Class("no"), h.Data("ln", fmt.Sprintf("%s:%s%d", block, side, n)), h.Title("Write a note on this line"), h.Str(n))
		}
		return h.Span(h.Class("no"), h.Str(n))
	}
	line := h.Div(h.Class(cls), no(l.Old, side == "-"), no(l.New, side == "+"), h.Span(h.Class("op"), h.Str(string(l.Op))), h.Span(h.Class("tx"), h.Str(l.Text)))
	extra := p.lineExtras(block, file, side, at)
	if extra == nil {
		return line
	}
	return group([]h.H{line, extra})
}

// firstLine is the first line of the unit's change, on the head side if it has one.
func firstLine(u *review.Unit) int {
	for _, l := range u.Lines {
		if l.New > 0 {
			return l.New
		}
	}
	for _, l := range u.Lines {
		if l.Old > 0 {
			return l.Old
		}
	}
	return 0
}

// unitID is a stable element id, so a live re-render morphs each card in
// place even when the queue order changes.
func unitID(key string) string {
	f := fnv.New64a()
	f.Write([]byte(key))
	return fmt.Sprintf("u%x", f.Sum64())
}

func short(sha string) string { return sha[:min(len(sha), 7)] }

// help lists every shortcut; the ? key shows it.
func (p *reviewPage) help() h.H {
	row := func(k, what string) h.H { return h.Li(h.Kbd(h.Str(k)), h.Span(h.Str(what))) }
	return h.Div(h.Class("keys-help"), h.Role("dialog"), h.Aria("label", "Keyboard shortcuts"),
		h.H3(h.Str("Keys")),
		h.Ul(
			row("j / k", "next and previous unit"),
			row("r", "mark the unit reviewed, go to the next"),
			row("c", "open the callers of the unit, or of the hop in focus"),
			row("n", "write a note on the unit's first change, or on the hop's call"),
			row("t", "keep the thread in the handoff"),
			row("o", "show or hide a unit's diff"),
			row("b", "open the basket"),
			row("?", "show or hide this list"),
		),
	)
}
