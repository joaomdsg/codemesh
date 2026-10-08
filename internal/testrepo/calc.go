package testrepo

// CalcBase is a small module: an exported API, a helper, a test.
var CalcBase = map[string]string{
	"go.mod": "module example.com/calc\n\ngo 1.27\n",
	"main.go": `package main

import "example.com/calc/calc"

func main() { println(calc.Add(1, 2), calc.Scale(2, 3), calc.Moved()) }
`,
	"calc/calc.go": `package calc

// Add adds.
func Add(a, b int) int { return a + b }

// Scale multiplies.
func Scale(x, k int) int {
	return helper(x) * k
}

func helper(x int) int { return x + 1 }

// Old is removed in head.
func Old() {}

// Moved moves to util.go unchanged.
func Moved() int { return 7 }
`,
	"calc/calc_test.go": `package calc_test

import (
	"testing"

	"example.com/calc/calc"
)

func TestAdd(t *testing.T) {
	if calc.Add(1, 2) != 3 {
		t.Fatal("sum")
	}
}
`,
}

// CalcHead edits CalcBase with one change of every review lane.
var CalcHead = map[string]string{
	"calc/calc.go": `package calc

// Add returns a plus b.
func Add(a, b int) int {
	return a + b
}

// Scale multiplies, never below zero.
func Scale(x, k int) int {
	if k < 0 {
		return 0
	}
	return helper(x, 1) * k
}

func helper(x, d int) int { return x + d }

// Clamp bounds x to [lo, hi].
func Clamp(x, lo, hi int) int { return min(max(x, lo), hi) }
`,
	"calc/util.go": `package calc

// Moved moves to util.go unchanged.
func Moved() int { return 7 }
`,
	"calc/calc_test.go": CalcBase["calc/calc_test.go"] + `
func TestClamp(t *testing.T) {
	if calc.Clamp(5, 0, 3) != 3 {
		t.Fatal("clamp")
	}
}
`,
	"README.md":            "# calc\n",
	"calc/testdata/in.txt": "fixture\n",
}
