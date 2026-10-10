// Package fix asks Claude Code to treat one prognosis in a throwaway git
// worktree, then analyses the result against the tree it started from. The
// user's checkout is never written: the worktree is removed on Close. On
// request a finished run becomes a draft pull request (OpenPR).
package fix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// State is where a run is.
type State string

const (
	Preparing State = "preparing" // creating the worktree and analysing it
	Working   State = "working"   // Claude is editing
	Checking  State = "checking"  // analysing the edited tree
	Done      State = "done"
	Failed    State = "failed"
	Stopped   State = "stopped"
)

// Event is one line of the runner's own progress.
type Event struct {
	At   time.Time
	Text string
}

// Step is one thing Claude did: a thought, or a tool call with its result.
// Start and End are offsets from the run's start, taken as the stream
// arrives.
type Step struct {
	ID         string // the tool call's id; "" for a thought
	Kind       string // think, read, search, edit, run or other
	Title      string // one line
	Path       string // the file it named, relative to the worktree when inside it
	File       string // the same file relative to the module, when it is in it
	Text       string // a thought
	Command    string // a shell command
	Old, New   string // the text an edit replaced, and its replacement
	Output     string // the call's result, clipped
	Failed     bool
	Start, End time.Duration
	Round      int // the round it belongs to, from 1
	// What the step did to the tree, whatever tool it used: the module lines
	// it read, and the files that differ once it finished.
	Reads   []Read
	Changes []Change
	lines   Read // the Read tool's offset and limit, until addStep places its file
}

// Side is one analysed tree.
type Side struct {
	Snap      *code.Snapshot
	Findings  []smell.Finding
	Prognoses []prognosis.Prognosis
	CheckOK   bool
	Output    string // the check's last lines
}

// Check is the command a change must leave passing: the repository's own CI
// gate when it has one, since `go test ./...` stops at nested modules and
// skips whatever else the gate runs, such as browser tests.
type Check struct {
	Name  string // the command as given to Claude
	Label string // what it checks, in words
	Where string // where it runs, in words
	Dir   string
	Args  []string
	Env   []string // added to the environment
}

func checkOf(root, mod string) Check {
	if info, err := os.Stat(filepath.Join(root, "ci.sh")); err == nil && info.Mode()&0o111 != 0 {
		return Check{Name: "./ci.sh", Label: "The repository's checks (./ci.sh)", Where: "the repository root", Dir: root, Args: []string{"./ci.sh"}}
	}
	for _, f := range []struct{ file, tool string }{{"Makefile", "make"}, {"justfile", "just"}} {
		if hasTarget(filepath.Join(root, f.file), "ci") {
			return Check{Name: f.tool + " ci", Label: "The repository's checks (" + f.tool + " ci)", Where: "the repository root", Dir: root, Args: []string{f.tool, "ci"}}
		}
	}
	if _, err := os.Stat(filepath.Join(mod, "Project.toml")); err == nil {
		// Parallel precompilation of package extensions can deadlock
		// Pkg.test; one task at a time cannot.
		return Check{Name: `JULIA_NUM_PRECOMPILE_TASKS=1 julia --project -e 'using Pkg; Pkg.test()'`, Label: "The package's tests", Where: "the package root", Dir: mod,
			Args: []string{"julia", "--project", "-e", "using Pkg; Pkg.test()"}, Env: []string{"JULIA_NUM_PRECOMPILE_TASKS=1"}}
	}
	return Check{Name: "go build ./... && go test ./...", Label: "The build and tests", Where: "the module root", Dir: mod, Args: []string{"sh", "-c", "go build ./... && go test ./..."}}
}

// hasTarget reports whether a Makefile or justfile defines a recipe named t.
func hasTarget(file, t string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, t+":") || strings.HasPrefix(l, t+" ") {
			return true
		}
	}
	return false
}

