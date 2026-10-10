package ui

import (
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
	T0     int64          `json:"t0"`
	T1     int64          `json:"t1"`
	Reads  []string       `json:"reads,omitempty"`
	Diffs  []replayChange `json:"diffs,omitempty"`
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
	decls := map[string][]*code.Decl{}
	for _, d := range base.Snap.Decls() {
		decls[d.File] = append(decls[d.File], d)
	}
	out := replay{Map: diagOf(base, nil, ""), Steps: []replayStep{}, Now: s.Took.Milliseconds(), Live: s.Live()}
	for _, st := range s.Steps {
		rs := replayStep{Kind: st.Kind, Title: st.Title, Path: st.Path, File: st.File, Text: st.Text,
			Old: st.Old, New: st.New, Out: st.Output, Failed: st.Failed, T0: st.Start.Milliseconds(), T1: st.End.Milliseconds(), Reads: st.Reads}
		for _, c := range st.Changes {
			rs.Diffs = append(rs.Diffs, replayChange{Path: c.Path, File: c.File, Start: c.Start, Old: c.Old, New: c.New, Decls: changedDecls(decls[c.File], c)})
		}
		out.Steps = append(out.Steps, rs)
	}
	return out
}

// changedDecls spreads a change's lines over the declarations they fall in,
// by the lines the declarations held when the run started. Edits earlier in
// the run shift later lines, so on a file edited many times this is near,
// not exact.
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
