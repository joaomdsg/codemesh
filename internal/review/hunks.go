package review

import (
	"slices"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
)

type span struct{ start, n int }

func spansOf(d gitx.FileDiff) (newSpans, oldSpans []span) {
	for _, h := range d.Hunks {
		newSpans = append(newSpans, span{h.NewStart, h.NewLines})
		oldSpans = append(oldSpans, span{h.OldStart, h.OldLines})
	}
	return newSpans, oldSpans
}

// touchedIn returns the IDs of f's decls that a hunk side touches.
func touchedIn(f *code.File, spans []span) []string {
	if f == nil {
		return nil
	}
	var ids []string
	for _, d := range f.Decls {
		if hits(d.Start, d.End, spans) {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// orderedSet keeps first-seen order, so units come out in diff order before
// the sort and ties stay stable.
type orderedSet struct {
	seen map[string]bool
	list []string
}

func (s *orderedSet) add(ids ...string) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	for _, id := range ids {
		if !s.seen[id] {
			s.seen[id] = true
			s.list = append(s.list, id)
		}
	}
}

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
