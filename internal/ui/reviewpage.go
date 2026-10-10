package ui

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"path"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// reviewPage is the review queue: the change as declarations, by lane, risk
// first, with reviewed units folded away.
type reviewPage struct {
	shell
	// The atlas island's inputs, all client-only: the server renders the
	// layout and the reviewed cards into them; the focused card is the
	// browser's own.
	Atlas    via.SignalCS[atlas]
	Reviewed via.SignalCS[[]string] `via:"init=[]"`
	Focus    via.SignalCS[string]   `via:"init=\"\""`

	err    string
	opened map[string]bool     // unit keys whose diff the reviewer asked to see
	budget int                 // diff lines rendered so far in this View
	inDiff map[string]string   // decl ID → unit key, for units in this View
	pages  map[review.Lane]int // pages of laneCap units listed past the first
	whole  map[string]bool     // unit keys whose diff shows past maxDiffLines
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

func (p *reviewPage) OnInit(ctx *via.Ctx) error {
	p.start(ctx)
	return nil
}

func (p *reviewPage) PageMeta() via.Meta { return via.Meta{Title: "Review · codemesh"} }

// Mark toggles one unit's reviewed mark.
func (p *reviewPage) Mark(_ *via.Ctx, key string) {
	p.save(key, !p.state.Reviewed(key))
}

// AcceptNoise marks every noise unit reviewed.
func (p *reviewPage) AcceptNoise(_ *via.Ctx) {
	a := p.src.Current()
	if a == nil || a.Review == nil {
		return
	}
	for _, u := range a.Review.Lane(review.Noise) {
		if !p.save(u.Key, true) {
			break
		}
	}
}

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

// Rescan re-analyses now, without waiting for the watcher.
func (p *reviewPage) Rescan(_ *via.Ctx) { p.src.Refresh() }

func (p *reviewPage) save(key string, done bool) bool {
	if err := p.state.Set(key, done); err != nil {
		p.err = "Review mark not saved: " + err.Error() + ". Check that the .git directory is writable, then try again."
		return false
	}
	p.err = ""
	return true
}

func (p *reviewPage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	if a.Review == nil {
		return p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("Nothing to review: "+a.Note+".")))
	}
	rev := a.Review
	if len(rev.Units) == 0 {
		// Nothing changed: the map, progress and shortcuts would frame nothing.
		return p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("No changes against "+a.Base+". Edits show up here as you make them.")))
	}
	done := len(rev.Units) - p.open(a)
	p.budget = 0
	p.inDiff = map[string]string{}
	for _, u := range rev.Units {
		p.inDiff[u.ID] = u.Key
	}
	var secs []review.Section
	var lanes []h.H
	for _, l := range review.Lanes {
		if sec := rev.Section(l, p.state); len(sec.Units) > 0 {
			secs = append(secs, sec)
			lanes = append(lanes, p.lane(a, sec))
		}
	}
	body := []h.H{p.summary(a, done)}
	if p.err != "" {
		body = append(body, h.Div(h.Class("banner err"), h.Role("alert"), h.Str(p.err)))
	}
	body = append(body, p.smellDelta(rev))
	body = append(body, lanes...)
	// The focused card becomes $_focus, which the atlas follows.
	track := on.EventCS("focusin", expr.Rawf("%s = evt.target.closest('.unit')?.id || %s", p.Focus.Ref(), p.Focus.Ref()))
	return p.frame(tabReview, a, h.Div(h.Class("review-layout"), track,
		h.Div(h.Class("review-side"), p.atlasIsland(a), p.outline(secs, rev.SharedName())),
		h.Div(append([]h.H{h.Class("review")}, body...)...),
	))
}

