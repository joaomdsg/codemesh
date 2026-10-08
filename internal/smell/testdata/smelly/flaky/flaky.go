package flaky

import "example.com/smelly/hub"

func Probe() int { return hub.B() }
