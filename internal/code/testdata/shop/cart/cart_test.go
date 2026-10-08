package cart_test

import (
	"testing"

	"example.com/shop/cart"
)

func TestCart_totalsItems(t *testing.T) {
	var c cart.Cart
	c.Add("x")
	if c.Total(1, 0, 0, 0, 0, 0) != 100 {
		t.Fatal("bad total")
	}
}
