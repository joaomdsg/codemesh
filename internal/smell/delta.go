package smell

// Delta splits two finding lists into the findings only head has (introduced)
// and the ones only base has (fixed). A finding matches one on the same decl
// first; those left over match by identity, one for one, so a second decl of
// the same name with the same smell still counts as introduced.
func Delta(base, head []Finding) (introduced, fixed []Finding) {
	inBase := make([]bool, len(base)) // matched by a head finding
	inHead := make([]bool, len(head))
	match := func(key func(Finding) string) {
		free := map[string][]int{}
		for i, f := range base {
			if !inBase[i] {
				free[key(f)] = append(free[key(f)], i)
			}
		}
		for j, f := range head {
			if k := key(f); !inHead[j] && len(free[k]) > 0 {
				inBase[free[k][0]], inHead[j] = true, true
				free[k] = free[k][1:]
			}
		}
	}
	match(func(f Finding) string { return identity(f) + "|" + f.Decl })
	match(identity)
	for j, f := range head {
		if !inHead[j] {
			introduced = append(introduced, f)
		}
	}
	for i, f := range base {
		if !inBase[i] {
			fixed = append(fixed, f)
		}
	}
	return introduced, fixed
}

// identity keys a decl's smell by its name, not its file or package, so a
// moved declaration carries its smell along instead of trading one.
func identity(f Finding) string {
	switch {
	case f.Decl != "":
		return string(f.Rule) + "|decl|" + f.Subject
	case f.File != "":
		return string(f.Rule) + "|file|" + f.File
	}
	return string(f.Rule) + "|pkg|" + f.Package + "|" + f.Target
}
