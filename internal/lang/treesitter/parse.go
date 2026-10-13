package treesitter

import (
	"fmt"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// This file is the only one that imports the tree-sitter runtime.

// node is a syntax node with the grammar and source needed to read it. The
// zero node stands for a missing child.
type node struct {
	n    *gts.Node
	lang *gts.Language
	src  []byte
}

func (x node) ok() bool       { return x.n != nil }
func (x node) kind() string   { return x.n.Type(x.lang) }
func (x node) text() string   { return x.n.Text(x.src) }
func (x node) start() int     { return int(x.n.StartByte()) }
func (x node) end() int       { return int(x.n.EndByte()) }
func (x node) startLine() int { return int(x.n.StartPoint().Row) + 1 }
func (x node) endLine() int   { return int(x.n.EndPoint().Row) + 1 }
func (x node) named() bool    { return x.n.IsNamed() }
func (x node) broken() bool   { return x.n.HasError() }
func (x node) leaf() bool     { return x.n.ChildCount() == 0 }

// errorLine is the line of the first syntax error below x.
func (x node) errorLine() int {
	if !x.n.IsError() {
		for _, c := range x.children() {
			if c.broken() {
				return c.errorLine()
			}
		}
	}
	return x.startLine()
}

// errorBytes is how many bytes of x sit inside syntax-error nodes.
func (x node) errorBytes() int {
	if x.n.IsError() {
		return x.end() - x.start()
	}
	n := 0
	for _, c := range x.children() {
		if c.broken() {
			n += c.errorBytes()
		}
	}
	return n
}

func (x node) field(name string) node {
	return node{x.n.ChildByFieldName(name, x.lang), x.lang, x.src}
}

func (x node) children() []node {
	cs := x.n.Children()
	out := make([]node, len(cs))
	for i, c := range cs {
		out[i] = node{c, x.lang, x.src}
	}
	return out
}

// tree is a parsed file.
type tree struct {
	t    *gts.Tree
	root node
}

func (t tree) release() { t.t.Release() }

// parsers holds one parser per grammar.
type parsers struct {
	byGrammar map[string]*gts.Parser
	langs     map[string]*gts.Language
}

func newParsers() *parsers {
	return &parsers{byGrammar: map[string]*gts.Parser{}, langs: map[string]*gts.Language{}}
}

func (p *parsers) parse(grammar string, src []byte) (tree, error) {
	ps := p.byGrammar[grammar]
	if ps == nil {
		entry := grammars.DetectLanguageByName(grammar)
		if entry == nil {
			return tree{}, fmt.Errorf("no %s grammar", grammar)
		}
		p.langs[grammar] = entry.Language()
		ps = gts.NewParser(p.langs[grammar])
		p.byGrammar[grammar] = ps
	}
	t, err := ps.Parse(src)
	if err != nil {
		return tree{}, err
	}
	return tree{t, node{t.RootNode(), p.langs[grammar], src}}, nil
}
