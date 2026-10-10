package code

import (
	"slices"
	"strings"
)

// UsedOutside reports whether a declaration in another package refers to d,
// or uses it through a constant, field or method.
func (s *Snapshot) UsedOutside(d *Decl) bool {
	outside := func(id string) bool {
		c := s.Decl(id)
		return c != nil && c.Package != d.Package
	}
	if slices.ContainsFunc(d.Callers, outside) || slices.ContainsFunc(d.Users, outside) {
		return true
	}
	// A constant is one value of its type's enum; unexporting it alone would
	// split the enum. A const's Signature names a same-package type bare,
	// once per name in its spec.
	if d.Kind == Const {
		typ, _, _ := strings.Cut(d.Signature, ", ")
		t := s.Decl(d.Package + "." + typ)
		return t != nil && t.Kind == Type && s.UsedOutside(t)
	}
	return false
}
