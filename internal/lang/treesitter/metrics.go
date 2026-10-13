package treesitter

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode"
)

// table names the node kinds one grammar's metrics and references read.
type table struct {
	branch  map[string]bool // each adds one to complexity
	logical map[string]bool // adds one when its operator is in ops
	ops     map[string]bool
	nest    map[string]bool // statements that nest

	plain, member, typ map[string]bool // identifier kinds, by the position they name
	typeCtx            map[string]bool // kinds whose descendants are type positions
	// via maps a kind that reaches a name through a receiver (obj.f, mod::f)
	// to its name and receiver fields. call maps such a kind to the call whose
	// function it must be to name a method: a Rust field read is no call.
	via  map[string][2]string
	call map[string]string

	// binds returns the patterns x introduces names in: a parameter, a let, a
	// loop variable. pattern lists the kinds a pattern nests names in, each with
	// the field of it that does not bind; names are the kinds that bind.
	binds   func(x node) []node
	pattern map[string]string
	names   map[string]bool
}

func set(kinds ...string) map[string]bool {
	m := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		m[k] = true
	}
	return m
}

// fields returns the named children of x that exist.
func fields(x node, names ...string) []node {
	var out []node
	for _, name := range names {
		if c := x.field(name); c.ok() {
			out = append(out, c)
		}
	}
	return out
}

func isComment(n node) bool { return strings.Contains(n.kind(), "comment") }

// each calls f on n and, while f returns true, on its descendants in order.
func each(n node, f func(node) bool) {
	if !f(n) {
		return
	}
	for _, c := range n.children() {
		each(c, f)
	}
}

// complexity is modified cyclomatic complexity: one plus each branch point,
// with a switch counting once however many cases it has.
func (t *table) complexity(fn node) int {
	n := 1
	each(fn, func(x node) bool {
		switch k := x.kind(); {
		case t.branch[k]:
			n++
		case t.logical[k]:
			if op := x.field("operator"); op.ok() && t.ops[op.text()] {
				n++
			}
		}
		return true
	})
	return n
}

// nesting is the deepest chain of nested branch statements. An else-if
// continues its if rather than nesting inside it.
func (t *table) nesting(n node, depth int) int {
	deepest := depth
	for _, c := range n.children() {
		d := depth
		if t.nest[c.kind()] && n.kind() != "else_clause" {
			d++
		}
		deepest = max(deepest, t.nesting(c, d))
	}
	return deepest
}

// shape hashes n's syntax tree: the kinds of its named nodes and the text of
// its tokens, so a changed operator, name or literal changes it. It is blind to
// comments and layout. A subtree rooted at a node in skip is left out.
func shape(n node, skip map[[2]int]bool) string {
	h := fnv.New64a()
	each(n, func(x node) bool {
		if isComment(x) || skip[[2]int{x.start(), x.end()}] {
			return false
		}
		switch {
		case len(x.children()) == 0:
			h.Write([]byte(x.kind() + "=" + x.text() + ";"))
		case x.named():
			h.Write([]byte(x.kind() + ";"))
		}
		return true
	})
	return fmt.Sprintf("%x", h.Sum64())
}

type refKind int

const (
	refPlain  refKind = iota // a bare name: not a method
	refMember                // reached through a receiver: a method, or a module's function
	refType                  // a type annotation
)

type recvKind int

const (
	recvNone    recvKind = iota // a bare name
	recvSelf                    // this, self, Self: the enclosing type
	recvType                    // a local of a known type
	recvName                    // a type or module named in place
	recvUnknown                 // an expression or an untyped local: says nothing
)

type ref struct {
	name string
	kind refKind
	recv recvKind
	on   string // the type or module recv names
}

var pathRe = regexp.MustCompile(`^[A-Za-z_]\w*((\.|::)[A-Za-z_]\w*)*$`)

// receiver says what r, the receiver of a member ref under a node of kind
// parent, names. A dotted path names its last segment, unless it starts at
// self or at a local.
func receiver(r node, parent string, locals map[string]string) (recvKind, string) {
	t := r.text()
	if !pathRe.MatchString(t) {
		return recvUnknown, ""
	}
	segs := strings.FieldsFunc(t, func(c rune) bool { return c == '.' || c == ':' })
	first, last := segs[0], segs[len(segs)-1]
	switch first {
	case "this", "self", "cls", "Self":
		switch {
		case first == "self" && strings.HasPrefix(parent, "scoped_"): // Rust's self::f is a module path
			return recvName, last
		case len(segs) > 1:
			return recvUnknown, ""
		}
		return recvSelf, ""
	}
	if typ, ok := locals[first]; ok {
		if len(segs) > 1 || typ == "" {
			return recvUnknown, ""
		}
		return recvType, typ
	}
	return recvName, last
}

