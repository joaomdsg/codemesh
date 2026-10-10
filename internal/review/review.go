// Package review turns a diff into a queue of change units: one per
// declaration added, removed or modified, triaged into lanes and ranked by
// risk inside each lane.
package review

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// Lane groups units by how much reading they need, most first.
type Lane int

const (
	Contract Lane = iota // exported API added, removed or re-signed, and go.mod
	Logic                // behaviour changes
	Tests                // test code
	Other                // non-Go files and long string literals
	Noise                // no change in meaning: comments, layout, moves, generated
)

var laneNames = [...]string{"Contract", "Logic", "Tests", "Other", "Noise"}

func (l Lane) String() string { return laneNames[l] }

// Lanes lists every lane in reading order.
var Lanes = []Lane{Contract, Logic, Tests, Other, Noise}

// Change says what happened to a unit.
type Change string

const (
	Added    Change = "added"
	Removed  Change = "removed"
	Modified Change = "modified"
	Moved    Change = "moved"
)

// Line is one line of a unit's own diff. Old and New are 1-based file line
// numbers, 0 on the side the line is absent from.
type Line struct {
	Op       byte // ' ', '+' or '-'
	Old, New int
	Text     string
}

// Unit is one reviewable change: a declaration, or a whole non-Go file.
type Unit struct {
	ID       string // decl ID, or the file path for file units
	Key      string // ID + "@" + hash of the unit's source; changes when the source does
	Name     string
	Package  string
	File     string // path on the head side, else the base side
	Kind     code.Kind
	Exported bool
	Change   Change
	Lane     Lane
	Risk     int
	Reasons  []string
	Callers  []string        // decl IDs calling the head version
	Smells   []smell.Finding // smells this change introduces here
	Lines    []Line
	Added    int
	Deleted  int
}

// Review is the triaged change between a base and the working tree.
type Review struct {
	Units      []*Unit
	Introduced []smell.Finding // smells in head and not in base
	Fixed      []smell.Finding // smells in base and not in head
}

// Input is what Build needs: both snapshots, the diff between them and the
// findings of each.
type Input struct {
	Base, Head                 *code.Snapshot
	Diffs                      []gitx.FileDiff
	BaseFindings, HeadFindings []smell.Finding
}

// Lane returns the units of one lane, in risk order.
func (r *Review) Lane(l Lane) []*Unit {
	var out []*Unit
	for _, u := range r.Units {
		if u.Lane == l {
			out = append(out, u)
		}
	}
	return out
}

// Build triages the diff into units.
func Build(in Input) *Review {
	b := &builder{in: in, src: map[string][]string{}}
	var touched orderedSet
	rev := &Review{}
	for _, d := range in.Diffs {
		p := cmp.Or(d.NewPath, d.OldPath)
		hf, bf := file(in.Head, d.NewPath), file(in.Base, d.OldPath)
		if ext := path.Ext(p); ext != ".go" && ext != ".jl" || hf == nil && bf == nil || hf != nil && hf.Generated {
			rev.Units = append(rev.Units, b.fileUnit(d, hf))
			continue
		}
		newSpans, oldSpans := spansOf(d)
		touched.add(touchedIn(hf, newSpans)...)
		touched.add(touchedIn(bf, oldSpans)...)
		if u := b.looseUnit(d, hf, bf, newSpans, oldSpans); u != nil {
			rev.Units = append(rev.Units, u)
		}
	}
	for _, id := range touched.list {
		rev.Units = append(rev.Units, b.declUnit(in.Base.Decl(id), in.Head.Decl(id)))
	}
	rev.Units = pairMoves(b, rev.Units)
	slices.SortStableFunc(rev.Units, func(a, b *Unit) int {
		return cmp.Or(cmp.Compare(a.Lane, b.Lane), cmp.Compare(b.Risk, a.Risk), cmp.Compare(a.File, b.File), cmp.Compare(a.Name, b.Name))
	})
	rev.Introduced, rev.Fixed = smell.Delta(in.BaseFindings, in.HeadFindings)
	attach(rev)
	return rev
}

type builder struct {
	in  Input
	src map[string][]string // absolute path → lines
}

func (b *builder) lines(s *code.Snapshot, rel string) []string {
	if s == nil || rel == "" {
		return nil
	}
	p := filepath.Join(s.Dir, filepath.FromSlash(rel))
	if l, ok := b.src[p]; ok {
		return l
	}
	l := readLines(p)
	b.src[p] = l
	return l
}

