package ui

import (
	"math"
	"strings"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/joaomdsg/codemesh/internal/live"
)

// replay is what Claude did, step by step, over the map it started from.
type replay struct {
	Map   diag         `json:"map"`
	Steps []replayStep `json:"steps"`
	Live  bool         `json:"live"`
	Now   int64        `json:"now"` // ms since the start
}

type replayStep struct {
	Kind   string         `json:"k"`
	Title  string         `json:"title"`
	Path   string         `json:"path,omitempty"`
	File   string         `json:"file,omitempty"`
	Text   string         `json:"text,omitempty"`
	Old    string         `json:"old,omitempty"`
	New    string         `json:"new,omitempty"`
	Out    string         `json:"out,omitempty"`
	Failed bool           `json:"fail,omitempty"`
	Round  int            `json:"round,omitempty"`
	T0     int64          `json:"t0"`
	T1     int64          `json:"t1"`
	Reads  []replayRead   `json:"reads,omitempty"`
	Diffs  []replayChange `json:"diffs,omitempty"`
}

type replayRead struct {
	File  string   `json:"file"`
	From  int      `json:"from,omitempty"`
	To    int      `json:"to,omitempty"`
	Decls []string `json:"decls,omitempty"` // declarations the lines fall in; none for a whole file
}

type replayChange struct {
	Path  string         `json:"path"`
	File  string         `json:"file,omitempty"`
	Start int            `json:"at"`
	Old   string         `json:"old"`
	New   string         `json:"new"`
	Decls map[string]int `json:"decls,omitempty"` // declaration ID → lines changed in it
}

func replayOf(a *live.Analysis, s fix.Snapshot) replay {
	// The map is the tree Claude started from, redrawn from the worktree as
	// it adds files and from the tree it left at the end, so new files get
	// tiles as they are written. Until the start is analysed the live one
	// stands in. At changes with the tree so the page redraws the map.
	base := a
	switch {
	case s.After != nil:
		base = &live.Analysis{At: time.UnixMilli(2), Snap: s.After.Snap}
	case s.Now != nil:
		base = &live.Analysis{At: time.UnixMilli(int64(10 + s.NowN)), Snap: s.Now}
	case s.Before != nil:
		base = &live.Analysis{At: time.UnixMilli(1), Snap: s.Before.Snap}
	}
	// Reads and edits land on the declarations of the tree the run started
	// from, so their line numbers are taken back to it.
	start := base.Snap
	if s.Before != nil {
		start = s.Before.Snap
	}
	decls := map[string][]*code.Decl{}
	for _, d := range start.Decls() {
		decls[d.File] = append(decls[d.File], d)
	}
	return replay{Map: diagOf(base, nil, ""), Steps: stepsOf(s.Steps, decls), Now: s.Took.Milliseconds(), Live: s.Live()}
}

// stepsOf lays out the steps with the declarations each read and edit
// touched. A step's lines are as the file stood then; the edits before it
// are undone to place them on the starting tree's declarations.
func stepsOf(steps []fix.Step, decls map[string][]*code.Decl) []replayStep {
	out := []replayStep{}
	done := map[string][]fix.Change{}
	for _, st := range steps {
		rs := replayStep{Kind: st.Kind, Title: st.Title, Path: st.Path, File: st.File, Text: st.Text,
			Old: st.Old, New: st.New, Out: st.Output, Failed: st.Failed, Round: st.Round, T0: st.Start.Milliseconds(), T1: st.End.Milliseconds()}
		for _, r := range st.Reads {
			rr := replayRead{File: r.File, From: r.From, To: r.To}
			if r.From > 0 {
				to := math.MaxInt
				if r.To > 0 {
					to = back(r.To, done[r.File])
				}
				rr.Decls = readDecls(decls[r.File], back(r.From, done[r.File]), to)
			}
			rs.Reads = append(rs.Reads, rr)
		}
		for _, c := range st.Changes {
			at := c
			at.Start = back(c.Start, done[c.File])
			rs.Diffs = append(rs.Diffs, replayChange{Path: c.Path, File: c.File, Start: c.Start, Old: c.Old, New: c.New, Decls: changedDecls(decls[c.File], at)})
		}
		for _, c := range st.Changes {
			done[c.File] = append(done[c.File], c)
		}
		out = append(out, rs)
	}
	return out
}

// back takes a line through the edits made to its file, latest first, to
// the line it was before them. A line an edit added goes to where the edit
// began.
func back(line int, edits []fix.Change) int {
	for i := len(edits) - 1; i >= 0; i-- {
		from, gone, added := changeSpan(edits[i])
		switch {
		case line >= from+added:
			line += gone - added
		case line >= from:
			line = from
		}
	}
	return line
}

// readDecls lists the declarations lines from to to overlap.
func readDecls(ds []*code.Decl, from, to int) []string {
	var out []string
	for _, d := range ds {
		if d.Start <= to && d.End >= from {
			out = append(out, d.ID)
		}
	}
	return out
}

// changedDecls spreads a change's lines over the declarations they fall in,
// by the lines the declarations held when the run started.
func changedDecls(ds []*code.Decl, c fix.Change) map[string]int {
	if len(ds) == 0 {
		return nil
	}
	from, gone, added := changeSpan(c)
	out := map[string]int{}
	for _, d := range ds {
		if n := min(d.End, from+gone-1) - max(d.Start, from) + 1; n > 0 {
			out[d.ID] += n
		}
	}
	if host := hostOf(ds, from); added > 0 && host != nil {
		out[host.ID] += added
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// changeSpan trims the lines a change left as they were at both ends: the
// first line it touched, and how many lines went and came.
func changeSpan(c fix.Change) (from, gone, added int) {
	old, new := strings.Split(c.Old, "\n"), strings.Split(c.New, "\n")
	pre := 0
	for pre < len(old) && pre < len(new) && old[pre] == new[pre] {
		pre++
	}
	suf := 0
	for suf < len(old)-pre && suf < len(new)-pre && old[len(old)-1-suf] == new[len(new)-1-suf] {
		suf++
	}
	return c.Start + pre, len(old) - pre - suf, len(new) - pre - suf
}

// hostOf is where added lines land: the declaration holding the line, else the
// one before it.
func hostOf(ds []*code.Decl, line int) *code.Decl {
	var host *code.Decl
	for _, d := range ds {
		if d.Start <= line && (host == nil || d.Start > host.Start) {
			host = d
		}
	}
	return host
}
