package envy

import (
	"example.com/smelly/core"
	"example.com/smelly/hub"
)

func local() int { return core.Used() }

// Greedy: 4 refs to hub, 1 to its own package.
func Greedy() int { return hub.A() + hub.B() + hub.C() + hub.D() + local() }

// Outnumbered: 4 refs to hub, 3 to its own package.
func Outnumbered() int { return hub.A() + hub.B() + hub.C() + hub.D() + local() + local() + local() }

// Tied: 4 refs to hub, 4 to its own package.
func Tied() int { return hub.A() + hub.B() + hub.C() + hub.D() + local() + local() + local() + local() }

// Few: 3 refs to hub, none to its own package.
func Few() int { return hub.A() + hub.B() + hub.C() }
