package code_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
)

// shop mirrors a small module: cart and store packages, a command, and a
// test in cart.
func shop() *code.Snapshot {
	b := code.NewBuilder(code.Go, "example.com/shop", "/shop")
	cartP := b.Package(code.Package{Path: "example.com/shop/cart"})
	cartF := b.File(cartP, code.File{Path: "cart/cart.go"})
	testF := b.File(cartP, code.File{Path: "cart/cart_test.go", Test: true})
	storeF := b.File(b.Package(code.Package{Path: "example.com/shop/store"}), code.File{Path: "store/store.go"})
	mainF := b.File(b.Package(code.Package{Path: "example.com/shop/cmd/shop"}), code.File{Path: "cmd/shop/main.go"})

	cart := b.Decl(cartF, code.Decl{ID: "example.com/shop/cart.Cart", Kind: code.Type, Exported: true})
	add := b.Decl(cartF, code.Decl{ID: "example.com/shop/cart.Cart.Add", Kind: code.Method, Exported: true})
	total := b.Decl(cartF, code.Decl{ID: "example.com/shop/cart.Cart.Total", Kind: code.Method, Exported: true})
	tst := b.Decl(testF, code.Decl{ID: "example.com/shop/cart_test.TestCart_totalsItems", Kind: code.Func, Test: true})
	price := b.Decl(storeF, code.Decl{ID: "example.com/shop/store.Price", Kind: code.Func, Exported: true})
	b.Decl(storeF, code.Decl{ID: "example.com/shop/store.Unused", Kind: code.Func, Exported: true})
	main := b.Decl(mainF, code.Decl{ID: "example.com/shop/cmd/shop.main", Kind: code.Func})

	b.Link(add, cart, code.Caller)
	b.Link(main, cart, code.Caller)
	b.Link(tst, add, code.Caller)
	b.Link(main, add, code.Caller)
	b.Link(total, price, code.Caller)
	return b.Finish()
}

func TestSnapshot_productionSkipsTestsAndUnknownIDs(t *testing.T) {
	t.Parallel()
	s := shop()

	got := s.Production(append(s.Decl("example.com/shop/cart.Cart.Add").Callers, "example.com/shop/cart.Missing"))
	assert.Equal(t, []*code.Decl{s.Decl("example.com/shop/cmd/shop.main")}, got)
}

func TestSnapshot_outsideCallsCountsOnlyProductionCallersInOtherPackages(t *testing.T) {
	t.Parallel()
	s := shop()
	c, unused := s.Decl("example.com/shop/cart.Cart"), s.Decl("example.com/shop/store.Unused")

	got := s.OutsideCalls([]*code.Decl{c, unused})
	assert.Equal(t, map[*code.Decl]int{c: 1}, got, "main counts; Cart's own methods and the test do not")
}
