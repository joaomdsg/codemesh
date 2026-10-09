// Package prognosis turns an analysis into findings a newcomer can act on:
// what is wrong at a place, why it matters, what to do and how to check it.
// Every number in a text comes from the snapshot or the smells.
package prognosis

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// Lens is the view a prognosis belongs to.
type Lens string

const (
	Health    Lens = "health"    // hard code, and hard code that keeps changing
	Reach     Lens = "reach"     // code many places depend on
	Structure Lens = "structure" // how packages divide the code
	Tests     Lens = "tests"     // missing tests and the smells that slow changes
)

// Level is how soon to act: 3 first, 1 when convenient.
type Level int

// Fact is one measure behind a prognosis, with its limit when it has one.
type Fact struct {
	Label        string
	Value, Limit int
}

// Ref is a place related to a prognosis and why it matters to it.
type Ref struct{ ID, Name, Why string }

// Prognosis is one place that needs attention.
type Prognosis struct {
	Key     string // kind and target; the same place keeps its key across analyses
	Lens    Lens
	Level   Level
	Target  string // decl ID, file path or package import path
	Name    string // the target as a reader names it
	File    string // file holding the target, "" for a package
	Line    int
	Title   string
	Summary string   // one sentence, for a hover
	Why     string   // why it matters, in plain words
	Do      []string // steps, in order
	Check   string   // how to tell it worked
	Facts   []Fact
	Related []Ref
}

// Thresholds. A hotspot's churn is relative to the module's own history, so
// a quiet repo still shows where its work goes.
const (
	reachCallers  = 50   // production callers that make a declaration load-bearing
	dominantShare = 50   // percent of the module's lines in one package
	dominantLines = 1000 // smaller modules may well be one package
	clusterMin    = 3    // many-params functions in one package that read as one problem
	// hotLimit is the complexity past which a busy function is worth fixing
	// soon rather than when next touched: halfway to the high limit.
	hotLimit   = (smell.ComplexFuncLimit + smell.ComplexFuncHighLimit) / 2
	maxRelated = 8
)

// Find returns the prognoses for s, most urgent first.
func Find(s *code.Snapshot, fs []smell.Finding) []Prognosis {
	ix := index(s)
	var out []Prognosis
	out = append(out, complexity(s, ix)...)
	out = append(out, reach(s, ix)...)
	out = append(out, structure(s, ix, fs)...)
	out = append(out, smells(s, ix, fs)...)
	slices.SortStableFunc(out, func(a, b Prognosis) int {
		return cmp.Or(cmp.Compare(b.Level, a.Level), cmp.Compare(a.Lens, b.Lens), cmp.Compare(a.Key, b.Key))
	})
	return out
}

type idx struct {
	file     map[string]*code.File
	hotChurn int // churn at or above which a file counts as busy
	lines    int // production lines in the module
}

func index(s *code.Snapshot) idx {
	ix := idx{file: map[string]*code.File{}}
	var churns []int
	for _, p := range s.Packages {
		ix.lines += p.Lines()
		for _, f := range p.Files {
			ix.file[f.Path] = f
			if !f.Test && f.Churn > 0 {
				churns = append(churns, f.Churn)
			}
		}
	}
	// The top quarter of files by churn, and never a file touched once.
	slices.Sort(churns)
	ix.hotChurn = 2
	if len(churns) > 0 {
		ix.hotChurn = max(2, churns[len(churns)*3/4])
	}
	return ix
}

func prod(s *code.Snapshot, ids []string) []*code.Decl {
	var out []*code.Decl
	for _, id := range ids {
		if d := s.Decl(id); d != nil && !d.Test {
			out = append(out, d)
		}
	}
	return out
}

// BusyChurn is the churn at or above which a file counts as busy.
func BusyChurn(s *code.Snapshot) int { return index(s).hotChurn }

// TestCallers counts the tests that call d directly.
func TestCallers(s *code.Snapshot, d *code.Decl) int { return testCallers(s, d) }

func testCallers(s *code.Snapshot, d *code.Decl) int {
	n := 0
	for _, id := range d.Callers {
		if c := s.Decl(id); c != nil && c.Test {
			n++
		}
	}
	return n
}