// locals returns the names bound inside n, each with the type it is
// declared or built as when the binding says, leaving out the subtrees rooted
// at nodes in skip. A bare name that one of them binds refers to it, not to a
// decl of the same name elsewhere.
func (t *table) locals(n node, skip map[[2]int]bool) map[string]string {
	out := map[string]string{}
	var pattern func(p node, typ string)
	pattern = func(p node, typ string) {
		switch k := p.kind(); {
		case t.names[k]:
			out[p.text()] = typ
		default:
			if f, ok := t.pattern[k]; ok {
				typ = ""
				if k == "typed_parameter" { // Python's x: T
					typ = typeName(p)
				}
				for _, c := range p.children() {
					if !same(p.field(f), c) {
						pattern(c, typ)
					}
				}
			}
		}
	}
	each(n, func(x node) bool {
		if isComment(x) || !same(x, n) && skip[[2]int{x.start(), x.end()}] {
			return false
		}
		if t.binds != nil && !same(x, n) {
			for _, p := range t.binds(x) {
				pattern(p, typeName(x))
			}
		}
		return true
	})
	return out
}

// typeName is the type a binding x declares, as in x: T, or builds, as in
// new T(), T(), T::new() or T { .. }; "" when it says neither.
func typeName(x node) string {
	if ty := x.field("type"); ty.ok() {
		var name string
		each(ty, func(c node) bool {
			if name == "" && (c.kind() == "type_identifier" || c.kind() == "identifier") {
				name = c.text()
			}
			return name == ""
		})
		return name
	}
	v := x.field("value")
	if !v.ok() {
		v = x.field("right")
	}
	switch v.kind() {
	case "new_expression":
		return v.field("constructor").text()
	case "struct_expression":
		return v.field("name").text()
	case "call", "call_expression":
		f := v.field("function")
		if f.kind() == "scoped_identifier" {
			f = f.field("path")
		}
		if t := f.text(); f.kind() != "" && pathRe.MatchString(t) {
			t = t[strings.LastIndexAny(t, ".:")+1:]
			if t != "" && unicode.IsUpper(rune(t[0])) {
				return t
			}
		}
	}
	return ""
}

// refs lists the identifiers under n, in order, leaving out the subtrees
// rooted at nodes in skip: other decls that sit inside n. The token that names
// n is not a reference, or a method would call the others of its name.
func (t *table) refs(n node, skip map[[2]int]bool) []ref {
	var out []ref
	own := n.field("name")
	if !own.ok() {
		own = n.field("left") // a Python assignment
	}
	locals := t.locals(n, skip)
	var walk func(x, parent, grand node, inType bool)
	walk = func(x, parent, grand node, inType bool) {
		if isComment(x) || same(own, x) || (parent.ok() && skip[[2]int{x.start(), x.end()}]) {
			return
		}
		k := x.kind()
		inType = inType || t.typeCtx[k]
		if t.typ[k] || t.member[k] || t.plain[k] {
			kind := refPlain
			if t.typ[k] || inType {
				kind = refType
			}
			switch v, ok := t.via[parent.kind()]; {
			case ok && same(parent.field(v[0]), x):
				if c, ok := t.call[parent.kind()]; ok && kind != refType && !(grand.kind() == c && same(grand.field("function"), parent)) {
					break
				}
				if kind == refPlain {
					kind = refMember
				}
				recv, on := receiver(parent.field(v[1]), parent.kind(), locals)
				out = append(out, ref{x.text(), kind, recv, on})
			case t.member[k]: // a key or a field declared: no receiver, no call
			case kind == refType:
				out = append(out, ref{name: x.text(), kind: refType})
			default:
				if _, local := locals[x.text()]; !local {
					out = append(out, ref{name: x.text(), kind: refPlain})
				}
			}
		}
		for _, c := range x.children() {
			walk(c, x, parent, inType)
		}
	}
	walk(n, node{}, node{}, false)
	return out
}

func same(a, b node) bool { return a.ok() && a.start() == b.start() && a.end() == b.end() }

// codeLines returns the set of lines holding a token that is not part of a
// comment.
func codeLines(root node) map[int]bool {
	lines := map[int]bool{}
	each(root, func(x node) bool {
		if isComment(x) {
			return false
		}
		if x.leaf() {
			for l := x.startLine(); l <= x.endLine(); l++ {
				lines[l] = true
			}
		}
		return true
	})
	return lines
}

var generatedMark = regexp.MustCompile(`(?i)@generated|code generated|do not edit|auto-?generated`)

// generated reports whether the first lines of src say a tool wrote the file.
func generated(src []byte) bool {
	head := src[:min(len(src), 512)]
	lines := strings.SplitN(string(head), "\n", 6)
	return generatedMark.MatchString(strings.Join(lines[:min(len(lines), 5)], "\n"))
}

var deprecatedMark = regexp.MustCompile(`@(?:\w+\.)*deprecated\b|#\[deprecated\b`)

// norm collapses runs of whitespace, so reformatting a signature does not
// change it.
func norm(s string) string { return strings.Join(strings.Fields(s), " ") }
