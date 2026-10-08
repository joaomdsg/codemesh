package ui

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// MapPage is the atlas of the module, coloured by a lens, beside the smells
// of the scope in view.
type MapPage struct {
	shell
	in   string // scope: "" for the module, a package path or a file path
	lens string
	decl string // selected decl ID
	rows int    // smells listed; grows a page at a time

	// The atlas island's inputs, client-only and rendered by the server: the
	// layout with a heat per declaration, and the tile to fly to.
	Atlas    via.SignalCS[atlas]
	Selected via.SignalCS[string] `via:"init=\"\""`
}

type lens struct {
	key, label, help string
}

var lenses = []lens{
	{"smells", "Smells", "smells per 100 lines, weighted by severity"},
	{"complexity", "Complexity", "each declaration's cyclomatic complexity"},
	{"churn", "Churn", "commits to the declaration's file in the last 90 days"},
	{"hotspot", "Hotspot", "churn × complexity: hard code that keeps changing"},
}

func (p *MapPage) OnInit(ctx *via.Ctx) error {
	q := ctx.Request().URL.Query()
	p.in, p.lens, p.decl = q.Get("in"), q.Get("lens"), q.Get("d")
	p.rows = smellsPage
	if !slices.ContainsFunc(lenses, func(l lens) bool { return l.key == p.lens }) {
		p.lens = "smells"
	}
	p.start(ctx)
	return nil
}

// smellsPage is how many smells the side panel lists at a time, in severity
// order; a big package has hundreds.
const smellsPage = 60

// More lists the next page of smells.
func (p *MapPage) More(_ *via.Ctx) { p.rows += smellsPage }

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
				feed(map[expr.Expr]any{p.Atlas.Ref(): p.atlas(a), p.Selected.Ref(): cmp.Or(p.decl, p.in)}),
				h.Div(h.Class("atlas atlas-map"), h.DataIgnoreMorph(),
					h.DataEffect(expr.Rawf("codemesh.atlas(el, %s, {focus: %s})", p.Atlas.Ref(), p.Selected.Ref()))),
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
		if pkg.Rel == p.in {
			return scope{pkg: pkg}
		}
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

// atlas lays the module out with each declaration's lens value bucketed into
// a heat from 0 to 5, and every tile named by its path or decl ID so a click
// can open it.
func (p *MapPage) atlas(a *live.Analysis) atlas {
	fi := indexFindings(a.Findings)
	values := map[int]float64{}
	at := atlasOf(a, mapW, mapH, func(i int, t *atlasTile, pkg *code.Package, f *code.File, d *code.Decl) {
		switch {
		case d != nil:
			t.ID = d.ID
			values[i] = p.value(fi.decl[d.ID], d.Lines, d.Complexity, f.Churn)
		case f != nil:
			t.ID = f.Path
		default:
			t.ID = pkg.Path
		}
	})
	top := 0.0
	for _, v := range values {
		top = math.Max(top, v)
	}
	for i, v := range values {
		at.Tiles[i].Heat = p.heat(v, top)
	}
	at.Lens = p.lens
	return at
}

// The map's layout space, close to the pane's shape on a desktop.
const mapW, mapH = 1000.0, 620.0

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
// bands, so the same code reads the same in any codebase; churn and hotspot
// have no natural scale and are relative to the hottest declaration.
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
	if len(fs) == 0 {
		return group([]h.H{h.H2(h.Str("Smells")), okLine("No smells here.")})
	}
	title := fmt.Sprintf("Smells · %d", len(fs))
	var rows []h.H
	for _, f := range fs[:min(len(fs), p.rows)] {
		rows = append(rows, findingRow(f, p.lens))
	}
	var more h.H
	if rest := len(fs) - p.rows; rest > 0 {
		more = h.Button(h.Class("btn more"), on.Click(p.More), h.Str(fmt.Sprintf("Show %d more smells", min(rest, smellsPage))))
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
		callers = append(callers, h.A(h.Href(p.href(c.File, c.ID)), h.Str(shortPkg(a, c.Package)+c.Name)))
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
		h.P(append([]h.H{h.Class("metrics")}, metrics...)...),
		via.When(len(fs) > 0, func() h.H { return h.Ul(append([]h.H{h.Class("findings")}, fs...)...) }),
		source(a.Snap, d),
		via.When(len(callers) > 0, func() h.H {
			return h.P(h.Class("callers-line"), h.Span(h.Class("hint"), h.Str("Called by")), group(callers))
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
	return h.Span(h.Class("badge"), h.Str(fmt.Sprintf("%s %d", label, v)))
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

// shortPkg prefixes a name with its package's last element when it is not
// the module root.
func shortPkg(a *live.Analysis, pkg string) string {
	if pkg == a.Snap.Module {
		return ""
	}
	return path.Base(pkg) + "."
}
