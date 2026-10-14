// Package app is what the UI calls: the workspace of repos, the analysis
// engine, the watchers of local working trees, gh, and the stores of review
// state, notes, threads and the handoff basket.
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/ghx"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/notes"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/store"
	"github.com/joaomdsg/codemesh/internal/thread"
	"github.com/joaomdsg/codemesh/internal/workspace"
)

// Config sets up an App.
type Config struct {
	// Poll is how often a local repo's working tree is checked for changes.
	// Zero means one second.
	Poll time.Duration
	// PRs lists pull requests. Nil means the gh CLI.
	PRs ghx.PRs
	Log *slog.Logger
}

// Event says something about a repo changed: its working tree, its pull
// requests, its summaries. Repo is empty for changes that belong to no repo,
// such as the basket or the list of repos.
type Event struct{ Repo string }

// App owns everything the pages read. Safe for concurrent use.
type App struct {
	// Events announces each change a page may want to show.
	Events *topic.Topic[Event]

	ctx    context.Context
	log    *slog.Logger
	ws     *workspace.Workspace
	eng    *analysis.Engine
	watch  *live.Watcher
	gh     ghx.PRs
	state  string // directory of per-repo state
	basket *handoff.Store

	mu      sync.Mutex
	repos   map[string]*repoState
	pending map[string]*Pending
	stops   map[string]func() // ends the forwarding of a watched repo's updates
	jobs    map[string]bool
	removed map[string]bool // repos being removed: not to be opened again meanwhile

	summaries sync.Mutex // one PR summary at a time: each loads two snapshots
}

// New opens the workspace and starts watching its local repos until ctx ends.
func New(ctx context.Context, cfg Config) (*App, error) {
	ws, err := workspace.Open()
	if err != nil {
		return nil, err
	}
	cacheDir, err := store.CacheDir()
	if err != nil {
		return nil, err
	}
	stateDir, err := store.StateDir()
	if err != nil {
		return nil, err
	}
	basket, err := handoff.Open(filepath.Join(stateDir, "basket.json"))
	if err != nil {
		return nil, fmt.Errorf("basket: %w", err)
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	a := &App{
		Events:  topic.New[Event](),
		ctx:     ctx,
		log:     log,
		ws:      ws,
		gh:      cfg.PRs,
		state:   filepath.Join(stateDir, "repos"),
		basket:  basket,
		repos:   map[string]*repoState{},
		pending: map[string]*Pending{},
		stops:   map[string]func(){},
		jobs:    map[string]bool{},
		removed: map[string]bool{},
	}
	if a.gh == nil {
		a.gh = ghx.CLI{}
	}
	worktrees := filepath.Join(cacheDir, "worktrees")
	a.eng = analysis.New(analysis.Config{
		Worktrees:   worktrees,
		Repo:        a.git,
		Load:        lang.LoadContext,
		Cap:         cacheCap,
		Store:       analysis.NewDisk(filepath.Join(cacheDir, "snapshots")),
		Fingerprint: lang.Fingerprint,
		Loaded:      func(k analysis.Key) { a.publish(k.Repo) },
		Log:         log,
	})
	a.watch = live.NewWatcher(ctx, a.eng, cmp.Or(cfg.Poll, time.Second))
	repos, err := ws.Repos()
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		a.sweepWorktrees(filepath.Join(worktrees, r.ID), r)
		a.startWatching(r)
	}
	return a, nil
}

// sweepWorktrees removes the worktrees a run that was killed left in dir.
func (a *App) sweepWorktrees(dir string, r workspace.Repo) {
	git, err := gitx.Open(r.Dir())
	if err == nil {
		err = git.GCWorktrees(dir, 0, nil)
	}
	if err != nil {
		a.log.Warn("sweeping worktrees", "repo", r.ID, "err", err)
	}
	os.Remove(dir)
}

func (a *App) publish(repo string) { a.Events.Publish(Event{Repo: repo}) }

// Repos lists the registered repos in the order they were added.
func (a *App) Repos() ([]workspace.Repo, error) { return a.ws.Repos() }

// Repo finds a registered repo by id.
func (a *App) Repo(id string) (workspace.Repo, bool) {
	repos, err := a.ws.Repos()
	if err != nil {
		return workspace.Repo{}, false
	}
	i := slices.IndexFunc(repos, func(r workspace.Repo) bool { return r.ID == id })
	if i < 0 {
		return workspace.Repo{}, false
	}
	return repos[i], true
}

// Pending is a git URL being cloned, or one whose clone failed.
type Pending struct {
	Arg string
	Err error // nil while cloning
}

// Pending lists the clones in progress and the ones that failed, oldest first.
func (a *App) Pending() []Pending {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Pending
	for _, p := range a.pending {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(x, y Pending) int { return cmp.Compare(x.Arg, y.Arg) })
	return out
}

