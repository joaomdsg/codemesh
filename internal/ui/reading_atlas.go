package ui

import (
	"math"

	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/thread"
)

// atlasPane is the module's map beside the reading, with the change lit and
// the open thread drawn across it. The base side lights what the change
// removed or rewrote, where it stood.
func (p *reviewPage) atlasPane(rd *reading, t thread.Thread) h.H {
	if p.noAtlas {
		return nil
	}
	side, pane := rd.head, "head"
	cards := unitCards(rd.review)
	at := rd.at
	if p.base && rd.base != nil {
		side, pane, at = rd.base, "base", -rd.at
		cards = map[string]string{}
		for _, u := range rd.review.Units {
			if u.Kind != "" && u.Change != review.Added {
				cards[u.ID] = unitID(u.Key)
			}
		}
	}
	centres := map[string][2]float64{}
	lay := atlasOf(side, 900, 600, func(_ int, tile *atlasTile, _ *code.Package, _ *code.File, d *code.Decl) {
		if d == nil {
			return
		}
		tile.Unit = cards[d.ID]
		centres[d.ID] = [2]float64{round1(tile.X + tile.W/2), round1(tile.Y + tile.H/2)}
	})
	lay.At = at
	path := [][2]float64{}
	if pane == "head" && t.Root != "" {
		for _, id := range pathIDs(t) {
			if c, ok := centres[id]; ok {
				path = append(path, c)
			}
		}
	}
	what := "this change"
	if rd.pr != nil {
		what = "this PR"
	}
	return h.Aside(h.Class("atlas-pane"), h.Aria("label", "Atlas"),
		h.Div(h.Class("pane-head"), h.Span(h.Class("hint"), h.Str("Atlas")),
			p.sideButton(rd, "Head", false, pane == "head"), p.sideButton(rd, "Base", true, pane == "base")),
		feed(map[expr.Expr]any{
			p.Atlas.Ref():    lay,
			p.Reviewed.Ref(): reviewedCards(rd.review, p.state),
			p.Path.Ref():     path,
			p.Pane.Ref():     pane,
		}),
		h.Div(h.Class("atlas"), h.DataIgnoreMorph(),
			h.DataEffect(expr.Rawf("codemesh.atlas(el, %s, {reviewed: %s, focus: %s, thread: %s, side: %s})",
				p.Atlas.Ref(), p.Reviewed.Ref(), p.Focus.Ref(), p.Path.Ref(), p.Pane.Ref()))),
		h.P(h.Class("atlas-legend"), h.Span(h.Class("lg-tile")), h.Str(" changed in "+what+" · "), h.Span(h.Class("lg-line")), h.Str(" thread (Head only)")),
	)
}

func (p *reviewPage) sideButton(rd *reading, label string, base, current bool) h.H {
	name := map[bool]string{false: "head", true: "base"}[base]
	cls := "seg"
	if current {
		cls += " on"
	}
	attrs := []h.H{h.Class(cls), h.Title("Show the " + name + " side of the atlas"), h.Str(label)}
	if rd.base == nil && base {
		attrs = append(attrs, h.Disabled(true))
	} else {
		attrs = append(attrs, on.Click(on.Bind(p.ShowSide, base)))
	}
	return h.Button(attrs...)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
