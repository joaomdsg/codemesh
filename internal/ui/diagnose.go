package ui

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

// diagnosePage is the health diagnosis: the map with a marker on each place
// that needs attention, and a full prognosis for the one opened, which
// Claude can be asked to treat in a throwaway worktree.
type diagnosePage struct {
	shell
	// From the path, not the query: via rebuilds a page's stream and action
	// URLs from its mount pattern alone, so a query would be gone by the
	// render that decides which actions exist.
	lens string
	open string // prognosis key

	// The island's inputs, client-only: the map with every lens's heat and
	// marks, and for a finished run the trees before and after it.
	Diag   via.SignalCS[diag]
	Before via.SignalCS[diag]
	After  via.SignalCS[diag]
	Replay via.SignalCS[replay]
}

var diagLenses = []lens{
	{string(prognosis.Health), "Health", "hard code; hotter when it also changes often"},
	{string(prognosis.Reach), "Reach", "how many places use each declaration"},
	{string(prognosis.Structure), "Structure", "how much each package depends on others"},
	{string(prognosis.Tests), "Tests & smells", "complex code no test calls, and smells that slow changes"},
}

var levelWords = map[prognosis.Level]string{3: "Fix first", 2: "Fix soon", 1: "When convenient"}

func (p *diagnosePage) OnInit(ctx *via.Ctx) error {
	p.lens, p.open = ctx.Request().PathValue("lens"), ctx.Request().PathValue("key")
	if !slices.ContainsFunc(diagLenses, func(l lens) bool { return l.key == p.lens }) {
		p.lens = string(prognosis.Health)
	}
	p.start(ctx)
	ctx.Listen(p.runs.Updates, func(*via.Ctx, int64) {})
	return nil
}

func (p *diagnosePage) PageMeta() via.Meta { return via.Meta{Title: "Diagnose · codemesh"} }

// Fix asks Claude to treat a prognosis.
func (p *diagnosePage) Fix(_ *via.Ctx, key string) {
	if g := p.find(key); g != nil {
		p.runs.Start(*g)
	}
}

// Stop ends Claude's work early.
func (p *diagnosePage) Stop(_ *via.Ctx, key string) {
	if r := p.runs.Get(key); r != nil {
		r.Stop()
	}
}

// PR opens a draft pull request with a finished run's change.
func (p *diagnosePage) PR(_ *via.Ctx, key string) {
	if r := p.runs.Get(key); r != nil {
		r.OpenPR()
	}
}

// Discard drops a run and its worktree.
func (p *diagnosePage) Discard(_ *via.Ctx, key string) { p.runs.Dismiss(key) }

func (p *diagnosePage) prognoses(a *live.Analysis) []prognosis.Prognosis {
	return prognosis.Find(a.Snap, a.Findings)
}

func (p *diagnosePage) find(key string) *prognosis.Prognosis {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return nil
	}
	return byKey(p.prognoses(a), key)
}

func byKey(gs []prognosis.Prognosis, key string) *prognosis.Prognosis {
	for i := range gs {
		if gs[i].Key == key {
			return &gs[i]
		}
	}
	return nil
}

func lensHref(lens string) string { return "/diagnose/" + lens }

func prognosisHref(key string) string { return "/prognosis/" + url.PathEscape(key) }

func (p *diagnosePage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabDiagnose, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	gs := p.prognoses(a)
	g := byKey(gs, p.open)
	run := p.runs.Get(p.open)
	if g == nil && run != nil {
		// The tree changed under an open run; its starting analysis still names it.
		if s := run.Snapshot(); s.Before != nil {
			g = byKey(s.Before.Prognoses, p.open)
		}
	}
	if g == nil {
		return p.frame(tabDiagnose, a, p.overview(a, gs))
	}
	return p.frame(tabDiagnose, a, p.panel(a, gs, g, run))
}

