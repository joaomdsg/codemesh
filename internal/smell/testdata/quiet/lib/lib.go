package lib

import "example.com/quiet/internal/priv"

func A() int { return 1 }
func B() int { return 2 }
func C() int { return 3 }
func D() int { return 4 }

// E is API no one in this module calls.
func E() int { return priv.Used() }
