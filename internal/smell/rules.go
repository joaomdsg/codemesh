package smell

import (
	"fmt"
	"math"
	pathpkg "path"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
)

func fileFindings(p *code.Package, f *code.File) []Finding {
	if f.Test || f.Generated {
		return nil
	}
	sev, limit, ok := tier(f.Lines, LargeFileLimit, largeFileHighLimit)
	if !ok {
		return nil
	}
	return []Finding{{
		Rule: LargeFile, Severity: sev, Package: p.Path, File: f.Path,
		Subject: f.Path, Measure: f.Lines, Limit: limit,
		Detail: fmt.Sprintf("%d lines, %s %d", f.Lines, limitWord(sev), limit),
	}}
}

func declFindings(s *code.Snapshot, p *code.Package, f *code.File, d *code.Decl) []Finding {
	// Nobody edits generated code by hand, so its smells are not actionable.
	if d.Test || f.Generated {
		return nil
	}
	at := site{s, p, f, d}
	out := at.reach()
	if d.Kind == code.Func || d.Kind == code.Method {
		out = append(out, at.size()...)
		// A main package exists to wire others together; leaning on them is its job.
		if other, n, own := envy(d); other != "" && p.Name != "main" {
			f := at.finding(EnviousFunc, Info, n, own, fmt.Sprintf("%d refs to %s, %d to own package", n, relOf(s, other), own))
			f.Target = other
			out = append(out, f)
		}
		out = append(out, at.layers()...)
	}
	return out
}

// layers flags a func that adds a layer and no behaviour: it only forwards
// its parameters, or its callers all pass one value it could default.
func (at site) layers() []Finding {
	d := at.d
	var out []Finding
	if d.Forwards != "" && !(at.p.Name == "main" && d.Name == "main") && d.Name != "init" {
		out = append(out, at.finding(PassThrough, Info, 0, 0, "only calls "+d.Forwards+" with its own arguments"))
	}
	// An importable package's exports have callers this module cannot see.
	open := d.Exported && at.s.Importable(at.p.Path)
	if len(d.Fixed) > 0 && d.Calls >= fixedArgMinCalls && !d.Escapes && !open {
		var fixed []string
		for _, a := range d.Fixed {
			fixed = append(fixed, a.Param+" is "+a.Value)
		}
		out = append(out, at.finding(FixedArg, Info, d.Calls, fixedArgMinCalls,
			fmt.Sprintf("%s at all %d production call sites", strings.Join(fixed, ", "), d.Calls)))
	}
	return out
}

// site is the declaration a finding is about, and where it sits.
type site struct {
	s *code.Snapshot
	p *code.Package
	f *code.File
	d *code.Decl
}

func (at site) finding(rule Rule, sev Severity, measure, limit int, detail string) Finding {
	return Finding{
		Rule: rule, Severity: sev, Package: at.p.Path, File: at.f.Path, Line: at.d.Start,
		Decl: at.d.ID, Subject: at.d.Name, Measure: measure, Limit: limit, Detail: detail,
	}
}

// reach flags a declaration nothing outside its package, or nothing at
// all, references.
func (at site) reach() []Finding {
	d, main := at.d, at.p.Name == "main"
	var out []Finding
	// An importable package's exports are API for other modules, which this
	// module cannot see; only a closed package's unused export is dead weight.
	if d.Exported && !at.s.Importable(at.p.Path) && !main && d.Kind != code.Method && !at.s.UsedOutside(d) {
		out = append(out, at.finding(UnusedExport, Info, 0, 0, "no use outside its package"))
	}
	if len(d.Callers) == 0 && deadCandidate(d, main) {
		sev := Info
		if d.Kind == code.Func {
			sev = Warn
		}
		out = append(out, at.finding(DeadCode, sev, 0, 0, "no references"))
	}
	return out
}

// size measures a func or method against the limits in smell.go.
func (at site) size() []Finding {
	d := at.d
	var out []Finding
	if sev, limit, ok := tier(d.Lines, LongFuncLimit, longFuncHighLimit); ok {
		out = append(out, at.finding(LongFunc, sev, d.Lines, limit, fmt.Sprintf("%d lines, %s %d", d.Lines, limitWord(sev), limit)))
	}
	if sev, limit, ok := tier(d.Complexity, ComplexFuncLimit, ComplexFuncHighLimit); ok {
		out = append(out, at.finding(ComplexFunc, sev, d.Complexity, limit, fmt.Sprintf("complexity %d, %s %d", d.Complexity, limitWord(sev), limit)))
	}
	if d.Nesting > deepNestingLimit {
		out = append(out, at.finding(DeepNesting, Warn, d.Nesting, deepNestingLimit, fmt.Sprintf("nesting %d, limit %d", d.Nesting, deepNestingLimit)))
	}
	if d.Params > ManyParamsLimit {
		out = append(out, at.finding(ManyParams, Warn, d.Params, ManyParamsLimit, fmt.Sprintf("%d params, limit %d", d.Params, ManyParamsLimit)))
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

// limitWord names the limit a tiered measure passed, so a high row's
// limit reads apart from a warn row's.
func limitWord(sev Severity) string {
	if sev == High {
		return "high limit"
	}
	return "limit"
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
// there number at least enviousFuncMinRefs and exceed those to d's own
// package, with the own count.
func envy(d *code.Decl) (other string, n, own int) {
	own = d.Refs[d.Package]
	for pkg, c := range d.Refs {
		if pkg == d.Package || c <= own || c < enviousFuncMinRefs {
			continue
		}
		if c > n || c == n && pkg < other {
			other, n = pkg, c
		}
	}
	return other, n, own
}

func relOf(s *code.Snapshot, path string) string {
	p := s.Package(path)
	switch {
	case p == nil:
		return path
	case p.Rel == ".":
		return pathpkg.Base(s.Module)
	}
	return p.Rel
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
