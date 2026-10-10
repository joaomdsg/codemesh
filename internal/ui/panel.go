package ui

import (
	"fmt"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

func (p *diagnosePage) panel(a *live.Analysis, gs []prognosis.Prognosis, g *prognosis.Prognosis, run *fix.Run) h.H {
	related := relatedRefs(gs, g)
	where := g.Name
	if g.File != "" {
		where = fmt.Sprintf("%s · %s:%d", g.Name, g.File, g.Line)
	}
	var steps []h.H
	for _, s := range g.Do {
		steps = append(steps, h.Li(h.Str(s)))
	}
	var src h.H
	if d := a.Snap.Decl(g.Target); d != nil {
		src = h.Details(h.Class("dx-fold"), h.Summary(h.Str(fmt.Sprintf("The code · %d lines", d.Lines))), source(a.Snap, d))
	}
	return h.Div(h.Class("dx dx-open"),
		h.Aside(h.Class("dx-side"),
			feed(map[expr.Expr]any{p.Diag.Ref(): diagOf(a, gs, string(g.Lens)).around(g.Target, refIDs(g.Related))}),
			h.Div(h.Class("dx-map dx-mini atlas atlas-map"), h.DataIgnoreMorph(),
				h.DataEffect(expr.Rawf("codemesh.diag(el, %s, {mode: 'mini'})", p.Diag.Ref()))),
			ramp(string(g.Lens)),
			via.When(len(related) > 0, func() h.H {
				return group([]h.H{h.H3(h.Str("Related")), h.Ul(append([]h.H{h.Class("dx-refs")}, related...)...)})
			}),
		),
		h.Article(h.Class("dx-panel"),
			h.Div(h.Class("panel-head"),
				h.A(h.Class("dx-back"), h.Href(lensHref(string(g.Lens))), h.Str("← All places")),
				h.Span(h.Class("dx-level"), levelMark(g.Level), h.Str(" "+levelWords[g.Level])),
			),
			h.Div(h.Class("dx-cols"),
				h.Div(h.Class("dx-prose"),
					h.H2(h.Class("dx-title"), h.Str(g.Title)),
					h.P(h.Class("loc"), h.Str(where)),
					h.P(h.Class("dx-summary"), h.Str(g.Summary)),
					h.H3(h.Str("Why it matters")),
					h.P(h.Str(g.Why)),
					h.H3(h.Str("What to do")),
					h.Ol(append([]h.H{h.Class("dx-steps")}, steps...)...),
					h.H3(h.Str("How to check")),
					h.P(h.Str(g.Check)),
				),
				h.Div(h.Class("dx-more"),
					h.H3(h.Str("The numbers")),
					h.Table(h.Class("dx-facts"), h.Thead(h.Tr(h.Th(h.Str("")), h.Th(h.Str("now")), h.Th(h.Str("limit")))), h.Tbody(factRows(g)...)),
					src,
				),
			),
		),
		p.treat(a, g, run),
	)
}

// relatedRefs lists g's related places, each linked when it has a prognosis of
// its own.
func relatedRefs(gs []prognosis.Prognosis, g *prognosis.Prognosis) []h.H {
	var out []h.H
	for _, r := range g.Related {
		target := h.Span(h.Class("dx-ref-name"), h.Str(r.Name))
		for _, o := range gs {
			if o.Target == r.ID && o.Key != g.Key {
				target = h.A(h.Class("dx-ref-name"), h.Href(prognosisHref(o.Key)), levelMark(o.Level), h.Str(" "+r.Name))
				break
			}
		}
		out = append(out, h.Li(h.Class("dx-ref"), h.Data("ref", r.ID), target, h.Span(h.Class("dx-ref-why"), h.Str(r.Why))))
	}
	return out
}

func factRows(g *prognosis.Prognosis) []h.H {
	var out []h.H
	for _, f := range g.Facts {
		limit := h.Td(h.Class("num"))
		if f.Limit > 0 {
			limit = h.Td(h.Class("num"), h.Str(f.Limit))
		}
		cls := "num"
		if f.Limit > 0 && f.Value > f.Limit {
			cls = "num over"
		}
		out = append(out, h.Tr(h.Td(h.Str(f.Label)), h.Td(h.Class(cls), h.Str(f.Value)), limit))
	}
	return out
}

func refIDs(rs []prognosis.Ref) []string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
	}
	return ids
}
