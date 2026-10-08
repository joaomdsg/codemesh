package main

import (
	"example.com/smelly/chaina"
	"example.com/smelly/core"
	"example.com/smelly/envy"
)

func main() {
	core.Used()
	chaina.Run()
	envy.Greedy()
}

// Exported is dead: main packages are not reported as unused-export.
func Exported() {}

func spare() {}

type holder struct{}

// run has no callers; methods are never reported.
func (holder) run() {}