func callerRefs(s *code.Snapshot, d *code.Decl, why string) []Ref {
	var out []Ref
	for _, c := range prod(s, d.Callers) {
		out = append(out, Ref{ID: c.ID, Name: c.Name, Why: why})
		if len(out) == maxRelated {
			break
		}
	}
	return out
}

func declRef(d *code.Decl) (name, file string, line int) { return d.Name, d.File, d.Start }

// complexity flags functions past the complexity limit: hotspots when their
// file keeps changing, and missing tests when no test calls them.
func complexity(s *code.Snapshot, ix idx) []Prognosis {
	var out []Prognosis
	for _, d := range s.Decls() {
		if d.Test || d.Complexity <= smell.ComplexFuncLimit {
			continue
		}
		churn := ix.file[d.File].Churn
		tests := testCallers(s, d)
		facts := []Fact{
			{"paths through it (complexity)", d.Complexity, smell.ComplexFuncLimit},
			{"commits to its file lately", churn, 0},
			{"lines", d.Lines, smell.LongFuncLimit},
			{"tests that call it", tests, 0},
		}
		name, file, line := declRef(d)
		p := Prognosis{
			Lens: Health, Target: d.ID, Name: name, File: file, Line: line, Facts: facts,
			Related: callerRefs(s, d, "calls it, so a change in behaviour reaches it"),
			Check:   fmt.Sprintf("Each function that replaces it stays at or below complexity %d, and the tests pass before and after.", smell.ComplexFuncLimit),
		}
		first := "Write tests that pin down what it does today, before changing it."
		if tests > 0 {
			first = fmt.Sprintf("Run the %s that call it, and add cases for the paths they miss.", plural(tests, "test"))
		}
		p.Do = []string{
			first,
			"Split it into smaller functions, one per step, each named for what it does.",
			"Replace nested ifs with early returns where a branch only handles an error or an edge case.",
			"Run the tests after each split, so a mistake shows up next to its cause.",
		}
		if churn >= ix.hotChurn {
			p.Key, p.Title = "hotspot:"+d.ID, "Hard to follow, and it keeps changing"
			p.Level = 1 + b2i(d.Complexity > hotLimit) + b2i(d.Complexity > smell.ComplexFuncHighLimit)
			p.Summary = fmt.Sprintf("%s has %d paths through it and its file changed in %s lately. Bugs collect where hard code meets frequent edits.", d.Name, d.Complexity, plural(churn, "commit"))
			p.Why = "Every if, loop and case adds a path that a reader has to follow and a test has to cover. Code that is both complex and edited often is where most bugs appear: each edit can break a path nobody checked again. Fixing it here pays back on every future change."
		} else {
			p.Key, p.Title = "complex:"+d.ID, "Hard to follow"
			p.Level = 1
			p.Summary = fmt.Sprintf("%s has %d paths through it, past the limit of %d. It is quiet now, so it can wait until you next change it.", d.Name, d.Complexity, smell.ComplexFuncLimit)
			p.Why = "Every if, loop and case adds a path that a reader has to follow and a test has to cover. It rarely changes, so it costs little today; simplify it the next time you have to edit it."
		}
		out = append(out, p)
		if tests == 0 {
			out = append(out, Prognosis{
				Key: "untested:" + d.ID, Lens: Tests, Level: 1 + b2i(churn >= ix.hotChurn && d.Complexity > hotLimit), Target: d.ID, Name: name, File: file, Line: line,
				Title:   "Complex, and no test calls it",
				Summary: fmt.Sprintf("%s has %d paths and no test calls it directly. A change here is checked by nothing but luck.", d.Name, d.Complexity),
				Why:     "Tests are how you find out a change broke something before your users do. Complex code with no test is the riskiest kind: there are many ways to break it and nothing to tell you.",
				Do: []string{
					"Write one test for the most common way it is used.",
					"Add a test for each error it can return.",
					"Only then refactor it; the tests tell you whether behaviour stayed the same.",
				},
				Check:   "At least one test calls it, and the test fails if you break the main path on purpose.",
				Facts:   facts,
				Related: callerRefs(s, d, "calls it; its own tests may reach it indirectly"),
			})
		}
	}
	return out
}

