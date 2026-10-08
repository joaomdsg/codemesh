// Package review turns a diff into a queue of change units: one per
// declaration added, removed or modified, triaged into lanes and ranked by
// risk inside each lane.
package review

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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
	Contract Lane = iota // exported API added, removed or re-signed
	Logic                // behaviour changes
	Tests                // test code
	Other                // non-Go files
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
	Callers  []string // decl IDs calling the head version
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
	touched := map[string]bool{}
	var ids []string
	touch := func(id string) {
		if !touched[id] {
			touched[id] = true
			ids = append(ids, id)
		}
	}
	rev := &Review{}
	for _, d := range in.Diffs {
		p := cmp.Or(d.NewPath, d.OldPath)
		hf, bf := file(in.Head, d.NewPath), file(in.Base, d.OldPath)
		if path.Ext(p) != ".go" || hf == nil && bf == nil || hf != nil && hf.Generated {
			rev.Units = append(rev.Units, b.fileUnit(d, hf))
			continue
		}
		var newSpans, oldSpans []span
		for _, h := range d.Hunks {
			newSpans = append(newSpans, span{h.NewStart, h.NewLines})
			oldSpans = append(oldSpans, span{h.OldStart, h.OldLines})
		}
		if hf != nil {
			for _, dc := range hf.Decls {
				if hits(dc.Start, dc.End, newSpans) {
					touch(dc.ID)
				}
			}
		}
		if bf != nil {
			for _, dc := range bf.Decls {
				if hits(dc.Start, dc.End, oldSpans) {
					touch(dc.ID)
				}
			}
		}
		if u := b.looseUnit(d, hf, bf, newSpans, oldSpans); u != nil {
			rev.Units = append(rev.Units, u)
		}
	}
	for _, id := range ids {
		rev.Units = append(rev.Units, b.declUnit(in.Base.Decl(id), in.Head.Decl(id)))
	}
	rev.Units = pairMoves(rev.Units)
	slices.SortStableFunc(rev.Units, func(a, b *Unit) int {
		return cmp.Or(cmp.Compare(a.Lane, b.Lane), cmp.Compare(b.Risk, a.Risk), cmp.Compare(a.File, b.File), cmp.Compare(a.Name, b.Name))
	})
	rev.Introduced, rev.Fixed = delta(in.BaseFindings, in.HeadFindings)
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
	data, err := os.ReadFile(p)
	var l []string
	if err == nil {
		l = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	b.src[p] = l
	return l
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

func (b *builder) rank(u *Unit, old, new *code.Decl) {
	resigned := old != nil && new != nil && old.Signature != new.Signature
	switch {
	case cmp.Or(new, old).Test:
		u.Lane = Tests
	case u.Exported && (u.Change != Modified || resigned):
		u.Lane = Contract
	default:
		u.Lane = Logic
	}
	risk := min(u.Added+u.Deleted, 40) / 4
	if resigned {
		u.Reasons = append(u.Reasons, "signature changed")
		risk += 6
	}
	if u.Change == Removed {
		risk += 4
	}
	if new != nil {
		u.Callers = new.Callers
		if n := len(new.Callers); n > 0 {
			u.Reasons = append(u.Reasons, plural(n, "caller"))
			risk += 3 * min(n, 10)
		}
	}
	if u.Exported {
		u.Reasons = append(u.Reasons, "exported")
		risk += 4
	}
	if new != nil && (new.Kind == code.Func || new.Kind == code.Method) {
		was := 0
		if old != nil {
			was = old.Complexity
		}
		if d := new.Complexity - was; old != nil && d != 0 {
			u.Reasons = append(u.Reasons, fmt.Sprintf("complexity %d (%+d)", new.Complexity, d))
			risk += 2 * max(d, 0)
		} else if new.Complexity > 10 {
			u.Reasons = append(u.Reasons, fmt.Sprintf("complexity %d", new.Complexity))
		}
		risk += new.Complexity / 2
		if !new.Test && !b.testedDirectly(new) {
			u.Reasons = append(u.Reasons, "no direct test")
			risk += 5
		}
	}
	u.Risk = risk
}

func (b *builder) testedDirectly(d *code.Decl) bool {
	return slices.ContainsFunc(d.Callers, func(id string) bool {
		c := b.in.Head.Decl(id)
		return c != nil && c.Test
	})
}

// fileUnit covers a non-Go or generated file as one unit.
func (b *builder) fileUnit(d gitx.FileDiff, hf *code.File) *Unit {
	p := cmp.Or(d.NewPath, d.OldPath)
	u := &Unit{ID: p, Name: p, File: p, Change: Modified, Lane: Other}
	switch d.Status {
	case gitx.Added:
		u.Change = Added
	case gitx.Deleted:
		u.Change = Removed
	case gitx.Renamed:
		u.Change = Moved
		u.Reasons = append(u.Reasons, "renamed from "+d.OldPath)
	}
	oldText, newText := b.lines(b.in.Base, d.OldPath), b.lines(b.in.Head, d.NewPath)
	switch {
	case d.Binary:
		u.Reasons = append(u.Reasons, "binary")
	case len(oldText)*len(newText) > maxCells:
		u.Reasons = append(u.Reasons, "too large to show")
	default:
		u.setLines(diffLines(oldText, newText, 1, 1))
	}
	switch {
	case hf != nil && hf.Generated:
		u.Lane = Noise
		u.Reasons = append(u.Reasons, "generated")
	case path.Base(p) == "go.sum":
		u.Lane = Noise
		u.Reasons = append(u.Reasons, "checksums")
	case path.Base(p) == "go.mod":
		u.Lane = Contract
		u.Reasons = append(u.Reasons, "dependencies")
	}
	u.Risk = min(u.Added+u.Deleted, 40) / 4
	u.Key = key(u.ID, strings.Join(newText, "\n")+"\x00"+strings.Join(oldText, "\n"))
	return u
}

// looseUnit gathers changed lines outside any declaration: imports and
// free-floating comments. Blank lines alone do not make a unit.
func (b *builder) looseUnit(d gitx.FileDiff, hf, bf *code.File, newSpans, oldSpans []span) *Unit {
	var lines []Line
	collect := func(f *code.File, s *code.Snapshot, rel string, spans []span, op byte) {
		if f == nil {
			return
		}
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
				lines = append(lines, l)
			}
		}
	}
	collect(bf, b.in.Base, d.OldPath, oldSpans, '-')
	collect(hf, b.in.Head, d.NewPath, newSpans, '+')
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

