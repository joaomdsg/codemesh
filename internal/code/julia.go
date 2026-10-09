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
	Modules []struct {
		Path             string
		Exports, Imports []string
		Errors           []string
	}
	Files []struct {
		Path, Module string
		Test         bool
		Lines        int
	}
	Decls []struct {
		Name, Kind, Module, File string
		Test                     bool
		Lines, Start, End        int
		Params                   int
		Complexity, Nesting      int
		Sig, Args, Shape         string
		Refs                     []string
	}
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
	cmd := exec.Command("julia", "--startup-file=no", "--history-file=no", "-e", juliaScript, abs)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("julia: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var in jl
	if err := json.Unmarshal(out, &in); err != nil {
		return nil, fmt.Errorf("julia: %w", err)
	}
	s := &Snapshot{Module: in.Module, Dir: abs, Lang: Julia, decls: map[string]*Decl{}, pkgs: map[string]*Package{}}

	exports := map[string]map[string]bool{}
	for _, m := range in.Modules {
		exports[m.Path] = map[string]bool{}
		for _, e := range m.Exports {
			exports[m.Path][e] = true
		}
	}
	files := map[string]*File{}
	for _, f := range in.Files {
		p := s.pkgs[f.Module]
		if p == nil {
			rel := "."
			if f.Module != in.Module {
				rel = strings.TrimPrefix(f.Module, in.Module+".")
			}
			p = &Package{Path: f.Module, Rel: rel, Name: f.Module[strings.LastIndex(f.Module, ".")+1:]}
			s.pkgs[f.Module] = p
			s.Packages = append(s.Packages, p)
		}
		file := &File{Path: f.Path, Package: p.Path, Test: f.Test, Lines: f.Lines}
		files[f.Path] = file
		p.Files = append(p.Files, file)
	}
	// owner maps a module to the Package holding its code: itself, or the
	// package of the file it is declared in.
	owner := map[string]string{}
	for _, m := range in.Modules {
		owner[m.Path] = m.Path
		for parent := m.Path; s.pkgs[owner[m.Path]] == nil && strings.Contains(parent, "."); {
			parent = parent[:strings.LastIndex(parent, ".")]
			owner[m.Path] = parent
		}
	}
	for _, m := range in.Modules {
		p := s.pkgs[owner[m.Path]]
		if p == nil {
			continue
		}
		p.Errors = append(p.Errors, m.Errors...)
		for _, i := range m.Imports {
			if o := owner[i]; s.pkgs[o] != nil && o != p.Path && !slices.Contains(p.Imports, o) {
				p.Imports = append(p.Imports, o)
			}
		}
	}

	byName := map[string][]*Decl{}
	refs := map[*Decl][]string{}
	seenFrom := map[*Decl]string{} // the package whose name reaches a decl
	homeOf := map[*Decl]string{}   // the module that declares its name
	for _, jd := range in.Decls {
		f := files[jd.File]
		if f == nil {
			continue
		}
		name := jd.Name
		if jd.Module != f.Package && strings.HasPrefix(jd.Module, f.Package+".") {
			name = strings.TrimPrefix(jd.Module, f.Package+".") + "." + name
		}
		// A method of another module's function, HyperSignal.render in an
		// extension, is reached through that module's name.
		home, short := jd.Module, jd.Name
		if i := strings.LastIndex(jd.Name, "."); i > 0 && owner[jd.Name[:i]] != "" {
			home, short = jd.Name[:i], jd.Name[i+1:]
		}
		kind := Kind(jd.Kind)
		if kind == Func && strings.Contains(jd.Name, ".") {
			// Base.show or JSON.lower: a method another module calls by
			// dispatch, which no name in this package shows.
			kind = Method
		}
		d := &Decl{Name: name, Kind: kind, Package: f.Package, File: f.Path, Test: jd.Test,
			Exported: exports[home][short], Start: jd.Start, End: jd.End, Lines: jd.Lines,
			Complexity: jd.Complexity, Nesting: jd.Nesting, Params: jd.Params, Refs: map[string]int{}}
		if d.Kind == Func {
			d.Signature = jd.Sig
		}
		h := fnv.New64a()
		h.Write([]byte(jd.Shape))
		d.Shape = fmt.Sprintf("%x", h.Sum64())
		d.ID = f.Package + "." + name
		d.ID += jd.Args
		if s.decls[d.ID] != nil {
			d.ID += fmt.Sprintf("#%d", d.Start)
		}
		s.decls[d.ID] = d
		f.Decls = append(f.Decls, d)
		refs[d] = jd.Refs
		homeOf[d] = home
		seenFrom[d] = owner[home]
		if seenFrom[d] == "" {
			seenFrom[d] = d.Package
		}
		short = short[strings.LastIndex(short, ".")+1:]
		byName[short] = append(byName[short], d)
	}

	imports := func(p, q string) bool { return p == q || slices.Contains(s.pkgs[p].Imports, q) }
	for from, names := range refs {
		seen := map[*Decl]bool{}
		for _, n := range names {
			mod, short := "", n
			if i := strings.LastIndex(n, "."); i > 0 {
				mod, short = n[:i], n[i+1:]
				first, rest, _ := strings.Cut(mod, ".")
				if a := in.Aliases[first]; a != "" {
					mod = strings.TrimSuffix(a+"."+rest, ".")
				}
			}
			for _, to := range byName[short] {
				switch {
				case to == from || seen[to]:
					continue
				case mod != "" && !strings.HasSuffix("."+homeOf[to], "."+mod):
					continue
				case mod == "" && seenFrom[to] != from.Package && !(to.Exported && imports(from.Package, seenFrom[to])):
					continue
				}
				seen[to] = true
				from.Refs[to.Package]++
				to.Callers = append(to.Callers, from.ID)
			}
		}
	}

	slices.SortFunc(s.Packages, func(a, b *Package) int { return cmp.Compare(a.Path, b.Path) })
	for _, p := range s.Packages {
		slices.SortFunc(p.Files, func(a, b *File) int { return cmp.Compare(a.Path, b.Path) })
		slices.Sort(p.Imports)
	}
	for _, d := range s.decls {
		slices.Sort(d.Callers)
	}
	return s, nil
}
