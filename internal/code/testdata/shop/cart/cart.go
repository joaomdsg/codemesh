package cart

import "example.com/shop/store"

// Cart holds items.
type Cart struct {
	items []string
}

// Add appends an item.
func (c *Cart) Add(item string) { c.items = append(c.items, item) }

// Total sums prices and applies discounts.
func (c *Cart) Total(a, b, d, e, f, g int) int {
	sum := 0
	for _, it := range c.items {
		if it != "" && a > 0 {
			sum += store.Price(it)
		} else if b > 0 || d > 0 {
			sum--
		}
	}
	switch {
	case e > 0:
		sum *= 2
	case f > 0:
		sum /= 2
	default:
	}
	return sum + g
}
