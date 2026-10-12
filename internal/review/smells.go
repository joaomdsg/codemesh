package review

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
