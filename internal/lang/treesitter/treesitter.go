// Package treesitter reads TypeScript and JavaScript, Python and Rust with
// tree-sitter grammars. It parses only: no type checker runs, so references
// are matched by name and scoped by imports, as the Julia frontend does.
package treesitter

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
)

// Frontend reads one language.
type Frontend struct{ l *language }

var (
	TypeScript = Frontend{&typescript}
	Python     = Frontend{&python}
	Rust       = Frontend{&rust}
)

func (f Frontend) Lang() code.Lang { return f.l.lang }

// Exts returns the extensions of the files it reads.
func (f Frontend) Exts() []string { return f.l.exts }

// Detect reports whether dir holds one of the language's project files.
func (f Frontend) Detect(dir string) bool {
	return slices.ContainsFunc(f.l.projects, func(p string) bool {
		_, err := os.Stat(filepath.Join(dir, p))
		return err == nil
	})
}

// language is what one grammar contributes to the shared loader.
type language struct {
	lang     code.Lang
	exts     []string
	projects []string
	module   func(dir string) string
	grammar  func(rel string) string // "" leaves the file out
	test     func(rel string) bool
	// pkg returns the package a file belongs to: its path, its directory
	// relative to the root and its name.
	pkg func(module, rel string) (path, dir, name string)
	// extract lists a file's decls and sets its imports.
	extract func(f *source) []rawDecl
	// resolve prepares the mapping of an import, as extract wrote it, to the
	// path of the module-internal package it names, "" for any other.
	resolve func(module string, srcs []*source) func(f *source, imp string) string
	// params counts a function's parameters, receiver excluded, and
	// normalises its parameter and return text.
	params func(fn node, method bool) (int, string)
	tbl    table
}

// source is one file to read.
type source struct {
	rel, grammar string
	test         bool
	pkg          string // import path
	pkgDir       string
	pkgName      string
	imports      []string
	root         node
	src          []byte
}

// rawDecl is a decl as a language's extract found it.
type rawDecl struct {
	name     string
	kind     code.Kind
	exported bool
	test     bool
	outer    node   // the whole statement: export keyword and decorators included
	node     node   // what shape and references read
	fn       node   // the function itself; zero for other kinds
	lead     int    // first line of the comments and attributes above, 0 for none
	leadText string // their text, and the decorators
}

type parsedDecl struct {
	d    code.Decl
	refs []ref
}

type parsedFile struct {
	f     code.File
	decls []parsedDecl
	err   string
}

func (f Frontend) Load(ctx context.Context, dir string) (*code.Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	l := f.l
	module := l.module(abs)
	srcs, err := l.sources(abs, module)
	if err != nil {
		return nil, err
	}
	resolve := l.resolve(module, srcs)

	ps := newParsers()
	b := code.NewBuilder(l.lang, module, abs)
	pkgs := map[string]*code.Package{}
	imports := map[string]map[string]bool{} // package → packages it imports, tests included
	type built struct {
		d    *code.Decl
		refs []ref
	}
	var all []built
	for _, s := range srcs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pf, err := l.parse(ps, abs, s)
		if err != nil {
			return nil, err
		}
		p := pkgs[s.pkg]
		if p == nil {
			p = b.Package(code.Package{Path: s.pkg, Rel: s.pkgDir, Name: s.pkgName})
			pkgs[s.pkg] = p
			imports[s.pkg] = map[string]bool{}
		}
		if pf.err != "" {
			p.Errors = append(p.Errors, pf.err)
		}
		for _, imp := range s.imports {
			to := resolve(s, imp)
			if to == "" || to == s.pkg {
				continue
			}
			imports[s.pkg][to] = true
			if !s.test && !slices.Contains(p.Imports, to) {
				p.Imports = append(p.Imports, to)
			}
		}
		file := b.File(p, pf.f)
		for _, pd := range pf.decls {
			all = append(all, built{b.Decl(file, pd.d), pd.refs})
		}
	}

	byName := map[string][]*code.Decl{}
	for _, x := range all {
		short := x.d.Name[strings.LastIndex(x.d.Name, ".")+1:]
		byName[short] = append(byName[short], x.d)
	}
	for _, x := range all {
		callers := map[*code.Decl]bool{}
		for _, r := range x.refs {
			for _, to := range byName[r.name] {
				visible := to.Package == x.d.Package || to.Exported && imports[x.d.Package][to.Package]
				if to == x.d || !visible || !r.reaches(to) {
					continue
				}
				if !callers[to] {
					callers[to] = true
					b.Link(x.d, to, code.Caller)
				}
				if r.kind == refType {
					b.Link(x.d, to, code.User)
				}
			}
		}
	}
	return b.Finish(), nil
}

