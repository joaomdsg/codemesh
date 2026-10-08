package ui

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/joaomdsg/codemesh/internal/treemap"
)

// MapPage is the treemap: packages, then a package's files, then a file's
// declarations, coloured by a lens, beside the findings for that scope.
type MapPage struct {
	shell
	in   string // scope: "" for the module, a package path or a file path
	lens string
	decl string // selected decl ID
}

type lens struct {
	key, label, help string
}

var lenses = []lens{
	{"smells", "Smells", "findings per 100 lines, weighted by severity"},
	{"complexity", "Complexity", "highest cyclomatic complexity inside"},
	{"churn", "Churn", "commits in the last 90 days"},
	{"hotspot", "Hotspot", "churn × complexity: hard code that keeps changing"},
}

func (p *MapPage) OnInit(ctx *via.Ctx) error {
	q := ctx.Request().URL.Query()
	p.in, p.lens, p.decl = q.Get("in"), q.Get("lens"), q.Get("d")
	if !slices.ContainsFunc(lenses, func(l lens) bool { return l.key == p.lens }) {
		p.lens = "smells"
	}
	p.start(ctx)
	return nil
}

func (p *MapPage) PageMeta() via.Meta { return via.Meta{Title: "Map · codemesh"} }

func (p *MapPage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabMap, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	sc := p.scope(a)
	return p.frame(tabMap, a,
		h.Div(h.Class("map-layout"),
			h.Section(h.Class("map-pane"),
				h.Div(h.Class("map-bar"), p.crumbs(a, sc), p.lensBar()),
				p.treemap(a, sc),
				h.P(h.Class("hint"), h.Str(p.lensHelp())),
			),
			h.Aside(h.Class("side"), p.side(a, sc)),
		),
	)
}

// scope is what the map currently shows.
type scope struct {
	pkg  *code.Package
	file *code.File
}

func (p *MapPage) scope(a *live.Analysis) scope {
	if pkg := a.Snap.Package(p.in); pkg != nil {
		return scope{pkg: pkg}
	}
	for _, pkg := range a.Snap.Packages {
		for _, f := range pkg.Files {
			if f.Path == p.in {
				return scope{pkg: pkg, file: f}
			}
		}
	}
	return scope{}
}

func (p *MapPage) href(in, decl string) string { return mapURL{p.lens, in, decl}.String() }

func (p *MapPage) crumbs(a *live.Analysis, sc scope) h.H {
	parts := []h.H{h.A(h.Href(p.href("", "")), h.Str(path.Base(a.Snap.Module)))}
	if sc.pkg != nil {
		parts = append(parts, h.Span(h.Class("sep"), h.Str("/")), h.A(h.Href(p.href(sc.pkg.Path, "")), h.Str(pkgName(a, sc.pkg))))
	}
	if sc.file != nil {
		parts = append(parts, h.Span(h.Class("sep"), h.Str("/")), h.A(h.Href(p.href(sc.file.Path, "")), h.Str(path.Base(sc.file.Path))))
	}
	return h.Nav(append([]h.H{h.Class("crumbs"), h.Aria("label", "Scope")}, parts...)...)
}

func (p *MapPage) lensBar() h.H {
	var kids []h.H
	for _, l := range lenses {
		cls := "seg"
		if l.key == p.lens {
			cls = "seg on"
		}
		kids = append(kids, h.A(h.Class(cls), h.Href(mapURL{l.key, p.in, p.decl}.String()), h.Title(l.help), h.Str(l.label)))
	}
	return h.Nav(append([]h.H{h.Class("segs"), h.Aria("label", "Colour by")}, kids...)...)
}

func (p *MapPage) lensHelp() string {
	for _, l := range lenses {
		if l.key == p.lens {
			return "Area is lines of code. Colour is " + l.help + "."
		}
	}
	return ""
}

// tile is one treemap cell before layout. A tile with kids is drawn as a
// frame holding them, one level deep, so the module view shows each
// package's files and a package view each file's declarations.
type tile struct {
	id, label, href, tip string
	lines                int
	value                float64
	selected             bool
	kids                 []tile
}

const (
	mapW, mapH = 1000.0, 620.0
	frameHead  = 18.0 // label strip of a frame
)

