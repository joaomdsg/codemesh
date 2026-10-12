package review

import "strings"

// SharedName returns a test for units whose name, ignoring case, another unit
// of the review shares: Build and build read alike in a list.
func (r *Review) SharedName() func(*Unit) bool {
	n := map[string]int{}
	for _, u := range r.Units {
		n[strings.ToLower(u.Name)]++
	}
	return func(u *Unit) bool { return n[strings.ToLower(u.Name)] > 1 }
}

// Section is one lane of a review: its units, and how many are reviewed.
type Section struct {
	Lane  Lane
	Units []*Unit
	Done  int
}

// Section returns lane l's units, riskiest first, with how many s has marked
// reviewed.
func (r *Review) Section(l Lane, s *State) Section {
	sec := Section{Lane: l, Units: r.Lane(l)}
	for _, u := range sec.Units {
		if s.Reviewed(u.Key) {
			sec.Done++
		}
	}
	return sec
}

// NeedsReading reports whether a unit in lane l is worth reading unasked: it
// is not yet reviewed, and its lane is Contract or Logic.
func (s *State) NeedsReading(u *Unit, l Lane) bool {
	return l <= Logic && !s.Reviewed(u.Key)
}