// Run is one treatment of one prognosis.
type Run struct {
	Key     string // the prognosis key
	Started time.Time

	mu       sync.Mutex
	state    State
	events   []Event
	steps    []Step
	wt, mod  string
	tree     *tree
	err      error
	usage    Usage
	summary  string
	before   *Side
	now      *code.Snapshot // the worktree mid-run, once Claude adds files
	nowN     int
	remap    chan struct{}
	after    *Side
	check    Check
	review   *review.Review
	p        prognosis.Prognosis
	repo     string // the user's repository
	head     string // the commit the worktree started from
	base     string // the branch checked out then; "" when detached
	gh       string
	pr       PR
	bg       sync.WaitGroup // OpenPR's work, which close waits for
	closing  bool           // close began; OpenPR starts no more work
	life     context.Context
	cleanup  func() error
	stop     context.CancelFunc // ends Claude's work; the run still checks the result
	end      context.CancelFunc // ends every stage, for Discard and shutdown
	done     chan struct{}
	finished time.Time
	updates  *topic.Topic[int64]
	round    int   // the round under way, from 1
	rounds   []int // what each round left
	ended    string
	roundErr string // why a round after the first failed
	stopping bool   // Stop was pressed and the run has yet to wind down
	origin   bool   // the repository has a remote to open a pull request on
	// summaryRound is the round Claude's summary came from; undone is the
	// round undone, 0 for none.
	summaryRound, undone int
}

// Snapshot is a consistent copy of a run's progress.
type Snapshot struct {
	Key           string
	State         State
	Events        []Event
	Steps         []Step
	Err           error
	Usage         Usage
	Summary       string
	Took          time.Duration
	Before, After *Side
	// Now is the worktree analysed while Claude works, after it adds or
	// writes a file the starting tree lacks; NowN counts those analyses.
	Now    *code.Snapshot
	NowN   int
	Review *review.Review
	Check  Check
	Base   string // the branch a pull request targets; "" when detached
	PR     PR
	Round  int
	rounds []int
	ended  string
	// Stopping says Stop was pressed while the round's checks still run.
	Stopping     bool
	SummaryRound int // the round Summary describes
	Undone       int // the round undone for leaving more, 0 for none
	roundErr     string
	Origin       bool // the repository has an origin remote to open a pull request on
}

// Live reports whether the run is still going: setting up, editing or checking.
func (s Snapshot) Live() bool {
	return s.State == Preparing || s.State == Working || s.State == Checking
}

// Editing reports whether Claude has yet to finish editing.
func (s Snapshot) Editing() bool { return s.State == Preparing || s.State == Working }

// Snapshot returns the run's progress so far.
func (r *Run) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	took := time.Since(r.Started)
	if !r.finished.IsZero() {
		took = r.finished.Sub(r.Started)
	}
	return Snapshot{Key: r.Key, State: r.state, Events: append([]Event(nil), r.events...), Steps: append([]Step(nil), r.steps...), Err: r.err,
		Usage: r.usage.clone(), Summary: r.summary, Took: took, Before: r.before, After: r.after, Now: r.now, NowN: r.nowN, Review: r.review, Check: r.check, Base: r.base, PR: r.pr,
		Round: r.round, rounds: slices.Clone(r.rounds), ended: r.ended, roundErr: r.roundErr,
		Stopping: r.stopping && r.state == Checking, SummaryRound: r.summaryRound, Undone: r.undone, Origin: r.origin}
}

// Stop ends Claude early; the run keeps what it had.
func (r *Run) Stop() {
	r.set(func() { r.stopping = true })
	r.stop()
}

func (r *Run) set(f func()) {
	r.mu.Lock()
	f()
	r.mu.Unlock()
	r.updates.Publish(time.Now().UnixNano())
}

func (r *Run) note(text string) {
	r.set(func() { r.events = append(r.events, Event{At: time.Now(), Text: text}) })
}

func (r *Run) fail(err error) {
	r.set(func() {
		if r.state != Stopped {
			r.state, r.err = Failed, err
		}
		r.finished = time.Now()
	})
}

