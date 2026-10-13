package treesitter

import (
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
)

var rust = language{
	lang:     "rust",
	exts:     []string{".rs"},
	projects: []string{"Cargo.toml"},
	module:   filepath.Base,
	grammar: func(rel string) string {
		if path.Ext(rel) == ".rs" {
			return "rust"
		}
		return ""
	},
	test: func(rel string) bool { return slices.Contains(strings.Split(rel, "/"), "tests") },
	pkg:  rsPkg,
	extract: func(f *source) []rawDecl {
		return rsItems(f, f.root.children(), false)
	},
	resolve: single(resolveRS),
	params:  rsParams,
	tbl: table{
		branch:  set("if_expression", "for_expression", "while_expression", "loop_expression", "match_expression"),
		logical: set("binary_expression"),
		ops:     set("&&", "||"),
		nest:    set("if_expression", "for_expression", "while_expression", "loop_expression", "match_expression"),
		plain:   set("identifier"),
		member:  set("field_identifier"),
		typ:     set("type_identifier"),
		typeCtx: set(),
		via: map[string][2]string{"field_expression": {"field", "value"}, "scoped_identifier": {"name", "path"},
			"scoped_type_identifier": {"name", "path"}},
		call:  map[string]string{"field_expression": "call_expression"},
		binds: rsBinds,
		pattern: map[string]string{"tuple_pattern": "", "tuple_struct_pattern": "type", "struct_pattern": "type", "field_pattern": "",
			"mut_pattern": "", "ref_pattern": "", "reference_pattern": "", "slice_pattern": "", "or_pattern": "", "captured_pattern": ""},
		names: set("identifier", "shorthand_field_identifier"),
	},
}

func rsBinds(x node) []node {
	switch x.kind() {
	case "let_declaration", "parameter", "for_expression", "let_condition":
		return fields(x, "pattern")
	case "closure_parameters":
		return x.children()
	}
	return nil
}

// rsPkg gives each file the module path Rust gives it, crate::a::b for
// src/a/b.rs. Files outside any src directory count as the crate's too.
func rsPkg(_, rel string) (string, string, string) {
	segs := strings.Split(strings.TrimSuffix(rel, ".rs"), "/")
	crate := "crate"
	if i := slices.Index(segs, "src"); i >= 0 {
		if i > 0 {
			crate = segs[i-1]
		}
		segs = segs[i+1:]
	}
	if n := len(segs); n > 0 && slices.Contains([]string{"lib", "main", "mod"}, segs[n-1]) {
		segs = segs[:n-1]
	}
	p := strings.Join(append([]string{crate}, segs...), "::")
	return p, path.Dir(rel), p[strings.LastIndex(p, ":")+1:]
}

func isRustLead(n node) bool { return n.kind() == "attribute_item" || isComment(n) }

// rsVis reports whether n is bare pub, and whether other modules of the crate
// can see it: pub(crate), pub(super) and pub(in path) reach them, pub(self)
// does not.
func rsVis(n node) (exported, crate bool) {
	for _, c := range n.children() {
		if c.kind() == "visibility_modifier" {
			t := c.text()
			return t == "pub", t != "pub(self)"
		}
	}
	return false, false
}

// rsItems lists the decls of items, those of an inline module included.
func rsItems(f *source, items []node, test bool) []rawDecl {
	var out []rawDecl
	for i, it := range items {
		lead, leadText := leading(items, i, isRustLead)
		test := test || strings.Contains(leadText, "cfg(test)") || strings.Contains(leadText, "#[test]")
		pub, crate := rsVis(it)
		decl := func(name string, kind code.Kind, fn node) rawDecl {
			return rawDecl{name: name, kind: kind, exported: pub, crate: crate, test: test, outer: it, node: it, fn: fn, lead: lead, leadText: leadText}
		}
		name := fieldText(it, "name")
		switch it.kind() {
		case "use_declaration":
			f.imports = append(f.imports, rsUse(it.field("argument"), "")...)
		case "mod_item":
			if body := it.field("body"); body.ok() {
				out = append(out, rsItems(f, body.children(), test)...)
			}
		case "function_item":
			out = append(out, decl(name, code.Func, it))
		case "struct_item", "enum_item", "union_item", "type_item":
			out = append(out, decl(name, code.Type, node{}))
		case "const_item", "static_item":
			out = append(out, decl(name, code.Const, node{}))
		case "trait_item":
			out = append(out, decl(name, code.Type, node{}))
			out = append(out, rsMethods(it, name, pub, crate, test)...)
		case "impl_item":
			ty := it.field("type")
			if g := ty.field("type"); g.ok() {
				ty = g
			}
			traitImpl := it.field("trait").ok()
			out = append(out, rsMethods(it, strings.TrimSpace(ty.text()), traitImpl, traitImpl, test)...)
		}
	}
	return out
}