// readLines reads a file's lines as git stores it.
func readLines(p string) []string {
	data, err := gitx.Content(p)
	if err != nil || data == nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (b *builder) text(s *code.Snapshot, d *code.Decl) []string {
	if d == nil {
		return nil
	}
	l := b.lines(s, d.File)
	if d.End > len(l) {
		return nil
	}
	return l[d.Start-1 : d.End]
}

func (b *builder) declUnit(old, new *code.Decl) *Unit {
	cur := cmp.Or(new, old)
	u := &Unit{ID: cur.ID, Name: cur.Name, Package: cur.Package, File: cur.File, Kind: cur.Kind, Exported: cur.Exported}
	oldText, newText := b.text(b.in.Base, old), b.text(b.in.Head, new)
	oldStart, newStart := 0, 0
	if old != nil {
		oldStart = old.Start
	}
	if new != nil {
		newStart = new.Start
	}
	u.setLines(diffLines(oldText, newText, oldStart, newStart))
	u.Key = key(u.ID, cmp.Or(strings.Join(newText, "\n"), strings.Join(oldText, "\n")))

	switch {
	case old == nil:
		u.Change = Added
	case new == nil:
		u.Change = Removed
	case old.Shape == new.Shape:
		u.Change, u.Lane = Modified, Noise
		u.Reasons = []string{"comments or layout only"}
		if old.File != new.File {
			u.Change, u.Reasons = Moved, []string{"moved from " + old.File}
		}
		return u
	default:
		u.Change = Modified
	}
	b.rank(u, old, new)
	return u
}

// fileUnit covers a non-Go or generated file as one unit.
func (b *builder) fileUnit(d gitx.FileDiff, hf *code.File) *Unit {
	p := cmp.Or(d.NewPath, d.OldPath)
	u := &Unit{ID: p, Name: p, File: p}
	u.Change, u.Reasons = fileChange(d)
	oldText, newText := b.lines(b.in.Base, d.OldPath), b.lines(b.in.Head, d.NewPath)
	switch {
	case d.Binary:
		u.Reasons = append(u.Reasons, "binary")
	case len(oldText)*len(newText) > maxCells:
		u.Reasons = append(u.Reasons, "too large to show")
	default:
		u.setLines(diffLines(oldText, newText, 1, 1))
	}
	lane, why := fileLane(p, hf, oldText, newText)
	u.Lane, u.Reasons = lane, append(u.Reasons, why...)
	u.Risk = min(u.Added+u.Deleted, 40) / 4
	u.Key = key(u.ID, strings.Join(newText, "\n")+"\x00"+strings.Join(oldText, "\n"))
	return u
}

func fileChange(d gitx.FileDiff) (Change, []string) {
	switch d.Status {
	case gitx.Added:
		return Added, nil
	case gitx.Deleted:
		return Removed, nil
	case gitx.Renamed:
		return Moved, []string{"renamed from " + d.OldPath}
	}
	return Modified, nil
}

// fileLane sorts a whole file into a lane, with the reasons for it: the
// first rule that matches, else Other.
func fileLane(p string, hf *code.File, oldText, newText []string) (Lane, []string) {
	f := laneFile{p, path.Base(p), hf != nil && hf.Generated, oldText, newText}
	for _, r := range laneRules {
		if r.match(f) {
			return r.lane, r.why
		}
	}
	return Other, nil
}

type laneFile struct {
	path, base       string
	generated        bool
	oldText, newText []string
}

func (f laneFile) goCode() bool { return path.Ext(f.path) == ".go" }

// laneRules are tried in order. Go code of a nested module has no type info
// here, so it is one unit per file.
var laneRules = []struct {
	match func(laneFile) bool
	lane  Lane
	why   []string
}{
	{func(f laneFile) bool { return f.generated }, Noise, []string{"generated"}},
	{func(f laneFile) bool { return f.base == "go.sum" }, Noise, []string{"checksums"}},
	{func(f laneFile) bool { return f.path == "go.mod" || f.path == "Project.toml" }, Contract, []string{"dependencies"}},
	{func(f laneFile) bool { return f.base == "Manifest.toml" }, Noise, []string{"resolved versions"}},
	{func(f laneFile) bool { return isFixture(f.path) }, Tests, []string{"test fixture"}},
	{func(f laneFile) bool { return f.base == "go.mod" }, Other, []string{"dependencies", "in another module"}},
	{func(f laneFile) bool { return f.goCode() && sameShape(f.oldText, f.newText) }, Noise, []string{"comments or layout only"}},
	{func(f laneFile) bool { return f.goCode() && strings.HasSuffix(f.path, "_test.go") }, Tests, []string{"in another module"}},
	{laneFile.goCode, Logic, []string{"in another module"}},
}

// looseUnit gathers changed lines outside any declaration: imports and
// free-floating comments. Blank lines alone do not make a unit.
func (b *builder) looseUnit(d gitx.FileDiff, hf, bf *code.File, newSpans, oldSpans []span) *Unit {
	lines := append(b.looseLines(bf, b.in.Base, d.OldPath, oldSpans, '-'),
		b.looseLines(hf, b.in.Head, d.NewPath, newSpans, '+')...)
	if len(lines) == 0 {
		return nil
	}
	p := cmp.Or(d.NewPath, d.OldPath)
	pkg := ""
	if f := cmp.Or(hf, bf); f != nil {
		pkg = f.Package
	}
	u := &Unit{ID: p + "#file", Name: path.Base(p) + " (file level)", Package: pkg, File: p, Change: Modified, Lane: Noise,
		Reasons: []string{"imports and file-level lines"}}
	u.setLines(lines)
	var all []string
	for _, l := range lines {
		all = append(all, string(l.Op)+l.Text)
	}
	u.Key = key(u.ID, strings.Join(all, "\n"))
	return u
}

func (b *builder) looseLines(f *code.File, s *code.Snapshot, rel string, spans []span, op byte) []Line {
	if f == nil {
		return nil
	}
	var out []Line
	text := b.lines(s, rel)
	for _, sp := range spans {
		for n := sp.start; n < sp.start+sp.n && n <= len(text); n++ {
			t := strings.TrimSpace(text[n-1])
			// A package clause only changes with the file itself.
			if t == "" || strings.HasPrefix(t, "package ") || inDecl(f, n) {
				continue
			}
			l := Line{Op: op, Text: text[n-1]}
			if op == '-' {
				l.Old = n
			} else {
				l.New = n
			}
			out = append(out, l)
		}
	}
	return out
}

func (u *Unit) setLines(lines []Line) {
	u.Lines = lines
	for _, l := range lines {
		switch l.Op {
		case '+':
			u.Added++
		case '-':
			u.Deleted++
		}
	}
}

// pairMoves joins an added and a removed declaration of the same name and
// kind when the name is unique among both: a declaration moved to another
// package. Unchanged, it is noise; edited, it is one modified unit with its
// diff, not a removal plus an addition.
func pairMoves(b *builder, units []*Unit) []*Unit {
	adds, rems := groupByName(units)
	gone := map[*Unit]bool{}
	for k, as := range adds {
		rs := rems[k]
		if len(as) != 1 || len(rs) != 1 {
			continue
		}
		u, r := as[0], rs[0]
		if len(u.Lines) == 0 || len(r.Lines) == 0 {
			continue
		}
		gone[r] = true
		if sameText(r, u) {
			u.Change, u.Lane, u.Risk = Moved, Noise, 0
			u.Reasons = []string{"moved from " + r.File}
			continue
		}
		b.remerge(u, r)
	}
	return slices.DeleteFunc(units, func(u *Unit) bool { return gone[u] })
}

type nameKind struct {
	name string
	kind code.Kind
}

// groupByName groups added and removed declaration units by name and kind.
func groupByName(units []*Unit) (adds, rems map[nameKind][]*Unit) {
	adds, rems = map[nameKind][]*Unit{}, map[nameKind][]*Unit{}
	for _, u := range units {
		k := nameKind{u.Name, u.Kind}
		switch {
		case u.Kind == "":
		case u.Change == Removed:
			rems[k] = append(rems[k], u)
		case u.Change == Added:
			adds[k] = append(adds[k], u)
		}
	}
	return adds, rems
}

// remerge turns an added unit into the modified version of a removed one.
func (b *builder) remerge(u, r *Unit) {
	var oldText, newText []string
	for _, l := range r.Lines {
		oldText = append(oldText, l.Text)
	}
	for _, l := range u.Lines {
		newText = append(newText, l.Text)
	}
	u.Added, u.Deleted = 0, 0
	u.setLines(diffLines(oldText, newText, r.Lines[0].Old, u.Lines[0].New))
	u.Change = Modified
	u.Reasons = append([]string{"moved from " + r.File}, u.Reasons...)
}

func sameText(removed, added *Unit) bool {
	var a, b []string
	for _, l := range removed.Lines {
		a = append(a, l.Text)
	}
	for _, l := range added.Lines {
		b = append(b, l.Text)
	}
	return slices.Equal(a, b)
}

// sameShape reports whether two versions of a Go file differ only in
// comments and layout. A side that is missing or does not parse differs.
func sameShape(old, new []string) bool {
	if len(old) == 0 || len(new) == 0 {
		return false
	}
	a, err := code.FileShape(strings.Join(old, "\n"))
	if err != nil {
		return false
	}
	b, err := code.FileShape(strings.Join(new, "\n"))
	return err == nil && a == b
}

func isFixture(p string) bool {
	return strings.HasPrefix(p, "testdata/") || strings.Contains(p, "/testdata/")
}

func file(s *code.Snapshot, rel string) *code.File {
	if s == nil || rel == "" {
		return nil
	}
	for _, p := range s.Packages {
		for _, f := range p.Files {
			if f.Path == rel {
				return f
			}
		}
	}
	return nil
}

func key(id, text string) string {
	sum := sha256.Sum256([]byte(text))
	return id + "@" + hex.EncodeToString(sum[:6])
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
