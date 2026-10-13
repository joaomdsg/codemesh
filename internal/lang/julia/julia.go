package julia

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
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
	b       *code.Builder
	pkgs    map[string]*code.Package
	exports map[string]map[string]bool // module → exported names
	files   map[string]*code.File
	owner   map[string]string // module → the Package holding its code: itself, or the package of the file it is declared in

	byName   map[string][]*code.Decl
	refs     map[*code.Decl][]string
	seenFrom map[*code.Decl]string // the package whose name reaches a decl
	homeOf   map[*code.Decl]string // the module that declares its name
}

// Frontend reads a Julia package with Julia's own parser, which julia.jl runs.
type Frontend struct{}

func (Frontend) Lang() code.Lang { return code.Julia }

// Detect treats a Project.toml without a go.mod beside it as a Julia package.
func (Frontend) Detect(dir string) bool {
	return exists(filepath.Join(dir, "Project.toml")) && !exists(filepath.Join(dir, "go.mod"))
}

func (Frontend) Load(ctx context.Context, dir string) (*code.Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return loadJulia(ctx, abs)
}

// loadJulia analyses the Julia package rooted at abs. Each module that owns files is a Package; a submodule
// declared inside another module's file stays in that file's Package, its
// names qualified ("Helpers.icon"). Each method is its own Decl.
//
// Julia dispatches at run time, so references are matched by name: a use of
// f links to every f the user can see, the same package's or an exported
// one, and M.f to M's f. Callers and Refs are an inference, not a resolution.
func loadJulia(ctx context.Context, abs string) (*code.Snapshot, error) {
	in, err := runJulia(ctx, abs)
	if err != nil {
		return nil, err
	}
	j := &juliaLoad{
		in:       in,
		b:        code.NewBuilder(code.Julia, in.Module, abs),
		pkgs:     map[string]*code.Package{},
		exports:  map[string]map[string]bool{},
		files:    map[string]*code.File{},
		owner:    map[string]string{},
		byName:   map[string][]*code.Decl{},
		refs:     map[*code.Decl][]string{},
		seenFrom: map[*code.Decl]string{},
		homeOf:   map[*code.Decl]string{},
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
	return j.b.Finish(), nil
}

func runJulia(ctx context.Context, abs string) (jl, error) {
	var in jl
	cmd := exec.CommandContext(ctx, "julia", "--startup-file=no", "--history-file=no", "-e", juliaScript, abs)
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
	p := j.pkgs[f.Module]
	if p == nil {
		rel := "."
		if f.Module != j.in.Module {
			rel = strings.TrimPrefix(f.Module, j.in.Module+".")
		}
		p = j.b.Package(code.Package{Path: f.Module, Rel: rel, Name: f.Module[strings.LastIndex(f.Module, ".")+1:]})
		j.pkgs[f.Module] = p
	}
	j.files[f.Path] = j.b.File(p, code.File{Path: f.Path, Test: f.Test, Lines: f.Lines})
}

func (j *juliaLoad) findOwners() {
	for _, m := range j.in.Modules {
		j.owner[m.Path] = m.Path
		for parent := m.Path; j.pkgs[j.owner[m.Path]] == nil && strings.Contains(parent, "."); {
			parent = parent[:strings.LastIndex(parent, ".")]
			j.owner[m.Path] = parent
		}
	}
}

func (j *juliaLoad) addImports() {
	for _, m := range j.in.Modules {
		p := j.pkgs[j.owner[m.Path]]
		if p == nil {
			continue
		}
		p.Errors = append(p.Errors, m.Errors...)
		for _, i := range m.Imports {
			if o := j.owner[i]; j.pkgs[o] != nil && o != p.Path && !slices.Contains(p.Imports, o) {
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
	d := j.b.Decl(f, j.newDecl(jd, f, home, short))
	j.refs[d] = jd.Refs
	j.homeOf[d] = home
	j.seenFrom[d] = j.owner[home]
	if j.seenFrom[d] == "" {
		j.seenFrom[d] = d.Package
	}
	short = short[strings.LastIndex(short, ".")+1:]
	j.byName[short] = append(j.byName[short], d)
}

func (j *juliaLoad) newDecl(jd jlDecl, f *code.File, home, short string) code.Decl {
	name := jd.Name
	if jd.Module != f.Package && strings.HasPrefix(jd.Module, f.Package+".") {
		name = strings.TrimPrefix(jd.Module, f.Package+".") + "." + name
	}
	kind := code.Kind(jd.Kind)
	if kind == code.Func && strings.Contains(jd.Name, ".") {
		// Base.show or JSON.lower: a method another module calls by
		// dispatch, which no name in this package shows.
		kind = code.Method
	}
	d := code.Decl{Name: name, Kind: kind, Test: jd.Test,
		Exported: j.exports[home][short], Start: jd.Start, End: jd.End, Lines: jd.Lines,
		Complexity: jd.Complexity, Nesting: jd.Nesting, Params: jd.Params}
	if d.Kind == code.Func {
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
		seen := map[*code.Decl]bool{}
		for _, n := range names {
			mod, short := j.qualify(n)
			for _, to := range j.byName[short] {
				if to == from || seen[to] || !j.reaches(from, to, mod) {
					continue
				}
				seen[to] = true
				j.b.Link(from, to, code.Caller)
			}
		}
	}
}

// reaches reports whether a use of to's name in from, qualified by mod when
// non-empty, can mean to.
func (j *juliaLoad) reaches(from, to *code.Decl, mod string) bool {
	if mod != "" {
		return strings.HasSuffix("."+j.homeOf[to], "."+mod)
	}
	seen := j.seenFrom[to]
	return seen == from.Package || to.Exported && slices.Contains(j.pkgs[from.Package].Imports, seen)
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

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
