package code_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnapshot_usedOutsideMeansAnotherPackageRefersToIt(t *testing.T) {
	t.Parallel()
	s := loadShop(t)

	assert.True(t, s.UsedOutside(s.Decl("example.com/shop/store.Price")), "cart's Total calls it")
	assert.True(t, s.UsedOutside(s.Decl("example.com/shop/cart.Cart")), "main declares one")
	assert.False(t, s.UsedOutside(s.Decl("example.com/shop/store.Unused")))
}