// Dismiss forgets a failed clone.
func (a *App) Dismiss(arg string) {
	a.mu.Lock()
	if p := a.pending[arg]; p != nil && p.Err != nil {
		delete(a.pending, arg)
	}
	a.mu.Unlock()
	a.publish("")
}

// AddRepo registers a local directory, or a git URL. A directory, with a
// leading ~ taken as the home directory, is registered before AddRepo returns.
// A URL is cloned in the background and listed by Pending until then; a failed
// clone stays there with its error. Anything else is an error.
func (a *App) AddRepo(arg string) error {
	if rest, ok := strings.CutPrefix(arg, "~/"); ok || arg == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			arg = filepath.Join(home, rest)
		}
	}
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		r, err := a.ws.Add(arg)
		if err != nil {
			return err
		}
		a.startWatching(r)
		a.publish("")
		return nil
	}
	if !workspace.IsURL(arg) {
		return fmt.Errorf("%s is not a directory", arg)
	}
	a.mu.Lock()
	if p := a.pending[arg]; p != nil && p.Err == nil {
		a.mu.Unlock()
		return nil
	}
	a.pending[arg] = &Pending{Arg: arg}
	a.mu.Unlock()
	go func() {
		r, err := a.ws.Add(arg)
		a.mu.Lock()
		if err != nil {
			a.pending[arg].Err = err
		} else {
			delete(a.pending, arg)
		}
		a.mu.Unlock()
		if err == nil {
			a.startWatching(r)
		}
		a.publish("")
	}()
	return nil
}

// RemoveRepo forgets a repo and deletes its clone, if it has one. Loads in
// flight for it are cancelled and waited for first, and its snapshots and
// worktrees dropped. Notes and review marks stay on disk.
func (a *App) RemoveRepo(id string) error {
	a.mu.Lock()
	a.removed[id] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.removed, id)
		a.mu.Unlock()
	}()
	a.stopWatching(id)
	a.eng.Forget(id)
	a.mu.Lock()
	delete(a.repos, id)
	a.mu.Unlock()
	err := a.ws.Remove(id)
	a.publish("")
	return err
}

// startWatching follows a local repo's working tree and relays its updates
// as events. A clone has no working tree to follow.
func (a *App) startWatching(r workspace.Repo) {
	if r.Path == "" {
		return
	}
	a.mu.Lock()
	if a.stops[r.ID] != nil {
		a.mu.Unlock()
		return
	}
	sub := a.watch.Add(r.ID, r.Path).Subscribe()
	a.stops[r.ID] = sub.Stop
	a.mu.Unlock()
	go func() {
		for {
			select {
			case <-a.ctx.Done():
				sub.Stop()
				return
			case <-sub.Ready():
			}
			batch, ok := sub.Drain()
			if len(batch) > 0 {
				a.changed(r.ID)
			}
			if !ok {
				return
			}
		}
	}()
}

func (a *App) stopWatching(id string) {
	a.mu.Lock()
	stop := a.stops[id]
	delete(a.stops, id)
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	a.watch.Remove(id)
}

// changed notes that a repo's tree moved: what was derived from it is stale.
func (a *App) changed(id string) {
	a.mu.Lock()
	rs := a.repos[id]
	a.mu.Unlock()
	if rs != nil {
		rs.mu.Lock()
		rs.gen++
		rs.roots = nil
		rs.updated = time.Now()
		rs.mu.Unlock()
	}
	a.publish(id)
}

// Rescan has the working-tree snapshots of a repo loaded afresh, without
// waiting for the watcher. Pages show the old ones until then.
func (a *App) Rescan(id string) {
	a.eng.Refresh(id, analysis.Worktree)
	a.changed(id)
}

// Updated is when the repo last changed, or when it was first read.
func (a *App) Updated(id string) time.Time {
	rs, err := a.repo(id)
	if err != nil {
		return time.Time{}
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.updated
}

// background runs fn on its own goroutine unless a job of that name is
// running, and announces the repo's change when it ends.
func (a *App) background(name, repo string, fn func()) {
	a.mu.Lock()
	if a.jobs[name] {
		a.mu.Unlock()
		return
	}
	a.jobs[name] = true
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.jobs, name)
			a.mu.Unlock()
			a.publish(repo)
		}()
		fn()
	}()
}

