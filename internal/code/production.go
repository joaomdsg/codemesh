package code

// Production returns the declarations with the given IDs that are not tests,
// in order. IDs that name no declaration are skipped.
func (s *Snapshot) Production(ids []string) []*Decl {
	var out []*Decl
	for _, id := range ids {
		if d := s.Decl(id); d != nil && !d.Test {
			out = append(out, d)
		}
	}
	return out
}

// Surface returns the declarations of p's non-test files, and those of them
// that are exported.
func (p *Package) Surface() (all, exported []*Decl) {
	for _, f := range p.Files {
		if f.Test {
			continue
		}
		for _, d := range f.Decls {
			all = append(all, d)
			if d.Exported {
				exported = append(exported, d)
			}
		}
	}
	return all, exported
}

// OutsideCalls counts, for each of decls, the production declarations in
// other packages that call it.
func (s *Snapshot) OutsideCalls(decls []*Decl) map[*Decl]int {
	outside := map[*Decl]int{}
	for _, d := range decls {
		for _, c := range s.Production(d.Callers) {
			if c.Package != d.Package {
				outside[d]++
			}
		}
	}
	return outside
}
