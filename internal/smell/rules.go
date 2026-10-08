package smell

import (
	"fmt"
	"math"
	"slices"

	"github.com/joaomdsg/codemesh/internal/code"
)

func fileFindings(p *code.Package, f *code.File) []Finding {
	if f.Test || f.Generated {
		return nil
	}
	sev, limit, ok := tier(f.Lines, LargeFileLimit, LargeFileHighLimit)
	if !ok {
		return nil
	}
	return []Finding{{
		Rule: LargeFile, Severity: sev, Package: p.Path, File: f.Path,
		Subject: f.Path, Measure: f.Lines, Limit: limit,
		Detail: fmt.Sprintf("%d lines, limit %d", f.Lines, limit),
	}}
}

func declFindings(s *code.Snapshot, p *code.Package, f *code.File, d *code.Decl) []Finding {
	// Nobody edits generated code by hand, so its smells are not actionable.
	if d.Test || f.Generated {
		return nil
	}
	at := func(rule Rule, sev Severity, measure, limit int, detail string) Finding {
		return Finding{
			Rule: rule, Severity: sev, Package: p.Path, File: f.Path, Line: d.Start,
			Decl: d.ID, Subject: d.Name, Measure: measure, Limit: limit, Detail: detail,
		}
	}
	var out []Finding
	isFunc := d.Kind == code.Func || d.Kind == code.Method
	if isFunc {
		if sev, limit, ok := tier(d.Lines, LongFuncLimit, LongFuncHighLimit); ok {
			out = append(out, at(LongFunc, sev, d.Lines, limit, fmt.Sprintf("%d lines, limit %d", d.Lines, limit)))
		}
		if sev, limit, ok := tier(d.Complexity, ComplexFuncLimit, ComplexFuncHighLimit); ok {
			out = append(out, at(ComplexFunc, sev, d.Complexity, limit, fmt.Sprintf("complexity %d, limit %d", d.Complexity, limit)))
		}
		if d.Nesting > DeepNestingLimit {
			out = append(out, at(DeepNesting, Warn, d.Nesting, DeepNestingLimit, fmt.Sprintf("nesting %d, limit %d", d.Nesting, DeepNestingLimit)))
		}
		if d.Params > ManyParamsLimit {
			out = append(out, at(ManyParams, Warn, d.Params, ManyParamsLimit, fmt.Sprintf("%d params, limit %d", d.Params, ManyParamsLimit)))
		}
	}
	main := p.Name == "main"
	// A main package exists to wire others together; leaning on them is its job.
	if isFunc && !main {
		if other, n, own := envy(d); other != "" {
			f := at(EnviousFunc, Info, n, own, fmt.Sprintf("%d refs to %s, %d to own package", n, relOf(s, other), own))
			f.Target = other
			out = append(out, f)
		}
	}
	// An importable package's exports are API for other modules, which this
	// module cannot see; only a closed package's unused export is dead weight.
	if d.Exported && !s.Importable(p.Path) && !main && d.Kind != code.Method && !usedOutside(s, d) {
		out = append(out, at(UnusedExport, Info, 0, 0, "no use outside its package"))
	}
	if len(d.Callers) == 0 && deadCandidate(d, main) {
		sev := Info
		if d.Kind == code.Func {
			sev = Warn
		}
		out = append(out, at(DeadCode, sev, 0, 0, "no references"))
	}
	return out
}

// tier grades v against a warn and a high limit.
func tier(v, warn, high int) (Severity, int, bool) {
	switch {
	case v > high:
		return High, high, true
	case v > warn:
		return Warn, warn, true
	}
	return Info, 0, false
}

func usedOutside(s *code.Snapshot, d *code.Decl) bool {
	return slices.ContainsFunc(d.Callers, func(id string) bool {
		c := s.Decl(id)
		return c != nil && c.Package != d.Package
	})
}

// deadCandidate reports whether an uncalled d is dead code rather than an
// entry point, a method (it may satisfy an interface) or an export that
// unused-export already covers.
func deadCandidate(d *code.Decl, main bool) bool {
	switch {
	case d.Kind == code.Method:
		return false
	case d.Kind == code.Func && (d.Name == "init" || d.Name == "main" && main):
		return false
	case d.Exported && !main:
		return false
	}
	return true
}

// envy returns the module package d references most, when its references
// there number at least EnviousFuncMinRefs and exceed those to d's own
// package, with the own count.
func envy(d *code.Decl) (other string, n, own int) {
	own = d.Refs[d.Package]
	for pkg, c := range d.Refs {
		if pkg == d.Package || c <= own || c < EnviousFuncMinRefs {
			continue
		}
		if c > n || c == n && pkg < other {
			other, n = pkg, c
		}
	}
	return other, n, own
}

func relOf(s *code.Snapshot, path string) string {
	if p := s.Package(path); p != nil {
		return p.Rel
	}
	return path
}

func unstableDeps(s *code.Snapshot) []Finding {
	instability := map[string]float64{}
	for _, p := range s.Packages {
		ca, ce := len(s.ImportedBy(p.Path)), len(p.Imports)
		if ca+ce > 0 {
			instability[p.Path] = float64(ce) / float64(ca+ce)
		}
	}
	var out []Finding
	for _, p := range s.Packages {
		for _, path := range p.Imports {
			q := s.Package(path)
			ip, iq := instability[p.Path], instability[path]
			if q == nil || q.Name == "main" || iq <= ip {
				continue
			}
			out = append(out, Finding{
				Rule: UnstableDep, Severity: Warn, Package: p.Path, Subject: p.Rel, Target: q.Path,
				Measure: percent(iq), Limit: percent(ip),
				Detail: fmt.Sprintf("imports %s (I=%.2f) from I=%.2f", q.Rel, iq, ip),
			})
		}
	}
	return out
}

func percent(f float64) int { return int(math.Round(f * 100)) }
