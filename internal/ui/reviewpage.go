package ui

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// ReviewPage is the review queue: the change as declarations, by lane, risk
// first, with reviewed units folded away.
type ReviewPage struct {
	shell
	err string
}

func (p *ReviewPage) OnInit(ctx *via.Ctx) error {
	p.start(ctx)
	return nil
}

func (p *ReviewPage) PageMeta() via.Meta { return via.Meta{Title: "Review · codemesh"} }

// Mark toggles one unit's reviewed mark.
func (p *ReviewPage) Mark(_ *via.Ctx, key string) {
	p.save(key, !p.state.Reviewed(key))
}

// AcceptNoise marks every noise unit reviewed.
func (p *ReviewPage) AcceptNoise(_ *via.Ctx) {
	a := p.src.Current()
	if a == nil || a.Review == nil {
		return
	}
	for _, u := range a.Review.Lane(review.Noise) {
		if !p.save(u.Key, true) {
			return
		}
	}
}

// Rescan re-analyses now, without waiting for the watcher.
func (p *ReviewPage) Rescan(_ *via.Ctx) { p.src.Refresh() }

func (p *ReviewPage) save(key string, done bool) bool {
	if err := p.state.Set(key, done); err != nil {
		p.err = "Review mark not saved: " + err.Error() + ". Check that the .git directory is writable, then try again."
		return false
	}
	p.err = ""
	return true
}

func (p *ReviewPage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	if a.Review == nil {
		return p.frame(tabReview, a, h.P(h.Class("empty"), h.Str("Nothing to review: "+a.Note+".")))
	}
	rev := a.Review
	done := len(rev.Units) - p.open(a)
	var lanes []h.H
	for _, l := range review.Lanes {
		if units := rev.Lane(l); len(units) > 0 {
			lanes = append(lanes, p.lane(a, l, units))
		}
	}
	body := []h.H{p.summary(a, done)}
	if p.err != "" {
		body = append(body, h.Div(h.Class("banner err"), h.Role("alert"), h.Str(p.err)))
	}
	if len(rev.Units) == 0 {
		body = append(body, h.P(h.Class("empty"), h.Str("No changes against "+a.Base+".")))
	}
	body = append(body, p.smellDelta(rev))
	body = append(body, lanes...)
	return p.frame(tabReview, a, h.Div(h.Class("review-layout"),
		p.outline(rev),
		h.Div(append([]h.H{h.Class("review")}, body...)...),
	))
}

func (p *ReviewPage) summary(a *live.Analysis, done int) h.H {
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
		h.P(h.Class("keys"), h.Kbd(h.Str("j")), h.Kbd(h.Str("k")), h.Str(" move "), h.Kbd(h.Str("r")), h.Str(" mark reviewed "), h.Kbd(h.Str("n")), h.Str(" next unreviewed")),
	)
}

