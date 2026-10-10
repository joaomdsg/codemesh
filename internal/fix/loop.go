package fix

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
)

// maxRounds bounds how often a run hands Claude what its change left: each
// round is another paid turn over a growing conversation.
const maxRounds = 5

// maxListed caps the new smells one follow-up names.
const maxListed = 20

// How a run's rounds ended; "" while it runs and when it was stopped.
const (
	clean   = "clean"   // the last round left nothing
	stalled = "stalled" // a round left no fewer than the one before
	capped  = "capped"  // maxRounds rounds ran
	failed  = "failed"  // Claude failed in a round after the first
)

// nextRound decides whether Claude goes again after round n left left and
// the round before it left prev.
func nextRound(n, prev, left int, stopped, broke bool) (more bool, ended string) {
	switch {
	case stopped:
		return false, ""
	case broke:
		return false, failed
	case left == 0:
		return false, clean
	case n > 1 && left >= prev:
		return false, stalled
	case n >= maxRounds:
		return false, capped
	}
	return true, ""
}

// leftover lists what a change left to do: smells it introduced, the
// problem if it is still there, and a check it broke. A check that failed
// before the run is not the run's to fix.
func leftover(p prognosis.Prognosis, check string, before, after *Side, rev *review.Review) []string {
	var out []string
	for i, f := range rev.Introduced {
		// A long list buries the rest; the next round brings the remainder.
		if i == maxListed {
			out = append(out, fmt.Sprintf("%d more new smells", len(rev.Introduced)-maxListed))
			break
		}
		out = append(out, fmt.Sprintf("New smell: %s %s (%s) at %s:%d", f.Rule, f.Subject, f.Detail, f.File, f.Line))
	}
	if slices.ContainsFunc(after.Prognoses, func(g prognosis.Prognosis) bool { return g.Key == p.Key }) {
		out = append(out, fmt.Sprintf("The problem is still there: %s, %s [%s]", p.Title, p.Name, p.Key))
	}
	if before.CheckOK && !after.CheckOK {
		out = append(out, fmt.Sprintf("`%s` now fails. Its output ends:\n%s", check, after.Output))
	}
	return out
}

// followUp is the prompt that resumes Claude's session with what its
// change left.
func followUp(left []string) string {
	var w strings.Builder
	w.WriteString("Your change was analysed and checked. It left these, which need another pass:\n")
	for _, l := range left {
		fmt.Fprintf(&w, "- %s\n", l)
	}
	w.WriteString("\nFix them without undoing what the change achieved. Do not commit and do not push. End with three lines saying what the whole change does and why.\n")
	return w.String()
}

// RoundsLabel names the round under way once Claude went again, or how
// many rounds a finished run took; "" for a single round.
func (s Snapshot) RoundsLabel() string {
	switch {
	case s.Live() && s.Round > 1:
		return fmt.Sprintf("round %d of up to %d", s.Round, maxRounds)
	case !s.Live() && len(s.rounds) > 1:
		return fmt.Sprintf("%d rounds", len(s.rounds))
	}
	return ""
}

// RoundsNote says how much each round left and why the loop stopped when it
// was not clean; "" for a single round.
func (s Snapshot) RoundsNote() string {
	if len(s.rounds) < 2 {
		return ""
	}
	left := make([]string, len(s.rounds))
	for i, n := range s.rounds {
		left[i] = strconv.Itoa(n)
	}
	text := fmt.Sprintf("%d rounds, left after each: %s.", len(s.rounds), strings.Join(left, " → "))
	switch s.ended {
	case stalled:
		text += " Stopped: no fewer left than the round before."
	case capped:
		text += fmt.Sprintf(" Stopped at the %d-round limit.", maxRounds)
	case failed:
		text += fmt.Sprintf(" Stopped: round %d ended with an error.", len(s.rounds))
	}
	return text
}
