package graphdiff_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/graphdiff"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/stretchr/testify/assert"
)

type link struct {
	from, to string
	kind     code.LinkKind
}

type spec struct {
	pkgs  map[string][]string // package path → imports
	decls []code.Decl         // ID, Package and the compared fields
	links []link
}

// snap builds a snapshot with one file per package.
func snap(s spec) *code.Snapshot {
	b := code.NewBuilder(code.Julia, "m", "/m")
	files := map[string]*code.File{}
	for path, imports := range s.pkgs {
		p := b.Package(code.Package{Path: path, Imports: imports})
		files[path] = b.File(p, code.File{Path: path + ".jl"})
	}
	byID := map[string]*code.Decl{}
	for _, d := range s.decls {
		byID[d.ID] = b.Decl(files[d.Package], d)
	}
	for _, l := range s.links {
		b.Link(byID[l.from], byID[l.to], l.kind)
	}
	return b.Finish()
}

func fn(id, pkg, name string) code.Decl {
	return code.Decl{ID: id, Package: pkg, Name: name, Kind: code.Func, Signature: "()", Shape: "s-" + name, Complexity: 1, Lines: 5}
}

func ids(ds []*code.Decl) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func TestDiff_reportsAddedRemovedAndChangedDecls(t *testing.T) {
	t.Parallel()
	changed := fn("m.changed", "m", "changed")
	sig, cx, lines, shape := fn("m.sig", "m", "sig"), fn("m.cx", "m", "cx"), fn("m.lines", "m", "lines"), fn("m.shape", "m", "shape")
	base := snap(spec{
		pkgs:  map[string][]string{"m": nil},
		decls: []code.Decl{fn("m.same", "m", "same"), fn("m.gone", "m", "gone"), changed, sig, cx, lines, shape},
	})
	sig.Signature = "(x)"
	cx.Complexity = 4
	lines.Lines = 9
	shape.Shape = "other"
	head := snap(spec{
		pkgs:  map[string][]string{"m": nil},
		decls: []code.Decl{fn("m.same", "m", "same"), fn("m.new", "m", "new"), changed, sig, cx, lines, shape},
	})

	d := graphdiff.Between(base, head, nil, nil)

	assert.Equal(t, []string{"m.new"}, ids(d.Added))
	assert.Equal(t, []string{"m.gone"}, ids(d.Removed))
	assert.Equal(t, []graphdiff.DeclChange{
		{ID: "m.cx", Complexity: true},
		{ID: "m.lines", Lines: true},
		{ID: "m.shape", Shape: true},
		{ID: "m.sig", Sig: true},
	}, d.Changed, "unchanged decls are left out; sorted by ID")
	assert.Empty(t, d.Moved)
}

func TestDiff_reportsEdgesAddedAndRemoved(t *testing.T) {
	t.Parallel()
	decls := []code.Decl{fn("m.a", "m", "a"), fn("m.b", "m", "b"), fn("m.c", "m", "c")}
	base := snap(spec{
		pkgs:  map[string][]string{"m": nil},
		decls: decls,
		links: []link{{"m.a", "m.b", code.Caller}, {"m.a", "m.c", code.Caller}, {"m.b", "m.c", code.User}},
	})
	head := snap(spec{
		pkgs:  map[string][]string{"m": nil},
		decls: decls,
		links: []link{{"m.a", "m.b", code.Caller}, {"m.c", "m.b", code.Caller}, {"m.b", "m.c", code.Caller}},
	})

	d := graphdiff.Between(base, head, nil, nil)

	assert.Equal(t, []graphdiff.Edge{
		{From: "m.b", To: "m.c", Kind: code.Caller},
		{From: "m.c", To: "m.b", Kind: code.Caller},
	}, d.EdgesAdded, "a user edge becoming a caller edge is a new caller edge")
	assert.Equal(t, []graphdiff.Edge{
		{From: "m.a", To: "m.c", Kind: code.Caller},
		{From: "m.b", To: "m.c", Kind: code.User},
	}, d.EdgesRemoved)
}