func (p *MapPage) treemap(a *live.Analysis, sc scope) h.H {
	tiles := p.tiles(a, sc)
	if len(tiles) == 0 {
		return h.P(h.Class("empty"), h.Str("No code here."))
	}
	top := 0.0
	for _, t := range tiles {
		top = math.Max(top, t.value)
		for _, k := range t.kids {
			top = math.Max(top, k.value)
		}
	}
	cells := p.layout(tiles, treemap.Rect{W: mapW, H: mapH}, top, true)
	return h.El("svg", append([]h.H{
		h.Class("treemap"), h.RawAttr("viewBox", fmt.Sprintf("0 0 %g %g", mapW, mapH)), h.Role("img"),
		h.Aria("label", "Treemap of the current scope"),
	}, cells...)...)
}

func (p *MapPage) layout(tiles []tile, area treemap.Rect, top float64, nest bool) []h.H {
	byID := map[string]tile{}
	var items []treemap.Item
	for _, t := range tiles {
		byID[t.id] = t
		items = append(items, treemap.Item{ID: t.id, Weight: float64(max(t.lines, 1))})
	}
	var out []h.H
	for _, lt := range treemap.Layout(items, area) {
		t := byID[lt.ID]
		r := lt.Rect.Inset(1)
		if nest && len(t.kids) > 0 && r.W > 60 && r.H > 2*frameHead {
			out = append(out, p.frameCell(t, r, top)...)
			continue
		}
		out = append(out, p.cell(t, r, top))
	}
	return out
}

// frameCell draws a container: a labelled frame, its kids inside.
func (p *MapPage) frameCell(t tile, r treemap.Rect, top float64) []h.H {
	head := h.El("a", h.Href(t.href),
		h.El("title", h.Str(t.tip)),
		h.El("rect", h.Class("frame"), num("x", r.X), num("y", r.Y), num("width", r.W), num("height", r.H), h.RawAttr("rx", "3")),
		h.El("text", h.Class("frame-label"), num("x", r.X+6), num("y", r.Y+13), h.Str(fit(t.label, r.W-12))),
	)
	inner := treemap.Rect{X: r.X + 2, Y: r.Y + frameHead, W: r.W - 4, H: r.H - frameHead - 2}
	return append([]h.H{head}, p.layout(t.kids, inner, top, false)...)
}

func (p *MapPage) cell(t tile, r treemap.Rect, top float64) h.H {
	cls := "cell heat" + fmt.Sprint(p.heat(t.value, top))
	if t.selected {
		cls += " sel"
	}
	kids := []h.H{
		h.Href(t.href),
		h.El("title", h.Str(t.tip)),
		h.El("rect", h.Class(cls), num("x", r.X), num("y", r.Y), num("width", r.W), num("height", r.H), h.RawAttr("rx", "2")),
	}
	if r.W > 40 && r.H > 17 {
		kids = append(kids, h.El("text", h.Class("cell-label"), num("x", r.X+5), num("y", r.Y+14), h.Str(fit(t.label, r.W-10))))
	}
	if r.W > 40 && r.H > 32 {
		kids = append(kids, h.El("text", h.Class("cell-sub"), num("x", r.X+5), num("y", r.Y+28), h.Str(fit(fmt.Sprintf("%d lines", t.lines), r.W-10))))
	}
	return h.El("a", kids...)
}

func (p *MapPage) tiles(a *live.Analysis, sc scope) []tile {
	fi := indexFindings(a.Findings)
	var out []tile
	switch {
	case sc.file != nil:
		for _, d := range sc.file.Decls {
			out = append(out, p.declTile(fi, sc.file, d))
		}
	case sc.pkg != nil:
		for _, f := range sc.pkg.Files {
			if !f.Test {
				out = append(out, p.fileTile(fi, f, true))
			}
		}
	default:
		for _, pkg := range a.Snap.Packages {
			out = append(out, p.pkgTile(fi, pkg, pkgName(a, pkg)))
		}
	}
	return out
}

func (p *MapPage) pkgTile(fi findingIndex, pkg *code.Package, label string) tile {
	t := tile{id: pkg.Path, label: label, href: p.href(pkg.Path, ""), lines: pkg.Lines()}
	var cx, churn int
	for _, f := range pkg.Files {
		if !f.Test {
			t.kids = append(t.kids, p.fileTile(fi, f, false))
			cx, churn = max(cx, maxComplexity(f.Decls)), max(churn, f.Churn)
		}
	}
	t.value = p.value(fi.pkg[pkg.Path], t.lines, cx, churn)
	t.tip = fmt.Sprintf("%s · %d lines · max complexity %d · %s", pkg.Path, t.lines, cx, plural(len(fi.pkg[pkg.Path]), "finding"))
	return t
}