// Runs holds the runs of one server, at most one per prognosis.
type Runs struct {
	Dir     string // the analysed module
	Updates *topic.Topic[int64]
	// Agent is the command that does the work, given the prompt and flags
	// after it. Tests swap it so no test ever starts Claude.
	Agent string
	// Self is this program, which the agent runs to list the prognoses of
	// its worktree; "" leaves that step out.
	Self string
	// Gh is the GitHub CLI that opens pull requests.
	Gh string

	mu   sync.Mutex
	runs map[string]*Run
}

// NewRuns returns the registry for the module in dir.
func NewRuns(dir string) *Runs {
	return &Runs{Dir: dir, Updates: topic.New[int64](), Agent: "claude", Gh: "gh", runs: map[string]*Run{}}
}

// Get returns the run for a prognosis key, or nil.
func (rs *Runs) Get(key string) *Run {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.runs[key]
}

// Start treats p in a fresh worktree of the module's last commit. A run
// already going for p is returned as it is.
func (rs *Runs) Start(p prognosis.Prognosis) *Run {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if r := rs.runs[p.Key]; r != nil {
		return r
	}
	life, end := context.WithCancel(context.Background())
	work, stop := context.WithCancel(life)
	r := &Run{Key: p.Key, Started: time.Now(), state: Preparing, remap: make(chan struct{}, 1), p: p, gh: rs.Gh, life: life, stop: stop, end: end, done: make(chan struct{}), updates: rs.Updates}
	rs.runs[p.Key] = r
	go func() {
		defer close(r.done)
		r.run(life, work, rs)
		r.mu.Lock()
		if r.finished.IsZero() {
			r.finished = time.Now() // ended early; this also stops the heartbeat
		}
		r.mu.Unlock()
	}()
	go r.heartbeat()
	return r
}

// heartbeat re-renders listening pages every two seconds while the run is
// live, so its clock moves through a long build or a quiet stretch of Claude.
func (r *Run) heartbeat() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		r.mu.Lock()
		over := !r.finished.IsZero()
		r.mu.Unlock()
		if over {
			return
		}
		r.updates.Publish(time.Now().UnixNano())
	}
}

// Dismiss stops a run and removes its worktree.
func (rs *Runs) Dismiss(key string) {
	rs.mu.Lock()
	r := rs.runs[key]
	delete(rs.runs, key)
	rs.mu.Unlock()
	if r != nil {
		r.close()
	}
	rs.Updates.Publish(time.Now().UnixNano())
}

// Close stops every run and removes every worktree.
func (rs *Runs) Close() {
	rs.mu.Lock()
	all := rs.runs
	rs.runs = map[string]*Run{}
	rs.mu.Unlock()
	for _, r := range all {
		r.close()
	}
}

// close ends the run and waits for it before removing the worktree, so a
// worktree still being created is removed too, not left behind.
func (r *Run) close() {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.end()
	<-r.done
	r.bg.Wait()
	r.mu.Lock()
	cleanup, branch := r.cleanup, r.pr.Branch
	r.cleanup = nil
	r.mu.Unlock()
	if cleanup != nil {
		_ = cleanup()
	}
	// The worktree shares the repository's refs; the branch lives on in the
	// remote, so the local copy goes with the worktree.
	if branch != "" {
		_, _ = command(context.Background(), r.repo, nil, "git", "branch", "-D", branch)
	}
}