// pairMoves joins an added and a removed unit with the same name and source
// shape, which is a declaration moved to another package.
func pairMoves(units []*Unit) []*Unit {
	out := units[:0]
	removed := map[string]*Unit{}
	for _, u := range units {
		if u.Change == Removed && u.Kind != "" {
			removed[u.Name] = u
		}
	}
	gone := map[*Unit]bool{}
	for _, u := range units {
		r := removed[u.Name]
		if u.Change != Added || r == nil || gone[r] || !sameText(r, u) {
			continue
		}
		u.Change, u.Lane, u.Risk = Moved, Noise, 0
		u.Reasons = []string{"moved from " + r.File}
		gone[r] = true
	}
	for _, u := range units {
		if !gone[u] {
			out = append(out, u)
		}
	}
	return out
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

func delta(base, head []smell.Finding) (introduced, fixed []smell.Finding) {
	id := func(f smell.Finding) string {
		return string(f.Rule) + "|" + f.Package + "|" + f.File + "|" + cmp.Or(f.Decl, f.Subject)
	}
	in := func(list []smell.Finding) map[string]bool {
		m := map[string]bool{}
		for _, f := range list {
			m[id(f)] = true
		}
		return m
	}
	inBase, inHead := in(base), in(head)
	for _, f := range head {
		if !inBase[id(f)] {
			introduced = append(introduced, f)
		}
	}
	for _, f := range base {
		if !inHead[id(f)] {
			fixed = append(fixed, f)
		}
	}
	return introduced, fixed
}

type span struct{ start, n int }

// hits reports whether a hunk side touches lines [start, end]. With -U0 a
// side of zero lines is a point after line s.
func hits(start, end int, spans []span) bool {
	for _, s := range spans {
		if s.n == 0 {
			if start <= s.start && s.start+1 <= end {
				return true
			}
			continue
		}
		if s.start <= end && s.start+s.n-1 >= start {
			return true
		}
	}
	return false
}

func inDecl(f *code.File, line int) bool {
	return slices.ContainsFunc(f.Decls, func(d *code.Decl) bool { return d.Start <= line && line <= d.End })
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
