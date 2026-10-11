// Package smell finds code smells in a code.Snapshot.
package smell

import (
	"cmp"
	"slices"

	"github.com/joaomdsg/codemesh/internal/code"
)

// Severity ranks a finding.
type Severity int

const (
	Info Severity = iota
	Warn
	High
)

func (s Severity) String() string {
	switch s {
	case Warn:
		return "warn"
	case High:
		return "high"
	}
	return "info"
}

// Rule names one smell detector.
type Rule string

const (
	LongFunc        Rule = "long-func"
	ComplexFunc     Rule = "complex-func"
	DeepNesting     Rule = "deep-nesting"
	ManyParams      Rule = "many-params"
	LargeFile       Rule = "large-file"
	UnusedExport    Rule = "unused-export"
	DeadCode        Rule = "dead-code"
	EnviousFunc     Rule = "envious-func"
	UnstableDep     Rule = "unstable-dep"
	UntestedPackage Rule = "untested-package"
	PassThrough     Rule = "pass-through"
	FixedArg        Rule = "fixed-arg"
)

// Thresholds: a measure above the limit is a finding. The High limits raise
// the severity.
const (
	LongFuncLimit        = 60
	longFuncHighLimit    = 120
	ComplexFuncLimit     = 10
	ComplexFuncHighLimit = 20
	deepNestingLimit     = 4
	ManyParamsLimit      = 5
	LargeFileLimit       = 600
	largeFileHighLimit   = 1200
	// enviousFuncMinRefs is the fewest references to the other package that
	// count as envy.
	enviousFuncMinRefs = 4
	// fixedArgMinCalls is the fewest production call sites that make one
	// constant a pattern rather than a coincidence.
	fixedArgMinCalls = 3
)

var why = map[Rule]string{
	LongFunc:        "A long function is hard to hold in your head.",
	ComplexFunc:     "Many paths to read and to test.",
	DeepNesting:     "Deep control flow hides the main path.",
	ManyParams:      "A long parameter list means the function does several jobs.",
	LargeFile:       "A large file usually mixes several concerns.",
	UnusedExport:    "Exported but unused outside its package: API surface nobody uses.",
	DeadCode:        "Declared and never referenced: weight with no value.",
	EnviousFunc:     "Most of its references go to one other package; it may belong there.",
	UnstableDep:     "Imports a package that changes more freely than itself, so that package's changes ripple into this one.",
	UntestedPackage: "No test refers to it: changes land unguarded.",
	PassThrough:     "It only hands its arguments to another function: a layer that adds an interface and no behaviour.",
	FixedArg:        "Every caller passes the same value: make it the default and drop the parameter.",
}

// Why is the one-line reason a rule matters.
func (r Rule) Why() string { return why[r] }

// Finding is one smell at one place.
type Finding struct {
	Rule     Rule
	Severity Severity
	Package  string // import path
	File     string // module-relative; "" for package-level findings
	Line     int    // 0 when the finding has no line
	Decl     string // decl ID, "" when the finding is not about one decl
	Subject  string // display name: decl name, file path or package directory
	Target   string // the other package, for envious-func and unstable-dep
	// Measure and Limit are in the rule's unit. For envious-func they are the
	// references to the other package and to the own package. For unstable-dep
	// they are the instability of the imported and the importing package, in
	// percent.
	Measure int
	Limit   int
	Detail  string // one short fragment, e.g. "73 lines, limit 60"
}

// Find runs every rule over s. Findings are sorted by severity, highest
// first, then by package, file and line.
func Find(s *code.Snapshot) []Finding {
	var out []Finding
	reached := testedFromOutside(s)
	for _, p := range s.Packages {
		if p.Name != "main" && !p.HasTests() && !reached[p.Path] {
			out = append(out, Finding{
				Rule: UntestedPackage, Severity: Info, Package: p.Path,
				Subject: p.Rel, Detail: "no test refers to it",
			})
		}
		for _, f := range p.Files {
			out = append(out, fileFindings(p, f)...)
			for _, d := range f.Decls {
				out = append(out, declFindings(s, p, f, d)...)
			}
		}
	}
	out = append(out, unstableDeps(s)...)
	slices.SortFunc(out, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(b.Severity, a.Severity),
			cmp.Compare(a.Package, b.Package),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Decl, b.Decl),
		)
	})
	return out
}

// testedFromOutside lists the packages a test in another package refers to,
// such as an integration test suite in its own directory.
func testedFromOutside(s *code.Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, d := range s.Decls() {
		if !d.Test {
			continue
		}
		for pkg := range d.Refs {
			if pkg != d.Package {
				out[pkg] = true
			}
		}
	}
	return out
}