func (p *ReviewPage) smellDelta(rev *review.Review) h.H {
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

// outline is the whole change at a glance: every unit by lane, with its
// mark, linking to its card.
func (p *ReviewPage) outline(rev *review.Review) h.H {
	var kids []h.H
	for _, l := range review.Lanes {
		units := rev.Lane(l)
		if len(units) == 0 {
			continue
		}
		var rows []h.H
		for _, u := range units {
			cls, glyph := "ol-row", "○"
			if p.state.Reviewed(u.Key) {
				cls, glyph = "ol-row done", "✓"
			}
			rows = append(rows, h.Li(h.A(h.Class(cls), h.Href("#"+unitID(u.Key)),
				h.Span(h.Class("ol-mark"), h.Str(glyph)),
				h.Span(h.Class("ol-name"), h.Str(u.Name)),
				h.Span(h.Class("ol-delta"), h.Str(fmt.Sprintf("+%d −%d", u.Added, u.Deleted))),
			)))
		}
		kids = append(kids, h.H3(h.Str(l.String())), h.Ul(rows...))
	}
	return h.Nav(append([]h.H{h.Class("outline"), h.Aria("label", "Units")}, kids...)...)
}

var laneHelp = map[review.Lane]string{
	review.Contract: "exported API added, removed or re-signed: read first, others depend on it",
	review.Logic:    "behaviour changes, riskiest first",
	review.Tests:    "test code",
	review.Other:    "files outside Go code",
	review.Noise:    "no change in meaning: comments, layout, moves, generated code",
}

func (p *ReviewPage) lane(a *live.Analysis, l review.Lane, units []*review.Unit) h.H {
	open := 0
	for _, u := range units {
		if !p.state.Reviewed(u.Key) {
			open++
		}
	}
	head := h.Div(h.Class("lane-head"),
		h.H3(h.Str(l.String())),
		h.Span(h.Class("hint"), h.Str(fmt.Sprintf("%d of %d open · %s", open, len(units), laneHelp[l]))),
	)
	var accept h.H
	if l == review.Noise && open > 0 {
		accept = h.Button(h.Class("btn"), on.Click(p.AcceptNoise), h.Str("Mark all noise reviewed"))
	}
	var cards []h.H
	for _, u := range units {
		cards = append(cards, p.card(a, u, l == review.Noise))
	}
	return h.Section(h.Class("lane lane-"+strings.ToLower(l.String())), h.Div(h.Class("lane-bar"), head, accept), group(cards))
}

func (p *ReviewPage) card(a *live.Analysis, u *review.Unit, folded bool) h.H {
	done := p.state.Reviewed(u.Key)
	cls := "unit"
	if done {
		cls += " reviewed"
	}
	label, glyph := "Mark reviewed", "○"
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
		h.Span(h.Class("unit-delta"), h.Span(h.Class("add"), h.Str(fmt.Sprintf("+%d", u.Added))), h.Str(" "), h.Span(h.Class("del"), h.Str(fmt.Sprintf("−%d", u.Deleted)))),
		h.Span(h.Class("chips"), group(chips)),
	)
	var body h.H
	if !done && !folded {
		body = group([]h.H{diffView(u), callersLine(a, u)})
	}
	if !done && folded {
		body = h.Details(h.Class("fold"), h.Summary(h.Str("Show diff")), diffView(u))
	}
	return h.Article(h.Class(cls), h.ID(unitID(u.Key)), h.TabIndex(0), head, body)
}

// contextRun is how many unchanged lines show around a change; longer runs
// fold.
const contextRun = 3

// maxDiffLines caps one unit's rendered diff; a fixture or a vendored file
// can run to thousands of lines nobody reads in a review.
const maxDiffLines = 400

func diffView(u *review.Unit) h.H {
	if len(u.Lines) == 0 {
		return nil
	}
	lines, cut := u.Lines, 0
	if len(lines) > maxDiffLines {
		lines, cut = lines[:maxDiffLines], len(lines)-maxDiffLines
	}
	var rows []h.H
	var run []review.Line
	flush := func(last bool) {
		if len(run) > 2*contextRun+1 {
			lead, tail := run[:contextRun], run[len(run)-contextRun:]
			if len(rows) == 0 {
				lead = nil
			}
			if last {
				tail = nil
			}
			for _, l := range lead {
				rows = append(rows, diffRow(l))
			}
			hidden := len(run) - len(lead) - len(tail)
			var inner []h.H
			for _, l := range run[len(lead) : len(run)-len(tail)] {
				inner = append(inner, diffRow(l))
			}
			rows = append(rows, h.Details(h.Class("skip"), h.Summary(h.Str(fmt.Sprintf("%d unchanged lines", hidden))), group(inner)))
			for _, l := range tail {
				rows = append(rows, diffRow(l))
			}
		} else {
			for _, l := range run {
				rows = append(rows, diffRow(l))
			}
		}
		run = nil
	}
	for _, l := range lines {
		if l.Op == ' ' {
			run = append(run, l)
			continue
		}
		flush(false)
		rows = append(rows, diffRow(l))
	}
	flush(true)
	if cut > 0 {
		rows = append(rows, h.P(h.Class("cut"), h.Str(fmt.Sprintf("%d more lines not shown. Open %s to read them.", cut, u.File))))
	}
	return h.Div(h.Class("diff"), group(rows))
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

func callersLine(a *live.Analysis, u *review.Unit) h.H {
	if len(u.Callers) == 0 {
		return nil
	}
	const show = 8
	var links []h.H
	for i, id := range u.Callers {
		if i == show {
			links = append(links, h.Span(h.Class("hint"), h.Str(fmt.Sprintf("and %d more", len(u.Callers)-show))))
			break
		}
		d := a.Snap.Decl(id)
		if d == nil {
			continue
		}
		links = append(links, h.A(h.Href(mapURL{in: d.File, decl: d.ID}.String()), h.Str(shortPkg(a, d.Package)+d.Name)))
	}
	return h.P(h.Class("callers-line"), h.Span(h.Class("hint"), h.Str("Called by ")), group(links))
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

func short(sha string) string { return sha[:min(len(sha), 7)] }
