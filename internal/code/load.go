package code

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedImports |
	packages.NeedModule | packages.NeedTypes | packages.NeedSyntax |
	packages.NeedTypesInfo | packages.NeedForTest

// Load analyses the module rooted at dir.
func Load(dir string) (*Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{Dir: abs, Mode: loadMode, Tests: true, Fset: token.NewFileSet()}
	loaded, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", abs, err)
	}
	l := &loader{
		s:    &Snapshot{decls: map[string]*Decl{}, pkgs: map[string]*Package{}},
		fset: cfg.Fset,
		objs: map[string]*Decl{},
		seen: map[string]bool{},
	}
	for _, p := range loaded {
		if p.Module != nil && p.Module.Main {
			l.s.Module, l.s.Dir = p.Module.Path, p.Module.Dir
			break
		}
	}
	if l.s.Module == "" {
		return nil, errors.New("no main module found in " + abs)
	}
	for _, p := range loaded {
		l.addPackage(p)
	}
	for _, u := range l.units {
		l.resolve(u)
	}
	l.finish()
	return l.s, nil
}

// unit is a decl with the syntax and type info it was checked with.
type unit struct {
	d    *Decl
	node ast.Node
	info *types.Info
}

type loader struct {
	s     *Snapshot
	fset  *token.FileSet
	objs  map[string]*Decl // objKey → decl
	seen  map[string]bool  // absolute file names already taken
	units []unit
}

func (l *loader) addPackage(p *packages.Package) {
	// The generated test main (path "p.test") is not code anyone wrote.
	if strings.HasSuffix(p.PkgPath, ".test") || p.Module == nil || p.Module.Path != l.s.Module {
		return
	}
	owner := cmp.Or(p.ForTest, p.PkgPath)
	pkg := l.s.pkgs[owner]
	if pkg == nil {
		pkg = &Package{Path: owner, Rel: l.rel(owner)}
		l.s.pkgs[owner] = pkg
		l.s.Packages = append(l.s.Packages, pkg)
	}
	if p.ForTest == "" {
		pkg.Name = p.Name
		for path := range p.Imports {
			if l.internal(path) {
				pkg.Imports = append(pkg.Imports, path)
			}
		}
		slices.Sort(pkg.Imports)
	}
	for _, e := range p.Errors {
		if msg := e.Error(); !slices.Contains(pkg.Errors, msg) {
			pkg.Errors = append(pkg.Errors, msg)
		}
	}
	for _, f := range p.Syntax {
		name := l.fset.File(f.Pos()).Name()
		if l.seen[name] {
			continue
		}
		l.seen[name] = true
		pkg.Files = append(pkg.Files, l.addFile(p, owner, name, f))
	}
}

func (l *loader) addFile(p *packages.Package, owner, name string, f *ast.File) *File {
	rel, err := filepath.Rel(l.s.Dir, name)
	if err != nil {
		rel = name
	}
	file := &File{
		Path:      filepath.ToSlash(rel),
		Package:   owner,
		Test:      strings.HasSuffix(name, "_test.go"),
		Generated: ast.IsGenerated(f),
	}
	src, err := os.ReadFile(name)
	if err != nil {
		src = nil
	}
	code := codeLines(l.fset.File(f.Pos()), src)
	file.Lines = len(code)
	for _, gd := range f.Decls {
		for _, u := range l.declsOf(p, gd) {
			u.d.Package, u.d.File, u.d.Test = owner, file.Path, file.Test
			u.d.Lines = countIn(code, u.d.Start, u.d.End)
			file.Decls = append(file.Decls, u.d)
			l.units = append(l.units, u)
			l.s.decls[u.d.ID] = u.d
		}
	}
	return file
}

