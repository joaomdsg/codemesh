package main

import (
	"fmt"

	"example.com/shop/cart"
)

func main() {
	var c cart.Cart
	c.Add("apple")
	fmt.Println(c.Total(1, 1, 1, 1, 1, 1))
}
