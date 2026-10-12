package smell

// ByDecl groups findings by the declaration they are about; findings about a
// file or package alone are left out.
func ByDecl(fs []Finding) map[string][]Finding {
	out := map[string][]Finding{}
	for _, f := range fs {
		if f.Decl != "" {
			out[f.Decl] = append(out[f.Decl], f)
		}
	}
	return out
}

// Worst is the highest severity among fs, Info when there are none.
func Worst(fs []Finding) Severity {
	worst := Info
	for _, f := range fs {
		worst = max(worst, f.Severity)
	}
	return worst
}
