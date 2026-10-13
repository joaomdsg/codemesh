package treesitter

import (
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
)

var python = language{
	lang:     "python",
	exts:     []string{".py"},
	projects: []string{"pyproject.toml", "setup.py"},
	module:   filepath.Base,
	grammar: func(rel string) string {
		if path.Ext(rel) == ".py" {
			return "python"
		}
		return ""
	},
	test:    pyTest,
	pkg:     pyPkg,
	extract: extractPy,
	resolve: single(resolvePy),
	params:  pyParams,
	tbl: table{
		branch: set("if_statement", "elif_clause", "for_statement", "while_statement", "except_clause",
			"conditional_expression", "match_statement", "if_clause", "for_in_clause"),
		logical:  set("boolean_operator"),
		ops:      set("and", "or"),
		nest:     set("if_statement", "for_statement", "while_statement", "try_statement", "match_statement"),
		plain:    set("identifier"),
		member:   set(),
		typ:      set(),
		typeCtx:  set("type"),
		memberOf: map[string]string{"attribute": "attribute"},
		binds:    pyBinds,
		pattern: map[string]string{"pattern_list": "", "tuple_pattern": "", "list_pattern": "", "typed_parameter": "",
			"list_splat_pattern": "", "dictionary_splat_pattern": "", "as_pattern_target": ""},
		names: set("identifier"),
	},
}

func pyBinds(x node) []node {
	switch x.kind() {
	case "assignment", "augmented_assignment", "for_statement", "for_in_clause":
		return fields(x, "left")
	case "named_expression":
		return fields(x, "name")
	case "as_pattern":
		return fields(x, "alias")
	case "parameters", "lambda_parameters":
		var out []node
		for _, c := range x.children() {
			if k := c.kind(); k == "default_parameter" || k == "typed_default_parameter" {
				out = append(out, fields(c, "name")...)
			} else {
				out = append(out, c)
			}
		}
		return out
	}
	return nil
}

func pyTest(rel string) bool {
	base := path.Base(rel)
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py" ||
		slices.ContainsFunc(strings.Split(path.Dir(rel), "/"), func(s string) bool { return s == "tests" || s == "test" })
}

// pyDotted is the dotted name of the package directory holding rel, with a
// leading src directory dropped; "" for the root.
func pyDotted(rel string) string {
	dir := path.Dir(rel)
	if dir == "." || dir == "src" {
		return ""
	}
	return strings.ReplaceAll(strings.TrimPrefix(dir, "src/"), "/", ".")
}

func pyPkg(module, rel string) (string, string, string) {
	d := pyDotted(rel)
	if d == "" {
		return module, path.Dir(rel), module
	}
	return d, path.Dir(rel), d[strings.LastIndex(d, ".")+1:]
}

func extractPy(f *source) []rawDecl {
	var out []rawDecl
	items := f.root.children()
	for i, it := range items {
		switch it.kind() {
		case "import_statement":
			for _, c := range it.children() {
				switch c.kind() {
				case "dotted_name":
					f.imports = append(f.imports, c.text())
				case "aliased_import":
					f.imports = append(f.imports, fieldText(c, "name"))
				}
			}
		case "import_from_statement":
			mod := it.field("module_name")
			f.imports = append(f.imports, mod.text())
			for _, c := range it.children() {
				name := c.text()
				switch {
				case same(c, mod):
					continue
				case c.kind() == "aliased_import":
					name = fieldText(c, "name")
				case c.kind() != "dotted_name":
					continue
				}
				if strings.HasSuffix(mod.text(), ".") {
					f.imports = append(f.imports, mod.text()+name)
				} else {
					f.imports = append(f.imports, mod.text()+"."+name)
				}
			}
		case "function_definition", "class_definition", "decorated_definition":
			out = append(out, pyDecl(items, i, it, "", true)...)
		case "expression_statement", "assignment":
			a := it
			if it.kind() == "expression_statement" {
				a = node{}
				for _, c := range it.children() {
					if c.kind() == "assignment" {
						a = c
					}
				}
			}
			if !a.ok() || a.kind() != "assignment" {
				continue
			}
			name := fieldText(a, "left")
			if name == strings.ToUpper(name) && strings.ContainsAny(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && a.field("left").kind() == "identifier" {
				lead, leadText := leading(items, i, isComment)
				out = append(out, rawDecl{name: name, kind: code.Const, exported: !strings.HasPrefix(name, "_"),
					outer: it, node: a, lead: lead, leadText: leadText})
			}
		}
	}
	return out
}

// pyDecl lists the decls of items[i]: a function, or a class and its methods.
// owner is the class name when items are a class body.
func pyDecl(items []node, i int, outer node, owner string, ownerExported bool) []rawDecl {
	def, decorators := outer, ""
	if outer.kind() == "decorated_definition" {
		def = outer.field("definition")
		for _, c := range outer.children() {
			if c.kind() == "decorator" {
				decorators += c.text() + "\n"
			}
		}
	}
	name := fieldText(def, "name")
	lead, leadText := leading(items, i, isComment)
	r := rawDecl{name: name, kind: code.Func, outer: outer, node: outer, lead: lead, leadText: leadText + "\n" + decorators}
	if owner != "" {
		r.name, r.kind = owner+"."+name, code.Method
	}
	r.exported = ownerExported && !strings.HasPrefix(name, "_")
	switch def.kind() {
	case "function_definition":
		r.fn = def
		return []rawDecl{r}
	case "class_definition":
		if owner != "" {
			return nil
		}
		r.kind = code.Type
		out := []rawDecl{r}
		members := def.field("body").children()
		for j, m := range members {
			if k := m.kind(); k == "function_definition" || k == "decorated_definition" {
				out = append(out, pyDecl(members, j, m, name, r.exported)...)
			}
		}
		return out
	}
	return nil
}

func pyParams(fn node, method bool) (int, string) {
	p := fn.field("parameters")
	n := 0
	for _, c := range p.children() {
		if !c.named() || isComment(c) {
			continue
		}
		if n++; method && n == 1 && (c.text() == "self" || c.text() == "cls") {
			n--
			method = false
		}
	}
	sig := p.text()
	if r := fn.field("return_type"); r.ok() {
		sig += " -> " + r.text()
	}
	return n, norm(sig)
}

// resolvePy maps an import, absolute or relative, to the package that holds
// the module or package it names.
func resolvePy(_ string, srcs []*source) func(*source, string) string {
	names := map[string]string{} // dotted package or module name → package path
	for _, s := range srcs {
		d := pyDotted(s.rel)
		names[d] = s.pkg
		if stem := strings.TrimSuffix(path.Base(s.rel), ".py"); stem != "__init__" {
			names[strings.TrimPrefix(d+"."+stem, ".")] = s.pkg
		}
	}
	return func(f *source, imp string) string {
		if level := len(imp) - len(strings.TrimLeft(imp, ".")); level > 0 {
			var parts []string
			if d := pyDotted(f.rel); d != "" {
				parts = strings.Split(d, ".")
			}
			if level-1 > len(parts) {
				return ""
			}
			parts = parts[:len(parts)-(level-1)]
			if rest := imp[level:]; rest != "" {
				parts = append(parts, rest)
			}
			imp = strings.Join(parts, ".")
		}
		return names[imp]
	}
}
