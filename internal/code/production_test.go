package code_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
)

func TestSnapshot_productionSkipsTestsAndUnknownIDs(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	got := s.Production(append(s.Decl("example.com/shop/cart.Cart.Add").Callers, "example.com/shop/cart.Missing"))
	assert.Equal(t, []*code.Decl{s.Decl("example.com/shop/cmd/shop.main")}, got)
}

func TestSnapshot_outsideCallsCountsOnlyProductionCallersInOtherPackages(t *testing.T) {
	t.Parallel()
	s := loadShop(t)
	c, unused := s.Decl("example.com/shop/cart.Cart"), s.Decl("example.com/shop/store.Unused")

	got := s.OutsideCalls([]*code.Decl{c, unused})
	assert.Equal(t, map[*code.Decl]int{c: 1}, got, "main counts; Cart's own methods and the test do not")
}
