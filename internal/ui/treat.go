package ui

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

// treat is the "let Claude try" block: a button, then the run's progress,
// then the result.
func (p *diagnosePage) treat(a *live.Analysis, g *prognosis.Prognosis, run *fix.Run) h.H {
	if run == nil {
		return h.Section(h.Class("dx-treat"),
			h.H3(h.Str("Let Claude try")),
			h.P(h.Class("hint"), h.Str("Claude Code works on a throwaway copy of the last commit and may run any command there. Your files are not changed; you see what it did and decide.")),
			h.Button(h.Class("btn"), on.Click(on.Bind(p.Fix, g.Key)), h.Str("Try a fix")),
		)
	}
	s := run.Snapshot()
	body := append([]h.H{h.Class("dx-treat"), p.runHead(g, s), runInfo(s)}, runBanners(s)...)
	// Once there is a result it comes first: whether the attempt worked is
	// the question; the replay is how it got there.
	if s.After != nil && s.Before != nil {
		body = append(body, p.result(g, s))
	}
	return h.Section(append(body, p.replayBlock(a, s)...)...)
}

func (p *diagnosePage) runHead(g *prognosis.Prognosis, s fix.Snapshot) h.H {
	note := runNote(s)
	return h.Div(h.Class("dx-run-head"),
		h.H3(h.Str(runTitle(s))),
		h.Span(h.Class("hint"), h.Str(runMeta(s))),
		h.Div(h.Class("dx-acts"),
			via.When(stoppable(s), func() h.H {
				return h.Button(h.Class("btn"), on.Click(on.Bind(p.Stop, g.Key)), h.Str("Stop"))
			}),
			p.prAct(g, s),
			via.When(!s.Live(), func() h.H {
				return h.Button(h.Class("btn"), on.Click(on.Bind(p.Discard, g.Key)), h.Str("Discard"))
			}),
		),
		via.When(note != "", func() h.H { return h.Span(h.Class("hint dx-run-note"), h.Str(note)) }),
	)
}

// stoppable reports whether Stop shows. Pressed during checks, they still
// finish; Claude just does not go again.
func stoppable(s fix.Snapshot) bool { return s.Live() && !s.Stopping }

// runNote is Claude's latest word while the run is going. It stays up for the
// whole run, so the replay below it does not shift between phases.
func runNote(s fix.Snapshot) string {
	if s.Stopping {
		return "Stopping once the checks finish."
	}
	waiting := s.Live() || s.State == fix.Stopped && s.After == nil // a stopped run still checks what it has
	if n := len(s.Events); n > 0 && waiting {
		return s.Events[n-1].Text
	}
	return ""
}

type banner struct{ class, text string }

// banners say that a run stopped early or what went wrong.
func banners(s fix.Snapshot) []banner {
	var out []banner
	if s.State == fix.Stopped {
		out = append(out, banner{"hint", "Stopped early. Below is what Claude had changed by then, checked the same way."})
	}
	if s.Err != nil {
		out = append(out, banner{"banner err", "It did not finish: " + s.Err.Error() + ". Nothing in your files changed. Discard and try again."})
	}
	if s.PR.Err != nil {
		out = append(out, banner{"banner err", "The pull request did not open: " + strings.TrimRight(s.PR.Err.Error(), ". ") + ". The change is still here; you can try again."})
	}
	return out
}

func runBanners(s fix.Snapshot) []h.H {
	var out []h.H
	for _, b := range banners(s) {
		out = append(out, h.P(h.Class(b.class), h.Str(b.text)))
	}
	return out
}

func (p *diagnosePage) replayBlock(a *live.Analysis, s fix.Snapshot) []h.H {
	if s.State == fix.Preparing && len(s.Steps) == 0 {
		return nil
	}
	return []h.H{
		feed(map[expr.Expr]any{p.Replay.Ref(): replayOf(a, s)}),
		h.Div(h.ID("dx-replay"), h.Class("rp"), h.DataIgnoreMorph(), h.TabIndex(0), h.Aria("label", "Replay of what Claude did. Left and right arrows step through it."),
			h.DataEffect(expr.Rawf("codemesh.replay(el, %s)", p.Replay.Ref()))),
	}
}

// prAct offers a finished run as a draft pull request, or says why not.
func (p *diagnosePage) prAct(g *prognosis.Prognosis, s fix.Snapshot) h.H {
	switch {
	case s.PR.URL != "":
		return h.A(h.Class("btn"), h.Href(s.PR.URL), h.Target("_blank"), h.Rel("noopener"), h.Str("Draft PR ↗"))
	case s.CanPR() && !s.PR.Opening:
		return h.Button(h.Class("btn"), on.Click(on.Bind(p.PR, g.Key)), h.Title("Pushes "+s.Base+" to origin first if origin lacks it"), h.Str("Open a draft PR"))
	}
	if why := noPR(s); why != "" {
		return h.Span(h.Class("hint"), h.Str(why))
	}
	return nil
}

