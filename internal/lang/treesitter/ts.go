package treesitter

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
)

var tsExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

var typescript = language{
	lang:     "typescript",
	exts:     tsExts,
	projects: []string{"package.json", "tsconfig.json"},
	module:   tsModule,
	grammar:  tsGrammar,
	test:     tsTest,
	pkg:      tsPkg,
	extract:  extractTS,
	resolve:  resolveTS,
	params:   tsParams,
	tbl: table{
		branch: set("if_statement", "for_statement", "for_in_statement", "while_statement", "do_statement",
			"switch_statement", "catch_clause", "ternary_expression"),
		logical: set("binary_expression"),
		ops:     set("&&", "||", "??"),
		nest:    set("if_statement", "for_statement", "for_in_statement", "while_statement", "do_statement", "switch_statement", "try_statement"),
		plain:   set("identifier"),
		member:  set("property_identifier"),
		typ:     set("type_identifier"),
		typeCtx: set(),
		via:     map[string][2]string{"member_expression": {"property", "object"}, "nested_type_identifier": {"name", "module"}},
		binds:   tsBinds,
		pattern: map[string]string{"object_pattern": "", "array_pattern": "", "rest_pattern": "", "pair_pattern": "key",
			"assignment_pattern": "right", "object_assignment_pattern": "right"},
		names: set("identifier", "shorthand_property_identifier_pattern"),
	},
}

func tsBinds(x node) []node {
	switch x.kind() {
	case "required_parameter", "optional_parameter":
		return fields(x, "pattern")
	case "variable_declarator":
		return fields(x, "name")
	case "arrow_function":
		return fields(x, "parameter")
	case "for_in_statement":
		return fields(x, "left")
	case "catch_clause":
		return fields(x, "parameter")
	case "formal_parameters": // plain JavaScript has no required_parameter
		return x.children()
	}
	return nil
}

func tsModule(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pj struct{ Name string }
		if json.Unmarshal(b, &pj) == nil && pj.Name != "" {
			return pj.Name
		}
	}
	return filepath.Base(dir)
}

func tsGrammar(rel string) string {
	switch {
	case strings.HasSuffix(rel, ".d.ts"), strings.HasSuffix(rel, ".d.mts"), strings.HasSuffix(rel, ".d.cts"):
		return ""
	case path.Ext(rel) == ".tsx":
		return "tsx"
	case slices.Contains([]string{".ts", ".mts", ".cts"}, path.Ext(rel)):
		return "typescript"
	case slices.Contains(tsExts, path.Ext(rel)):
		return "javascript"
	}
	return ""
}

var tsTestName = regexp.MustCompile(`\.(test|spec)\.[cm]?[jt]sx?$`)

func tsTest(rel string) bool {
	return tsTestName.MatchString(rel) || slices.Contains(strings.Split(rel, "/"), "__tests__")
}

func tsPkg(module, rel string) (string, string, string) {
	dir := path.Dir(rel)
	if dir == "." {
		return module, ".", path.Base(module)
	}
	return module + "/" + dir, dir, path.Base(dir)
}

func isFunctionValue(n node) bool {
	return n.ok() && slices.Contains([]string{"arrow_function", "function_expression", "function"}, n.kind())
}

func extractTS(f *source) []rawDecl {
	var out []rawDecl
	exported := map[string]bool{}
	items := f.root.children()
	for i, it := range items {
		switch it.kind() {
		case "import_statement":
			f.imports = append(f.imports, tsSpecifier(it))
		case "export_statement":
			if d := it.field("declaration"); d.ok() {
				out = append(out, tsDecl(items, i, d, true)...)
				continue
			}
			if it.field("source").ok() {
				f.imports = append(f.imports, tsSpecifier(it))
				f.reexports = append(f.reexports, tsSpecifier(it))
			}
			each(it, func(x node) bool {
				if x.kind() == "export_specifier" {
					exported[fieldText(x, "name")] = true
				}
				return true
			})
		default:
			out = append(out, tsDecl(items, i, it, false)...)
		}
	}
	for i, r := range out {
		if exported[r.name] && r.kind != code.Method {
			out[i].exported = true
		}
	}
	return out
}

// tsSpecifier returns the module an import or re-export names, unquoted.
func tsSpecifier(n node) string { return strings.Trim(fieldText(n, "source"), "\"'`") }

// tsDecl lists the decls of items[i]; d is its declaration, inside the export
// statement when there is one.
func tsDecl(items []node, i int, d node, exported bool) []rawDecl {
	outer := items[i]
	lead, leadText := leading(items, i, isComment)
	one := func(name string, kind code.Kind, fn node) rawDecl {
		return rawDecl{name: name, kind: kind, exported: exported, outer: outer, node: d, fn: fn, lead: lead, leadText: leadText}
	}
	name := fieldText(d, "name")
	if name == "" {
		name = "default"
	}
	switch d.kind() {
	case "function_declaration", "generator_function_declaration":
		return []rawDecl{one(name, code.Func, d)}
	case "interface_declaration", "enum_declaration", "type_alias_declaration":
		return []rawDecl{one(name, code.Type, node{})}
	case "class_declaration", "abstract_class_declaration":
		out := []rawDecl{one(name, code.Type, node{})}
		members := d.field("body").children()
		for j, m := range members {
			var fn node
			switch m.kind() {
			case "method_definition":
				fn = m
			case "public_field_definition":
				if v := m.field("value"); isFunctionValue(v) {
					fn = v
				}
			}
			if !fn.ok() {
				continue
			}
			mlead, mtext := leading(members, j, isComment)
			out = append(out, rawDecl{name: name + "." + fieldText(m, "name"), kind: code.Method, exported: exported && !tsPrivate(m),
				outer: m, node: m, fn: fn, lead: mlead, leadText: mtext})
		}
		return out
	case "lexical_declaration", "variable_declaration":
		var out []rawDecl
		var declarators []node
		for _, c := range d.children() {
			if c.kind() == "variable_declarator" && c.field("name").kind() == "identifier" {
				declarators = append(declarators, c)
			}
		}
		for _, c := range declarators {
			r := rawDecl{name: fieldText(c, "name"), kind: code.Var, exported: exported, outer: outer, node: c, lead: lead, leadText: leadText}
			if len(declarators) > 1 {
				r.outer, r.lead, r.leadText = c, 0, ""
			}
			if v := c.field("value"); isFunctionValue(v) {
				r.kind, r.fn = code.Func, v
			} else if d.kind() == "lexical_declaration" && strings.HasPrefix(d.text(), "const") {
				r.kind = code.Const
			}
			out = append(out, r)
		}
		return out
	}
	return nil
}

func tsPrivate(m node) bool {
	if strings.HasPrefix(fieldText(m, "name"), "#") {
		return true
	}
	for _, c := range m.children() {
		if c.kind() == "accessibility_modifier" && c.text() != "public" {
			return true
		}
	}
	return false
}

func tsParams(fn node, _ bool) (int, string) {
	p := fn.field("parameters")
	if !p.ok() {
		if one := fn.field("parameter"); one.ok() {
			return 1, norm(one.text())
		}
		return 0, ""
	}
	n := 0
	for _, c := range p.children() {
		if c.named() && !isComment(c) {
			n++
		}
	}
	sig := p.text()
	if r := fn.field("return_type"); r.ok() {
		sig += r.text()
	}
	return n, norm(sig)
}