func (p *reviewPage) summary(a *live.Analysis, done int) h.H {
	rev := a.Review
	added, deleted := 0, 0
	for _, u := range rev.Units {
		added += u.Added
		deleted += u.Deleted
	}
	progress := fmt.Sprintf("%d of %d reviewed", done, len(rev.Units))
	if done == len(rev.Units) && done > 0 {
		progress = "✓ All " + plural(done, "unit") + " reviewed"
	}
	return h.Div(h.Class("review-head"),
		h.Div(
			h.H2(h.Str("Working tree against "+a.Base)),
			h.P(h.Class("hint"), h.Str(fmt.Sprintf("merge-base %s · %s · +%d −%d", short(a.BaseSHA), plural(len(rev.Units), "unit"), added, deleted))),
		),
		h.Div(h.Class("review-progress"),
			h.Meter(h.RawAttr("min", "0"), h.RawAttr("max", fmt.Sprint(max(len(rev.Units), 1))), h.RawAttr("value", fmt.Sprint(done)), h.Aria("label", "Reviewed")),
			h.Span(h.Class(map[bool]string{true: "ok", false: "hint"}[done == len(rev.Units) && done > 0]), h.Str(progress)),
			h.Button(h.Class("btn"), on.Click(p.Rescan), h.Str("Rescan")),
		),
		h.P(h.Class("keys"), key("move", "j", "k"), key("mark reviewed", "r"), key("next unreviewed", "n"), key("show diff", "o")),
	)
}

func (p *reviewPage) smellDelta(rev *review.Review) h.H {
	if len(rev.Introduced) == 0 && len(rev.Fixed) == 0 {
		return nil
	}
	row := func(f smell.Finding, cls string) h.H {
		return h.Li(h.Class("finding "+cls), sevMark(f.Severity),
			h.Span(h.Class("rule"), h.Str(ruleLabel(f.Rule))),
			h.Span(h.Class("subject"), h.Str(f.Subject)),
			h.Span(h.Class("detail"), h.Str(f.Detail)))
	}
	var rows []h.H
	for _, f := range rev.Introduced {
		rows = append(rows, row(f, "new"))
	}
	for _, f := range rev.Fixed {
		rows = append(rows, row(f, "gone"))
	}
	title := fmt.Sprintf("Smells: %d new, %d fixed", len(rev.Introduced), len(rev.Fixed))
	return h.Details(h.Class("delta"),
		h.Summary(h.Str(title)),
		h.Ul(append([]h.H{h.Class("findings")}, rows...)...),
	)
}

// atlasIsland is the D3 map of the module with this change's declarations
// lit. It ignores morphs, so a re-render never wipes what D3 drew.
func (p *reviewPage) atlasIsland(a *live.Analysis) h.H {
	return group([]h.H{
		feed(map[expr.Expr]any{
			p.Atlas.Ref():    reviewAtlas(a),
			p.Reviewed.Ref(): reviewedCards(a.Review, p.state),
		}),
		h.Div(h.Class("atlas"), h.DataIgnoreMorph(),
			h.DataEffect(expr.Rawf("codemesh.atlas(el, %s, {reviewed: %s, focus: %s})", p.Atlas.Ref(), p.Reviewed.Ref(), p.Focus.Ref()))),
	})
}

// outline is the whole change at a glance: every unit by lane, with its
// mark, linking to its card.
func (p *reviewPage) outline(secs []review.Section, shared func(*review.Unit) bool) h.H {
	var kids []h.H
	for _, sec := range secs {
		var rows []h.H
		shown := p.visible(sec.Lane, sec.Units)
		for _, u := range shown {
			rows = append(rows, p.outlineRow(u, shared(u)))
		}
		if hidden := len(sec.Units) - len(shown); hidden > 0 {
			rows = append(rows, h.Li(h.Class("ol-more"), h.Str(fmt.Sprintf("%d more", hidden))))
		}
		kids = append(kids, h.H3(h.Str(sec.Lane.String()), h.Span(h.Class("ol-count"), h.Str(fmt.Sprintf(" %d/%d", sec.Done, len(sec.Units))))), h.Ul(rows...))
	}
	return h.Nav(append([]h.H{h.Class("outline"), h.Aria("label", "Units")}, kids...)...)
}

// outlineRow names the unit's package when another unit shares its name.
func (p *reviewPage) outlineRow(u *review.Unit, shared bool) h.H {
	cls, glyph := "ol-row", "○"
	if p.state.Reviewed(u.Key) {
		cls, glyph = "ol-row done", "✓"
	}
	return h.Li(h.A(h.Class(cls), h.Href("#"+unitID(u.Key)),
		h.Span(h.Class("ol-mark"), h.Str(glyph)),
		h.Span(h.Class("ol-name"), h.Str(u.Name), via.When(shared, func() h.H { return h.Span(h.Class("ol-pkg"), h.Str(" "+path.Base(u.Package))) })),
		h.Span(h.Class("ol-delta"), delta(u.Added, u.Deleted)),
	))
}

