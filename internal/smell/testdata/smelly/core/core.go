package core

import "example.com/smelly/flaky"

type T struct{}

type unusedType struct{}

const unusedConst = 1

var unusedVar = 1

// Hidden is exported and never used.
var Hidden = 1

func init() {}

func helper() int { return flaky.Probe() }

// Used is called from other packages.
func Used() int { return helper() }

func Orphan() int { return 1 }

// OnlyTested is called only from core's own external test.
func OnlyTested() int { return 2 }

// Method has no callers; methods are never reported.
func (T) Method() {}

func unusedFn() {}
