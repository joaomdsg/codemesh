package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// result shows the attempt as the map before and after, what the change
// fixed and broke, and its code in impact order.
func (p *diagnosePage) result(g *prognosis.Prognosis, s fix.Snapshot) h.H {
	fixed, added := outcome(s)
	units := slices.Clone(s.Review.Units)
	slices.SortStableFunc(units, func(a, b *review.Unit) int { return cmp.Compare(b.Risk, a.Risk) })
	changed := make([]string, 0, len(units))
	var cards []h.H
	for _, u := range units {
		changed = append(changed, u.ID)
		cards = append(cards, changeCard(u))
	}
	gone, resigned := broken(s.Before.Snap, s.After.Snap)
	a := &live.Analysis{At: time.Now(), Snap: s.Before.Snap}
	b := &live.Analysis{At: time.Now(), Snap: s.After.Snap}
	was, now := diagOf(a, s.Before.Prognoses, string(g.Lens)).around(g.Target, nil), diagOf(b, s.After.Prognoses, string(g.Lens)).around(g.Target, changed)
	was.Broke, now.Broke = gone, resigned
	return h.Div(h.Class("dx-result"),
		verdict(g, s),
		rounds(s),
		h.Div(h.Class("dx-scores"),
			score(s.Check.Label+" pass", s.Before.CheckOK, s.After.CheckOK),
			count("Places needing attention", len(s.Before.Prognoses), len(s.After.Prognoses)),
			count("Smells", len(s.Before.Findings), len(s.After.Findings)),
		),
		feed(map[expr.Expr]any{
			p.Before.Ref(): was,
			p.After.Ref():  now,
		}),
		h.Div(h.Class("dx-compare"),
			h.Figure(h.Figcaption(h.Str("Before")), h.Div(h.Class("dx-map dx-cmp atlas atlas-map"), h.DataIgnoreMorph(),
				h.DataEffect(expr.Rawf("codemesh.diag(el, %s, {mode: 'compare'})", p.Before.Ref())))),
			h.Figure(h.Figcaption(h.Str("After · changed code outlined")), h.Div(h.Class("dx-map dx-cmp atlas atlas-map"), h.DataIgnoreMorph(),
				h.DataEffect(expr.Rawf("codemesh.diag(el, %s, {mode: 'compare'})", p.After.Ref())))),
		),
		h.Div(h.Class("dx-cmp-key"), ramp(string(g.Lens)),
			h.P(h.Class("ramp"), h.Span(h.Class("ramp-end"), h.Span(h.Class("sw sw-chg"), h.Aria("hidden", "true")), h.Str("code the change touched"))),
			h.P(h.Class("ramp"), h.Span(h.Class("ramp-end"), h.Span(h.Class("sw sw-broke"), h.Aria("hidden", "true")), h.Str("exported, and its signature changed or it was removed: callers may break")))),
		prognosisDelta("Fixed", "gone", fixed),
		prognosisDelta("New", "new", added),
		smellDelta("New smells", "new", s.Review.Introduced),
		smellDelta("Smells fixed", "gone", s.Review.Fixed),
		via.When(s.Summary != "", func() h.H {
			return group([]h.H{h.H3(h.Str(s.SummaryTitle())), h.Div(h.Class("dx-claude"), markdown(s.Summary))})
		}),
		via.When(!s.After.CheckOK, func() h.H {
			return h.Details(h.Class("dx-fold"), h.Summary(h.Str(s.Check.Name+" output")), h.Pre(h.Class("code dx-out"), h.Str(s.After.Output)))
		}),
		via.When(len(cards) > 0, func() h.H {
			return h.Details(h.Class("dx-fold"), h.Summary(h.Str(fmt.Sprintf("The code changes · %s, most impact first", plural(len(cards), "declaration")))), group(cards))
		}),
	)
}

// outcome splits what a run changed on the map into the prognoses it cleared
// and the ones it added.
func outcome(s fix.Snapshot) (fixed, added []prognosis.Prognosis) {
	before, after := keys(s.Before.Prognoses), keys(s.After.Prognoses)
	for _, x := range s.Before.Prognoses {
		if !after[x.Key] {
			fixed = append(fixed, x)
		}
	}
	for _, x := range s.After.Prognoses {
		if !before[x.Key] {
			added = append(added, x)
		}
	}
	return fixed, added
}

// rounds says what each of Claude's rounds left, when there was more than one.
func rounds(s fix.Snapshot) h.H {
	note := s.RoundsNote()
	if note == "" {
		return nil
	}
	return h.P(h.Class("hint"), h.Str(note))
}

