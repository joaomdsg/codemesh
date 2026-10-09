package ui

import (
	"cmp"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// DiagnosePage is the health diagnosis: the map with a marker on each place
// that needs attention, and a full prognosis for the one opened, which
// Claude can be asked to treat in a throwaway worktree.
type DiagnosePage struct {
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

// replay is what Claude did, step by step, over the map it started from.
type replay struct {
	Map   diag         `json:"map"`
	Steps []replayStep `json:"steps"`
	Live  bool         `json:"live"`
	Now   int64        `json:"now"` // ms since the start
}

type replayStep struct {
	Kind   string `json:"k"`
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	File   string `json:"file,omitempty"`
	Text   string `json:"text,omitempty"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
	Out    string `json:"out,omitempty"`
	Failed bool   `json:"fail,omitempty"`
	T0     int64  `json:"t0"`
	T1     int64  `json:"t1"`
}

func replayOf(a *live.Analysis, s fix.Snapshot) replay {
	// The map is the tree Claude started from; until it is analysed, the
	// live one stands in.
	base := a
	if s.Before != nil {
		base = &live.Analysis{At: time.UnixMilli(1), Snap: s.Before.Snap}
	}
	out := replay{Map: diagOf(base, nil, ""), Steps: []replayStep{}, Now: s.Took.Milliseconds(),
		Live: s.State == fix.Preparing || s.State == fix.Working || s.State == fix.Checking}
	for _, st := range s.Steps {
		out.Steps = append(out.Steps, replayStep{Kind: st.Kind, Title: st.Title, Path: st.Path, File: st.File, Text: st.Text,
			Old: st.Old, New: st.New, Out: st.Output, Failed: st.Failed, T0: st.Start.Milliseconds(), T1: st.End.Milliseconds()})
	}
	return out
}

var diagLenses = []lens{
	{string(prognosis.Health), "Health", "hard code; hotter when it also changes often"},
	{string(prognosis.Reach), "Reach", "how many places use each declaration"},
	{string(prognosis.Structure), "Structure", "how much each package depends on others"},
	{string(prognosis.Tests), "Tests & smells", "complex code no test calls, and smells that slow changes"},
}

var levelWords = map[prognosis.Level]string{3: "Fix first", 2: "Fix soon", 1: "When convenient"}

func (p *DiagnosePage) OnInit(ctx *via.Ctx) error {
	p.lens, p.open = ctx.Request().PathValue("lens"), ctx.Request().PathValue("key")
	if !slices.ContainsFunc(diagLenses, func(l lens) bool { return l.key == p.lens }) {
		p.lens = string(prognosis.Health)
	}
	p.start(ctx)
	ctx.Listen(p.runs.Updates, func(*via.Ctx, int64) {})
	return nil
}

func (p *DiagnosePage) PageMeta() via.Meta { return via.Meta{Title: "Diagnose · codemesh"} }

// Fix asks Claude to treat a prognosis.
func (p *DiagnosePage) Fix(_ *via.Ctx, key string) {
	if g := p.find(key); g != nil {
		p.runs.Start(*g)
	}
}

// Stop ends Claude's work early.
func (p *DiagnosePage) Stop(_ *via.Ctx, key string) {
	if r := p.runs.Get(key); r != nil {
		r.Stop()
	}
}

// Discard drops a run and its worktree.
func (p *DiagnosePage) Discard(_ *via.Ctx, key string) { p.runs.Dismiss(key) }

func (p *DiagnosePage) prognoses(a *live.Analysis) []prognosis.Prognosis {
	return prognosis.Find(a.Snap, a.Findings)
}

func (p *DiagnosePage) find(key string) *prognosis.Prognosis {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return nil
	}
	for _, g := range p.prognoses(a) {
		if g.Key == key {
			return &g
		}
	}
	return nil
}

func lensHref(lens string) string { return "/diagnose/" + lens }

func prognosisHref(key string) string { return "/prognosis/" + url.PathEscape(key) }

func (p *DiagnosePage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabDiagnose, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	gs := p.prognoses(a)
	var g *prognosis.Prognosis
	for i := range gs {
		if gs[i].Key == p.open {
			g = &gs[i]
		}
	}
	run := p.runs.Get(p.open)
	if g == nil && run != nil {
		// The tree changed under an open run; its starting analysis still names it.
		if s := run.Snapshot(); s.Before != nil {
			for i := range s.Before.Prognoses {
				if s.Before.Prognoses[i].Key == p.open {
					g = &s.Before.Prognoses[i]
				}
			}
		}
	}
	if g == nil {
		return p.frame(tabDiagnose, a, p.overview(a, gs))
	}
	return p.frame(tabDiagnose, a, p.panel(a, gs, g, run))
}

func (p *DiagnosePage) overview(a *live.Analysis, gs []prognosis.Prognosis) h.H {
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
			legend(p.lens),
		),
		h.Aside(h.Class("dx-side"), list),
	)
}

// census counts the open prognoses by urgency, across every lens.
func (p *DiagnosePage) census(gs []prognosis.Prognosis) h.H {
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

func (p *DiagnosePage) lensBar() h.H {
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

func legend(l string) h.H {
	for _, x := range diagLenses {
		if x.key == l {
			return h.P(h.Class("hint dx-legend"),
				h.Str("Area is lines of code. Colour: "+x.help+". "),
				levelMark(3), h.Str(" fix first "), levelMark(2), h.Str(" fix soon "), levelMark(1), h.Str(" when convenient. Hover a marker for the short version; click it for the full one. Click any tile to see what calls it."))
		}
	}
	return nil
}

func levelMark(l prognosis.Level) h.H {
	return h.Span(h.Class(fmt.Sprintf("lvl lvl-%d", l)), h.Aria("hidden", "true"))
}

func (p *DiagnosePage) panel(a *live.Analysis, gs []prognosis.Prognosis, g *prognosis.Prognosis, run *fix.Run) h.H {
	var related []h.H
	for _, r := range g.Related {
		target := h.Span(h.Class("dx-ref-name"), h.Str(r.Name))
		for _, o := range gs {
			if o.Target == r.ID && o.Key != g.Key {
				target = h.A(h.Class("dx-ref-name"), h.Href(prognosisHref(o.Key)), levelMark(o.Level), h.Str(" "+r.Name))
				break
			}
		}
		related = append(related, h.Li(h.Class("dx-ref"), h.Data("ref", r.ID), target, h.Span(h.Class("dx-ref-why"), h.Str(r.Why))))
	}
	where := g.Name
	if g.File != "" {
		where = fmt.Sprintf("%s · %s:%d", g.Name, g.File, g.Line)
	}
	var steps []h.H
	for _, s := range g.Do {
		steps = append(steps, h.Li(h.Str(s)))
	}
	var facts []h.H
	for _, f := range g.Facts {
		limit := h.Td(h.Class("num"))
		if f.Limit > 0 {
			limit = h.Td(h.Class("num"), h.Str(f.Limit))
		}
		cls := "num"
		if f.Limit > 0 && f.Value > f.Limit {
			cls = "num over"
		}
		facts = append(facts, h.Tr(h.Td(h.Str(f.Label)), h.Td(h.Class(cls), h.Str(f.Value)), limit))
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
			via.When(len(related) > 0, func() h.H {
				return group([]h.H{h.H3(h.Str("Related")), h.Ul(append([]h.H{h.Class("dx-refs")}, related...)...)})
			}),
		),
		h.Article(h.Class("dx-panel"),
			h.Div(h.Class("panel-head"),
				h.A(h.Class("dx-back"), h.Href(lensHref(string(g.Lens))), h.Str("← All places")),
				h.Span(h.Class("dx-level"), levelMark(g.Level), h.Str(" "+levelWords[g.Level])),
			),
			h.H2(h.Class("dx-title"), h.Str(g.Title)),
			h.P(h.Class("loc"), h.Str(where)),
			h.P(h.Class("dx-summary"), h.Str(g.Summary)),
			h.H3(h.Str("Why it matters")),
			h.P(h.Str(g.Why)),
			h.H3(h.Str("What to do")),
			h.Ol(append([]h.H{h.Class("dx-steps")}, steps...)...),
			h.H3(h.Str("How to check")),
			h.P(h.Str(g.Check)),
			h.Details(h.Class("dx-fold"), h.Summary(h.Str("The numbers")),
				h.Table(h.Class("dx-facts"), h.Thead(h.Tr(h.Th(h.Str("")), h.Th(h.Str("now")), h.Th(h.Str("limit")))), h.Tbody(facts...))),
			src,
		),
		p.treat(a, g, run),
	)
}

func refIDs(rs []prognosis.Ref) []string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
	}
	return ids
}

// treat is the "let Claude try" block: a button, then the run's progress,
// then the result.
func (p *DiagnosePage) treat(a *live.Analysis, g *prognosis.Prognosis, run *fix.Run) h.H {
	if run == nil {
		return h.Section(h.Class("dx-treat"),
			h.H3(h.Str("Let Claude try")),
			h.P(h.Class("hint"), h.Str("Claude Code works on a throwaway copy of the last commit and may run any command there. Your files are not changed; you see what it did and decide.")),
			h.Button(h.Class("btn"), on.Click(on.Bind(p.Fix, g.Key)), h.Str("Try a fix")),
		)
	}
	s := run.Snapshot()
	live := s.State == fix.Preparing || s.State == fix.Working || s.State == fix.Checking
	note := ""
	if n := len(s.Events); n > 0 && live {
		note = s.Events[n-1].Text
	}
	head := h.Div(h.Class("dx-run-head"),
		h.H3(h.Str(runTitle(s))),
		h.Span(h.Class("hint"), h.Str(runMeta(s))),
		via.When(s.State == fix.Preparing || s.State == fix.Working, func() h.H {
			return h.Button(h.Class("btn"), on.Click(on.Bind(p.Stop, g.Key)), h.Str("Stop"))
		}),
		via.When(!live, func() h.H {
			return h.Button(h.Class("btn"), on.Click(on.Bind(p.Discard, g.Key)), h.Str("Discard"))
		}),
		via.When(note != "", func() h.H { return h.Span(h.Class("hint dx-run-note"), h.Str(note)) }),
	)
	body := []h.H{h.Class("dx-treat"), head,
		feed(map[expr.Expr]any{p.Replay.Ref(): replayOf(a, s)}),
		h.Div(h.Class("rp"), h.DataIgnoreMorph(), h.TabIndex(0), h.Aria("label", "Replay of what Claude did. Left and right arrows step through it."),
			h.DataEffect(expr.Rawf("codemesh.replay(el, %s)", p.Replay.Ref()))),
	}
	if s.Err != nil {
		body = append(body, h.P(h.Class("banner err"), h.Str("It did not finish: "+s.Err.Error()+". Nothing in your files changed. Discard and try again.")))
	}
	if s.After != nil && s.Before != nil {
		body = append(body, p.result(g, s))
	}
	return h.Section(body...)
}

func runTitle(s fix.Snapshot) string {
	switch s.State {
	case fix.Preparing:
		return "Preparing a throwaway copy…"
	case fix.Working:
		return "Claude is working…"
	case fix.Checking:
		return "Checking the result…"
	case fix.Stopped:
		return "Stopped"
	case fix.Failed:
		return "Did not finish"
	}
	return "Claude's attempt"
}

func runMeta(s fix.Snapshot) string {
	meta := s.Took.Round(time.Second).String()
	if s.Cost > 0 {
		meta += fmt.Sprintf(" · $%.2f", s.Cost)
	}
	return meta
}

// result shows the attempt as the map before and after, what the change
// fixed and broke, and its code in impact order.
func (p *DiagnosePage) result(g *prognosis.Prognosis, s fix.Snapshot) h.H {
	before, after := keys(s.Before.Prognoses), keys(s.After.Prognoses)
	var fixed, added []prognosis.Prognosis
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
	verdict := okLine("This problem is gone.")
	if after[g.Key] {
		verdict = h.P(h.Class("dx-verdict warn"), h.Str("▲ The problem is still there. The changes may still help; read them before deciding."))
	}
	if !s.After.CheckOK && s.Before.CheckOK {
		verdict = h.P(h.Class("dx-verdict bad"), h.Str("▲ "+s.Check.Name+" passed before and fails now. Do not keep this as it is."))
	}
	units := slices.Clone(s.Review.Units)
	slices.SortStableFunc(units, func(a, b *review.Unit) int { return cmp.Compare(b.Risk, a.Risk) })
	changed := make([]string, 0, len(units))
	var cards []h.H
	for _, u := range units {
		changed = append(changed, u.ID)
		cards = append(cards, changeCard(u))
	}
	a := &live.Analysis{At: time.Now(), Snap: s.Before.Snap}
	b := &live.Analysis{At: time.Now(), Snap: s.After.Snap}
	return h.Div(h.Class("dx-result"),
		verdict,
		h.Div(h.Class("dx-scores"),
			score(s.Check.Name+" passes", s.Before.CheckOK, s.After.CheckOK),
			count("Places needing attention", len(s.Before.Prognoses), len(s.After.Prognoses)),
			count("Smells", len(s.Before.Findings), len(s.After.Findings)),
		),
		feed(map[expr.Expr]any{
			p.Before.Ref(): diagOf(a, s.Before.Prognoses, string(g.Lens)).around(g.Target, nil),
			p.After.Ref():  diagOf(b, s.After.Prognoses, string(g.Lens)).around(g.Target, changed),
		}),
		h.Div(h.Class("dx-compare"),
			h.Figure(h.Figcaption(h.Str("Before")), h.Div(h.Class("dx-map dx-cmp atlas atlas-map"), h.DataIgnoreMorph(),
				h.DataEffect(expr.Rawf("codemesh.diag(el, %s, {mode: 'compare'})", p.Before.Ref())))),
			h.Figure(h.Figcaption(h.Str("After · changed code outlined")), h.Div(h.Class("dx-map dx-cmp atlas atlas-map"), h.DataIgnoreMorph(),
				h.DataEffect(expr.Rawf("codemesh.diag(el, %s, {mode: 'compare'})", p.After.Ref())))),
		),
		delta2("Fixed", "gone", fixed),
		delta2("New", "new", added),
		via.When(s.Summary != "", func() h.H {
			return group([]h.H{h.H3(h.Str("Claude's summary")), h.P(h.Class("dx-claude"), h.Str(s.Summary))})
		}),
		via.When(!s.After.CheckOK, func() h.H {
			return h.Details(h.Class("dx-fold"), h.Summary(h.Str(s.Check.Name+" output")), h.Pre(h.Class("code dx-out"), h.Str(s.After.Output)))
		}),
		via.When(len(cards) > 0, func() h.H {
			return h.Details(h.Class("dx-fold"), h.Summary(h.Str(fmt.Sprintf("The code changes · %s, most impact first", plural(len(cards), "declaration")))), group(cards))
		}),
	)
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

func delta2(title, cls string, gs []prognosis.Prognosis) h.H {
	if len(gs) == 0 {
		return nil
	}
	var rows []h.H
	for _, g := range gs {
		rows = append(rows, h.Li(h.Class("finding "+cls), levelMark(g.Level), h.Span(h.Class("rule"), h.Str(g.Title)), h.Span(h.Class("detail"), h.Str(g.Name))))
	}
	return group([]h.H{h.H3(h.Str(fmt.Sprintf("%s · %d", title, len(gs)))), h.Ul(append([]h.H{h.Class("findings")}, rows...)...)})
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

// diag is the island's input: the atlas layout, a heat per tile for every
// lens, the marks, and the call edges the island walks to ring a selection.
type diag struct {
	At    int64            `json:"at"`
	W     float64          `json:"w"`
	H     float64          `json:"h"`
	Lens  string           `json:"lens"`
	Tiles []atlasTile      `json:"tiles"`
	Heats map[string][]int `json:"heats"`
	Marks []mark           `json:"marks"`
	Calls [][2]int         `json:"calls"` // [caller tile, callee tile], production code only
	// A view centred on one place: the tile to frame, and the tiles to
	// outline, which are its related places or the code a change touched.
	Focus string   `json:"focus,omitempty"`
	Ring  []string `json:"ring,omitempty"`
}

func (d diag) around(focus string, ring []string) diag {
	d.Focus, d.Ring = focus, ring
	return d
}

type mark struct {
	Tile    int    `json:"t"`
	Level   int    `json:"l"`
	Lens    string `json:"lens"`
	Key     string `json:"key"`
	Title   string `json:"title"`
	Summary string `json:"sum"`
	Name    string `json:"name"`
}

func diagOf(a *live.Analysis, gs []prognosis.Prognosis, lens string) diag {
	s := a.Snap
	busy := prognosis.BusyChurn(s)
	instab := map[string]float64{}
	for _, r := range depRows(s) {
		instab[r.pkg.Path] = r.unstable
	}
	heats := map[string][]int{}
	for _, l := range diagLenses {
		heats[l.key] = nil
	}
	tile := map[string]int{}
	at := atlasOf(a, mapW, mapH, func(i int, t *atlasTile, pkg *code.Package, f *code.File, d *code.Decl) {
		var hv [4]int
		switch {
		case d != nil:
			t.ID = d.ID
			hv = declHeats(s, d, f, busy, instab[pkg.Path])
		case f != nil:
			t.ID = f.Path
		default:
			t.ID = pkg.Path
		}
		tile[t.ID] = i
		for j, l := range diagLenses {
			heats[l.key] = append(heats[l.key], hv[j])
		}
	})
	out := diag{At: at.At, W: at.W, H: at.H, Lens: lens, Tiles: at.Tiles, Heats: heats, Marks: []mark{}, Calls: [][2]int{}}
	for _, g := range gs {
		i, ok := tile[g.Target]
		if !ok {
			continue
		}
		out.Marks = append(out.Marks, mark{Tile: i, Level: int(g.Level), Lens: string(g.Lens), Key: g.Key, Title: g.Title, Summary: g.Summary, Name: g.Name})
	}
	for _, d := range s.Decls() {
		to, ok := tile[d.ID]
		if !ok {
			continue
		}
		for _, c := range d.Callers {
			if from, ok := tile[c]; ok && from != to {
				out.Calls = append(out.Calls, [2]int{from, to})
			}
		}
	}
	return out
}

// declHeats buckets a declaration 0 to 5 for each lens, in diagLenses order.
func declHeats(s *code.Snapshot, d *code.Decl, f *code.File, busy int, instability float64) [4]int {
	cx := 0
	if d.Kind == code.Func || d.Kind == code.Method {
		cx = band(float64(d.Complexity), 3, 6, 10, 20)
	}
	health := cx
	if cx > 0 && f.Churn >= busy {
		health = min(5, cx+1)
	}
	callers := 0
	for _, c := range d.Callers {
		if x := s.Decl(c); x != nil && !x.Test {
			callers++
		}
	}
	reach := band(float64(callers), 1, 3, 10, 30, 100)
	structure := 1 + int(math.Round(instability*4))
	tests := 0
	if cx > 0 && prognosis.TestCallers(s, d) == 0 {
		tests = cx
	}
	if d.Params > smell.ManyParamsLimit {
		tests = max(tests, 3)
	}
	return [4]int{health, reach, structure, tests}
}

// band is the count of thresholds v reaches, so 0 below the first.
func band(v float64, at ...float64) int {
	n := 0
	for _, t := range at {
		if v >= t {
			n++
		}
	}
	return min(n, 5)
}
