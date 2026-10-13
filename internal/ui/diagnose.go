package ui

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

// diagnosePage is the health diagnosis: the map with a marker on each place
// that needs attention, and a full prognosis for the one opened.
type diagnosePage struct {
	shell
	// From the path, not the query: via rebuilds a page's stream and action
	// URLs from its mount pattern alone, so a query would be gone by the
	// render that decides which actions exist.
	lens string
	open string // prognosis key
	err  string // the last basket write that failed

	// The island's input, client-only: the map with every lens's heat and
	// marks.
	Diag via.SignalCS[diag]
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
	return p.start(ctx)
}

// Handoff puts the prognosis with this key in the handoff basket.
func (p *diagnosePage) Handoff(_ *via.Ctx, key string) {
	a := p.analysis()
	defer a.Release()
	if a.Snap == nil {
		return
	}
	if g := byKey(p.prognoses(a), key); g != nil {
		_, err := p.app.BasketAdd(prognosisItem(a, *g))
		p.err = basketError(err)
	}
}

// handoff is g's "Add to handoff" control.
func (p *diagnosePage) handoff(g *prognosis.Prognosis) h.H {
	in := inBasket(p.app.Basket(), handoff.Prognosis, p.repo.ID, g.Key)
	return handoffControl(in, on.Click(on.Bind(p.Handoff, g.Key)))
}

func (p *diagnosePage) PageMeta() via.Meta { return via.Meta{Title: "Health · codemesh"} }

func (p *diagnosePage) prognoses(a *app.Analysis) []prognosis.Prognosis { return a.Prognoses() }

func byKey(gs []prognosis.Prognosis, key string) *prognosis.Prognosis {
	for i := range gs {
		if gs[i].Key == key {
			return &gs[i]
		}
	}
	return nil
}

func (p *diagnosePage) lensHref(lens string) string { return p.href("health/" + lens) }

func (p *diagnosePage) prognosisHref(key string) string {
	return p.href("prognosis/" + url.PathEscape(key))
}

// diag is the island's input for lens: the analysis laid out, with the links
// the island builds pointing at this repo.
func (p *diagnosePage) diag(a *app.Analysis, gs []prognosis.Prognosis, lens string) diag {
	d := diagOf(a, gs, lens)
	d.Base = p.base()
	return d
}

func (p *diagnosePage) View() h.H {
	a := p.analysis()
	defer a.Release()
	if a.Snap == nil {
		return p.unavailable(tabHealth, a)
	}
	gs := p.prognoses(a)
	g := byKey(gs, p.open)
	if g == nil {
		return p.frame(tabHealth, a, basketErrorLine(p.err), p.overview(a, gs))
	}
	return p.frame(tabHealth, a, basketErrorLine(p.err), p.panel(a, gs, g))
}

func (p *diagnosePage) overview(a *app.Analysis, gs []prognosis.Prognosis) h.H {
	var here []prognosis.Prognosis
	for _, g := range gs {
		if string(g.Lens) == p.lens {
			here = append(here, g)
		}
	}
	shown := here[:min(len(here), 6)]
	// One reason said six times is noise: say it once above the list.
	shared := len(shown) > 1 && !slices.ContainsFunc(shown, func(g prognosis.Prognosis) bool { return g.Title != shown[0].Title })
	var rows []h.H
	for _, g := range shown {
		row := []h.H{h.Class("dx-row"), h.Href(p.prognosisHref(g.Key)), levelMark(g.Level), h.Span(h.Class("dx-row-title"), h.Str(g.Name))}
		if !shared {
			row = append(row, h.Span(h.Class("dx-row-why"), h.Str(g.Title)))
		}
		rows = append(rows, h.Li(h.A(row...), h.Div(h.Class("dx-act"), p.handoff(&g))))
	}
	list := okLine("Nothing needs attention through this lens.")
	if len(rows) > 0 {
		var why h.H
		if shared {
			why = h.P(h.Class("hint dx-why"), h.Str(shown[0].Title))
		}
		list = group([]h.H{
			h.H2(h.Str("Start here")),
			why,
			h.Ul(append([]h.H{h.Class("dx-list")}, rows...)...),
			via.When(len(here) > len(rows), func() h.H {
				return h.P(h.Class("hint"), h.Str(fmt.Sprintf("%d more on the map.", len(here)-len(rows))))
			}),
		})
	}
	return h.Div(h.Class("dx"),
		h.Section(h.Class("dx-pane"),
			h.Div(h.Class("map-bar"), p.census(gs), p.lensBar()),
			feed(map[expr.Expr]any{p.Diag.Ref(): p.diag(a, gs, p.lens)}),
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
		kids = append(kids, h.A(h.Class(cls), h.Href(p.lensHref(l.key)), h.Title(l.help), h.Str(l.label)))
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
