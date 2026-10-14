package app

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/ghx"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/workspace"
)

// summary is what the inbox keeps of a review: enough to count and rank it
// without the units' diffs.
type summary struct {
	units          []unitRef
	roots          []RootUnits
	introduced     int
	added, deleted int // git's count of the whole change, not the units' own diffs
	err            error
}

type unitRef struct {
	key  string
	lane review.Lane
	risk int
}

func (s *summary) add(root string, r *review.Review) {
	for _, u := range r.Units {
		s.units = append(s.units, unitRef{u.Key, u.Lane, u.Risk})
	}
	s.roots = append(s.roots, RootUnits{root, len(r.Units)})
	s.introduced += len(r.Introduced)
}

// merge folds in another root's summary. Every root reads the same diff, so
// the line counts are not added up.
func (s *summary) merge(o *summary) {
	s.units = append(s.units, o.units...)
	s.roots = append(s.roots, o.roots...)
	s.introduced += o.introduced
	s.added, s.deleted = max(s.added, o.added), max(s.deleted, o.deleted)
}

func summarize(root string, r *review.Review, files []gitx.FileDiff) *summary {
	s := &summary{}
	s.add(root, r)
	s.added, s.deleted = changedLines(files)
	return s
}

// changedLines is how many lines the diff adds and deletes, as git counts them.
func changedLines(files []gitx.FileDiff) (added, deleted int) {
	for _, f := range files {
		for _, h := range f.Hunks {
			added += h.NewLines
			deleted += h.OldLines
		}
	}
	return added, deleted
}

// scopeFiles is the part of a diff that lies in root and in none of the roots
// nested below it, with paths relative to root, as its snapshot names its
// files. A file of another root is not the root's to read: its snapshot has no
// such file, so it would only show as an empty unit.
func scopeFiles(files []gitx.FileDiff, root string, roots []lang.Root) []gitx.FileDiff {
	prefix := root + "/"
	if root == "." {
		prefix = ""
	}
	var nested []string
	for _, r := range roots {
		if r.Dir != root && r.Dir != "." && strings.HasPrefix(r.Dir+"/", prefix) {
			nested = append(nested, r.Dir+"/")
		}
	}
	rel := func(p string) string {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || slices.ContainsFunc(nested, func(n string) bool { return strings.HasPrefix(p, n) }) {
			return ""
		}
		return rest
	}
	var out []gitx.FileDiff
	for _, f := range files {
		f.OldPath, f.NewPath = rel(f.OldPath), rel(f.NewPath)
		if f.OldPath != "" || f.NewPath != "" {
			out = append(out, f)
		}
	}
	return out
}

// Reading is what an inbox row says about a change: how much of it needs
// reading, and how far along the reviewer is.
type Reading struct {
	Ready           bool  // false until the review is built
	Err             error // the review could not be built
	Units, Done     int
	Contract, Logic int // units in those lanes not yet reviewed
	MaxRisk         int
	Introduced      int // smells the change adds
	Added, Deleted  int // lines of the whole change, as git counts them
	Roots           []RootUnits
}

// RootUnits is how many of a change's units one root holds.
type RootUnits struct {
	Dir   string
	Units int
}

// Reading is the inbox's view of one change in a repo: pull request n, or the
// working tree when n is 0. It is the same Reading the inbox rows show.
func (a *App) Reading(id string, n int) Reading {
	rs, err := a.repo(id)
	if err != nil {
		return Reading{}
	}
	if n == 0 {
		if rs.info.Path == "" {
			return Reading{}
		}
		return a.treeReading(rs)
	}
	pr, err := a.pr(rs, n, false)
	if err != nil {
		return Reading{}
	}
	return a.prReading(rs, pr)
}

// NeedsReading is true while Contract or Logic units wait.
func (r Reading) NeedsReading() bool { return r.Contract+r.Logic > 0 }

func readingOf(s *summary, st *review.State) Reading {
	if s == nil {
		return Reading{}
	}
	r := Reading{Ready: true, Err: s.err, Units: len(s.units), Introduced: s.introduced, Added: s.added, Deleted: s.deleted,
		Roots: slices.SortedFunc(slices.Values(s.roots), func(x, y RootUnits) int { return cmp.Compare(x.Dir, y.Dir) })}
	for _, u := range s.units {
		r.MaxRisk = max(r.MaxRisk, u.risk)
		switch {
		case st.Reviewed(u.key):
			r.Done++
		case u.lane == review.Contract:
			r.Contract++
		case u.lane == review.Logic:
			r.Logic++
		}
	}
	return r
}

// InboxItem is one change to read: a pull request, or a repo's dirty working
// tree when PR is nil.
type InboxItem struct {
	Repo    workspace.Repo
	PR      *ghx.PR
	Reading Reading
}

