package review

import (
	"cmp"

	"github.com/joaomdsg/codemesh/internal/smell"
)

func delta(base, head []smell.Finding) (introduced, fixed []smell.Finding) {
	id := func(f smell.Finding) string {
		return string(f.Rule) + "|" + f.Package + "|" + f.File + "|" + cmp.Or(f.Decl, f.Subject)
	}
	in := func(list []smell.Finding) map[string]bool {
		m := map[string]bool{}
		for _, f := range list {
			m[id(f)] = true
		}
		return m
	}
	inBase, inHead := in(base), in(head)
	for _, f := range head {
		if !inBase[id(f)] {
			introduced = append(introduced, f)
		}
	}
	for _, f := range base {
		if !inHead[id(f)] {
			fixed = append(fixed, f)
		}
	}
	return introduced, fixed
}

// attach puts each introduced smell on the unit it is about: its decl, or
// failing that the first unit of its file.
func attach(rev *Review) {
	byID, byFile := map[string]*Unit{}, map[string]*Unit{}
	for _, u := range rev.Units {
		byID[u.ID] = u
		if _, ok := byFile[u.File]; !ok {
			byFile[u.File] = u
		}
	}
	for _, f := range rev.Introduced {
		u := byID[f.Decl]
		if u == nil && f.Decl == "" {
			u = byFile[f.File]
		}
		if u != nil && f.File != "" {
			u.Smells = append(u.Smells, f)
		}
	}
}