func (p *diagnosePage) overview(a *live.Analysis, gs []prognosis.Prognosis) h.H {
	var here []prognosis.Prognosis
	for _, g := range gs {
		if string(g.Lens) == p.lens {
			here = append(here, g)
		}
	}
	var rows []h.H
	for _, g := range here[:min(len(here), 6)] {
		rows = append(rows, h.Li(h.A(h.Class("dx-row"), h.Href(prognosisHref(g.Key)),
			levelMark(g.Level), h.Span(h.Class("dx-row-title"), h.Str(g.Title)), h.Span(h.Class("dx-row-name"), h.Str(g.Name)))))
	}
	list := okLine("Nothing needs attention through this lens.")
	if len(rows) > 0 {
		list = group([]h.H{
			h.H2(h.Str("Start here")),
			h.Ul(append([]h.H{h.Class("dx-list")}, rows...)...),
			via.When(len(here) > len(rows), func() h.H {
				return h.P(h.Class("hint"), h.Str(fmt.Sprintf("%d more on the map.", len(here)-len(rows))))
			}),
		})
	}
	return h.Div(h.Class("dx"),
		h.Section(h.Class("dx-pane"),
			h.Div(h.Class("map-bar"), p.census(gs), p.lensBar()),
			feed(map[expr.Expr]any{p.Diag.Ref(): diagOf(a, gs, p.lens)}),
			h.Div(h.Class("dx-map atlas atlas-map"), h.DataIgnoreMorph(),
				h.DataEffect(expr.Rawf("codemesh.diag(el, %s, {mode: 'full'})", p.Diag.Ref()))),
			legend(p.lens, a.Snap.Lang),
		),
		h.Aside(h.Class("dx-side"), list),
	)
}

// census counts the open prognoses by urgency, across every lens.
func (p *diagnosePage) census(gs []prognosis.Prognosis) h.H {
	if len(gs) == 0 {
		return okLine("Nothing needs attention.")
	}
	n := map[prognosis.Level]int{}
	for _, g := range gs {
		n[g.Level]++
	}
	var kids []h.H
	for _, l := range []prognosis.Level{3, 2, 1} {
		if n[l] > 0 {
			kids = append(kids, h.Span(h.Class("dx-count"), levelMark(l), h.Str(fmt.Sprintf("%d %s", n[l], strings.ToLower(levelWords[l])))))
		}
	}
	return h.P(append([]h.H{h.Class("dx-census")}, kids...)...)
}

func (p *diagnosePage) lensBar() h.H {
	var kids []h.H
	for _, l := range diagLenses {
		cls := "seg"
		if l.key == p.lens {
			cls = "seg on"
		}
		kids = append(kids, h.A(h.Class(cls), h.Href(lensHref(l.key)), h.Title(l.help), h.Str(l.label)))
	}
	return h.Nav(append([]h.H{h.Class("segs"), h.Aria("label", "Lens")}, kids...)...)
}

func legend(l string, lang code.Lang) h.H {
	return group([]h.H{
		ramp(l),
		h.P(h.Class("hint dx-legend"),
			h.Str("Area is lines of code. "),
			h.Span(h.Class("dx-lvl"), levelMark(3), h.Str(" fix first")), h.Span(h.Class("dx-lvl"), levelMark(2), h.Str(" fix soon")),
			h.Span(h.Class("dx-lvl"), levelMark(1), h.Str(" when convenient.")),
			h.Str(" Hover a marker for the short version; click it for the full one. Click any tile to see what calls it; Escape clears.")),
		via.When(lang == code.Julia, func() h.H {
			return h.P(h.Class("hint dx-legend"), h.Str("Julia picks a method when the code runs, so calls are matched by name: callers and reach are estimates."))
		}),
	})
}

// rampEnds names what the coldest and hottest colours mean in each lens, by
// the bands prognosis.Heats uses.
var rampEnds = map[string][2]string{
	string(prognosis.Health):    {"simple", "complexity 20+, or 10+ and changing often"},
	string(prognosis.Reach):     {"no callers", "100+ callers"},
	string(prognosis.Structure): {"depends on little", "depends on much"},
	string(prognosis.Tests):     {"tested or simple", "complex, and no test calls it"},
}

// ramp is the lens's colour scale, cold to hot, with what its ends mean.
func ramp(l string) h.H {
	ends, ok := rampEnds[l]
	if !ok {
		return nil
	}
	sw := []h.H{h.Class("ramp-sw"), h.Aria("hidden", "true")}
	for i := range 6 {
		sw = append(sw, h.Span(h.Class(fmt.Sprintf("sw h%d", i))))
	}
	return h.P(h.Class("ramp"),
		h.Span(h.Class("ramp-end"), h.Str(ends[0])), h.Span(sw...), h.Span(h.Class("ramp-end"), h.Str(ends[1])),
		h.Span(h.Class("ramp-end"), h.Span(h.Class("sw sw-exp"), h.Aria("hidden", "true")), h.Str("exported: other packages can use it")))
}

func levelMark(l prognosis.Level) h.H {
	return h.Span(h.Class(fmt.Sprintf("lvl lvl-%d", l)), h.Aria("hidden", "true"))
}