// Inbox lists every open pull request and every dirty working tree of the
// workspace. What is not analysed yet is analysed in the background; its
// Reading is not Ready until then, and each finished analysis fires an event.
// Items that need reading come first, riskiest first.
func (a *App) Inbox() []InboxItem {
	repos, _ := a.ws.Repos()
	var out []InboxItem
	for _, r := range repos {
		rs, err := a.repo(r.ID)
		if err != nil {
			continue
		}
		if r.Path != "" {
			if rd := a.treeReading(rs); rd.Units > 0 {
				out = append(out, InboxItem{Repo: r, Reading: rd})
			}
		}
		for _, pr := range a.PRs(r.ID).PRs {
			out = append(out, InboxItem{Repo: r, PR: &pr, Reading: a.prReading(rs, pr)})
		}
	}
	slices.SortStableFunc(out, func(x, y InboxItem) int {
		return cmp.Or(
			-cmp.Compare(b2i(x.Reading.NeedsReading()), b2i(y.Reading.NeedsReading())),
			-cmp.Compare(x.Reading.MaxRisk, y.Reading.MaxRisk),
			cmp.Compare(x.Repo.Name, y.Repo.Name),
		)
	})
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Unreviewed counts the units of the repo's working-tree review not yet
// marked reviewed. It is 0 until the review is built.
func (a *App) Unreviewed(id string) int {
	rs, err := a.repo(id)
	if err != nil || rs.info.Path == "" {
		return 0
	}
	rd := a.treeReading(rs)
	return rd.Units - rd.Done
}

// treeReading reads the working-tree review of a local repo, and has it
// rebuilt in the background when the tree changed since.
func (a *App) treeReading(rs *repoState) Reading {
	rs.mu.Lock()
	stale := !rs.treeDone || rs.treeGen != rs.gen
	merged := &summary{}
	ready := false
	for _, t := range rs.tree {
		if t.sum != nil {
			merged.merge(t.sum)
			ready = true
		}
	}
	rs.mu.Unlock()
	if stale {
		a.background("tree:"+rs.info.ID, rs.info.ID, func() {
			rs.mu.Lock()
			gen := rs.gen
			rs.mu.Unlock()
			for _, root := range a.Roots(rs.info.ID) {
				a.WorkingReview(rs.info.ID, root.Dir).Release()
			}
			rs.mu.Lock()
			rs.treeGen, rs.treeDone = gen, true
			rs.mu.Unlock()
		})
	}
	if !ready {
		return Reading{}
	}
	return readingOf(merged, rs.state)
}

func (a *App) prReading(rs *repoState, pr ghx.PR) Reading {
	key := fmt.Sprintf("%d@%s", pr.Number, pr.HeadOID)
	rs.mu.Lock()
	s := rs.prSum[key]
	rs.mu.Unlock()
	if s == nil {
		a.background("prsum:"+rs.info.ID+":"+key, rs.info.ID, func() {
			a.summaries.Lock()
			defer a.summaries.Unlock()
			s := &summary{}
			refs, err := a.fetchPR(rs, pr.Number)
			if err != nil {
				s.err = err
			}
			for _, root := range refs.roots {
				r, err := a.prReview(rs, refs, root.Dir)
				if err != nil {
					s.err = err
					break
				}
				s.add(root.Dir, r.Review)
				s.added, s.deleted = changedLines(r.Files)
				r.Release()
			}
			rs.mu.Lock()
			rs.prSum[key] = s
			rs.mu.Unlock()
		})
	}
	return readingOf(s, rs.state)
}

// LangInfo is a language a repo holds and how far its links can be trusted.
type LangInfo struct{ Name, Precision string }

// Card is what the workspace page shows of one repo.
type Card struct {
	Repo    workspace.Repo
	Langs   []LangInfo
	Ready   bool   // some root is analysed, so the census counts it
	Census  [3]int // fix first, fix soon, when convenient
	Updated time.Time
	PRs     PRList
}

// Cards describes each repo. The health census counts only what is already
// analysed, in memory or, for a working tree, stored; it loads nothing. A
// repo is analysed once one of its pages opens.
func (a *App) Cards() []Card {
	repos, _ := a.ws.Repos()
	var out []Card
	for _, r := range repos {
		rs, err := a.repo(r.ID)
		if err != nil {
			out = append(out, Card{Repo: r})
			continue
		}
		c := Card{Repo: r, PRs: a.PRs(r.ID)}
		for _, root := range a.Roots(r.ID) {
			l := LangInfo{Name: string(root.Lang), Precision: Precision(root.Lang)}
			if !slices.Contains(c.Langs, l) {
				c.Langs = append(c.Langs, l)
			}
		}
		c.Ready, c.Census = a.health(rs)
		c.Updated = a.Updated(r.ID)
		out = append(out, c)
	}
	return out
}

// health is the census of the repo's analysed roots, counted again when
// one of their snapshots changes.
func (a *App) health(rs *repoState) (bool, [3]int) {
	var ans []*Analysis
	var snaps []*code.Snapshot
	for _, root := range a.Roots(rs.info.ID) {
		an := a.Cached(rs.info.ID, root.Dir, "")
		defer an.Release()
		ans = append(ans, an)
		snaps = append(snaps, an.Snap)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if !rs.healthDone || !slices.Equal(rs.health, snaps) {
		var census [3]int
		for _, an := range ans {
			if an.Snap == nil {
				continue
			}
			for _, p := range an.Prognoses() {
				if l := int(p.Level); l >= 1 && l <= 3 {
					census[3-l]++
				}
			}
		}
		rs.health, rs.census, rs.healthDone = snaps, census, true
	}
	return slices.ContainsFunc(snaps, func(s *code.Snapshot) bool { return s != nil }), rs.census
}