// noPR says why a run offers no pull request, or that one is opening; ""
// while the run goes, when it ended without a result, or when one is offered.
func noPR(s fix.Snapshot) string {
	switch {
	case s.PR.Opening:
		return "Opening a draft PR…"
	case s.PR.URL != "" || s.CanPR() || s.State != fix.Done || s.After == nil:
		return ""
	case s.Base == "":
		return "No PR: the repository was on no branch when the run started."
	case !s.Origin:
		return "No PR: the repository has no origin remote."
	}
	return "A PR needs the checks to pass."
}

var runTitles = map[fix.State]string{
	fix.Preparing: "Checking the starting point…",
	fix.Working:   "Claude is working…",
	fix.Checking:  "Checking the result…",
	fix.Stopped:   "Stopped",
	fix.Failed:    "Did not finish",
}

func runTitle(s fix.Snapshot) string { return cmp.Or(runTitles[s.State], "Claude's attempt") }

// runMeta is the run's clock and cost: an estimate, marked ≈, while Claude
// works, and Claude's own tally once each round ends.
func runMeta(s fix.Snapshot) string {
	meta := s.Took.Round(time.Second).String()
	if r := s.RoundsLabel(); r != "" {
		meta = r + " · " + meta
	}
	usd, all := s.Usage.USD()
	if usd == 0 {
		return meta
	}
	cost := fmt.Sprintf("$%.2f", usd)
	if !s.Usage.Exact {
		cost = "≈ " + cost
	}
	if !all {
		cost += " + unpriced models"
	}
	return meta + " · " + cost
}

// runInfo says who did the work and what it took, with a fold per model.
func runInfo(s fix.Snapshot) h.H {
	u := s.Usage
	if u.Model == "" && len(u.Models) == 0 {
		// Holds the line the run info will fill, so the page does not shift
		// when Claude starts.
		if s.Editing() {
			return h.P(h.Class("dx-runinfo dx-runinfo-wait"), h.Str("Model and cost show once Claude starts."))
		}
		return nil
	}
	how := "Costs are estimated from list prices while Claude works; Claude's own tally replaces them when each round ends."
	if u.Exact {
		how = "Costs are Claude's own tally."
	}
	return h.Details(h.Class("dx-fold dx-runinfo"),
		h.Summary(h.Str(strings.Join(usageFacts(u), " · "))),
		h.Div(h.Class("dx-usage-wrap"), h.Table(h.Class("dx-usage"), h.Tbody(usageRows(u)...))),
		h.P(h.Class("hint"), h.Str(how), via.When(u.Session != "", func() h.H { return h.Str(" Session " + u.Session + ".") })),
	)
}

// usageFacts is the one-line summary of a run's usage.
func usageFacts(u fix.Usage) []string {
	var helpers []string
	for _, m := range slices.Sorted(maps.Keys(u.Models)) {
		if m != u.Model {
			helpers = append(helpers, m)
		}
	}
	facts := []string{cmp.Or(u.Model, "model not reported")}
	if len(helpers) > 0 {
		facts = append(facts, "helpers on "+strings.Join(helpers, ", "))
	}
	if u.Calls() == 0 {
		facts = append(facts, "no replies yet")
	} else {
		in, write, read, out := u.Tokens()
		facts = append(facts, plural(u.Calls(), "API call"),
			fmt.Sprintf("tokens: %s in, %s cache written, %s cache read, %s out", toks(in), toks(write), toks(read), toks(out)))
	}
	if u.Version != "" {
		facts = append(facts, "Claude Code "+u.Version)
	}
	return facts
}

// usageRows is the usage table: a header, then a row per model.
func usageRows(u fix.Usage) []h.H {
	rows := []h.H{h.Tr(h.Th(h.Str("Model")), h.Th(h.Str("Calls")), h.Th(h.Str("Input")), h.Th(h.Str("Cache written")),
		h.Th(h.Str("Cache read")), h.Th(h.Str("Output")), h.Th(h.Str("Cost")))}
	for _, name := range slices.Sorted(maps.Keys(u.Models)) {
		m := u.Models[name]
		cost := "no price"
		if m.Priced {
			cost = fmt.Sprintf("$%.2f", m.USD)
		}
		rows = append(rows, h.Tr(h.Td(h.Str(name)), h.Td(h.Str(m.Calls)), h.Td(h.Str(toks(m.Input))), h.Td(h.Str(toks(m.CacheWrite))),
			h.Td(h.Str(toks(m.CacheRead))), h.Td(h.Str(toks(m.Output))), h.Td(h.Str(cost))))
	}
	return rows
}

// toks writes a token count short: 950, 12k, 6.4M.
func toks(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