func (p *MapPage) fileTile(fi findingIndex, f *code.File, withDecls bool) tile {
	cx := maxComplexity(f.Decls)
	t := tile{
		id: f.Path, label: path.Base(f.Path), href: p.href(f.Path, ""), lines: f.Lines,
		value: p.value(fi.file[f.Path], f.Lines, cx, f.Churn),
		tip:   fmt.Sprintf("%s · %d lines · max complexity %d · %d commits · %s", f.Path, f.Lines, cx, f.Churn, plural(len(fi.file[f.Path]), "finding")),
	}
	if withDecls {
		for _, d := range f.Decls {
			t.kids = append(t.kids, p.declTile(fi, f, d))
		}
	}
	return t
}

func (p *MapPage) declTile(fi findingIndex, f *code.File, d *code.Decl) tile {
	return tile{
		id: d.ID, label: d.Name, href: p.href(f.Path, d.ID), lines: d.Lines,
		value:    p.value(fi.decl[d.ID], d.Lines, d.Complexity, f.Churn),
		tip:      fmt.Sprintf("%s %s · %d lines · complexity %d · %s", d.Kind, d.Name, d.Lines, d.Complexity, plural(len(fi.decl[d.ID]), "finding")),
		selected: d.ID == p.decl,
	}
}

func (p *MapPage) value(fs []smell.Finding, lines, cx, churn int) float64 {
	switch p.lens {
	case "complexity":
		return float64(cx)
	case "churn":
		return float64(churn)
	case "hotspot":
		return float64(churn * cx)
	}
	w := 0
	for _, f := range fs {
		w += weight(f.Severity)
	}
	return 100 * float64(w) / float64(max(lines, 1))
}

// heat buckets a value into 0 (cold) to 5. Smells and complexity use fixed
// bands, so the same code reads the same in any scope or codebase; churn and
// hotspot have no natural scale and are relative to the hottest tile in view.
func (p *MapPage) heat(v, top float64) int {
	if v <= 0 {
		return 0
	}
	bands := map[string][]float64{
		"smells":     {1, 3, 6, 12},
		"complexity": {5, 10, 15, 20},
	}[p.lens]
	if bands == nil {
		return min(5, 1+int(4.999*v/top))
	}
	for i, limit := range bands {
		if v <= limit {
			return i + 1
		}
	}
	return 5
}

func weight(s smell.Severity) int {
	switch s {
	case smell.High:
		return 9
	case smell.Warn:
		return 3
	}
	return 1
}

func (p *MapPage) side(a *live.Analysis, sc scope) h.H {
	if d := a.Snap.Decl(p.decl); d != nil {
		return p.declPanel(a, d)
	}
	var fs []smell.Finding
	for _, f := range a.Findings {
		switch {
		case sc.file != nil && f.File != sc.file.Path:
		case sc.pkg != nil && f.Package != sc.pkg.Path:
		default:
			fs = append(fs, f)
		}
	}
	title := "Findings"
	if len(fs) > 0 {
		title = fmt.Sprintf("Findings · %d", len(fs))
	}
	if len(fs) == 0 {
		return group([]h.H{h.H2(h.Str(title)), h.P(h.Class("ok"), h.Str("✓ No findings in this scope."))})
	}
	const show = 60
	var rows []h.H
	for _, f := range fs[:min(len(fs), show)] {
		rows = append(rows, findingRow(f, p.lens))
	}
	var more h.H
	if len(fs) > show {
		more = h.P(h.Class("hint"), h.Str(fmt.Sprintf("%d more. Narrow the scope to see them.", len(fs)-show)))
	}
	return group([]h.H{h.H2(h.Str(title)), h.Ul(append([]h.H{h.Class("findings")}, rows...)...), more})
}

