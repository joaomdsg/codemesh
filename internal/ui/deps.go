package ui

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// DepsPage is a dependency structure matrix: row imports column, cells count
// references, rows ordered from consumers down to foundations.
type DepsPage struct{ shell }

func (p *DepsPage) OnInit(ctx *via.Ctx) error {
	p.start(ctx)
	return nil
}

func (p *DepsPage) PageMeta() via.Meta { return via.Meta{Title: "Dependencies · codemesh"} }

func (p *DepsPage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabDeps, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	return p.frame(tabDeps, a, matrix(a))
}

type depRow struct {
	pkg      *code.Package
	layer    int
	ca, ce   int
	unstable float64
}

func matrix(a *live.Analysis) h.H {
	rows := depRows(a.Snap)
	idx := map[string]int{}
	for i, r := range rows {
		idx[r.pkg.Path] = i
	}
	refs := packageRefs(a.Snap)
	bad := map[[2]string]smell.Finding{}
	for _, f := range a.Findings {
		if f.Rule == smell.UnstableDep {
			bad[[2]string{f.Package, f.Target}] = f
		}
	}
	head := []h.H{h.Th(h.Class("dsm-name"), h.Str("package")), h.Th(h.Title("instability Ce/(Ca+Ce)"), h.Str("I")),
		h.Th(h.Title("packages importing it"), h.Str("in")), h.Th(h.Title("packages it imports"), h.Str("out"))}
	for i := range rows {
		head = append(head, h.Th(h.Class("dsm-col"), h.Str(i+1)))
	}
	var body []h.H
	for i, r := range rows {
		cells := []h.H{
			h.Th(h.Class("dsm-name"), h.A(h.Href("/?in="+urlEscape(r.pkg.Path)), h.Span(h.Class("dsm-no"), h.Str(i+1)), h.Str(r.pkg.Rel))),
			h.Td(h.Class("num"), h.Str(fmt.Sprintf("%.2f", r.unstable))),
			h.Td(h.Class("num"), h.Str(r.ca)),
			h.Td(h.Class("num"), h.Str(r.ce)),
		}
		for j, c := range rows {
			if i == j {
				cells = append(cells, h.Td(h.Class("dsm-self")))
				continue
			}
			if !slices.Contains(r.pkg.Imports, c.pkg.Path) {
				cells = append(cells, h.Td())
				continue
			}
			n := refs[[2]string{r.pkg.Path, c.pkg.Path}]
			cls, tip := "dsm-dep", fmt.Sprintf("%s → %s: %s", r.pkg.Rel, c.pkg.Rel, plural(n, "reference"))
			if f, ok := bad[[2]string{r.pkg.Path, c.pkg.Path}]; ok {
				cls, tip = "dsm-dep dsm-bad", tip+". "+f.Detail
			}
			cells = append(cells, h.Td(h.Class(cls), h.Title(tip), h.Str(n)))
		}
		body = append(body, h.Tr(cells...))
	}
	unstable := 0
	for range bad {
		unstable++
	}
	summary := "✓ Every package depends only on packages at least as stable as itself."
	if unstable > 0 {
		summary = fmt.Sprintf("%s break the Stable Dependencies Principle: a package imports one that changes more easily than it does.", plural(unstable, "import"))
	}
	return h.Div(h.Class("deps"),
		h.H2(h.Str("Dependencies")),
		h.P(h.Class("hint"), h.Str("Row imports column. A cell counts references. Rows run from consumers down to foundations, so every mark sits right of the diagonal. I is instability: 0 is depended on and stable, 1 depends on others and is free to change.")),
		h.P(h.Class(map[bool]string{true: "warn", false: "ok"}[unstable > 0]), h.Str(summary)),
		h.Div(h.Class("dsm-wrap"),
			h.Table(h.Class("dsm"), h.Thead(h.Tr(head...)), h.Tbody(body...)),
		),
	)
}

// depRows orders packages by layer, highest first: a package's layer is one
// more than the deepest layer it imports.
func depRows(s *code.Snapshot) []depRow {
	layer := map[string]int{}
	var depth func(p string, seen map[string]bool) int
	depth = func(path string, seen map[string]bool) int {
		if l, ok := layer[path]; ok {
			return l
		}
		pkg := s.Package(path)
		if pkg == nil || seen[path] {
			return 0
		}
		seen[path] = true
		l := 0
		for _, imp := range pkg.Imports {
			l = max(l, depth(imp, seen)+1)
		}
		layer[path] = l
		return l
	}
	var rows []depRow
	for _, p := range s.Packages {
		ca, ce := len(s.ImportedBy(p.Path)), len(p.Imports)
		i := 0.0
		if ca+ce > 0 {
			i = float64(ce) / float64(ca+ce)
		}
		rows = append(rows, depRow{pkg: p, layer: depth(p.Path, map[string]bool{}), ca: ca, ce: ce, unstable: i})
	}
	slices.SortStableFunc(rows, func(a, b depRow) int {
		return cmp.Or(cmp.Compare(b.layer, a.layer), cmp.Compare(a.pkg.Path, b.pkg.Path))
	})
	return rows
}

// packageRefs sums decl references between packages, test code excluded.
func packageRefs(s *code.Snapshot) map[[2]string]int {
	out := map[[2]string]int{}
	for _, d := range s.Decls() {
		if d.Test {
			continue
		}
		for to, n := range d.Refs {
			out[[2]string{d.Package, to}] += n
		}
	}
	return out
}
