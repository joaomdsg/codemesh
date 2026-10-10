// Package live keeps an up-to-date analysis of a working tree: it re-runs the
// loader, the smell detectors and the review whenever the tree changes, and
// announces each new analysis on a topic.
package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// churnWindow is how far back from the last commit churn counts commits.
const churnWindow = 90 * 24 * time.Hour

// Analysis is one full pass over the tree.
type Analysis struct {
	At       time.Time
	Took     time.Duration
	Snap     *code.Snapshot
	Findings []smell.Finding
	Base     string // base ref as given
	BaseSHA  string // merge-base commit; "" without git or commits
	Review   *review.Review
	Err      error  // the load failed; Snap may hold the previous analysis
	Note     string // why there is no review, when there is none
}

// Source runs analyses and holds the latest one. Safe for concurrent use.
type Source struct {
	Dir     string
	Base    string // ref to review against; "" picks the repo default
	Updates *topic.Topic[int64]
	Log     *slog.Logger

	mu   sync.RWMutex
	cur  *Analysis
	repo *gitx.Repo

	run sync.Mutex // serialises Refresh; guards the base fields below

	// The base worktree lives as long as its merge-base is current, since the
	// review reads base source from it.
	baseSHA   string
	baseSnap  *code.Snapshot
	baseFinds []smell.Finding
	cleanup   func() error
}

// New returns a Source for dir. It does not analyse yet.
func New(dir, base string, log *slog.Logger) *Source {
	s := &Source{Dir: dir, Base: base, Updates: topic.New[int64](), Log: log}
	if repo, err := gitx.Open(dir); err == nil {
		s.repo = repo
	}
	return s
}

// Current returns the latest analysis, or nil before the first one.
func (s *Source) Current() *Analysis {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Refresh analyses the tree now and publishes the result.
func (s *Source) Refresh() *Analysis {
	s.run.Lock()
	defer s.run.Unlock()
	start := time.Now()
	a := s.analyse()
	a.At, a.Took = time.Now(), time.Since(start)
	s.mu.Lock()
	if a.Err != nil && s.cur != nil {
		a.Snap, a.Findings, a.Review = s.cur.Snap, s.cur.Findings, s.cur.Review
	}
	s.cur = a
	s.mu.Unlock()
	s.Updates.Publish(a.At.UnixNano())
	s.Log.Info("analysed", "took", a.Took.Round(time.Millisecond), "err", a.Err)
	return a
}

func (s *Source) analyse() *Analysis {
	a := &Analysis{}
	snap, err := code.Load(s.Dir)
	if err != nil {
		a.Err = err
		return a
	}
	a.Snap = snap
	if s.repo == nil {
		a.Note = "not a git repository, so there is nothing to review"
		a.Findings = smell.Find(snap)
		return a
	}
	if churn, err := s.repo.Churn(churnWindow); err == nil {
		applyChurn(snap, s.repo.Dir, churn)
	}
	a.Findings = smell.Find(snap)
	a.Base = s.Base
	if a.Base == "" {
		a.Base = s.repo.DefaultBase()
	}
	sha, err := s.repo.MergeBase(a.Base)
	if err != nil {
		a.Note = fmt.Sprintf("no merge-base with %s: %v", a.Base, err)
		return a
	}
	a.BaseSHA = sha
	if err := s.loadBase(sha); err != nil {
		a.Note = "base did not load: " + err.Error()
		return a
	}
	diffs, err := s.repo.Diff(sha)
	if err != nil {
		a.Note = "diff failed: " + err.Error()
		return a
	}
	a.Review = review.Build(review.Input{
		Base: s.baseSnap, Head: snap, Diffs: diffs,
		BaseFindings: s.baseFinds, HeadFindings: a.Findings,
	})
	return a
}

func (s *Source) loadBase(sha string) error {
	if sha == s.baseSHA && s.baseSnap != nil {
		return nil
	}
	s.closeBase()
	dir, cleanup, err := s.repo.Worktree(sha)
	if err != nil {
		return err
	}
	// The module may sit below the repo root; the base must load the same one.
	rel, err := filepath.Rel(s.repo.Dir, s.absDir())
	if err != nil {
		rel = "."
	}
	snap, err := code.Load(filepath.Join(dir, rel))
	if err != nil {
		_ = cleanup()
		return err
	}
	s.baseSHA, s.baseSnap, s.cleanup = sha, snap, cleanup
	s.baseFinds = smell.Find(snap)
	return nil
}

func (s *Source) closeBase() {
	if s.cleanup != nil {
		if err := s.cleanup(); err != nil {
			s.Log.Warn("base worktree cleanup", "err", err)
		}
	}
	s.baseSHA, s.baseSnap, s.baseFinds, s.cleanup = "", nil, nil, nil
}

// Close removes the base worktree.
func (s *Source) Close() {
	s.run.Lock()
	defer s.run.Unlock()
	s.closeBase()
}

func (s *Source) absDir() string {
	abs, err := filepath.Abs(s.Dir)
	if err != nil {
		return s.Dir
	}
	return abs
}

// Watch re-analyses whenever the tree's fingerprint changes, polling every
// interval, until ctx ends. Polling keeps it dependency-free and catches
// branch switches and commits as well as edits.
func (s *Source) Watch(ctx context.Context, interval time.Duration) {
	last := s.fingerprint()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if fp := s.fingerprint(); fp != last {
				last = fp
				s.Refresh()
			}
		}
	}
}

