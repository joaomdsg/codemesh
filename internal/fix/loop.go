package fix

import (
	"context"
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

// How a run's rounds ended; "" while it runs and when it was stopped. A round
// that left more is undone however the loop ended.
const (
	clean   = "clean"   // the last round left nothing
	stalled = "stalled" // a round left as many as the one before
	undone  = "undone"  // a round left more than the one before, and was undone
	capped  = "capped"  // maxRounds rounds ran
	failed  = "failed"  // Claude failed in a round after the first
	halted  = "halted"  // stopped during a round's checks; the round finished
	cut     = "cut"     // stopped while Claude worked; the round was cut short
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
	case n > 1 && left > prev:
		return false, undone
	case n > 1 && left == prev:
		return false, stalled
	case n >= maxRounds:
		return false, capped
	}
	return true, ""
}

// leftover lists what a change left to do, and counts it: smells it
// introduced, the problem if it is still there, and a check it broke. A check
// that failed before the run is not the run's to fix.
func leftover(p prognosis.Prognosis, check string, before, after *Side, rev *review.Review) ([]string, int) {
	var out []string
	total := len(rev.Introduced)
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
		total++
	}
	if before.CheckOK && !after.CheckOK {
		out = append(out, fmt.Sprintf("`%s` now fails. Its output ends:\n%s", check, after.Output))
		total++
	}
	return out, total
}

// outcome is how a round ended: its analysed result, how much it left,
// and the commit keep made of it.
type outcome struct {
	after           *Side
	rev             *review.Review
	left            int
	stopped, failed bool
	sha             string
	summary         string // Claude's summary as the round was kept
	summaryRound    int
}

// settle ends round n: it undoes the round when it left more than the one
// kept before it, even when it failed or was stopped, finishes the run when
// Claude does not go again, and otherwise keeps the round. It reports
// whether Claude goes again.
func (r *Run) settle(ctx context.Context, n int, now outcome, kept *outcome) bool {
	more, worse := r.endRound(now.left, now.stopped, now.failed)
	if worse {
		if err := r.undo(ctx, kept.sha); err != nil {
			r.fail(err)
			return false
		}
		now = *kept
		r.set(func() { r.summary, r.summaryRound, r.undone = now.summary, now.summaryRound, n })
	}
	if !more {
		r.finish(now.after, now.rev)
		return false
	}
	sha, err := r.keep(ctx, n)
	if err != nil {
		r.fail(err)
		return false
	}
	now.sha = sha
	r.mu.Lock()
	now.summary, now.summaryRound = r.summary, r.summaryRound
	r.mu.Unlock()
	*kept = now
	return true
}

// keep commits the worktree as it stands, so a worse round can be undone
// back to it. A pull request folds these commits into one.
func (r *Run) keep(ctx context.Context, n int) (string, error) {
	if err := r.git(ctx, "add", "--all"); err != nil {
		return "", err
	}
	if err := r.commit(ctx, "--no-verify", "--allow-empty", "-m", fmt.Sprintf("codemesh round %d", n)); err != nil {
		return "", err
	}
	out, err := command(ctx, r.wt, nil, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// undo puts the worktree back to a commit keep made.
func (r *Run) undo(ctx context.Context, rev string) error {
	if err := r.git(ctx, "reset", "--quiet", "--hard", rev); err != nil {
		return err
	}
	return r.git(ctx, "clean", "--quiet", "-fd")
}

// followUp is the prompt that resumes Claude's session with what its
// change left.
func followUp(left []string) string {
	var w strings.Builder
	w.WriteString("Your change was analysed and checked. It left these, which need another pass:\n")
	for _, l := range left {
		fmt.Fprintf(&w, "- %s\n", l)
	}
	w.WriteString("\nA round that leaves more than this one is undone.")
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
// was not clean; for a single round, only that it was stopped.
func (s Snapshot) RoundsNote() string {
	if len(s.rounds) < 2 {
		if s.ended == halted {
			return "Stopped by you; Claude did not go again."
		}
		return ""
	}
	left := make([]string, len(s.rounds))
	for i, n := range s.rounds {
		left[i] = strconv.Itoa(n)
	}
	last := len(s.rounds)
	if s.ended == cut {
		left[last-1] += " (stopped)"
	}
	text := fmt.Sprintf("%d rounds, left after each: %s.", last, strings.Join(left, " → "))
	switch s.ended {
	case stalled:
		text += " Stopped: as many left as the round before."
	case capped:
		text += fmt.Sprintf(" Stopped at the %d-round limit.", maxRounds)
	case failed:
		why, _, _ := strings.Cut(s.roundErr, "\n")
		text += fmt.Sprintf(" Stopped: round %d ended with an error: %s.", last, strings.TrimRight(why, ": ."))
	case halted:
		text += " Stopped by you; Claude did not go again."
	case cut:
		text += fmt.Sprintf(" Stopped by you during round %d.", last)
	}
	if s.Undone > 0 {
		text += fmt.Sprintf(" Round %d left more, so its changes were undone.", s.Undone)
	}
	return text
}

// SummaryTitle heads Claude's summary, naming its round when a later round
// failed or was stopped before summing up.
func (s Snapshot) SummaryTitle() string { return "Claude's summary" + s.summaryFrom() }

func (s Snapshot) summaryFrom() string {
	if n := len(s.rounds); s.Undone == 0 && s.SummaryRound > 0 && s.SummaryRound < n {
		return fmt.Sprintf(", from round %d", s.SummaryRound)
	}
	return ""
}
