package code

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// Frontend turns one language's source tree into a Snapshot.
type Frontend interface {
	Lang() Lang
	// Detect reports whether dir is the root of a project this frontend reads.
	Detect(dir string) bool
	Load(ctx context.Context, dir string) (*Snapshot, error)
}

// LinkKind is the kind of reference between two decls.
type LinkKind int

const (
	// Caller: from references to by name or call; it lands in to.Callers and
	// counts in from.Refs.
	Caller LinkKind = iota
	// User: from reaches the type to through a constant, field or method; it
	// lands in to.Users.
	User
)

// Builder assembles a Snapshot, whose indexes are unexported, for a loader
// outside this package.
type Builder struct {
	s *Snapshot
}

// NewBuilder starts a Snapshot of the module at dir, an absolute path.
func NewBuilder(lang Lang, module, dir string) *Builder {
	return &Builder{s: &Snapshot{Lang: lang, Module: module, Dir: dir, decls: map[string]*Decl{}, pkgs: map[string]*Package{}}}
}

// Package adds p and returns the stored package.
func (b *Builder) Package(p Package) *Package {
	b.s.Packages = append(b.s.Packages, &p)
	b.s.pkgs[p.Path] = &p
	return &p
}

// File adds f to p and returns the stored file.
func (b *Builder) File(p *Package, f File) *File {
	f.Package = p.Path
	p.Files = append(p.Files, &f)
	return &f
}

// Decl adds a copy of d to f and returns it. An ID already taken gets "#" and
// its ordinal among the duplicates appended, so IDs stay unique in the Snapshot
// and an edit that only moves a declaration does not rename it.
func (b *Builder) Decl(f *File, d Decl) *Decl { return b.Attach(f, &d) }

// Attach adds d itself to f, as Decl does a copy, for a loader that holds
// pointers to its decls while it builds them.
func (b *Builder) Attach(f *File, d *Decl) *Decl {
	d.Package, d.File = f.Package, f.Path
	if d.Refs == nil {
		d.Refs = map[string]int{}
	}
	if b.s.decls[d.ID] != nil {
		id := d.ID
		for n := 2; b.s.decls[d.ID] != nil; n++ {
			d.ID = fmt.Sprintf("%s#%d", id, n)
		}
	}
	b.s.decls[d.ID] = d
	f.Decls = append(f.Decls, d)
	return d
}

// Link records that from references to. Callers and Users hold each decl
// once; Refs counts every Caller link.
func (b *Builder) Link(from, to *Decl, kind LinkKind) {
	switch kind {
	case Caller:
		from.Refs[to.Package]++
		if !slices.Contains(to.Callers, from.ID) {
			to.Callers = append(to.Callers, from.ID)
		}
	case User:
		if !slices.Contains(to.Users, from.ID) {
			to.Users = append(to.Users, from.ID)
		}
	}
}

// Finish sorts packages, files, imports, callers and users, and returns the
// Snapshot. The Builder must not be used after.
func (b *Builder) Finish() *Snapshot {
	slices.SortFunc(b.s.Packages, func(a, b *Package) int { return cmp.Compare(a.Path, b.Path) })
	for _, p := range b.s.Packages {
		slices.SortFunc(p.Files, func(a, b *File) int { return cmp.Compare(a.Path, b.Path) })
		slices.Sort(p.Imports)
	}
	for _, d := range b.s.decls {
		slices.Sort(d.Callers)
		slices.Sort(d.Users)
	}
	return b.s
}