// reach flags declarations so many places call that changing them is a
// project of its own.
func reach(s *code.Snapshot, ix idx) []Prognosis {
	var out []Prognosis
	for _, d := range s.Decls() {
		if d.Test {
			continue
		}
		callers := prod(s, d.Callers)
		if len(callers) < reachCallers {
			continue
		}
		pkgs := map[string]int{}
		for _, c := range callers {
			pkgs[c.Package]++
		}
		var rel []Ref
		for _, p := range slices.Sorted(maps.Keys(pkgs)) {
			rel = append(rel, Ref{ID: p, Name: pkgShort(s, p), Why: fmt.Sprintf("%s here call it", plural(pkgs[p], "declaration"))})
		}
		slices.SortStableFunc(rel, func(a, b Ref) int { return cmp.Compare(pkgs[b.ID], pkgs[a.ID]) })
		name, file, line := declRef(d)
		out = append(out, Prognosis{
			Key: "reach:" + d.ID, Lens: Reach, Level: 1, Target: d.ID, Name: name, File: file, Line: line,
			Title:   "Many places depend on this",
			Summary: fmt.Sprintf("%s is used by %s in %s. Changing how it behaves changes all of them.", d.Name, plural(len(callers), "declaration"), plural(len(pkgs), "package")),
			Why:     "This is load-bearing code. Nothing is wrong with it, but a change here ripples far, so it needs more care than the code around it: stable names and signatures, and tests at this boundary.",
			Do: []string{
				"Keep its name, parameters and results stable; add a new function rather than changing this one's meaning.",
				"Make sure tests cover it directly, not only through its callers.",
				"Before any change, list its callers on the map and decide which need to change too.",
			},
			Check:   "A change to it comes with tests for it, and its callers still build and pass.",
			Facts:   []Fact{{"declarations that use it", len(callers), reachCallers}, {"packages that use it", len(pkgs), 0}, {"tests that call it", testCallers(s, d), 0}},
			Related: rel[:min(len(rel), maxRelated)],
		})
	}
	return out
}

// structure flags packages that hold most of the code, and dependencies on
// less stable packages.
func structure(s *code.Snapshot, ix idx, fs []smell.Finding) []Prognosis {
	var out []Prognosis
	for _, p := range s.Packages {
		n := p.Lines()
		if ix.lines < dominantLines || n*100 < ix.lines*dominantShare {
			continue
		}
		var files []*code.File
		for _, f := range p.Files {
			if !f.Test {
				files = append(files, f)
			}
		}
		slices.SortFunc(files, func(a, b *code.File) int { return cmp.Compare(b.Lines, a.Lines) })
		var rel []Ref
		for _, f := range files[:min(len(files), maxRelated)] {
			rel = append(rel, Ref{ID: f.Path, Name: path.Base(f.Path), Why: fmt.Sprintf("%d lines, %s lately", f.Lines, plural(f.Churn, "commit"))})
		}
		share := n * 100 / ix.lines
		out = append(out, Prognosis{
			Key: "dominant:" + p.Path, Lens: Structure, Level: 2, Target: p.Path, Name: pkgShort(s, p.Path),
			Title:   "One package holds most of the code",
			Summary: fmt.Sprintf("%s holds %d%% of the module's code, in %s. Everything in it can reach everything else.", pkgShort(s, p.Path), share, plural(len(files), "file")),
			Why:     "Packages are walls: code outside a package can only use what it exports. When most code sits in one package, there are no walls, so any part can come to depend on any other, and a change anywhere can break something far away. Smaller packages with clear jobs keep changes local.",
			Do: []string{
				"Find groups of files that work on the same thing; the largest files listed below are a start.",
				"Move one group into its own package (under internal/ if outsiders should not import it), exporting only what the rest needs.",
				"Repeat one group at a time, building and testing after each move.",
			},
			Check:   fmt.Sprintf("No package holds more than %d%% of the code, and each package's name says what it does.", dominantShare),
			Facts:   []Fact{{"share of the module's lines, %", share, dominantShare}, {"lines", n, 0}, {"files", len(files), 0}},
			Related: rel,
		})
	}
	for _, f := range fs {
		if f.Rule != smell.UnstableDep {
			continue
		}
		out = append(out, Prognosis{
			Key: "unstable:" + f.Package + ">" + f.Target, Lens: Structure, Level: 2, Target: f.Package, Name: pkgShort(s, f.Package),
			Title:   "Depends on something that changes more than it does",
			Summary: fmt.Sprintf("%s imports %s, which is less stable. Changes there will keep forcing changes here.", pkgShort(s, f.Package), pkgShort(s, f.Target)),
			Why:     "A package that many others rely on should only rely on packages that change even less. Otherwise the churn of the less stable one ripples through it to everything above.",
			Do: []string{
				"Find what it uses from the other package.",
				"Define a small interface in this package for that use, and have the other package satisfy it.",
				"Or move the shared piece down into a stable package both can import.",
			},
			Check:   "The import is gone, or the imported package is now the more stable of the two.",
			Facts:   []Fact{{"instability of the imported package, %", f.Measure, 0}, {"instability of this package, %", f.Limit, 0}},
			Related: []Ref{{ID: f.Target, Name: pkgShort(s, f.Target), Why: "the less stable package it imports"}},
		})
	}
	return out
}