// fingerprint hashes the names, sizes and mtimes of the files analysis reads,
// plus HEAD, so commits and checkouts count as changes.
func (s *Source) fingerprint() string {
	h := sha256.New()
	_ = filepath.WalkDir(s.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != s.Dir && unwatchedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !watched(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	if s.repo != nil {
		head, _ := s.repo.Head()
		fmt.Fprintln(h, head)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Load loads the module in dir and sets each file's churn from its repo,
// when dir is in one: the analysis a Source runs, for a tree it does not watch.
func Load(dir string) (*code.Snapshot, []smell.Finding, error) {
	snap, err := code.Load(dir)
	if err != nil {
		return nil, nil, err
	}
	if repo, err := gitx.Open(dir); err == nil {
		if churn, err := repo.Churn(churnWindow); err == nil {
			applyChurn(snap, repo.Dir, churn)
		}
	}
	return snap, smell.Find(snap), nil
}

// applyChurn copies per-path commit counts onto the snapshot's files. Churn
// paths are relative to the repo root, file paths to the module root.
func applyChurn(snap *code.Snapshot, repoDir string, churn map[string]int) {
	prefix := ""
	if rel, err := filepath.Rel(repoDir, snap.Dir); err == nil && rel != "." {
		prefix = filepath.ToSlash(rel) + "/"
	}
	for _, p := range snap.Packages {
		for _, f := range p.Files {
			f.Churn = churn[prefix+f.Path]
		}
	}
}

// StatePath is where review state lives: inside the git dir, so it is never
// committed, or under the module when there is no git.
func StatePath(dir string) string {
	gitDir := filepath.Join(dir, ".git")
	if repo, err := gitx.Open(dir); err == nil {
		gitDir = filepath.Join(repo.Dir, ".git")
	}
	if info, err := os.Stat(gitDir); err == nil && !info.IsDir() {
		// A linked worktree's .git is a file; keep state beside the module.
		gitDir = filepath.Join(dir, ".codemesh")
	}
	return filepath.Join(gitDir, "codemesh", "reviewed.json")
}

// unwatchedDir reports whether analysis skips a directory: hidden ones,
// vendored code and test data.
func unwatchedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules"
}

// watched reports whether a file's edits change the analysis: Go and Julia
// sources and the files that name their dependencies.
func watched(name string) bool {
	switch name {
	case "go.mod", "go.sum", "Project.toml", "Manifest.toml":
		return true
	}
	return strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".jl")
}
