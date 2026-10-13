package treesitter

import (
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// tsConfig is the part of a tsconfig.json that maps specifiers to files.
type tsConfig struct {
	base    string // directory the paths are relative to, from the project root
	hasBase bool   // baseUrl was set: it also resolves bare specifiers
	paths   map[string][]string
}

// tsPackage is a package.json of the project, the places it points at.
type tsPackage struct {
	dir     string // from the project root
	exports json.RawMessage
	entries []string // main, module, source and types
}

// resolveTS maps an import to the packages of the files it names, the way a
// bundler would: relative to the importer, through tsconfig paths and baseUrl,
// or as the name of a package of the project. With an extension appended, as
// a directory's index, or with a .js that stands for the .ts beside it. A file
// that re-exports hands over the packages of what it re-exports, so an import
// of a barrel reaches the decls behind it. Other bare specifiers name
// dependencies and resolve to nothing.
func resolveTS(abs, _ string, srcs []*source) func(*source, string) []string {
	files := map[string]*source{}
	dirSet := map[string]bool{}
	for _, s := range srcs {
		files[s.rel] = s
		for d := path.Dir(s.rel); !dirSet[d]; d = path.Dir(d) {
			dirSet[d] = true
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	configs := map[string]tsConfig{}
	workspace := map[string]tsPackage{}
	for _, d := range dirs {
		if c, ok := readTSConfig(abs, d); ok {
			configs[d] = c
		}
		if name, p, ok := readTSPackage(abs, d); ok && workspace[name].dir == "" {
			workspace[name] = p
		}
	}

	lookup := func(target string) *source {
		stem := strings.TrimSuffix(target, path.Ext(target))
		for _, base := range []string{target, path.Join(target, "index"), stem} {
			for _, e := range append([]string{""}, tsExts...) {
				if s := files[base+e]; s != nil {
					return s
				}
			}
		}
		return nil
	}
	first := func(targets []string) *source {
		for _, t := range targets {
			if s := lookup(t); s != nil {
				return s
			}
		}
		return nil
	}
	file := func(f *source, imp string) *source {
		if strings.HasPrefix(imp, ".") {
			return lookup(path.Join(path.Dir(f.rel), imp))
		}
		for d := path.Dir(f.rel); ; d = path.Dir(d) {
			if c, ok := configs[d]; ok {
				if s := first(c.targets(imp)); s != nil {
					return s
				}
				break
			}
			if d == "." {
				break
			}
		}
		name := ""
		for n := range workspace {
			if (imp == n || strings.HasPrefix(imp, n+"/")) && len(n) > len(name) {
				name = n
			}
		}
		if name == "" {
			return nil
		}
		return first(workspace[name].targets("." + strings.TrimPrefix(imp, name)))
	}
	return func(f *source, imp string) []string {
		var out []string
		seen := map[string]bool{}
		var follow func(s *source)
		follow = func(s *source) {
			if seen[s.rel] {
				return
			}
			seen[s.rel] = true
			out = append(out, s.pkg)
			for _, re := range s.reexports {
				if t := file(s, re); t != nil {
					follow(t)
				}
			}
		}
		if s := file(f, imp); s != nil {
			follow(s)
		}
		return out
	}
}

func readTSConfig(abs, dir string) (tsConfig, bool) {
	b, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(dir), "tsconfig.json"))
	if err != nil {
		return tsConfig{}, false
	}
	var tc struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	co := &tc.CompilerOptions
	if json.Unmarshal(stripJSONC(b), &tc) != nil || co.BaseURL == "" && len(co.Paths) == 0 {
		return tsConfig{}, false
	}
	return tsConfig{base: path.Join(dir, co.BaseURL), hasBase: co.BaseURL != "", paths: co.Paths}, true
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// stripJSONC drops the comments and trailing commas a tsconfig.json may hold.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '"':
			j := i + 1
			for j < len(b) && b[j] != '"' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(b))
			out = append(out, b[i:j]...)
			i = j - 1
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			if end := bytes.Index(b[i+2:], []byte("*/")); end < 0 {
				i = len(b)
			} else {
				i += end + 3
			}
		default:
			out = append(out, c)
		}
	}
	return trailingComma.ReplaceAll(out, []byte("$1"))
}

