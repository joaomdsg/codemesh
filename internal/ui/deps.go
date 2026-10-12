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

// depsPage is a dependency structure matrix: row imports column, cells count
// references, rows ordered from consumers down to foundations.
type depsPage struct{ shell }

func (p *depsPage) OnInit(ctx *via.Ctx) error {
	p.start(ctx)
	return nil
}

func (p *depsPage) PageMeta() via.Meta { return via.Meta{Title: "Dependencies · codemesh"} }

func (p *depsPage) View() h.H {
	a := p.src.Current()
	if a == nil || a.Snap == nil {
		return p.frame(tabDeps, a, h.P(h.Class("empty"), h.Str("Analysing the module…")))
	}
	return p.frame(tabDeps, a, h.Div(h.Class("map-layout"), matrix(a), h.Aside(h.Class("side"), structureFindings(a))))
}

// structureFindings lists the smells about how packages relate, the ones a
// matrix makes you look for.
func structureFindings(a *live.Analysis) h.H {
	var rows []h.H
	for _, f := range a.Findings {
		switch f.Rule {
		case smell.UnstableDep, smell.EnviousFunc, smell.UntestedPackage:
			rows = append(rows, findingRow(f, "smells"))
		}
	}
	if len(rows) == 0 {
		return group([]h.H{h.H2(h.Str("Structure")), okLine("No structural smells.")})
	}
	return group([]h.H{
		h.H2(h.Str(fmt.Sprintf("Structure · %d", len(rows)))),
		h.Ul(append([]h.H{h.Class("findings")}, rows...)...),
	})
}

type depRow struct {
	pkg      *code.Package
	layer    int
	ca, ce   int
	unstable float64
}

// dsm is what the matrix draws from: the rows in order, the references
// between packages, and the unstable imports to mark.
type dsm struct {
	a    *live.Analysis
	rows []depRow
	refs map[[2]string]int
	bad  map[[2]string]smell.Finding
}

func matrix(a *live.Analysis) h.H {
	m := dsm{a: a, rows: depRows(a.Snap), refs: packageRefs(a.Snap), bad: map[[2]string]smell.Finding{}}
	for _, f := range a.Findings {
		if f.Rule == smell.UnstableDep {
			m.bad[[2]string{f.Package, f.Target}] = f
		}
	}
	var body []h.H
	for i := range m.rows {
		body = append(body, m.row(i))
	}
	summary := okLine("Every package imports only packages at least as stable as itself.")
	if len(m.bad) > 0 {
		summary = h.P(h.Class("warn"), h.Str(fmt.Sprintf("▲ %s of a less stable package, whose changes ripple back.", plural(len(m.bad), "import"))))
	}
	return h.Div(h.Class("deps"),
		h.H2(h.Str("Dependencies")),
		summary,
		h.Div(h.Class("dsm-wrap"),
			h.Table(h.Class("dsm"), h.Thead(h.Tr(m.head()...)), h.Tbody(body...)),
		),
		h.P(h.Class("hint"), h.Str("Row imports column; a cell counts references. I runs from 0, relied on by others, to 1, free to change.")),
	)
}

func (m dsm) head() []h.H {
	head := []h.H{h.Th(h.Class("dsm-name"), h.Str("package")), h.Th(h.Title("instability: the share of its links that are its own imports"), h.Str("I")),
		h.Th(h.Title("packages importing it"), h.Str("in")), h.Th(h.Title("packages it imports"), h.Str("out"))}
	for i := range m.rows {
		head = append(head, h.Th(h.Class("dsm-col"), h.Str(i+1)))
	}
	return head
}

func (m dsm) row(i int) h.H {
	r := m.rows[i]
	cells := []h.H{
		h.Th(h.Class("dsm-name"), h.A(h.Href(mapURL{in: r.pkg.Path}.String()), h.Span(h.Class("dsm-no"), h.Str(i+1)), h.Str(pkgName(m.a, r.pkg)))),
		h.Td(h.Class("num"), h.Str(fmt.Sprintf("%.2f", r.unstable))),
		h.Td(h.Class("num"), h.Str(r.ca)),
		h.Td(h.Class("num"), h.Str(r.ce)),
	}
	for j, c := range m.rows {
		cells = append(cells, m.cell(r, c, i == j))
	}
	return h.Tr(cells...)
}

func (m dsm) cell(r, c depRow, self bool) h.H {
	switch {
	case self:
		return h.Td(h.Class("dsm-self"))
	case !slices.Contains(r.pkg.Imports, c.pkg.Path):
		return h.Td()
	}
	n := m.refs[[2]string{r.pkg.Path, c.pkg.Path}]
	cls, tip := "dsm-dep", fmt.Sprintf("%s → %s: %s", pkgName(m.a, r.pkg), pkgName(m.a, c.pkg), plural(n, "reference"))
	if f, ok := m.bad[[2]string{r.pkg.Path, c.pkg.Path}]; ok {
		cls, tip = "dsm-dep dsm-bad", tip+". "+f.Detail
	}
	return h.Td(h.Class(cls), h.Title(tip), h.Str(n))
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
