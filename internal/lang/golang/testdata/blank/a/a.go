package a

import "strconv"

// Each blank identifier below is a package-level object outside the
// package scope.
var N, _ = strconv.Atoi("1")

var _ = N

const C, _ = 1, 2

type _ int

func _() {}
