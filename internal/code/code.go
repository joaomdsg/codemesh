// Package code loads a Go module into a Snapshot: its packages, files and
// declarations, with metrics and type-checked references between them.
package code

import (
	"slices"
	"strings"
)

// Snapshot is one module as analysed at one point in time.
type Snapshot struct {
	Module   string // module path
	Dir      string // absolute module root
	Packages []*Package
	// Nested are the directories, relative to Dir, of modules inside this
	// one. Go loads them as separate modules, so their code is not here.
	Nested []string

	decls map[string]*Decl
	pkgs  map[string]*Package
}

// Package is one import path of the module. Its external test package's
// files (package foo_test) are attributed to it.
type Package struct {
	Path    string   // import path
	Rel     string   // directory relative to the module root, "." for the root
	Name    string   // package name
	Imports []string // module-internal import paths of non-test files, sorted
	Files   []*File
	Errors  []string // load, parse and type errors
}

// File is one Go source file.
type File struct {
	Path      string // slash-separated, relative to the module root
	Package   string // import path of the owning Package
	Test      bool
	Generated bool
	Lines     int // lines holding code, comments and blanks excluded
	Churn     int // commits touching the file in the churn window; set by the caller
	Decls     []*Decl
}

// Kind is the kind of a declaration.
type Kind string

const (
	Func   Kind = "func"
	Method Kind = "method"
	Type   Kind = "type"
	Var    Kind = "var"
	Const  Kind = "const"
)

// Decl is one top-level declaration: a func, a method, a type spec or a
// var/const spec.
type Decl struct {
	ID       string // types package path + "." + Name; unique in a Snapshot
	Name     string // "Name", or "Recv.Name" for methods
	Kind     Kind
	Package  string // import path of the owning Package
	File     string // File.Path
	Test     bool   // declared in a _test.go file
	Exported bool
	// Data marks a package-level const or var whose value is only string
	// literals over several lines, such as an inlined script. It counts as
	// one code line: its content is not Go a reader has to follow.
	Data bool

	Start, End int // 1-based line span, doc comment included
	Lines      int // code lines in the span

	Complexity int    // cyclomatic complexity; funcs and methods only
	Nesting    int    // deepest statement nesting; funcs and methods only
	Params     int    // parameter count; funcs and methods only
	Signature  string // type signature, for contract-change detection
	Shape      string // hash of the syntax tree, blind to comments and layout

	// Refs counts references from this decl to declarations of each
	// module-internal package, keyed by import path. Own package included.
	Refs map[string]int
	// Callers are the IDs of other decls referencing this one, sorted.
	Callers []string
}

// Decl returns the declaration with the given ID, or nil.
func (s *Snapshot) Decl(id string) *Decl { return s.decls[id] }

// Package returns the package with the given import path, or nil.
func (s *Snapshot) Package(path string) *Package { return s.pkgs[path] }

// Decls returns every declaration in package then file order.
func (s *Snapshot) Decls() []*Decl {
	var out []*Decl
	for _, p := range s.Packages {
		for _, f := range p.Files {
			out = append(out, f.Decls...)
		}
	}
	return out
}

// ImportedBy returns the module-internal packages importing path, sorted.
func (s *Snapshot) ImportedBy(path string) []string {
	var out []string
	for _, p := range s.Packages {
		if slices.Contains(p.Imports, path) {
			out = append(out, p.Path)
		}
	}
	return out
}

// Importable reports whether other modules can import the package at path:
// no internal directory guards it and it is not a command.
func (s *Snapshot) Importable(path string) bool {
	rel := strings.TrimPrefix(path, s.Module)
	if strings.Contains(rel+"/", "/internal/") {
		return false
	}
	p := s.Package(path)
	return p == nil || p.Name != "main"
}

// Lines returns the code lines of a package, test files excluded.
func (p *Package) Lines() int {
	n := 0
	for _, f := range p.Files {
		if !f.Test {
			n += f.Lines
		}
	}
	return n
}

// HasTests reports whether the package has any test file.
func (p *Package) HasTests() bool {
	return slices.ContainsFunc(p.Files, func(f *File) bool { return f.Test })
}