// Wait returns once no background analysis or load is running. Callers that
// delete the directories the app works in, such as tests, wait first.
func (a *App) Wait() {
	for {
		a.mu.Lock()
		n := len(a.jobs)
		a.mu.Unlock()
		if n == 0 && !a.eng.Busy() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close ends the app's analysis: it cancels the loads in flight, refuses new
// ones, and waits for them and for the background jobs, or for ctx to end.
func (a *App) Close(ctx context.Context) error {
	defer lang.Close()
	if err := a.eng.Close(ctx); err != nil {
		return err
	}
	for {
		a.mu.Lock()
		n := len(a.jobs)
		a.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// repoState is what the app keeps per repo: the git handle, the stores, and
// what was derived from the tree, each tagged with the generation of the tree
// it came from.
type repoState struct {
	info  workspace.Repo
	git   *gitx.Repo
	state *review.State
	notes *notes.Store
	thr   *thread.Store

	wt sync.Mutex // one working-tree review is built at a time

	mu      sync.Mutex
	gen     int // advances when the working tree changes
	updated time.Time
	roots   []lang.Root
	tree    map[string]*treeReview // by root
	prs     *prCache
	prSum   map[string]*summary // by PR number and head
	fetched map[int]*fetchedPR  // by PR number

	// What the inbox shows was derived at these generations.
	treeGen, warmGen int
	treeDone, warmed bool
	health           []*code.Snapshot // the snapshots the census counted, by root
	census           [3]int           // fix first, fix soon, when convenient
	healthDone       bool
}

// cacheCap is how many snapshots stay in memory: enough for the roots and
// review bases of the repos a session moves between. A stored one comes
// back from disk, but an evicted one would be read again.
const cacheCap = 32

// repo returns the state of a registered repo, opening it on first use.
func (a *App) repo(id string) (*repoState, error) {
	a.mu.Lock()
	rs, gone := a.repos[id], a.removed[id]
	a.mu.Unlock()
	if gone {
		return nil, fmt.Errorf("repo %q is being removed", id)
	}
	if rs != nil {
		return rs, nil
	}
	info, ok := a.Repo(id)
	if !ok {
		return nil, fmt.Errorf("no repo %q", id)
	}
	git, err := gitx.Open(info.Dir())
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(a.state, id)
	legacy := filepath.Join(git.Dir, ".git", "codemesh", "reviewed.json")
	if fi, err := os.Stat(filepath.Join(git.Dir, ".git")); err != nil || !fi.IsDir() {
		legacy = ""
	}
	state, err := review.OpenStateFrom(filepath.Join(dir, "reviewed.json"), legacy)
	if err != nil {
		return nil, fmt.Errorf("review state: %w", err)
	}
	ns, err := notes.Open(filepath.Join(dir, "notes.json"))
	if err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}
	ts, err := thread.Open(filepath.Join(dir, "threads.json"))
	if err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	rs = &repoState{
		info: info, git: git, state: state, notes: ns, thr: ts,
		updated: time.Now(), tree: map[string]*treeReview{}, prSum: map[string]*summary{}, fetched: map[int]*fetchedPR{},
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if have := a.repos[id]; have != nil {
		return have, nil
	}
	a.repos[id] = rs
	return rs, nil
}

func (a *App) git(id string) (*gitx.Repo, error) {
	rs, err := a.repo(id)
	if err != nil {
		return nil, err
	}
	return rs.git, nil
}

// Reviewed is the repo's set of units marked reviewed.
func (a *App) Reviewed(id string) (*review.State, error) {
	rs, err := a.repo(id)
	if err != nil {
		return nil, err
	}
	return rs.state, nil
}

// Notes is the repo's review notes.
func (a *App) Notes(id string) (*notes.Store, error) {
	rs, err := a.repo(id)
	if err != nil {
		return nil, err
	}
	return rs.notes, nil
}

// Threads is the repo's blast-radius threads.
func (a *App) Threads(id string) (*thread.Store, error) {
	rs, err := a.repo(id)
	if err != nil {
		return nil, err
	}
	return rs.thr, nil
}

// Roots lists the projects in the repo, such as one Go module and one Julia
// package, sorted by directory.
func (a *App) Roots(id string) []lang.Root {
	rs, err := a.repo(id)
	if err != nil {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.roots == nil {
		roots, err := lang.Roots(rs.info.Dir())
		if err != nil {
			a.log.Warn("finding roots", "repo", id, "err", err)
		}
		rs.roots = append([]lang.Root{}, roots...)
	}
	return rs.roots
}

// ErrNoRoot is the error of a request for a root the repo, or the pull
// request, does not have.
var ErrNoRoot = errors.New("no such project in this repo")

// root is want if the repo has that root, or its first when want is empty.
func (a *App) root(id, want string) (string, error) {
	return pickRoot(a.Roots(id), want)
}

func pickRoot(roots []lang.Root, want string) (string, error) {
	switch {
	case len(roots) == 0:
		return "", errors.New("no analysable project in this repo")
	case want == "":
		return roots[0].Dir, nil
	case slices.ContainsFunc(roots, func(r lang.Root) bool { return r.Dir == want }):
		return want, nil
	}
	return "", fmt.Errorf("%w: %q", ErrNoRoot, want)
}

// Precision says how far a language's links can be trusted: Go resolves
// calls with types; the rest match them by name.
func Precision(l code.Lang) string {
	if l == code.Go {
		return "typed"
	}
	return "named"
}
