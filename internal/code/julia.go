package code

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os/exec"
	"slices"
	"strings"
)

//go:embed julia.jl
var juliaScript string

// jl is what julia.jl prints.
type jl struct {
	Module  string
	Aliases map[string]string
	Modules []jlModule
	Files   []jlFile
	Decls   []jlDecl
}

type jlModule struct {
	Path             string
	Exports, Imports []string
	Errors           []string
}

type jlFile struct {
	Path, Module string
	Test         bool
	Lines        int
}

type jlDecl struct {
	Name, Kind, Module, File string
	Test                     bool
	Lines, Start, End        int
	Params                   int
	Complexity, Nesting      int
	Sig, Args, Shape         string
	Refs                     []string
}

// juliaLoad holds what loadJulia builds the Snapshot from.
type juliaLoad struct {
	in      jl
	s       *Snapshot
	exports map[string]map[string]bool // module → exported names
	files   map[string]*File
	owner   map[string]string // module → the Package holding its code: itself, or the package of the file it is declared in

	byName   map[string][]*Decl
	refs     map[*Decl][]string
	seenFrom map[*Decl]string // the package whose name reaches a decl
	homeOf   map[*Decl]string // the module that declares its name
}

// loadJulia analyses the Julia package rooted at abs with Julia's own parser,
// which julia.jl runs. Each module that owns files is a Package; a submodule
// declared inside another module's file stays in that file's Package, its
// names qualified ("Helpers.icon"). Each method is its own Decl.
//
// Julia dispatches at run time, so references are matched by name: a use of
// f links to every f the user can see, the same package's or an exported
// one, and M.f to M's f. Callers and Refs are an inference, not a resolution.
func loadJulia(abs string) (*Snapshot, error) {
	in, err := runJulia(abs)
	if err != nil {
		return nil, err
	}
	j := &juliaLoad{
		in:       in,
		s:        &Snapshot{Module: in.Module, Dir: abs, Lang: Julia, decls: map[string]*Decl{}, pkgs: map[string]*Package{}},
		exports:  map[string]map[string]bool{},
		files:    map[string]*File{},
		owner:    map[string]string{},
		byName:   map[string][]*Decl{},
		refs:     map[*Decl][]string{},
		seenFrom: map[*Decl]string{},
		homeOf:   map[*Decl]string{},
	}
	for _, m := range in.Modules {
		j.exports[m.Path] = map[string]bool{}
		for _, e := range m.Exports {
			j.exports[m.Path][e] = true
		}
	}
	for _, f := range in.Files {
		j.addFile(f)
	}
	j.findOwners()
	j.addImports()
	for _, jd := range in.Decls {
		j.addDecl(jd)
	}
	j.linkRefs()
	j.finish()
	return j.s, nil
}

func runJulia(abs string) (jl, error) {
	var in jl
	cmd := exec.Command("julia", "--startup-file=no", "--history-file=no", "-e", juliaScript, abs)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return in, fmt.Errorf("julia: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(out, &in); err != nil {
		return in, fmt.Errorf("julia: %w", err)
	}
	return in, nil
}

func (j *juliaLoad) addFile(f jlFile) {
	p := j.s.pkgs[f.Module]
	if p == nil {
		rel := "."
		if f.Module != j.in.Module {
			rel = strings.TrimPrefix(f.Module, j.in.Module+".")
		}
		p = &Package{Path: f.Module, Rel: rel, Name: f.Module[strings.LastIndex(f.Module, ".")+1:]}
		j.s.pkgs[f.Module] = p
		j.s.Packages = append(j.s.Packages, p)
	}
	file := &File{Path: f.Path, Package: p.Path, Test: f.Test, Lines: f.Lines}
	j.files[f.Path] = file
	p.Files = append(p.Files, file)
}

func (j *juliaLoad) findOwners() {
	for _, m := range j.in.Modules {
		j.owner[m.Path] = m.Path
		for parent := m.Path; j.s.pkgs[j.owner[m.Path]] == nil && strings.Contains(parent, "."); {
			parent = parent[:strings.LastIndex(parent, ".")]
			j.owner[m.Path] = parent
		}
	}
}