// rsMethods lists the functions in the body of an impl or trait. exported
// and crate say whether every one is: in a trait impl or a public trait they
// are exported, in a trait impl or a crate-visible trait they are visible.
func rsMethods(owner node, name string, exported, crate, test bool) []rawDecl {
	var out []rawDecl
	members := owner.field("body").children()
	for j, m := range members {
		if k := m.kind(); k != "function_item" && k != "function_signature_item" {
			continue
		}
		lead, leadText := leading(members, j, isRustLead)
		pub, vis := rsVis(m)
		out = append(out, rawDecl{name: name + "." + fieldText(m, "name"), kind: code.Method, exported: exported || pub, crate: crate || vis,
			test: test || strings.Contains(leadText, "#[test]"), outer: m, node: m, fn: m, lead: lead, leadText: leadText})
	}
	return out
}

// rsUse expands a use tree into the full paths it imports.
func rsUse(n node, prefix string) []string {
	if !n.ok() {
		return nil
	}
	switch n.kind() {
	case "scoped_use_list":
		prefix += fieldText(n, "path") + "::"
		return rsUse(n.field("list"), prefix)
	case "use_list":
		var out []string
		for _, c := range n.children() {
			if c.named() {
				out = append(out, rsUse(c, prefix)...)
			}
		}
		return out
	case "use_as_clause":
		return rsUse(n.field("path"), prefix)
	case "use_wildcard":
		return []string{prefix + strings.TrimSuffix(n.text(), "::*")}
	}
	return []string{prefix + n.text()}
}

func rsParams(fn node, _ bool) (int, string) {
	p := fn.field("parameters")
	n := 0
	for _, c := range p.children() {
		if c.named() && !isComment(c) && c.kind() != "self_parameter" {
			n++
		}
	}
	sig := p.text()
	if r := fn.field("return_type"); r.ok() {
		sig += " -> " + r.text()
	}
	return n, norm(sig)
}

// resolveRS maps a use path to the longest module path that is a package of
// the crate, trying the path as written and then relative to the file's own
// module, as a bare `use child::x` is.
func resolveRS(_ string, srcs []*source) func(*source, string) string {
	pkgs := map[string]bool{}
	for _, s := range srcs {
		pkgs[s.pkg] = true
	}
	return func(f *source, imp string) string {
		cur := strings.Split(f.pkg, "::")
		segs := strings.Split(imp, "::")
		var base []string // what the path is relative to
		switch segs[0] {
		case "crate":
			segs = segs[1:]
			base = cur[:1]
		case "self":
			segs = segs[1:]
			base = cur
		case "super":
			base = cur
			for len(segs) > 0 && segs[0] == "super" {
				if len(base) <= 1 {
					return ""
				}
				segs, base = segs[1:], base[:len(base)-1]
			}
		}
		if base != nil {
			return longest(pkgs, base, segs, 0)
		}
		if p := longest(pkgs, cur, segs, len(cur)); p != "" {
			return p
		}
		return longest(pkgs, cur[:1], segs, 1)
	}
}

// longest returns the longest package path of base plus a prefix of segs that
// is longer than floor segments, "" for none.
func longest(pkgs map[string]bool, base, segs []string, floor int) string {
	for n := len(segs); n >= 0; n-- {
		p := append(slices.Clone(base), segs[:n]...)
		if len(p) > floor && pkgs[strings.Join(p, "::")] {
			return strings.Join(p, "::")
		}
	}
	return ""
}
