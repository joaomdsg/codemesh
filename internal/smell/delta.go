package smell

// Delta splits two finding lists into the findings only head has (introduced)
// and the ones only base has (fixed).
func Delta(base, head []Finding) (introduced, fixed []Finding) {
	inBase, inHead := idSet(base), idSet(head)
	for _, f := range head {
		if !inBase[identity(f)] {
			introduced = append(introduced, f)
		}
	}
	for _, f := range base {
		if !inHead[identity(f)] {
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

func idSet(list []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range list {
		m[identity(f)] = true
	}
	return m
}