func (r *Run) run(life, ctx context.Context, rs *Runs) {
	start, rel, ok := r.prepare(life, rs.Dir)
	if !ok {
		return
	}
	r.note("Analysing the starting point and running its checks, so the result has something to compare with.")
	startMod := filepath.Join(start, rel)
	// Stop ends Claude's work, not this: a check cut short would read as one
	// that failed before the run.
	before, err := analyse(life, startMod, checkOf(start, startMod))
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.before, r.state, r.round = before, Working, 1 })
	r.note("Stop ends it early and keeps what it changed so far.")
	prompt := r.p.Prompt(r.brief(rs.Self, before))
	var kept outcome
	for n := 1; ; n++ {
		err := r.work(ctx, rs.Agent, prompt)
		if err != nil && n == 1 {
			r.fail(err)
			return
		}
		if err != nil {
			r.set(func() { r.roundErr = err.Error() })
		}
		after, rev, ok := r.result(life, before)
		if !ok {
			return
		}
		left, total := leftover(r.p, r.check.Name, before, after, rev)
		now := outcome{after: after, rev: rev, left: total, stopped: ctx.Err() != nil, failed: err != nil}
		if !r.settle(life, n, now, &kept) {
			return
		}
		r.set(func() {
			// The analysed tree stands in as the map while Claude goes again;
			// the live estimate counts again until the next tally.
			r.now, r.nowN, r.state, r.usage.Exact = after.Snap, r.nowN+1, Working, false
		})
		r.note(fmt.Sprintf("Round %d: Claude goes again on what its change left.", n+1))
		prompt = followUp(left)
	}
}

// endRound records how much a round left and reports whether Claude goes
// again, and whether the round left more than the one before it.
func (r *Run) endRound(left int, stopped, failed bool) (more, worse bool) {
	var resumable bool
	r.set(func() {
		prev := 0
		if n := len(r.rounds); n > 0 {
			prev = r.rounds[n-1]
		}
		r.rounds = append(r.rounds, left)
		worse = r.round > 1 && left > prev
		more, r.ended = nextRound(r.round, prev, left, stopped, failed)
		if stopped {
			r.ended = halted
			if r.state == Stopped {
				r.ended = cut
			}
		}
		resumable = r.usage.Session != ""
		if more && resumable {
			r.round++
		}
	})
	if more && !resumable {
		// A fresh session would get the follow-up without the change it
		// refers to.
		r.note("Claude's session has no id to resume, so it cannot go again.")
		return false, worse
	}
	return more, worse
}

func (r *Run) finish(after *Side, rev *review.Review) {
	r.set(func() {
		r.after, r.review = after, rev
		if r.state != Stopped {
			r.state = Done
		}
		r.finished = time.Now()
	})
}

// prepare opens the repository and cuts the two worktrees; it returns the
// starting point's and the module's path inside a worktree.
func (r *Run) prepare(life context.Context, dir string) (start, rel string, ok bool) {
	repo, head, ok := r.open(dir)
	if !ok {
		return "", "", false
	}
	// The starting point gets a worktree of its own, kept for the run: the
	// review reads each side's source from its snapshot's directory when it
	// is built, after Claude has edited Claude's copy.
	wt, start, err := r.worktrees(repo, head)
	if err != nil {
		r.fail(err)
		return "", "", false
	}
	if life.Err() != nil {
		return "", "", false
	}
	abs, _ := filepath.Abs(dir)
	rel, err = filepath.Rel(repo.Dir, abs)
	if err != nil {
		rel = "."
	}
	mod := filepath.Join(wt, rel)
	check := checkOf(wt, mod)
	r.set(func() { r.check, r.wt, r.mod, r.tree = check, wt, mod, newTree(wt, mod) })
	return start, rel, true
}

// open opens the repository and records where the run starts; it fails the
// run and reports false when it cannot.
func (r *Run) open(dir string) (*gitx.Repo, string, bool) {
	repo, err := gitx.Open(dir)
	if err != nil {
		r.fail(fmt.Errorf("not a git repository: %w", err))
		return nil, "", false
	}
	head, err := repo.Head()
	if err != nil || head == "" {
		r.fail(errors.New("the repository has no commits to start from"))
		return nil, "", false
	}
	base, err := repo.Branch()
	if err != nil {
		r.fail(err)
		return nil, "", false
	}
	_, originErr := command(context.Background(), repo.Dir, nil, "git", "remote", "get-url", "origin")
	r.set(func() { r.repo, r.head, r.base, r.origin = repo.Dir, head, base, originErr == nil })
	r.note("Copying the last commit (" + head[:7] + ") into a throwaway worktree. Uncommitted edits are not included.")
	return repo, head, true
}