// smells covers the smells that slow every change: long parameter lists,
// large files and untested packages.
func smells(s *code.Snapshot, ix idx, fs []smell.Finding) []Prognosis {
	var out []Prognosis
	params := map[string][]*code.Decl{}
	for _, f := range fs {
		switch f.Rule {
		case smell.ManyParams:
			if d := s.Decl(f.Decl); d != nil {
				params[d.Package] = append(params[d.Package], d)
			}
		case smell.LargeFile:
			out = append(out, largeFile(s, ix, f))
		case smell.UntestedPackage:
			p := s.Package(f.Package)
			if p == nil {
				continue
			}
			out = append(out, Prognosis{
				Key: "untested-pkg:" + p.Path, Lens: Tests, Level: 1 + b2i(p.Lines() >= 200), Target: p.Path, Name: pkgShort(s, p.Path),
				Title:   "No tests at all",
				Summary: fmt.Sprintf("Nothing tests %s (%d lines). Breaking it would go unnoticed.", pkgShort(s, p.Path), p.Lines()),
				Why:     "Without tests, the only way to know a change works is to try it by hand, every time. Even one test that runs the package's main path catches the breakage that matters most.",
				Do: []string{
					"Write one test that runs the package's main path end to end.",
					"Add a test whenever you fix a bug here, so it stays fixed.",
				},
				Check: "go test reports at least one test for this package, and it fails when you break the main path on purpose.",
				Facts: []Fact{{"lines", p.Lines(), 0}, {"test files", 0, 0}},
			})
		}
	}
	for _, pkg := range slices.Sorted(maps.Keys(params)) {
		ds := params[pkg]
		slices.SortFunc(ds, func(a, b *code.Decl) int { return cmp.Or(cmp.Compare(b.Params, a.Params), cmp.Compare(a.ID, b.ID)) })
		if len(ds) < clusterMin {
			for _, d := range ds {
				out = append(out, manyParams(s, d))
			}
			continue
		}
		top := ds[0]
		var rel []Ref
		for _, d := range ds[1:min(len(ds), maxRelated+1)] {
			rel = append(rel, Ref{ID: d.ID, Name: d.Name, Why: fmt.Sprintf("takes %d parameters", d.Params)})
		}
		name, file, line := declRef(top)
		out = append(out, Prognosis{
			Key: "params:" + pkg, Lens: Tests, Level: 2, Target: top.ID, Name: name, File: file, Line: line,
			Title:   "The same data passed around by hand",
			Summary: fmt.Sprintf("%d functions in %s take more than %d parameters, up to %d in %s. They likely pass the same values from one to the next.", len(ds), pkgShort(s, pkg), smell.ManyParamsLimit, top.Params, top.Name),
			Why:     "A long parameter list is easy to get wrong: two values of the same type can be swapped and still compile. When several functions pass the same group of values along, that group wants to be one type with a name.",
			Do: []string{
				"Compare the parameter lists below and find the values they share.",
				"Group the shared values into one struct with a clear name, and pass that instead.",
				"Change one function at a time, building and testing after each.",
			},
			Check:   fmt.Sprintf("Each of these functions takes %d parameters or fewer, and the tests pass.", smell.ManyParamsLimit),
			Facts:   []Fact{{"functions with too many parameters", len(ds), clusterMin}, {"most parameters in one", top.Params, smell.ManyParamsLimit}},
			Related: rel,
		})
	}
	return out
}

