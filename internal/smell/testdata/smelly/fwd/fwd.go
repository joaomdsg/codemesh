package fwd

import (
	"fmt"
	"os"
)

const modeFast = 2

// Use calls everything below, so dead-code leaves them alone.
func Use() {
	_ = plain(1, 2) + swapped(1, 2) + fixedOne(1) + twoSteps(1, 2) + old(1, 2) + variadic(1, 2)
	_, _ = readAll("x")
	_ = packed(1, 2)
	box{}.put("x")

	_ = render("a", true, 1) + render("b", true, 2) + render("c", true, 3)
	_ = Render(true) + Render(true) + Render(true)
	_ = twice(true) + twice(true)
	_ = valued(7) + valued(7) + valued(7)
	f := valued
	_ = f(8)
	_ = mixed(1) + mixed(1) + mixed(2)
	_ = mode(modeFast) + mode(modeFast) + mode(modeFast)
}

func target(a, b int) int { return a*b + 1 }

// plain only forwards its parameters.
func plain(a, b int) int { return target(a, b) }

// swapped reorders them, which is a decision.
func swapped(a, b int) int { return target(b, a) }

// fixedOne adds a constant, knowledge the callee lacks.
func fixedOne(a int) int { return target(a, 1) }

func readAll(name string) ([]byte, error) { return os.ReadFile(name) }

func twoSteps(a, b int) int {
	x := target(a, b)
	return x
}

// Deprecated: call target.
func old(a, b int) int { return target(a, b) }

func variadic(xs ...int) int { return sum(xs...) }

// packed hands its arguments over as one slice, not spread.
func packed(xs ...any) string { return fmt.Sprint(xs) }

func sum(xs ...int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

type inner struct{}

func (inner) put(string) {}

type box struct{ in inner }

func (b box) put(s string) { b.in.put(s) }

func render(s string, wide bool, pad int) int {
	if wide {
		return len(s) + pad
	}
	return pad
}

// Render is API for other modules, whose calls this module cannot see.
func Render(wide bool) int { return mixed(len(fmt.Sprint(wide))) }

func twice(wide bool) int { return len(fmt.Sprint(wide)) }

func valued(n int) int { return n + 1 }

func mixed(n int) int { return n + 2 }

func mode(m int) int { return m + 3 }