var laneHelp = map[review.Lane]string{
	review.Contract: "exported API and go.mod: read first, others depend on them",
	review.Logic:    "behaviour changes, riskiest first",
	review.Tests:    "test code",
	review.Other:    "non-Go files and long string literals",
	review.Noise:    "no change in meaning: comments, layout, moves, generated code",
}

func (p *reviewPage) lane(a *live.Analysis, sec review.Section) h.H {
	l, units := sec.Lane, sec.Units
	head := h.Div(h.Class("lane-head"),
		h.H3(h.Str(l.String())),
		h.Span(h.Class("hint"), h.Str(fmt.Sprintf("%d of %d reviewed · %s", sec.Done, len(units), laneHelp[l]))),
	)
	var accept h.H
	if l == review.Noise && sec.Done < len(units) {
		accept = h.Button(h.Class("btn"), on.Click(p.AcceptNoise), h.Str("Mark all noise reviewed"))
	}
	var cards []h.H
	shown := p.visible(l, units)
	for _, u := range shown {
		cards = append(cards, p.card(a, u, l))
	}
	if hidden := len(units) - len(shown); hidden > 0 {
		cards = append(cards, h.Button(h.Class("btn more"), on.Click(on.Bind(p.MoreUnits, int(l))),
			h.Str(fmt.Sprintf("Show %d more units (%d left)", min(hidden, laneCap), hidden))))
	}
	return h.Section(h.Class("lane lane-"+strings.ToLower(l.String())), h.Div(h.Class("lane-bar"), head, accept), group(cards))
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

func (p *reviewPage) card(a *live.Analysis, u *review.Unit, l review.Lane) h.H {
	done := p.state.Reviewed(u.Key)
	cls := "unit"
	if done {
		cls += " reviewed"
	}
	label, glyph := "Mark reviewed", ""
	if done {
		label, glyph = "Reviewed. Click to reopen", "✓"
	}
	var chips []h.H
	for _, f := range u.Smells {
		chips = append(chips, h.Span(h.Class("chip chip-smell"), h.Title(f.Detail+". "+f.Rule.Why()), h.Str("new: "+strings.ToLower(ruleLabel(f.Rule)))))
	}
	for _, r := range u.Reasons {
		chips = append(chips, h.Span(h.Class("chip"), h.Str(r)))
	}
	where := u.File
	if n := firstLine(u); n > 0 {
		where = fmt.Sprintf("%s:%d", u.File, n)
	}
	head := h.Div(h.Class("unit-head"),
		h.Button(h.Class("mark"), h.Aria("label", label), h.Title(label), on.Click(on.Bind(p.Mark, u.Key)), h.Str(glyph)),
		h.Span(h.Class("unit-name"), h.Str(u.Name)),
		h.Span(h.Class("badge change-"+string(u.Change)), h.Str(string(u.Change))),
		h.A(h.Class("unit-loc"), h.Href(mapHref(u)), h.Str(where)),
		h.Span(h.Class("unit-delta"), delta(u.Added, u.Deleted)),
		h.Span(h.Class("chips"), group(chips)),
	)
	var body h.H
	switch {
	case p.showDiff(u, l):
		body = h.Div(h.Class("unit-body"), p.diffView(u), p.callersLine(a, u))
		if p.opened[u.Key] {
			body = group([]h.H{body, h.Div(h.Class("fold"), h.Button(h.Class("expand"), on.Click(on.Bind(p.Open, u.Key)), h.Str("Hide diff")))})
		}
	case len(u.Lines) > 0:
		body = h.Div(h.Class("fold"), h.Button(h.Class("expand"), on.Click(on.Bind(p.Open, u.Key)), h.Str("Show diff · "+plural(len(u.Lines), "line"))))
	}
	return h.Article(h.Class(cls), h.ID(unitID(u.Key)), h.TabIndex(0), head, body)
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
	lines, cut := u.Lines, 0
	if len(lines) > maxDiffLines && !p.whole[u.Key] {
		lines, cut = lines[:maxDiffLines], len(lines)-maxDiffLines
	}
	rows := foldRows(lines)
	if cut > 0 {
		rows = append(rows, h.Button(h.Class("expand"), on.Click(on.Bind(p.ShowWhole, u.Key)), h.Str(fmt.Sprintf("Show the other %s", plural(cut, "line")))))
	}
	return h.Div(h.Class("diff"), group(rows))
}

// foldRows renders the lines, folding each long run of unchanged ones.
func foldRows(lines []review.Line) []h.H {
	var rows []h.H
	var run []review.Line
	for _, l := range lines {
		if l.Op == ' ' {
			run = append(run, l)
			continue
		}
		rows = append(rows, runRows(run, len(rows) == 0, false)...)
		run = nil
		rows = append(rows, diffRow(l))
	}
	return append(rows, runRows(run, len(rows) == 0, true)...)
}

// runRows renders a run of unchanged lines. A long one keeps contextRun lines
// beside each change and folds the rest; first and last mean no change lies
// before or after it, so that side keeps none.
func runRows(run []review.Line, first, last bool) []h.H {
	if len(run) <= 2*contextRun+1 {
		return diffRows(run)
	}
	lead, tail := run[:contextRun], run[len(run)-contextRun:]
	if first {
		lead = nil
	}
	if last {
		tail = nil
	}
	skipped := run[len(lead) : len(run)-len(tail)]
	rows := diffRows(lead)
	rows = append(rows, h.Details(h.Class("skip"), h.Summary(h.Str(fmt.Sprintf("%d unchanged lines", len(skipped)))), group(diffRows(skipped))))
	return append(rows, diffRows(tail)...)
}

func diffRows(lines []review.Line) []h.H {
	var rows []h.H
	for _, l := range lines {
		rows = append(rows, diffRow(l))
	}
	return rows
}

func diffRow(l review.Line) h.H {
	cls := map[byte]string{' ': "dl", '+': "dl dl-add", '-': "dl dl-del"}[l.Op]
	no := func(n int) h.H {
		if n == 0 {
			return h.Span(h.Class("no"))
		}
		return h.Span(h.Class("no"), h.Str(n))
	}
	return h.Div(h.Class(cls), no(l.Old), no(l.New), h.Span(h.Class("op"), h.Str(string(l.Op))), h.Span(h.Class("tx"), h.Str(l.Text)))
}

// callersLine lists who calls the unit. Callers changed in this same diff
// come first and link to their card, so a contract change and its call
// sites are read together.
func (p *reviewPage) callersLine(a *live.Analysis, u *review.Unit) h.H {
	if len(u.Callers) == 0 {
		return nil
	}
	const show = 8
	ids := slices.Clone(u.Callers)
	slices.SortStableFunc(ids, func(x, y string) int {
		_, cx := p.inDiff[x]
		_, cy := p.inDiff[y]
		return cmp.Compare(b2i(cy), b2i(cx))
	})
	var links []h.H
	for i, id := range ids {
		if i == show {
			links = append(links, h.Span(h.Class("hint"), h.Str(fmt.Sprintf("and %d more", len(ids)-show))))
			break
		}
		d := a.Snap.Decl(id)
		if d == nil {
			continue
		}
		name := shortPkg(a, d.Package) + d.Name
		if key, ok := p.inDiff[id]; ok {
			links = append(links, h.A(h.Class("changed"), h.Href("#"+unitID(key)), h.Title("Changed in this diff"), h.Str(name), h.Span(h.Class("badge"), h.Str("changed"))))
			continue
		}
		links = append(links, h.A(h.Href(mapURL{in: d.File, decl: d.ID}.String()), h.Str(name)))
	}
	return h.P(h.Class("callers-line"), h.Span(h.Class("hint"), h.Str("Called by")), group(links))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mapHref(u *review.Unit) string {
	if u.Kind == "" {
		return mapURL{in: u.File}.String()
	}
	return mapURL{in: u.File, decl: u.ID}.String()
}

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

// key is one keyboard hint; its keys and label never wrap apart.
func key(label string, keys ...string) h.H {
	var kids []h.H
	for _, k := range keys {
		kids = append(kids, h.Kbd(h.Str(k)))
	}
	return h.Span(h.Class("key"), group(kids), h.Str(label))
}

func short(sha string) string { return sha[:min(len(sha), 7)] }
