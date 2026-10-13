package treesitter

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
)

// table names the node kinds one grammar's metrics and references read.
type table struct {
	branch  map[string]bool // each adds one to complexity
	logical map[string]bool // adds one when its operator is in ops
	ops     map[string]bool
	nest    map[string]bool // statements that nest

	plain, member, typ map[string]bool   // identifier kinds, by the position they name
	typeCtx            map[string]bool   // kinds whose descendants are type positions
	memberOf           map[string]string // kind → the field holding a name reached through a path or attribute
}

func set(kinds ...string) map[string]bool {
	m := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		m[k] = true
	}
	return m
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

// shape hashes the kinds of n's named nodes, blind to comments, layout and
// names. A subtree rooted at a node in skip is left out.
func shape(n node, skip map[[2]int]bool) string {
	h := fnv.New64a()
	each(n, func(x node) bool {
		if isComment(x) || skip[[2]int{x.start(), x.end()}] {
			return false
		}
		if x.named() {
			h.Write([]byte(x.kind() + ";"))
		}
		return true
	})
	return fmt.Sprintf("%x", h.Sum64())
}

type refKind int

const (
	refPlain  refKind = iota // a bare name: not a method
	refMember                // reached through a path or attribute: a method or a function
	refType                  // a type annotation
)

type ref struct {
	name string
	kind refKind
}

// refs lists the identifiers under n, in order, leaving out the subtrees
// rooted at nodes in skip: other decls that sit inside n.
func (t *table) refs(n node, skip map[[2]int]bool) []ref {
	var out []ref
	var walk func(x, parent node, inType bool)
	walk = func(x, parent node, inType bool) {
		if isComment(x) || (parent.ok() && skip[[2]int{x.start(), x.end()}]) {
			return
		}
		k := x.kind()
		inType = inType || t.typeCtx[k]
		switch {
		case t.typ[k]:
			out = append(out, ref{x.text(), refType})
		case t.member[k]:
			out = append(out, ref{x.text(), refMember})
		case t.plain[k]:
			switch f := t.memberOf[parent.kind()]; {
			case inType:
				out = append(out, ref{x.text(), refType})
			case f != "" && same(parent.field(f), x):
				out = append(out, ref{x.text(), refMember})
			default:
				out = append(out, ref{x.text(), refPlain})
			}
		}
		for _, c := range x.children() {
			walk(c, x, inType)
		}
	}
	walk(n, node{}, false)
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