func (j *juliaLoad) addImports() {
	for _, m := range j.in.Modules {
		p := j.s.pkgs[j.owner[m.Path]]
		if p == nil {
			continue
		}
		p.Errors = append(p.Errors, m.Errors...)
		for _, i := range m.Imports {
			if o := j.owner[i]; j.s.pkgs[o] != nil && o != p.Path && !slices.Contains(p.Imports, o) {
				p.Imports = append(p.Imports, o)
			}
		}
	}
}

func (j *juliaLoad) addDecl(jd jlDecl) {
	f := j.files[jd.File]
	if f == nil {
		return
	}
	// A method of another module's function, HyperSignal.render in an
	// extension, is reached through that module's name.
	home, short := jd.Module, jd.Name
	if i := strings.LastIndex(jd.Name, "."); i > 0 && j.owner[jd.Name[:i]] != "" {
		home, short = jd.Name[:i], jd.Name[i+1:]
	}
	d := j.newDecl(jd, f, home, short)
	if j.s.decls[d.ID] != nil {
		d.ID += fmt.Sprintf("#%d", d.Start)
	}
	j.s.decls[d.ID] = d
	f.Decls = append(f.Decls, d)
	j.refs[d] = jd.Refs
	j.homeOf[d] = home
	j.seenFrom[d] = j.owner[home]
	if j.seenFrom[d] == "" {
		j.seenFrom[d] = d.Package
	}
	short = short[strings.LastIndex(short, ".")+1:]
	j.byName[short] = append(j.byName[short], d)
}

func (j *juliaLoad) newDecl(jd jlDecl, f *File, home, short string) *Decl {
	name := jd.Name
	if jd.Module != f.Package && strings.HasPrefix(jd.Module, f.Package+".") {
		name = strings.TrimPrefix(jd.Module, f.Package+".") + "." + name
	}
	kind := Kind(jd.Kind)
	if kind == Func && strings.Contains(jd.Name, ".") {
		// Base.show or JSON.lower: a method another module calls by
		// dispatch, which no name in this package shows.
		kind = Method
	}
	d := &Decl{Name: name, Kind: kind, Package: f.Package, File: f.Path, Test: jd.Test,
		Exported: j.exports[home][short], Start: jd.Start, End: jd.End, Lines: jd.Lines,
		Complexity: jd.Complexity, Nesting: jd.Nesting, Params: jd.Params, Refs: map[string]int{}}
	if d.Kind == Func {
		d.Signature = jd.Sig
	}
	h := fnv.New64a()
	h.Write([]byte(jd.Shape))
	d.Shape = fmt.Sprintf("%x", h.Sum64())
	d.ID = f.Package + "." + name + jd.Args
	return d
}

func (j *juliaLoad) linkRefs() {
	for from, names := range j.refs {
		seen := map[*Decl]bool{}
		for _, n := range names {
			mod, short := j.qualify(n)
			for _, to := range j.byName[short] {
				if to == from || seen[to] || !j.reaches(from, to, mod) {
					continue
				}
				seen[to] = true
				from.Refs[to.Package]++
				to.Callers = append(to.Callers, from.ID)
			}
		}
	}
}

// reaches reports whether a use of to's name in from, qualified by mod when
// non-empty, can mean to.
func (j *juliaLoad) reaches(from, to *Decl, mod string) bool {
	if mod != "" {
		return strings.HasSuffix("."+j.homeOf[to], "."+mod)
	}
	seen := j.seenFrom[to]
	return seen == from.Package || to.Exported && slices.Contains(j.s.pkgs[from.Package].Imports, seen)
}

// qualify splits a referenced name into its module, with an import alias
// expanded, and its short name.
func (j *juliaLoad) qualify(n string) (mod, short string) {
	i := strings.LastIndex(n, ".")
	if i <= 0 {
		return "", n
	}
	mod, short = n[:i], n[i+1:]
	first, rest, _ := strings.Cut(mod, ".")
	if a := j.in.Aliases[first]; a != "" {
		mod = strings.TrimSuffix(a+"."+rest, ".")
	}
	return mod, short
}

func (j *juliaLoad) finish() {
	slices.SortFunc(j.s.Packages, func(a, b *Package) int { return cmp.Compare(a.Path, b.Path) })
	for _, p := range j.s.Packages {
		slices.SortFunc(p.Files, func(a, b *File) int { return cmp.Compare(a.Path, b.Path) })
		slices.Sort(p.Imports)
	}
	for _, d := range j.s.decls {
		slices.Sort(d.Callers)
	}
}