func (l *loader) declsOf(p *packages.Package, gd ast.Decl) []unit {
	qual := types.RelativeTo(p.Types)
	switch gd := gd.(type) {
	case *ast.FuncDecl:
		d := &Decl{Kind: Func, Name: gd.Name.Name, Exported: gd.Name.IsExported()}
		if recv := recvName(gd); recv != "" {
			d.Kind, d.Name = Method, recv+"."+gd.Name.Name
			d.Exported = d.Exported && ast.IsExported(recv)
		}
		d.ID = p.PkgPath + "." + d.Name
		d.Start, d.End = l.span(gd.Doc, gd.Pos(), gd.End())
		d.Params = paramCount(gd.Type.Params)
		if gd.Body != nil {
			d.Complexity, d.Nesting = complexity(gd.Body), nesting(gd.Body)
		}
		if obj := p.TypesInfo.Defs[gd.Name]; obj != nil {
			d.Signature = types.TypeString(obj.Type(), qual)
			l.objs[objKey(obj)] = d
		}
		d.Shape = shape(gd)
		return []unit{{d, gd, p.TypesInfo}}
	case *ast.GenDecl:
		if gd.Tok == token.IMPORT {
			return nil
		}
		var out []unit
		for _, spec := range gd.Specs {
			d := &Decl{Kind: kindOf(gd.Tok)}
			doc, start, end := specDoc(spec), spec.Pos(), spec.End()
			// A spec outside parentheses shares the GenDecl's keyword and doc.
			if !gd.Lparen.IsValid() {
				doc, start, end = gd.Doc, gd.Pos(), gd.End()
			}
			d.Start, d.End = l.span(doc, start, end)
			var typ []string
			for _, id := range specNames(spec) {
				if d.Name == "" {
					d.Name, d.Exported = id.Name, id.IsExported()
				}
				if obj := p.TypesInfo.Defs[id]; obj != nil {
					if gd.Tok == token.TYPE {
						typ = append(typ, surface(obj.Type().Underlying(), qual))
					} else {
						typ = append(typ, types.TypeString(obj.Type(), qual))
					}
					l.objs[objKey(obj)] = d
				}
			}
			if d.Name == "" || d.Name == "_" {
				continue
			}
			d.ID = p.PkgPath + "." + d.Name
			d.Signature = strings.Join(typ, ", ")
			d.Shape = shape(spec)
			out = append(out, unit{d, spec, p.TypesInfo})
		}
		return out
	}
	return nil
}

// resolve records every reference from u's syntax to a module declaration.
func (l *loader) resolve(u unit) {
	ast.Inspect(u.node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := u.info.Uses[id]
		if obj == nil || obj.Pkg() == nil {
			return true
		}
		to := l.objs[objKey(origin(obj))]
		if to == nil || to == u.d {
			return true
		}
		if u.d.Refs == nil {
			u.d.Refs = map[string]int{}
		}
		u.d.Refs[to.Package]++
		if !slices.Contains(to.Callers, u.d.ID) {
			to.Callers = append(to.Callers, u.d.ID)
		}
		return true
	})
}

func (l *loader) finish() {
	slices.SortFunc(l.s.Packages, func(a, b *Package) int { return cmp.Compare(a.Path, b.Path) })
	for _, p := range l.s.Packages {
		slices.SortFunc(p.Files, func(a, b *File) int { return cmp.Compare(a.Path, b.Path) })
	}
	for _, d := range l.s.decls {
		slices.Sort(d.Callers)
	}
}

func (l *loader) span(doc *ast.CommentGroup, start, end token.Pos) (int, int) {
	if doc != nil {
		start = doc.Pos()
	}
	return l.fset.Position(start).Line, l.fset.Position(end).Line
}

func (l *loader) rel(path string) string {
	if path == l.s.Module {
		return "."
	}
	return strings.TrimPrefix(path, l.s.Module+"/")
}

func (l *loader) internal(path string) bool {
	return path == l.s.Module || strings.HasPrefix(path, l.s.Module+"/")
}

// surface is the part of a type other packages can see: a struct's exported
// and embedded fields, or the whole type otherwise. Adding a private field is
// then no signature change.
func surface(t types.Type, qual types.Qualifier) string {
	st, ok := t.(*types.Struct)
	if !ok {
		return types.TypeString(t, qual)
	}
	var fields []string
	for f := range st.Fields() {
		if f.Exported() || f.Embedded() {
			fields = append(fields, f.Name()+" "+types.TypeString(f.Type(), qual))
		}
	}
	return "struct{" + strings.Join(fields, "; ") + "}"
}

// objKey names a package-level object or method the same way in every
// package variant, so references from test variants resolve too.
func objKey(obj types.Object) string {
	if fn, ok := obj.(*types.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil {
			return obj.Pkg().Path() + "." + typeName(recv.Type()) + "." + obj.Name()
		}
	}
	if obj.Parent() != obj.Pkg().Scope() {
		return ""
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

func origin(obj types.Object) types.Object {
	switch o := obj.(type) {
	case *types.Func:
		return o.Origin()
	case *types.Var:
		return o.Origin()
	}
	return obj
}

func typeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		return t.Obj().Name()
	case *types.Interface:
		return ""
	}
	return t.String()
}

func recvName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.ParenExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

func kindOf(tok token.Token) Kind {
	switch tok {
	case token.TYPE:
		return Type
	case token.CONST:
		return Const
	}
	return Var
}

func specDoc(s ast.Spec) *ast.CommentGroup {
	switch s := s.(type) {
	case *ast.TypeSpec:
		return s.Doc
	case *ast.ValueSpec:
		return s.Doc
	}
	return nil
}

func specNames(s ast.Spec) []*ast.Ident {
	switch s := s.(type) {
	case *ast.TypeSpec:
		return []*ast.Ident{s.Name}
	case *ast.ValueSpec:
		return s.Names
	}
	return nil
}
