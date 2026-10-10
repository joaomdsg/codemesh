// Package fix asks Claude Code to treat one prognosis in a throwaway git
// worktree, then analyses the result against the tree it started from. The
// user's checkout is never written: the worktree is removed on Close. On
// request a finished run becomes a draft pull request (OpenPR).
package fix

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/go-via/via/topic"
	"github.com/joaomdsg/codemesh/internal/code"
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
	// Stopping says Stop was pressed while checks still run: the starting
	// point's, or a round's.
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
		Stopping: r.stopping && (r.state == Preparing || r.state == Checking), SummaryRound: r.summaryRound, Undone: r.undone, Origin: r.origin}
}

// Stop ends Claude early; the run keeps what it had, unless that left more
// than the round before.
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
	if ctx.Err() != nil {
		r.stoppedBeforeClaude(before)
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

// stoppedBeforeClaude ends a run stopped during its starting check: the
// result is the starting point, which needs no second check.
func (r *Run) stoppedBeforeClaude(before *Side) {
	r.set(func() { r.before, r.state = before, Stopped })
	r.note("Stopped before Claude started.")
	r.finish(before, review.Build(review.Input{Base: before.Snap, Head: before.Snap, BaseFindings: before.Findings, HeadFindings: before.Findings}))
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