func (r *Run) worktrees(repo *gitx.Repo, head string) (wt, start string, err error) {
	wt, cleanup, err := repo.Worktree(head)
	if err != nil {
		return "", "", err
	}
	r.set(func() { r.cleanup = cleanup })
	start, cleanStart, err := repo.Worktree(head)
	if err != nil {
		return "", "", err
	}
	r.set(func() { r.cleanup = func() error { return errors.Join(cleanup(), cleanStart()) } })
	return wt, start, nil
}

// brief is what the first prompt needs beyond the prognosis.
func (r *Run) brief(self string, before *Side) prognosis.Brief {
	b := prognosis.Brief{Check: r.check.Name, Where: r.check.Where, Nearby: prognosis.Nearby(before.Prognoses, r.p)}
	if self != "" {
		b.List = self + " prognoses " + r.mod
	}
	return b
}

// work lets Claude edit the worktree. It returns Claude's error when the
// run was not stopped.
func (r *Run) work(ctx context.Context, agent, prompt string) error {
	working, worked := context.WithCancel(ctx)
	go r.remapping(working, r.mod)
	err := r.claude(ctx, agent, r.mod, prompt)
	worked()
	if ctx.Err() != nil {
		r.set(func() { r.state = Stopped })
		r.note("Stopped. Showing what changed so far.")
		return nil
	}
	r.set(func() { r.state = Checking })
	if err == nil {
		r.note("Analysing the result and running the checks again.")
	}
	return err
}

// result analyses and checks the worktree and reviews it against the start.
func (r *Run) result(life context.Context, before *Side) (*Side, *review.Review, bool) {
	if life.Err() != nil {
		return nil, nil, false
	}
	after, err := analyse(life, r.mod, r.check)
	if err != nil {
		r.fail(fmt.Errorf("the edited code did not load: %w", err))
		return nil, nil, false
	}
	diffs, err := (&gitx.Repo{Dir: r.wt}).Diff(r.head)
	if err != nil {
		r.fail(err)
		return nil, nil, false
	}
	rev := review.Build(review.Input{Base: before.Snap, Head: after.Snap, Diffs: diffs, BaseFindings: before.Findings, HeadFindings: after.Findings})
	return after, rev, true
}

// addsFiles reports whether changes write a Go file the starting tree lacks.
func (r *Run) addsFiles(changes []Change) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.before == nil {
		return false
	}
	for _, c := range changes {
		test := strings.HasSuffix(c.File, "_test.go") || strings.HasPrefix(c.File, "test/")
		if test || !strings.HasSuffix(c.File, ".go") && !strings.HasSuffix(c.File, ".jl") {
			continue
		}
		if !hasFile(r.before.Snap, c.File) {
			return true
		}
	}
	return false
}

func hasFile(s *code.Snapshot, path string) bool {
	for _, p := range s.Packages {
		for _, f := range p.Files {
			if f.Path == path {
				return true
			}
		}
	}
	return false
}

// remapping re-analyses the worktree each time Claude adds or writes a new
// file, so the replay map gains its tile while the run is live. A tree that
// does not load mid-edit keeps the last map.
func (r *Run) remapping(ctx context.Context, mod string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.remap:
		}
		snap, err := code.Load(mod)
		if err != nil || ctx.Err() != nil {
			continue
		}
		r.set(func() { r.now, r.nowN = snap, r.nowN+1 })
	}
}

// analyse loads a tree and runs its check. The check is capped: a hung test
// must not hold the result back.
func analyse(ctx context.Context, dir string, c Check) (*Side, error) {
	snap, fs, err := live.Load(dir)
	if err != nil {
		return nil, err
	}
	s := &Side{Snap: snap, Findings: fs, Prognoses: prognosis.Find(snap, fs)}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, c.Args[0], c.Args[1:]...)
	group(cmd)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	out, err := cmd.CombinedOutput()
	s.CheckOK, s.Output = exited(err) == nil, tail(string(out), 40)
	return s, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
