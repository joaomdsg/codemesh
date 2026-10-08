// Package store keeps prices.
package store

// Price returns the price of an item in cents.
func Price(item string) int {
	if item == "" {
		return 0
	}
	return len(item) * 100
}

// Unused is exported and nobody calls it.
func Unused() {}

func dead() {}

type Item struct {
	Name string
}
