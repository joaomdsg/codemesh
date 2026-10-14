package code_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilder_buildsIndexedSnapshotWithSortedEdges(t *testing.T) {
	t.Parallel()
	b := code.NewBuilder(code.Julia, "Demo", "/demo")
	p := b.Package(code.Package{Path: "Demo", Rel: ".", Name: "Demo", Imports: []string{"Demo.B", "Demo.A"}})
	f := b.File(p, code.File{Path: "src/b.jl", Lines: 9})
	f2 := b.File(p, code.File{Path: "src/a.jl", Lines: 3})
	target := b.Decl(f, code.Decl{ID: "Demo.target", Name: "target", Kind: code.Func})
	z := b.Decl(f, code.Decl{ID: "Demo.z", Name: "z", Kind: code.Func})
	a := b.Decl(f2, code.Decl{ID: "Demo.a", Name: "a", Kind: code.Func})

	b.Link(z, target, code.Caller)
	b.Link(a, target, code.Caller)
	b.Link(a, target, code.Caller)
	b.Link(z, target, code.User)
	b.Link(a, target, code.User)
	b.Link(a, target, code.User)
	s := b.Finish()

	assert.Equal(t, code.Julia, s.Lang)
	assert.Equal(t, "Demo", s.Module)
	assert.Equal(t, "/demo", s.Dir)
	assert.Same(t, target, s.Decl("Demo.target"))
	assert.Same(t, p, s.Package("Demo"))
	assert.Equal(t, []string{"Demo.a", "Demo.z"}, target.Callers, "sorted, one entry per caller")
	assert.Equal(t, []string{"Demo.a", "Demo.z"}, target.Users)
	assert.Equal(t, 2, a.Refs["Demo"], "each reference counts, as Go counts them")
	assert.Equal(t, 1, z.Refs["Demo"])
	assert.Equal(t, []string{"Demo.A", "Demo.B"}, p.Imports)
	assert.Equal(t, []string{"src/a.jl", "src/b.jl"}, []string{p.Files[0].Path, p.Files[1].Path})
	assert.Equal(t, []*code.Decl{a, target, z}, s.Decls(), "package, file, then insertion order")
	assert.Equal(t, "Demo", target.Package)
	assert.Equal(t, "src/b.jl", target.File)
	assert.Equal(t, "Demo", f.Package)
}

func TestBuilder_makesDuplicateIDsUnique(t *testing.T) {
	t.Parallel()
	b := code.NewBuilder(code.Julia, "Demo", "/demo")
	p := b.Package(code.Package{Path: "Demo"})
	f := b.File(p, code.File{Path: "src/a.jl"})
	first := b.Decl(f, code.Decl{ID: "Demo.f", Start: 1})
	second := b.Decl(f, code.Decl{ID: "Demo.f", Start: 7})
	third := b.Decl(f, code.Decl{ID: "Demo.f", Start: 12})
	s := b.Finish()

	assert.Equal(t, "Demo.f", first.ID)
	assert.Equal(t, "Demo.f#2", second.ID, "the ordinal among the duplicates, which an edit above them does not move")
	assert.Equal(t, "Demo.f#3", third.ID)
	assert.Same(t, second, s.Decl("Demo.f#2"))
}

func TestBuilder_attachKeepsTheCallersDecl(t *testing.T) {
	t.Parallel()
	b := code.NewBuilder(code.Go, "demo", "/demo")
	p := b.Package(code.Package{Path: "demo"})
	f := b.File(p, code.File{Path: "a.go"})
	first := &code.Decl{ID: "demo.f", Start: 1}
	second := &code.Decl{ID: "demo.f", Start: 7}

	assert.Same(t, first, b.Attach(f, first))
	assert.Same(t, second, b.Attach(f, second))
	s := b.Finish()

	assert.Equal(t, "demo.f#2", second.ID)
	assert.Same(t, second, s.Decl("demo.f#2"))
	assert.Equal(t, "a.go", second.File)
	assert.Equal(t, "demo", second.Package)
	assert.Equal(t, []*code.Decl{first, second}, f.Decls)
}

var _ code.Frontend = (*frontendStub)(nil)

type frontendStub struct{}

func (frontendStub) Lang() code.Lang                                      { return code.Julia }
func (frontendStub) Detect(string) bool                                   { return false }
func (frontendStub) Load(context.Context, string) (*code.Snapshot, error) { return nil, nil }

func TestSnapshot_reindexRestoresLookupsAfterAJSONRoundTrip(t *testing.T) {
	t.Parallel()
	b := code.NewBuilder(code.Go, "demo", "/demo")
	p := b.Package(code.Package{Path: "demo/a", Rel: "a", Name: "a"})
	f := b.File(p, code.File{Path: "a/a.go", Lines: 4})
	target := b.Decl(f, code.Decl{ID: "demo/a.T", Name: "T", Kind: code.Func})
	b.Link(b.Decl(f, code.Decl{ID: "demo/a.U", Name: "U", Kind: code.Func}), target, code.Caller)
	data, err := json.Marshal(b.Finish())
	require.NoError(t, err)

	var s code.Snapshot
	require.NoError(t, json.Unmarshal(data, &s))
	s.Reindex()

	require.NotNil(t, s.Decl("demo/a.T"))
	assert.Same(t, s.Packages[0].Files[0].Decls[0], s.Decl("demo/a.T"))
	assert.Same(t, s.Packages[0], s.Package("demo/a"))
	assert.Equal(t, []string{"demo/a.U"}, s.Decl("demo/a.T").Callers)
}