// reaches reports whether a reference of this kind can mean to: a bare name
// is not a method, a type annotation names a type.
func (r ref) reaches(to *code.Decl) bool {
	switch r.kind {
	case refPlain:
		return to.Kind != code.Method
	case refType:
		return to.Kind == code.Type
	}
	return true
}

var skipDirs = []string{"node_modules", "vendor", "target", "dist", "build", "__pycache__", "venv", "testdata"}

// sources lists the files below abs the language reads, in path order,
// skipping hidden and dependency directories.
func (l *language) sources(abs, module string) ([]*source, error) {
	var out []*source
	err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); p != abs && (strings.HasPrefix(name, ".") || slices.Contains(skipDirs, name)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if g := l.grammar(rel); g != "" {
			path, dir, name := l.pkg(module, rel)
			out = append(out, &source{rel: rel, grammar: g, test: l.test(rel), pkg: path, pkgDir: dir, pkgName: name})
		}
		return nil
	})
	return out, err
}

// parse reads one file: its lines, its decls with their metrics and the
// names each refers to. The tree is released before it returns.
func (l *language) parse(ps *parsers, abs string, s *source) (parsedFile, error) {
	src, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(s.rel)))
	if err != nil {
		return parsedFile{}, err
	}
	t, err := ps.parse(s.grammar, src)
	if err != nil {
		return parsedFile{}, err
	}
	defer t.release()
	s.root, s.src = t.root, src

	lines := codeLines(t.root)
	pf := parsedFile{f: code.File{Path: s.rel, Test: s.test, Generated: generated(src), Lines: len(lines)}}
	if t.root.broken() {
		pf.err = s.rel + ": syntax errors"
	}
	raws := l.extract(s)
	inner := map[[2]int]bool{}
	for _, r := range raws {
		inner[[2]int{r.node.start(), r.node.end()}] = true
	}
	for _, r := range raws {
		own := [2]int{r.node.start(), r.node.end()}
		delete(inner, own)
		d := code.Decl{
			ID: s.pkg + "." + r.name, Name: r.name, Kind: r.kind, Test: s.test || r.test, Exported: r.exported,
			Start: r.outer.startLine(), End: r.outer.endLine(),
			Shape: shape(r.node, inner), Deprecated: deprecatedMark.MatchString(r.leadText),
		}
		if r.lead > 0 {
			d.Start = min(d.Start, r.lead)
		}
		for n := d.Start; n <= d.End; n++ {
			if lines[n] {
				d.Lines++
			}
		}
		if r.fn.ok() {
			d.Complexity, d.Nesting = l.tbl.complexity(r.fn), l.tbl.nesting(r.fn, 0)
			d.Params, d.Signature = l.params(r.fn, strings.Contains(r.name, "."))
		}
		pf.decls = append(pf.decls, parsedDecl{d, l.tbl.refs(r.node, inner)})
		inner[own] = true
	}
	return pf, nil
}

// leading returns the first line of the run of comments, and whatever else
// lead accepts, directly above items[i], and their text.
func leading(items []node, i int, lead func(node) bool) (int, string) {
	line, first := 0, items[i].startLine()
	var text []string
	for j := i - 1; j >= 0 && lead(items[j]) && first-items[j].endLine() <= 1; j-- {
		if j > 0 && items[j-1].endLine() >= items[j].startLine() {
			break // trails the statement before it
		}
		first, line = items[j].startLine(), items[j].startLine()
		text = append(text, items[j].text())
	}
	return line, strings.Join(text, "\n")
}

func fieldText(n node, field string) string {
	if c := n.field(field); c.ok() {
		return c.text()
	}
	return ""
}
