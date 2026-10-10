package code

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedImports |
	packages.NeedModule | packages.NeedTypes | packages.NeedSyntax |
	packages.NeedTypesInfo | packages.NeedForTest

// Load analyses the Go module or the Julia package rooted at dir: a
// Project.toml without a go.mod beside it makes it Julia.
func Load(dir string) (*Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if exists(filepath.Join(abs, "Project.toml")) && !exists(filepath.Join(abs, "go.mod")) {
		return loadJulia(abs)
	}
	return loadGo(abs)
}

func loadGo(abs string) (*Snapshot, error) {
	cfg := &packages.Config{Dir: abs, Mode: loadMode, Tests: true, Fset: token.NewFileSet()}
	loaded, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", abs, err)
	}
	l := &loader{
		s:    &Snapshot{Lang: Go, decls: map[string]*Decl{}, pkgs: map[string]*Package{}},
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
	l.s.Nested = nestedModules(l.s.Dir)
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
		l.addImports(pkg, p)
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

func (l *loader) addImports(pkg *Package, p *packages.Package) {
	for path := range p.Imports {
		if l.internal(path) {
			pkg.Imports = append(pkg.Imports, path)
		}
	}
	slices.Sort(pkg.Imports)
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
	for _, gd := range f.Decls {
		for _, u := range l.declsOf(p, gd) {
			u.d.Package, u.d.File, u.d.Test = owner, file.Path, file.Test
			if u.d.Data {
				keepFirst(code, u.d.Start, u.d.End)
			}
			u.d.Lines = countIn(code, u.d.Start, u.d.End)
			file.Decls = append(file.Decls, u.d)
			l.units = append(l.units, u)
			l.s.decls[u.d.ID] = u.d
		}
	}
	file.Lines = len(code)
	return file
}

func (l *loader) declsOf(p *packages.Package, gd ast.Decl) []unit {
	switch gd := gd.(type) {
	case *ast.FuncDecl:
		return []unit{l.funcUnit(p, gd)}
	case *ast.GenDecl:
		if gd.Tok == token.IMPORT {
			return nil
		}
		var out []unit
		for _, spec := range gd.Specs {
			if u, ok := l.specUnit(p, gd, spec); ok {
				out = append(out, u)
			}
		}
		return out
	}
	return nil
}

func (l *loader) funcUnit(p *packages.Package, gd *ast.FuncDecl) unit {
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
		d.Signature = types.TypeString(obj.Type(), types.RelativeTo(p.Types))
		l.index(obj, d)
	}
	d.Shape = shape(gd)
	return unit{d, gd, p.TypesInfo}
}

func (l *loader) specUnit(p *packages.Package, gd *ast.GenDecl, spec ast.Spec) (unit, bool) {
	qual := types.RelativeTo(p.Types)
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
		obj := p.TypesInfo.Defs[id]
		if obj == nil {
			continue
		}
		if gd.Tok == token.TYPE {
			typ = append(typ, surface(obj.Type().Underlying(), qual))
		} else {
			typ = append(typ, types.TypeString(obj.Type(), qual))
		}
		l.index(obj, d)
	}
	if d.Name == "" || d.Name == "_" {
		return unit{}, false
	}
	d.ID = p.PkgPath + "." + d.Name
	d.Signature = strings.Join(typ, ", ")
	d.Data = gd.Tok != token.TYPE && l.isData(spec.(*ast.ValueSpec), p.TypesInfo)
	d.Shape = shape(spec)
	return unit{d, spec, p.TypesInfo}, true
}

// index makes obj resolvable to d. Objects outside the package scope, such
// as a blank identifier, have no key; storing them under "" would resolve
// every local variable to d.
func (l *loader) index(obj types.Object, d *Decl) {
	if k := objKey(obj); k != "" {
		l.objs[k] = d
	}
}

// resolve records every reference from u's syntax to a module declaration.
func (l *loader) resolve(u unit) {
	ast.Inspect(u.node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if sel := u.info.Selections[n]; sel != nil {
				l.use(u, sel.Recv())
				l.use(u, sel.Type())
			}
		case *ast.Ident:
			l.ref(u, n)
		}
		return true
	})
}

func (l *loader) ref(u unit, id *ast.Ident) {
	obj := u.info.Uses[id]
	if obj == nil || obj.Pkg() == nil {
		return
	}
	if c, ok := obj.(*types.Const); ok {
		l.use(u, c.Type())
	}
	to := l.objs[objKey(origin(obj))]
	if to == nil || to == u.d {
		return
	}
	if u.d.Refs == nil {
		u.d.Refs = map[string]int{}
	}
	u.d.Refs[to.Package]++
	if !slices.Contains(to.Callers, u.d.ID) {
		to.Callers = append(to.Callers, u.d.ID)
	}
}

// use records that u reaches the module type t through a constant, a
// field, a field's value or a method. Callers misses this when u never
// spells the type's name.
func (l *loader) use(u unit, t types.Type) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return
	}
	to := l.objs[objKey(n.Origin().Obj())]
	if to == nil || to == u.d || slices.Contains(to.Users, u.d.ID) {
		return
	}
	to.Users = append(to.Users, u.d.ID)
}

func (l *loader) finish() {
	slices.SortFunc(l.s.Packages, func(a, b *Package) int { return cmp.Compare(a.Path, b.Path) })
	for _, p := range l.s.Packages {
		slices.SortFunc(p.Files, func(a, b *File) int { return cmp.Compare(a.Path, b.Path) })
		// A nested module shares the path prefix but is another module.
		p.Imports = slices.DeleteFunc(p.Imports, func(path string) bool { return l.s.pkgs[path] == nil })
	}
	for _, d := range l.s.decls {
		slices.Sort(d.Callers)
		slices.Sort(d.Users)
	}
}

// nestedModules lists the directories below root holding a go.mod, the
// outermost only. The go command skips the same directories: hidden ones,
// testdata, and those starting with an underscore.
func nestedModules(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		return nil
	})
	return out
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

// isData reports whether vs holds text rather than code: every value is a
// string literal, a concatenation of them, or a conversion of one, as in
// json.RawMessage(`…`), and they run over several lines. A one-line string,
// such as an enum value, stays code: changing it changes behaviour.
func (l *loader) isData(vs *ast.ValueSpec, info *types.Info) bool {
	var lit func(e ast.Expr) bool
	lit = func(e ast.Expr) bool {
		switch e := ast.Unparen(e).(type) {
		case *ast.BasicLit:
			return e.Kind == token.STRING
		case *ast.BinaryExpr:
			return e.Op == token.ADD && lit(e.X) && lit(e.Y)
		case *ast.CallExpr:
			return len(e.Args) == 1 && info.Types[e.Fun].IsType() && lit(e.Args[0])
		}
		return false
	}
	if len(vs.Values) == 0 || slices.ContainsFunc(vs.Values, func(e ast.Expr) bool { return !lit(e) }) {
		return false
	}
	return l.fset.Position(vs.Values[0].Pos()).Line < l.fset.Position(vs.Values[len(vs.Values)-1].End()).Line
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

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
