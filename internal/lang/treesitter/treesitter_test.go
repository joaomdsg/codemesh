package treesitter_test

import (
	"context"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/lang/treesitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func load(t *testing.T, f treesitter.Frontend, dir string) *code.Snapshot {
	t.Helper()
	s, err := f.Load(context.Background(), dir)
	require.NoError(t, err)
	return s
}

func decl(t *testing.T, s *code.Snapshot, id string) *code.Decl {
	t.Helper()
	d := s.Decl(id)
	require.NotNil(t, d, id)
	return d
}

func TestDetect_looksForTheLanguagesProjectFile(t *testing.T) {
	t.Parallel()

	assert.True(t, treesitter.TypeScript.Detect("testdata/ts"))
	assert.True(t, treesitter.Python.Detect("testdata/py"))
	assert.True(t, treesitter.Rust.Detect("testdata/rs"))
	assert.False(t, treesitter.TypeScript.Detect("testdata/py"))
	assert.False(t, treesitter.Python.Detect("testdata/rs"))
	assert.False(t, treesitter.Rust.Detect("testdata/ts"))
}

func TestLoadTypeScript_mapsDeclsMetricsAndCallersAcrossFiles(t *testing.T) {
	t.Parallel()
	s := load(t, treesitter.TypeScript, "testdata/ts")

	assert.Equal(t, code.Lang("typescript"), s.Lang)
	assert.Equal(t, "shop", s.Module)

	total := decl(t, s, "shop/src.total")
	assert.Equal(t, code.Func, total.Kind)
	assert.True(t, total.Exported)
	assert.Equal(t, 4, total.Start, "the doc comment is part of it")
	assert.Equal(t, 13, total.End)
	assert.Equal(t, 9, total.Lines)
	assert.Equal(t, 4, total.Complexity, "one for, one if and one &&")
	assert.Equal(t, 2, total.Nesting)
	assert.Equal(t, 1, total.Params)
	assert.Equal(t, "(items: Item[]): number", total.Signature)
	assert.Equal(t, []string{"shop/src.Cart.add"}, total.Callers)

	assert.False(t, decl(t, s, "shop/src.unused").Exported)
	assert.Empty(t, decl(t, s, "shop/src.unused").Callers, "util imports shop/src but cannot see a private function")
	grade := decl(t, s, "shop/src.grade")
	assert.Equal(t, 3, grade.Complexity)
	assert.Equal(t, 1, grade.Nesting, "an else-if continues its if")
	assert.Equal(t, code.Type, decl(t, s, "shop/src.Cart").Kind)
	assert.True(t, decl(t, s, "shop/src.Cart.add").Exported)
	assert.Equal(t, code.Method, decl(t, s, "shop/src.Cart.add").Kind)
	assert.False(t, decl(t, s, "shop/src.Cart.reset").Exported, "private")
	assert.Equal(t, code.Const, decl(t, s, "shop/src.LIMIT").Kind)

	price := decl(t, s, "shop/src/util.price")
	assert.Equal(t, code.Func, price.Kind, "an arrow function bound to a const")
	assert.True(t, price.Deprecated)
	assert.Equal(t, 3, price.Start)
	assert.Equal(t, 2, price.Complexity, "the ternary")
	assert.Equal(t, []string{"shop/src.total"}, price.Callers)
	assert.Contains(t, decl(t, s, "shop/src.Item").Users, "shop/src/util.price", "a parameter type")

	assert.Equal(t, []string{"shop/src/util"}, s.Package("shop/src").Imports)
	assert.Equal(t, []string{"shop/src"}, s.Package("shop/src/util").Imports)
	var tests []string
	for _, f := range s.Package("shop/src").Files {
		if f.Test {
			tests = append(tests, f.Path)
		}
	}
	assert.Equal(t, []string{"src/cart.test.ts"}, tests)
}

func TestLoadPython_mapsDeclsMetricsAndCallersAcrossFiles(t *testing.T) {
	t.Parallel()
	s := load(t, treesitter.Python, "testdata/py")

	total := decl(t, s, "app.total")
	assert.Equal(t, code.Func, total.Kind)
	assert.True(t, total.Exported)
	assert.Equal(t, 5, total.Start)
	assert.Equal(t, 11, total.End)
	assert.Equal(t, 7, total.Lines, "the docstring counts")
	assert.Equal(t, 4, total.Complexity)
	assert.Equal(t, 2, total.Nesting)
	assert.Equal(t, 1, total.Params)
	assert.Equal(t, []string{"app.Cart.add", "app.test_total"}, total.Callers, "a caller in another file, tests included")

	assert.False(t, decl(t, s, "app._unused").Exported, "leading underscore")
	add := decl(t, s, "app.Cart.add")
	assert.Equal(t, code.Method, add.Kind)
	assert.Equal(t, 1, add.Params, "self is not a parameter")
	assert.Equal(t, code.Const, decl(t, s, "app.LIMIT").Kind)

	price := decl(t, s, "app.core.price")
	assert.True(t, price.Deprecated, "the decorator")
	assert.Equal(t, 4, price.Start, "the decorator is part of it")
	assert.Equal(t, 2, price.Complexity, "the conditional expression")
	assert.Equal(t, []string{"app.total"}, price.Callers)
	assert.Contains(t, decl(t, s, "app.Item").Users, "app.Cart.add", "a parameter annotation")

	assert.Equal(t, []string{"app.core"}, s.Package("app").Imports)
	assert.True(t, s.Package("app").HasTests())
}

func TestLoadRust_mapsDeclsMetricsAndCallersAcrossFiles(t *testing.T) {
	t.Parallel()
	s := load(t, treesitter.Rust, "testdata/rs")

	total := decl(t, s, "crate.total")
	assert.Equal(t, code.Func, total.Kind)
	assert.True(t, total.Exported)
	assert.Equal(t, 5, total.Start)
	assert.Equal(t, 14, total.End)
	assert.Equal(t, 4, total.Complexity)
	assert.Equal(t, 2, total.Nesting)
	assert.Equal(t, 1, total.Params)
	assert.Equal(t, []string{"crate.Cart.add", "crate.totals"}, total.Callers)

	assert.False(t, decl(t, s, "crate.unused").Exported)
	assert.Equal(t, code.Type, decl(t, s, "crate.Cart").Kind)
	assert.True(t, decl(t, s, "crate.Cart.add").Exported)
	assert.False(t, decl(t, s, "crate.Cart.reset").Exported)
	assert.True(t, decl(t, s, "crate.totals").Test, "inside #[cfg(test)]")
	assert.False(t, total.Test)

	cost := decl(t, s, "crate::price.cost")
	assert.True(t, cost.Deprecated)
	assert.Equal(t, 1, cost.Start, "the attribute is part of it")
	assert.Equal(t, 2, cost.Complexity, "a match counts once")
	assert.Equal(t, []string{"crate.total"}, cost.Callers)

	assert.Equal(t, []string{"crate::price"}, s.Package("crate").Imports)
	assert.Empty(t, s.Package("crate::price").Imports, "std is not in the crate")
}