// targets lists the files, extension and index left off, that imp may name.
func (c tsConfig) targets(imp string) []string {
	var out []string
	best, star := -1, ""
	var pick []string
	for pat, ts := range c.paths {
		pre, suf, wild := strings.Cut(pat, "*")
		switch {
		case !wild && pat == imp:
			best, star, pick = len(imp)+1, "", ts
		case wild && strings.HasPrefix(imp, pre) && strings.HasSuffix(imp, suf) && len(imp) >= len(pre)+len(suf) && len(pre) > best:
			best, star, pick = len(pre), imp[len(pre):len(imp)-len(suf)], ts
		}
	}
	for _, t := range pick {
		out = append(out, path.Join(c.base, strings.Replace(t, "*", star, 1)))
	}
	if c.hasBase {
		out = append(out, path.Join(c.base, imp))
	}
	return out
}

func readTSPackage(abs, dir string) (string, tsPackage, bool) {
	b, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(dir), "package.json"))
	if err != nil {
		return "", tsPackage{}, false
	}
	var pj struct {
		Name                        string
		Exports                     json.RawMessage
		Main, Module, Source, Types string
	}
	if json.Unmarshal(b, &pj) != nil || pj.Name == "" {
		return "", tsPackage{}, false
	}
	var entries []string
	for _, e := range []string{pj.Source, pj.Module, pj.Main, pj.Types} {
		if e != "" {
			entries = append(entries, e)
		}
	}
	return pj.Name, tsPackage{dir: dir, exports: pj.Exports, entries: entries}, true
}

// buildDirs are where a package's exports point to build output, with the
// source of it in src.
var buildDirs = []string{"dist", "lib", "build", "out", "esm", "cjs"}

// targets lists the source files sub, "." or "./x", of the package may name:
// those its exports map points at, with the build directory read as src, and
// then the ones the path names directly.
func (p tsPackage) targets(sub string) []string {
	var out []string
	add := func(rel string) { out = append(out, path.Join(p.dir, rel)) }
	leaves := exportLeaves(p.exports, sub)
	if sub == "." {
		leaves = append(leaves, p.entries...)
	}
	for _, l := range leaves {
		l = strings.TrimPrefix(l, "./")
		for _, e := range []string{".d.mts", ".d.cts", ".d.ts"} {
			l = strings.TrimSuffix(l, e)
		}
		l = strings.TrimSuffix(l, path.Ext(l))
		add(l)
		if dir, rest, ok := strings.Cut(l, "/"); ok && slices.Contains(buildDirs, dir) {
			add("src/" + rest)
		}
	}
	if sub == "." {
		add("src/index")
		add("index")
	} else {
		add(sub)
		add("src/" + sub)
	}
	return out
}

// exportLeaves lists the file paths the exports field gives sub: a subpath
// key, a pattern key with one *, or the conditions of "." itself.
func exportLeaves(raw json.RawMessage, sub string) []string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	subpaths := false
	for k := range m {
		subpaths = subpaths || strings.HasPrefix(k, ".")
	}
	if !subpaths {
		if sub == "." {
			return leaves(v, "")
		}
		return nil
	}
	if t, ok := m[sub]; ok {
		return leaves(t, "")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if pre, suf, wild := strings.Cut(k, "*"); wild && strings.HasPrefix(sub, pre) && strings.HasSuffix(sub, suf) && len(sub) >= len(pre)+len(suf) {
			return leaves(m[k], sub[len(pre):len(sub)-len(suf)])
		}
	}
	return nil
}

// leaves lists the strings under v, a target or a tree of conditions, with
// star standing in for each *.
func leaves(v any, star string) []string {
	switch x := v.(type) {
	case string:
		return []string{strings.ReplaceAll(x, "*", star)}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, leaves(e, star)...)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			out = append(out, leaves(x[k], star)...)
		}
		return out
	}
	return nil
}
