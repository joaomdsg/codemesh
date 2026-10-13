// Package graphdiff compares two Snapshots of one module: which declarations
// appeared, vanished, moved or changed, and which references and imports
// came and went between them.
package graphdiff

import (
	"cmp"
	"slices"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// Diff is the difference from base to head.
type Diff struct {
	// Added are head decls with no base counterpart; Removed are base decls
	// with no head counterpart. Both are sorted by ID.
	Added, Removed []*code.Decl
	// Moved pairs a removed and an added decl of the same name, kind and
	// shape. Neither is in Added or Removed.
	Moved []Move
	// Changed lists decls present on both sides, moved ones included, whose
	// metrics differ. ID is the head ID.
	Changed []DeclChange
	// EdgesAdded and EdgesRemoved are reference edges. Base IDs of moved
	// decls are read as their head IDs, so a move alone changes no edge.
	EdgesAdded, EdgesRemoved []Edge
	// ImportsAdded and ImportsRemoved are module-internal imports per
	// package path. A package absent on one side has all its imports added
	// or removed.
	ImportsAdded, ImportsRemoved map[string][]string
	// Introduced and Fixed are smell.Delta of the two finding lists.
	Introduced, Fixed []smell.Finding
}

// Move is a decl that kept its name, kind and shape under another ID.
type Move struct{ From, To string }

// DeclChange flags what differs between a decl's two versions.
type DeclChange struct {
	ID                            string
	Sig, Complexity, Lines, Shape bool
}

// Edge says From references To. Kind is code.Caller when From calls or
// names To, code.User when it reaches the type To through a member.
type Edge struct {
	From, To string
	Kind     code.LinkKind
}

// Between compares base with head. The findings are each side's smell findings.
func Between(base, head *code.Snapshot, baseF, headF []smell.Finding) *Diff {
	r := &Diff{}
	r.Moved = pairMoves(base, head)
	toHead := map[string]string{}
	moved := map[string]bool{}
	for _, m := range r.Moved {
		toHead[m.From] = m.To
		moved[m.From], moved[m.To] = true, true
	}

	for _, d := range head.Decls() {
		if base.Decl(d.ID) == nil && !moved[d.ID] {
			r.Added = append(r.Added, d)
		}
	}
	for _, d := range base.Decls() {
		if head.Decl(d.ID) == nil && !moved[d.ID] {
			r.Removed = append(r.Removed, d)
		}
	}
	byID := func(a, b *code.Decl) int { return cmp.Compare(a.ID, b.ID) }
	slices.SortFunc(r.Added, byID)
	slices.SortFunc(r.Removed, byID)

	r.Changed = changed(base, head, toHead)
	r.EdgesAdded, r.EdgesRemoved = edgeDelta(edges(base, toHead), edges(head, nil))
	r.ImportsAdded, r.ImportsRemoved = importDelta(base, head)
	r.Introduced, r.Fixed = smell.Delta(baseF, headF)
	return r
}

type nameKindShape struct {
	name  string
	kind  code.Kind
	shape string
}

func pairMoves(base, head *code.Snapshot) []Move {
	removed, added := map[nameKindShape][]*code.Decl{}, map[nameKindShape][]*code.Decl{}
	for _, d := range base.Decls() {
		if head.Decl(d.ID) == nil && d.Shape != "" {
			k := nameKindShape{d.Name, d.Kind, d.Shape}
			removed[k] = append(removed[k], d)
		}
	}
	for _, d := range head.Decls() {
		if base.Decl(d.ID) == nil && d.Shape != "" {
			k := nameKindShape{d.Name, d.Kind, d.Shape}
			added[k] = append(added[k], d)
		}
	}
	var out []Move
	for k, rs := range removed {
		if as := added[k]; len(rs) == 1 && len(as) == 1 {
			out = append(out, Move{From: rs[0].ID, To: as[0].ID})
		}
	}
	slices.SortFunc(out, func(a, b Move) int { return cmp.Compare(a.From, b.From) })
	return out
}

func changed(base, head *code.Snapshot, toHead map[string]string) []DeclChange {
	var out []DeclChange
	for _, o := range base.Decls() {
		id := cmp.Or(toHead[o.ID], o.ID)
		n := head.Decl(id)
		if n == nil {
			continue
		}
		c := DeclChange{
			ID:         id,
			Sig:        o.Signature != n.Signature,
			Complexity: o.Complexity != n.Complexity,
			Lines:      o.Lines != n.Lines,
			Shape:      o.Shape != n.Shape,
		}
		if c.Sig || c.Complexity || c.Lines || c.Shape {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b DeclChange) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// edges inverts Callers and Users into From→To edges, renaming IDs through
// rename.
func edges(s *code.Snapshot, rename map[string]string) map[Edge]bool {
	name := func(id string) string { return cmp.Or(rename[id], id) }
	out := map[Edge]bool{}
	for _, d := range s.Decls() {
		for _, c := range d.Callers {
			out[Edge{name(c), name(d.ID), code.Caller}] = true
		}
		for _, u := range d.Users {
			out[Edge{name(u), name(d.ID), code.User}] = true
		}
	}
	return out
}

func edgeDelta(base, head map[Edge]bool) (added, removed []Edge) {
	for e := range head {
		if !base[e] {
			added = append(added, e)
		}
	}
	for e := range base {
		if !head[e] {
			removed = append(removed, e)
		}
	}
	byEdge := func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To), cmp.Compare(a.Kind, b.Kind))
	}
	slices.SortFunc(added, byEdge)
	slices.SortFunc(removed, byEdge)
	return added, removed
}

func importDelta(base, head *code.Snapshot) (added, removed map[string][]string) {
	added, removed = map[string][]string{}, map[string][]string{}
	for _, p := range head.Packages {
		var baseImports []string
		if b := base.Package(p.Path); b != nil {
			baseImports = b.Imports
		}
		if d := missing(p.Imports, baseImports); len(d) > 0 {
			added[p.Path] = d
		}
	}
	for _, p := range base.Packages {
		var headImports []string
		if h := head.Package(p.Path); h != nil {
			headImports = h.Imports
		}
		if d := missing(p.Imports, headImports); len(d) > 0 {
			removed[p.Path] = d
		}
	}
	return added, removed
}

// missing returns the entries of have that are not in other.
func missing(have, other []string) []string {
	var out []string
	for _, s := range have {
		if !slices.Contains(other, s) {
			out = append(out, s)
		}
	}
	return out
}