// verdict says whether the problem is gone; a run that broke the checks
// outranks that.
func verdict(g *prognosis.Prognosis, s fix.Snapshot) h.H {
	switch {
	case !s.After.CheckOK && s.Before.CheckOK:
		return h.P(h.Class("dx-verdict bad"), h.Str("▲ "+s.Check.Label+" passed before and fail now. Do not keep this as it is."))
	case keys(s.After.Prognoses)[g.Key]:
		return h.P(h.Class("dx-verdict warn"), h.Str("▲ The problem is still there. The changes may still help; read them before deciding."))
	}
	return okLine("This problem is gone.")
}

func keys(gs []prognosis.Prognosis) map[string]bool {
	out := map[string]bool{}
	for _, g := range gs {
		out[g.Key] = true
	}
	return out
}

func score(label string, before, after bool) h.H {
	word := map[bool]string{true: "yes", false: "no"}
	cls := "dx-score"
	if before && !after {
		cls += " worse"
	}
	return h.Div(h.Class(cls), h.Span(h.Class("dx-score-label"), h.Str(label)), h.Span(h.Class("dx-score-val"), h.Str(word[before]+" → "+word[after])))
}

func count(label string, before, after int) h.H {
	cls := "dx-score"
	switch {
	case after < before:
		cls += " better"
	case after > before:
		cls += " worse"
	}
	return h.Div(h.Class(cls), h.Span(h.Class("dx-score-label"), h.Str(label)), h.Span(h.Class("dx-score-val"), h.Str(fmt.Sprintf("%d → %d", before, after))))
}

func prognosisDelta(title, cls string, gs []prognosis.Prognosis) h.H {
	if len(gs) == 0 {
		return nil
	}
	var rows []h.H
	for _, g := range gs {
		rows = append(rows, h.Li(h.Class("finding "+cls), levelMark(g.Level), h.Span(h.Class("rule"), h.Str(g.Title)), h.Span(h.Class("detail"), h.Str(g.Name))))
	}
	return group([]h.H{h.H3(h.Str(fmt.Sprintf("%s · %d", title, len(gs)))), h.Ul(append([]h.H{h.Class("findings")}, rows...)...)})
}

func smellDelta(title, cls string, fs []smell.Finding) h.H {
	if len(fs) == 0 {
		return nil
	}
	var rows []h.H
	for _, f := range fs {
		rows = append(rows, h.Li(h.Class("finding "+cls), sevMark(f.Severity), h.Span(h.Class("rule"), h.Str(ruleLabel(f.Rule))),
			h.Span(h.Class("detail"), h.Str(f.Subject+" · "+f.Detail))))
	}
	return group([]h.H{h.H3(h.Str(fmt.Sprintf("%s · %d", title, len(fs)))), h.Ul(append([]h.H{h.Class("findings")}, rows...)...)})
}

// broken lists the exported declarations of a that b removed, and those b
// re-signed.
func broken(a, b *code.Snapshot) (gone, resigned []string) {
	for _, d := range a.Decls() {
		if !d.Exported || d.Test {
			continue
		}
		switch n := b.Decl(d.ID); {
		case n == nil:
			gone = append(gone, d.ID)
		case n.Signature != d.Signature:
			resigned = append(resigned, d.ID)
		}
	}
	return gone, resigned
}

func changeCard(u *review.Unit) h.H {
	var chips []h.H
	for _, f := range u.Smells {
		chips = append(chips, h.Span(h.Class("chip chip-smell"), h.Str("new: "+strings.ToLower(ruleLabel(f.Rule)))))
	}
	for _, r := range u.Reasons {
		chips = append(chips, h.Span(h.Class("chip"), h.Str(r)))
	}
	var rows []h.H
	for _, l := range u.Lines[:min(len(u.Lines), maxDiffLines)] {
		rows = append(rows, diffRow(l))
	}
	return h.Article(h.Class("unit"),
		h.Div(h.Class("unit-head"),
			h.Span(h.Class("unit-name"), h.Str(u.Name)),
			h.Span(h.Class("badge"), h.Str(string(u.Change))),
			h.Span(h.Class("unit-loc"), h.Str(u.File)),
			h.Span(h.Class("unit-delta"), delta(u.Added, u.Deleted)),
			h.Span(h.Class("chips"), group(chips)),
		),
		via.When(len(rows) > 0, func() h.H { return h.Div(h.Class("diff"), group(rows)) }),
	)
}
