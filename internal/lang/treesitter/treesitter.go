// Package treesitter reads TypeScript and JavaScript, Python and Rust with
// tree-sitter grammars. It parses only: no type checker runs, so references
// are matched by name and scoped by imports, as the Julia frontend does.
package treesitter

import (
	"context"
	"fmt"
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
	// paths of the module-internal packages it names, none for any other. It
	// runs after every file is parsed, so it can read what extract set.
	resolve func(dir, module string, srcs []*source) func(f *source, imp string) []string
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
	reexports    []string // the imports that also hand their names on
	root         node
	src          []byte
}

// rawDecl is a decl as a language's extract found it.
type rawDecl struct {
	name     string
	kind     code.Kind
	exported bool
	crate    bool // seen from the crate's other modules, exported or not
	test     bool
	outer    node   // the whole statement: export keyword and decorators included
	node     node   // what shape and references read
	fn       node   // the function itself; zero for other kinds
	lead     int    // first line of the comments and attributes above, 0 for none
	leadText string // their text, and the decorators
}

type parsedDecl struct {
	d     code.Decl
	refs  []ref
	crate bool
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

	ps := newParsers()
	parsed := make([]parsedFile, len(srcs))
	for i, s := range srcs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if parsed[i], err = l.parse(ps, abs, s); err != nil {
			return nil, err
		}
	}
	resolve := l.resolve(abs, module, srcs)

	b := code.NewBuilder(l.lang, module, abs)
	pkgs := map[string]*code.Package{}
	imports := map[string]map[string]bool{}     // package → packages it imports, tests included
	fileImports := map[string]map[string]bool{} // file → packages it imports, its own included
	crate := map[*code.Decl]bool{}
	type built struct {
		d    *code.Decl
		refs []ref
	}
	var all []built
	for i, s := range srcs {
		pf := parsed[i]
		p := pkgs[s.pkg]
		if p == nil {
			p = b.Package(code.Package{Path: s.pkg, Rel: s.pkgDir, Name: s.pkgName})
			pkgs[s.pkg] = p
			imports[s.pkg] = map[string]bool{}
		}
		if pf.err != "" {
			p.Errors = append(p.Errors, pf.err)
		}
		fileImports[s.rel] = map[string]bool{}
		for _, imp := range s.imports {
			for _, to := range resolve(s, imp) {
				fileImports[s.rel][to] = true
				if to == s.pkg {
					continue
				}
				imports[s.pkg][to] = true
				if !s.test && !slices.Contains(p.Imports, to) {
					p.Imports = append(p.Imports, to)
				}
			}
		}
		file := b.File(p, pf.f)
		for _, pd := range pf.decls {
			d := b.Decl(file, pd.d)
			crate[d] = pd.crate
			all = append(all, built{d, pd.refs})
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
			var cands []*code.Decl
			for _, to := range byName[r.name] {
				visible := to.Package == x.d.Package || (to.Exported || crate[to]) && imports[x.d.Package][to.Package]
				if visible && r.reaches(to) {
					cands = append(cands, to)
				}
			}
			// Without types, a member name several decls answer to says nothing
			// about which one is meant; linking all of them invents callers.
			if r.kind == refMember && len(cands) > 1 {
				continue
			}
			// A name declared in the file, or in a module the file imports,
			// is the likelier target than a same-named decl elsewhere.
			for _, ok := range []func(*code.Decl) bool{
				func(to *code.Decl) bool { return to.File == x.d.File },
				func(to *code.Decl) bool { return fileImports[x.d.File][to.Package] },
			} {
				if kept := slices.DeleteFunc(slices.Clone(cands), func(to *code.Decl) bool { return !ok(to) }); len(kept) > 0 {
					cands = kept
					break
				}
			}
			for _, to := range cands {
				if to == x.d {
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

// single adapts a resolver that names at most one package, "" for none.
func single(r func(module string, srcs []*source) func(*source, string) string) func(dir, module string, srcs []*source) func(*source, string) []string {
	return func(_, module string, srcs []*source) func(*source, string) []string {
		resolve := r(module, srcs)
		return func(f *source, imp string) []string {
			if p := resolve(f, imp); p != "" {
				return []string{p}
			}
			return nil
		}
	}
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
			if p == abs {
				return err
			}
			// One directory we cannot read does not make the project unreadable.
			return filepath.SkipDir
		}
		if d.IsDir() {
			if name := d.Name(); p != abs && (strings.HasPrefix(name, ".") || slices.Contains(skipDirs, name)) {
				return filepath.SkipDir
			}
			return nil
		}
		// A link may dangle and a pipe blocks a read; git does not follow links either.
		if !d.Type().IsRegular() {
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
		return parsedFile{f: code.File{Path: s.rel, Test: s.test}, err: s.rel + ": cannot be read"}, nil
	}
	t, err := ps.parse(s.grammar, src)
	if err != nil {
		return parsedFile{}, err
	}
	defer t.release()
	s.root, s.src = t.root, src

	lines := codeLines(t.root)
	pf := parsedFile{f: code.File{Path: s.rel, Test: s.test, Generated: generated(src), Lines: len(lines)}}
	// The parser recovers around a bad token (a valid '&' in a JSX string, say)
	// and leaves the rest of the tree sound. Once a quarter of the file is
	// error nodes the recovery has failed and the decls can't be trusted.
	if t.root.broken() && t.root.errorBytes()*4 > len(src) {
		pf.err = fmt.Sprintf("%s:%d: syntax errors", s.rel, t.root.errorLine())
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
		pf.decls = append(pf.decls, parsedDecl{d, l.tbl.refs(r.node, inner), r.crate})
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
