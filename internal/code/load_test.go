package code_test

import (
	"sync"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var shop = sync.OnceValues(func() (*code.Snapshot, error) { return code.Load("testdata/shop") })

func loadShop(t *testing.T) *code.Snapshot {
	t.Helper()
	s, err := shop()
	require.NoError(t, err)
	return s
}

func TestLoad_listsModulePackagesSortedWithInternalImports(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	assert.Equal(t, "example.com/shop", s.Module)
	var paths []string
	for _, p := range s.Packages {
		paths = append(paths, p.Path)
		assert.Empty(t, p.Errors, p.Path)
	}
	assert.Equal(t, []string{"example.com/shop/cart", "example.com/shop/cmd/shop", "example.com/shop/store"}, paths)
	assert.Equal(t, []string{"example.com/shop/store"}, s.Package("example.com/shop/cart").Imports)
	assert.Equal(t, "cart", s.Package("example.com/shop/cart").Rel)
	assert.Equal(t, []string{"example.com/shop/cart"}, s.ImportedBy("example.com/shop/store"))
}

func TestLoad_attributesExternalTestFilesToTheirPackage(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	cart := s.Package("example.com/shop/cart")
	var files []string
	for _, f := range cart.Files {
		files = append(files, f.Path)
	}
	assert.Equal(t, []string{"cart/cart.go", "cart/cart_test.go"}, files)
	assert.True(t, cart.HasTests())
	assert.False(t, s.Package("example.com/shop/store").HasTests())
	assert.True(t, s.Decl("example.com/shop/cart_test.TestCart_totalsItems").Test)
}

func TestLoad_measuresFunctions(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	total := s.Decl("example.com/shop/cart.Cart.Total")
	require.NotNil(t, total)
	assert.Equal(t, code.Method, total.Kind)
	assert.Equal(t, "Cart.Total", total.Name)
	assert.True(t, total.Exported)
	assert.Equal(t, 6, total.Params)
	// 1 + for + if + && + else-if + || + the switch.
	assert.Equal(t, 7, total.Complexity)
	assert.Equal(t, 2, total.Nesting, "an else-if is not deeper than its if")
	assert.Equal(t, 13, total.Start, "doc comment starts the span")
	assert.Equal(t, 31, total.End)
	assert.Equal(t, 18, total.Lines, "the doc comment line is not code")
	assert.Equal(t, "func(a int, b int, d int, e int, f int, g int) int", total.Signature)

	price := s.Decl("example.com/shop/store.Price")
	assert.Equal(t, 2, price.Complexity)
	assert.Equal(t, 1, price.Nesting)
}

func TestLoad_resolvesCallersAndRefsThroughTypes(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	assert.Equal(t, []string{"example.com/shop/cart.Cart.Total"}, s.Decl("example.com/shop/store.Price").Callers)
	assert.Equal(t,
		[]string{"example.com/shop/cart_test.TestCart_totalsItems", "example.com/shop/cmd/shop.main"},
		s.Decl("example.com/shop/cart.Cart.Add").Callers)
	assert.Empty(t, s.Decl("example.com/shop/store.Unused").Callers)
	assert.Equal(t, map[string]int{"example.com/shop/store": 1, "example.com/shop/cart": 1}, s.Decl("example.com/shop/cart.Cart.Total").Refs)
	assert.Contains(t, s.Decl("example.com/shop/cart.Cart").Callers, "example.com/shop/cart.Cart.Add")
}

func TestSnapshot_importableExcludesCommandsAndInternalPackages(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	assert.True(t, s.Importable("example.com/shop/cart"))
	assert.False(t, s.Importable("example.com/shop/cmd/shop"), "a command")
	assert.False(t, s.Importable("example.com/shop/internal/x"))
	assert.False(t, s.Importable("example.com/shop/internal"))
}

func TestLoad_signsStructsByTheirExportedFieldsOnly(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	assert.Equal(t, "struct{}", s.Decl("example.com/shop/cart.Cart").Signature, "items is unexported")
	assert.Equal(t, "struct{Name string}", s.Decl("example.com/shop/store.Item").Signature)
}

func TestLoad_countsCodeLinesPerFile(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	store := s.Package("example.com/shop/store")
	require.Len(t, store.Files, 1)
	// 19 lines: 3 comment lines and 4 blank lines.
	assert.Equal(t, 12, store.Files[0].Lines)
	assert.Equal(t, 12, store.Lines())
}

func TestDecl_shapeIgnoresCommentsAndLayoutButNotCode(t *testing.T) {
	t.Parallel()
	a, err := code.ShapeOf("package p\n// doc\nfunc F(x int) int {\n\treturn x + 1 // inc\n}\n")
	require.NoError(t, err)
	b, err := code.ShapeOf("package p\nfunc F(x int) int { return x +\n1 }\n")
	require.NoError(t, err)
	c, err := code.ShapeOf("package p\nfunc F(x int) int { return x + 2 }\n")
	require.NoError(t, err)

	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
}

func TestLoad_ignoresBlankIdentifiersWhenResolvingReferences(t *testing.T) {
	t.Parallel()
	s, err := code.Load("testdata/blank")
	require.NoError(t, err)

	f := s.Decl("example.com/blank/b.F")
	require.NotNil(t, f)
	assert.Equal(t, map[string]int{"example.com/blank/b": 1}, f.Refs, "only the T in its signature")
	for _, d := range s.Decls() {
		assert.NotContains(t, d.Refs, "", d.ID)
	}
}

func TestLoad_countsAStringLiteralDeclarationAsDataOnItsFirstLine(t *testing.T) {
	t.Parallel()
	s, err := code.Load("testdata/data")
	require.NoError(t, err)

	for _, name := range []string{"script", "Schema", "greeting"} {
		d := s.Decl("example.com/data/page." + name)
		require.NotNil(t, d, name)
		assert.True(t, d.Data, name)
		assert.Equal(t, 1, d.Lines, name)
	}
	assert.False(t, s.Decl("example.com/data/page.Kind").Data, "a one-line string is a value, not data")
	q := s.Decl("example.com/data/page.Query")
	assert.False(t, q.Data)
	assert.Equal(t, 5, q.Lines, "a literal inside a function still reads as code")
	// package, import, three data lines, Kind, Query's five, Page's one.
	assert.Equal(t, 12, s.Package("example.com/data/page").Files[0].Lines)
}

func TestLoad_namesTheNestedModulesItLeavesOut(t *testing.T) {
	t.Parallel()
	s, err := code.Load("testdata/nested")
	require.NoError(t, err)

	assert.Equal(t, []string{"sub"}, s.Nested, "not the module inside it, not a testdata fixture")
	assert.Empty(t, s.Package("example.com/nested/a").Imports, "an import of another module is not a module-internal edge")
}

func TestLoad_countsASwitchAsOneDecisionHoweverManyCases(t *testing.T) {
	t.Parallel()
	s, err := code.Load("testdata/switchy")
	require.NoError(t, err)

	for _, name := range []string{"Dispatch", "Kind", "Wait"} {
		assert.Equal(t, 2, s.Decl("example.com/switchy/s."+name).Complexity, name)
	}
}
