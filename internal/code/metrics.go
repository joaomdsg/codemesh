package code

import (
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"hash/fnv"
	"io"
	"strings"
)

// codeLines returns the set of lines holding at least one token.
func codeLines(tf *token.File, src []byte) map[int]bool {
	lines := map[int]bool{}
	if tf == nil || src == nil {
		return lines
	}
	fset := token.NewFileSet()
	f := fset.AddFile(tf.Name(), -1, len(src))
	var s scanner.Scanner
	s.Init(f, src, nil, 0)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return lines
		}
		// The scanner inserts semicolons at line ends; they are not code.
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		first := f.Line(pos)
		for i := range strings.Count(lit, "\n") + 1 {
			lines[first+i] = true
		}
	}
}

func countIn(lines map[int]bool, start, end int) int {
	n := 0
	for l := start; l <= end; l++ {
		if lines[l] {
			n++
		}
	}
	return n
}

// complexity is McCabe's cyclomatic complexity: one plus each branch point.
func complexity(body *ast.BlockStmt) int {
	n := 1
	ast.Inspect(body, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			n++
		case *ast.CaseClause:
			if x.List != nil {
				n++
			}
		case *ast.CommClause:
			if x.Comm != nil {
				n++
			}
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				n++
			}
		}
		return true
	})
	return n
}

// nesting is the deepest chain of nested if/for/switch/select statements.
// An else-if continues its if rather than nesting inside it.
func nesting(body *ast.BlockStmt) int {
	type frame struct {
		n ast.Node
		d int
	}
	stack := []frame{{body, 0}}
	deepest := 0
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if n == body {
			return true
		}
		parent := stack[len(stack)-1]
		d := parent.d
		switch n.(type) {
		case *ast.IfStmt:
			if p, ok := parent.n.(*ast.IfStmt); !ok || p.Else != n {
				d++
			}
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			d++
		}
		deepest = max(deepest, d)
		stack = append(stack, frame{n, d})
		return true
	})
	return deepest
}

func paramCount(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, f := range fl.List {
		n += max(1, len(f.Names))
	}
	return n
}

// shape hashes a syntax tree's node kinds, names, literals and operators,
// so comment and layout edits leave it unchanged.
func shape(n ast.Node) string {
	h := fnv.New64a()
	ast.Inspect(n, func(n ast.Node) bool {
		if n == nil {
			io.WriteString(h, ")")
			return true
		}
		switch x := n.(type) {
		case *ast.CommentGroup, *ast.Comment:
			return false
		case *ast.Ident:
			fmt.Fprintf(h, "(id %s", x.Name)
		case *ast.BasicLit:
			fmt.Fprintf(h, "(lit %d %s", x.Kind, x.Value)
		case *ast.BinaryExpr:
			fmt.Fprintf(h, "(bin %s", x.Op)
		case *ast.UnaryExpr:
			fmt.Fprintf(h, "(un %s", x.Op)
		case *ast.AssignStmt:
			fmt.Fprintf(h, "(as %s", x.Tok)
		case *ast.IncDecStmt:
			fmt.Fprintf(h, "(inc %s", x.Tok)
		case *ast.BranchStmt:
			fmt.Fprintf(h, "(br %s", x.Tok)
		case *ast.RangeStmt:
			fmt.Fprintf(h, "(range %s", x.Tok)
		case *ast.ChanType:
			fmt.Fprintf(h, "(chan %d", x.Dir)
		case *ast.GenDecl:
			fmt.Fprintf(h, "(gen %s", x.Tok)
		default:
			fmt.Fprintf(h, "(%T", n)
		}
		return true
	})
	return hex.EncodeToString(h.Sum(nil))
}

// ShapeOf returns the Shape of the first declaration in a Go source file.
func ShapeOf(src string) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	if len(f.Decls) == 0 {
		return "", errors.New("no declaration")
	}
	return shape(f.Decls[0]), nil
}