func (p *MapPage) declPanel(a *live.Analysis, d *code.Decl) h.H {
	var fs []h.H
	for _, f := range a.Findings {
		if f.Decl == d.ID {
			fs = append(fs, h.Li(h.Class("finding"), sevMark(f.Severity), h.Span(h.Class("rule"), h.Str(ruleLabel(f.Rule))),
				h.Span(h.Class("detail"), h.Str(f.Detail)), h.P(h.Class("why"), h.Str(f.Rule.Why()))))
		}
	}
	var callers []h.H
	for _, id := range d.Callers {
		c := a.Snap.Decl(id)
		if c == nil {
			continue
		}
		callers = append(callers, h.Li(h.A(h.Href(p.href(c.File, c.ID)), h.Str(shortPkg(a, c.Package)+c.Name))))
	}
	metrics := []h.H{metric("lines", d.Lines), metric("callers", len(d.Callers))}
	if d.Kind == code.Func || d.Kind == code.Method {
		metrics = append(metrics, metric("complexity", d.Complexity), metric("nesting", d.Nesting), metric("params", d.Params))
	}
	return group([]h.H{
		h.Div(h.Class("panel-head"),
			h.H2(h.Str(d.Name)),
			h.A(h.Class("close"), h.Href(p.href(p.in, "")), h.Aria("label", "Close"), h.Str("✕")),
		),
		h.P(h.Class("loc"), h.Str(fmt.Sprintf("%s %s:%d", d.Kind, d.File, d.Start))),
		h.Dl(append([]h.H{h.Class("metrics")}, metrics...)...),
		via.When(len(fs) > 0, func() h.H { return h.Ul(append([]h.H{h.Class("findings")}, fs...)...) }),
		source(a.Snap, d),
		via.When(len(callers) > 0, func() h.H {
			return group([]h.H{h.H3(h.Str("Called by")), h.Ul(append([]h.H{h.Class("callers")}, callers...)...)})
		}),
	})
}

func source(s *code.Snapshot, d *code.Decl) h.H {
	data, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(d.File)))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if d.End > len(lines) {
		return nil
	}
	var rows []h.H
	for i, l := range lines[d.Start-1 : d.End] {
		rows = append(rows, h.Span(h.Class("ln"), h.Span(h.Class("no"), h.Str(d.Start+i)), h.Span(h.Class("tx"), h.Str(l))))
	}
	return h.Pre(append([]h.H{h.Class("code")}, rows...)...)
}

func metric(label string, v int) h.H {
	return h.Div(h.Dt(h.Str(label)), h.Dd(h.Str(v)))
}

func sevMark(s smell.Severity) h.H {
	glyph := map[smell.Severity]string{smell.High: "▲", smell.Warn: "●", smell.Info: "○"}[s]
	return h.Span(h.Class("sev sev-"+strings.ToLower(s.String())), h.Title(s.String()), h.Str(glyph))
}

var ruleLabels = map[smell.Rule]string{
	smell.LongFunc:        "Long function",
	smell.ComplexFunc:     "Complex function",
	smell.DeepNesting:     "Deep nesting",
	smell.ManyParams:      "Many parameters",
	smell.LargeFile:       "Large file",
	smell.UnusedExport:    "Unused export",
	smell.DeadCode:        "Dead code",
	smell.EnviousFunc:     "Envious function",
	smell.UnstableDep:     "Unstable dependency",
	smell.UntestedPackage: "Untested package",
}

func ruleLabel(r smell.Rule) string {
	if l, ok := ruleLabels[r]; ok {
		return l
	}
	return string(r)
}

type findingIndex struct {
	pkg, file, decl map[string][]smell.Finding
}

func indexFindings(fs []smell.Finding) findingIndex {
	fi := findingIndex{map[string][]smell.Finding{}, map[string][]smell.Finding{}, map[string][]smell.Finding{}}
	for _, f := range fs {
		fi.pkg[f.Package] = append(fi.pkg[f.Package], f)
		if f.File != "" {
			fi.file[f.File] = append(fi.file[f.File], f)
		}
		if f.Decl != "" {
			fi.decl[f.Decl] = append(fi.decl[f.Decl], f)
		}
	}
	return fi
}

func maxComplexity(ds []*code.Decl) int {
	n := 0
	for _, d := range ds {
		n = max(n, d.Complexity)
	}
	return n
}

// shortPkg prefixes a name with its package's last element when it is not
// the module root.
func shortPkg(a *live.Analysis, pkg string) string {
	if pkg == a.Snap.Module {
		return ""
	}
	return path.Base(pkg) + "."
}

func num(name string, v float64) h.Attr { return h.RawAttr(name, fmt.Sprintf("%.1f", v)) }

// fit trims s to roughly the characters that fit in width px of 12px text.
func fit(s string, width float64) string {
	n := int(width / 7)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 2 {
		return ""
	}
	return string(r[:n-1]) + "…"
}