func TestDiff_reportsAMoveOnceAndKeepsItsEdgesStable(t *testing.T) {
	t.Parallel()
	moved := fn("m.a.helper", "m.a", "helper")
	other := fn("m.a.other", "m.a", "other")
	user := fn("m.b.use", "m.b", "use")
	base := snap(spec{
		pkgs:  map[string][]string{"m.a": nil, "m.b": nil, "m.c": nil},
		decls: []code.Decl{moved, other, user},
		links: []link{{"m.b.use", "m.a.helper", code.Caller}},
	})
	movedTo := fn("m.c.helper", "m.c", "helper")
	movedTo.Lines = 7
	head := snap(spec{
		pkgs:  map[string][]string{"m.a": nil, "m.b": nil, "m.c": nil},
		decls: []code.Decl{movedTo, other, user},
		links: []link{{"m.b.use", "m.c.helper", code.Caller}},
	})

	d := graphdiff.Between(base, head, nil, nil)

	assert.Equal(t, []graphdiff.Move{{From: "m.a.helper", To: "m.c.helper"}}, d.Moved)
	assert.Empty(t, d.Added)
	assert.Empty(t, d.Removed)
	assert.Empty(t, d.EdgesAdded, "the caller still calls the same decl")
	assert.Empty(t, d.EdgesRemoved)
	assert.Equal(t, []graphdiff.DeclChange{{ID: "m.c.helper", Lines: true}}, d.Changed, "a move is compared under its head ID")
}

func TestDiff_doesNotPairAmbiguousReshapedOrUnshapedMoves(t *testing.T) {
	t.Parallel()
	noShape := func(id, pkg string) code.Decl {
		d := fn(id, pkg, "bare")
		d.Shape = ""
		return d
	}
	base := snap(spec{
		pkgs:  map[string][]string{"m.a": nil, "m.b": nil, "m.c": nil},
		decls: []code.Decl{fn("m.a.dup", "m.a", "dup"), fn("m.b.dup", "m.b", "dup"), fn("m.a.edit", "m.a", "edit"), noShape("m.a.bare", "m.a")},
	})
	edited := fn("m.c.edit", "m.c", "edit")
	edited.Shape = "different"
	head := snap(spec{
		pkgs:  map[string][]string{"m.a": nil, "m.b": nil, "m.c": nil},
		decls: []code.Decl{fn("m.c.dup", "m.c", "dup"), edited, noShape("m.c.bare", "m.c")},
	})

	d := graphdiff.Between(base, head, nil, nil)

	assert.Empty(t, d.Moved)
	assert.Equal(t, []string{"m.c.bare", "m.c.dup", "m.c.edit"}, ids(d.Added))
	assert.Equal(t, []string{"m.a.bare", "m.a.dup", "m.a.edit", "m.b.dup"}, ids(d.Removed))
}

func TestDiff_reportsImportsAddedAndRemovedPerPackage(t *testing.T) {
	t.Parallel()
	base := snap(spec{pkgs: map[string][]string{"m.a": {"m.b", "m.c"}, "m.b": nil, "m.c": nil, "m.old": {"m.b"}}})
	head := snap(spec{pkgs: map[string][]string{"m.a": {"m.b", "m.d"}, "m.b": nil, "m.c": nil, "m.d": nil, "m.new": {"m.a"}}})

	d := graphdiff.Between(base, head, nil, nil)

	assert.Equal(t, map[string][]string{"m.a": {"m.d"}, "m.new": {"m.a"}}, d.ImportsAdded)
	assert.Equal(t, map[string][]string{"m.a": {"m.c"}, "m.old": {"m.b"}}, d.ImportsRemoved)
}

func TestDiff_takesIntroducedAndFixedFromSmellDelta(t *testing.T) {
	t.Parallel()
	kept := smell.Finding{Rule: smell.LongFunc, Decl: "m.a", Subject: "a"}
	fixed := smell.Finding{Rule: smell.DeepNesting, Decl: "m.b", Subject: "b"}
	introduced := smell.Finding{Rule: smell.ManyParams, Decl: "m.c", Subject: "c"}
	s := snap(spec{pkgs: map[string][]string{"m": nil}})

	d := graphdiff.Between(s, s, []smell.Finding{kept, fixed}, []smell.Finding{kept, introduced})

	assert.Equal(t, []smell.Finding{introduced}, d.Introduced)
	assert.Equal(t, []smell.Finding{fixed}, d.Fixed)
}