func manyParams(s *code.Snapshot, d *code.Decl) Prognosis {
	name, file, line := declRef(d)
	return Prognosis{
		Key: "params:" + d.ID, Lens: Tests, Level: 1, Target: d.ID, Name: name, File: file, Line: line,
		Title:   "Too many parameters",
		Summary: fmt.Sprintf("%s takes %d parameters. Callers can mix them up without the compiler noticing.", d.Name, d.Params),
		Why:     "A long parameter list usually means the function does several jobs, or that some of its parameters belong together as one type.",
		Do:      []string{"Group parameters that always travel together into a struct.", "If it does several jobs, split it, so each part needs fewer inputs."},
		Check:   fmt.Sprintf("It takes %d parameters or fewer.", smell.ManyParamsLimit),
		Facts:   []Fact{{"parameters", d.Params, smell.ManyParamsLimit}},
		Related: callerRefs(s, d, "calls it, and would change with its parameters"),
	}
}

func largeFile(s *code.Snapshot, ix idx, f smell.Finding) Prognosis {
	file := ix.file[f.File]
	var ds []*code.Decl
	if file != nil {
		ds = slices.Clone(file.Decls)
	}
	slices.SortFunc(ds, func(a, b *code.Decl) int { return cmp.Compare(b.Lines, a.Lines) })
	var rel []Ref
	for _, d := range ds[:min(len(ds), maxRelated)] {
		rel = append(rel, Ref{ID: d.ID, Name: d.Name, Why: fmt.Sprintf("%d lines", d.Lines)})
	}
	churn := 0
	if file != nil {
		churn = file.Churn
	}
	return Prognosis{
		Key: "large-file:" + f.File, Lens: Tests, Level: 1 + b2i(f.Severity == smell.High || churn >= ix.hotChurn), Target: f.File, Name: path.Base(f.File), File: f.File,
		Title:   "A file doing too many things",
		Summary: fmt.Sprintf("%s has %d lines of code, past %d. Finding your way around it takes longer each time.", path.Base(f.File), f.Measure, f.Limit),
		Why:     "A large file usually mixes several concerns. Each edit means scrolling past code that has nothing to do with it, and two people changing it at once collide.",
		Do:      []string{"Group its declarations by what they work on.", "Move each group into its own file in the same package; nothing outside changes.", "Name each file for its group."},
		Check:   fmt.Sprintf("No file in the package is over %d lines, and the package builds unchanged.", smell.LargeFileLimit),
		Facts:   []Fact{{"lines of code", f.Measure, f.Limit}, {"commits lately", churn, 0}},
		Related: rel,
	}
}

// Prompt is the task given to a coding agent asked to treat the prognosis.
// check is the command the change must leave passing, and where it runs.
func (p Prognosis) Prompt(check, where string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Improve the health of this Go module at one place.\n\nProblem: %s\nWhere: %s", p.Title, p.Name)
	if p.File != "" {
		fmt.Fprintf(&b, " (%s:%d)", p.File, p.Line)
	}
	fmt.Fprintf(&b, "\nWhat we see: %s\nWhy it matters: %s\n\nWhat to do:\n", p.Summary, p.Why)
	for i, d := range p.Do {
		fmt.Fprintf(&b, "%d. %s\n", i+1, d)
	}
	if len(p.Related) > 0 {
		b.WriteString("\nRelated places:\n")
		for _, r := range p.Related {
			fmt.Fprintf(&b, "- %s: %s\n", r.Name, r.Why)
		}
	}
	fmt.Fprintf(&b, "\nDone when: %s\n\nRules: keep behaviour the same. Keep exported names and signatures unless the problem is about them. Follow the repository's CLAUDE.md, AGENTS.md and CONVENTIONS.md where they exist. Run `%s` from %s before you finish and leave it passing: it is this repository's own check. Do not commit and do not push. Touch only what this problem needs. End with three lines saying what you changed and why.\n", p.Check, check, where)
	return b.String()
}

func pkgShort(s *code.Snapshot, p string) string {
	if p == s.Module {
		return path.Base(p)
	}
	return strings.TrimPrefix(p, s.Module+"/")
}

func b2i(b bool) Level {
	if b {
		return 1
	}
	return 0
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
