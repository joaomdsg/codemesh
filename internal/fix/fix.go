// Package fix asks Claude Code to treat one prognosis in a throwaway git
// worktree, then analyses the result against the tree it started from. The
// user's checkout is never written: the worktree is removed on Close. On
// request a finished run becomes a draft pull request (OpenPR).
package fix

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	Kind       string // Think, Read, Search, Edit, Exec or Other
	Tool       string
	Title      string // one line
	Path       string // the file it named, relative to the worktree when inside it
	File       string // the same file relative to the module, when it is in it
	Text       string // a thought
	Command    string // a shell command
	Old, New   string // the text an edit replaced, and its replacement
	Output     string // the call's result, clipped
	Failed     bool
	Start, End time.Duration
	// What the step did to the tree, whatever tool it used: the module files
	// a shell command named, and the files that differ once it finished.
	Reads   []string
	Changes []Change
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
	Name  string // the command as shown and as given to Claude
	Where string // where it runs, in words
	Dir   string
	Args  []string
	Env   []string // added to the environment
}

func checkOf(root, mod string) Check {
	if info, err := os.Stat(filepath.Join(root, "ci.sh")); err == nil && info.Mode()&0o111 != 0 {
		return Check{Name: "./ci.sh", Where: "the repository root", Dir: root, Args: []string{"./ci.sh"}}
	}
	for _, f := range []struct{ file, tool string }{{"Makefile", "make"}, {"justfile", "just"}} {
		if hasTarget(filepath.Join(root, f.file), "ci") {
			return Check{Name: f.tool + " ci", Where: "the repository root", Dir: root, Args: []string{f.tool, "ci"}}
		}
	}
	if _, err := os.Stat(filepath.Join(mod, "Project.toml")); err == nil {
		// Parallel precompilation of package extensions can deadlock
		// Pkg.test; one task at a time cannot.
		return Check{Name: `JULIA_NUM_PRECOMPILE_TASKS=1 julia --project -e 'using Pkg; Pkg.test()'`, Where: "the package root", Dir: mod,
			Args: []string{"julia", "--project", "-e", "using Pkg; Pkg.test()"}, Env: []string{"JULIA_NUM_PRECOMPILE_TASKS=1"}}
	}
	return Check{Name: "go build ./... && go test ./...", Where: "the module root", Dir: mod, Args: []string{"sh", "-c", "go build ./... && go test ./..."}}
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
	life     context.Context
	cleanup  func() error
	stop     context.CancelFunc // ends Claude's work; the run still checks the result
	end      context.CancelFunc // ends every stage, for Discard and shutdown
	done     chan struct{}
	finished time.Time
	updates  *topic.Topic[int64]
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
}

// Snapshot returns the run's progress so far.
func (r *Run) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	took := time.Since(r.Started)
	if !r.finished.IsZero() {
		took = r.finished.Sub(r.Started)
	}
	return Snapshot{Key: r.Key, State: r.state, Events: append([]Event(nil), r.events...), Steps: append([]Step(nil), r.steps...), Err: r.err,
		Usage: r.usage.clone(), Summary: r.summary, Took: took, Before: r.before, After: r.after, Now: r.now, NowN: r.nowN, Review: r.review, Check: r.check, Base: r.base, PR: r.pr}
}

// Stop ends Claude early; the run keeps what it had.
func (r *Run) Stop() { r.stop() }

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
		r.run(life, work, rs.Dir, rs.Agent, rs.Self, p)
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

func (r *Run) run(life, ctx context.Context, dir, agent, self string, p prognosis.Prognosis) {
	repo, err := gitx.Open(dir)
	if err != nil {
		r.fail(fmt.Errorf("not a git repository: %w", err))
		return
	}
	head, err := repo.Head()
	if err != nil || head == "" {
		r.fail(errors.New("the repository has no commits to start from"))
		return
	}
	base, err := repo.Branch()
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.repo, r.head, r.base = repo.Dir, head, base })
	r.note("Copying the last commit (" + head[:7] + ") into a throwaway worktree. Uncommitted edits are not included.")
	// The starting point gets a worktree of its own, kept for the run: the
	// review reads each side's source from its snapshot's directory when it
	// is built, after Claude has edited Claude's copy.
	wt, cleanup, err := repo.Worktree(head)
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.cleanup = cleanup })
	start, cleanStart, err := repo.Worktree(head)
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.cleanup = func() error { return errors.Join(cleanup(), cleanStart()) } })
	if life.Err() != nil {
		return
	}
	abs, _ := filepath.Abs(dir)
	rel, err := filepath.Rel(repo.Dir, abs)
	if err != nil {
		rel = "."
	}
	mod := filepath.Join(wt, rel)
	check := checkOf(wt, mod)
	r.set(func() { r.check, r.wt, r.mod, r.tree = check, wt, mod, newTree(wt, mod) })

	r.note("Analysing the starting point and running " + check.Name + " on it.")
	before, err := analyse(ctx, filepath.Join(start, rel), checkOf(start, filepath.Join(start, rel)))
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.before, r.state = before, Working })

	r.note("Claude is working on it.")
	brief := prognosis.Brief{Check: check.Name, Where: check.Where, Nearby: prognosis.Nearby(before.Prognoses, p)}
	if self != "" {
		brief.List = self + " prognoses " + mod
	}
	working, worked := context.WithCancel(ctx)
	go r.remapping(working, mod)
	err = r.claude(ctx, agent, mod, p.Prompt(brief))
	worked()
	if err != nil && ctx.Err() == nil {
		r.fail(err)
		return
	}
	if ctx.Err() != nil {
		r.set(func() { r.state = Stopped })
		r.note("Stopped. Showing what changed so far.")
	} else {
		r.set(func() { r.state = Checking })
		r.note("Analysing the result and running " + check.Name + ".")
	}

	if life.Err() != nil {
		return
	}
	after, err := analyse(life, mod, check)
	if err != nil {
		r.fail(fmt.Errorf("the edited code did not load: %w", err))
		return
	}
	diffs, err := (&gitx.Repo{Dir: wt}).Diff(head)
	if err != nil {
		r.fail(err)
		return
	}
	rev := review.Build(review.Input{Base: before.Snap, Head: after.Snap, Diffs: diffs, BaseFindings: before.Findings, HeadFindings: after.Findings})
	r.set(func() {
		r.after, r.review = after, rev
		if r.state != Stopped {
			r.state = Done
		}
		r.finished = time.Now()
	})
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
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	out, err := cmd.CombinedOutput()
	s.CheckOK, s.Output = err == nil, tail(string(out), 40)
	return s, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// claude runs Claude Code headless in dir and turns its stream into events.
// It may run any command there, unsandboxed: the push ban and the throwaway
// worktree are the only guards.
func (r *Run) claude(ctx context.Context, agent, dir, prompt string) error {
	cmd := exec.CommandContext(ctx, agent, "-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "Bash(git push *)")
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start claude: %w", err)
	}
	r.read(stdout)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("claude: %w: %s", err, tail(stderr.String(), 5))
	}
	return nil
}

func (r *Run) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		at := time.Since(r.Started)
		for _, e := range parse(sc.Bytes()) {
			switch {
			case e.step != nil:
				st := *e.step
				st.Start, st.End = at, at
				st.Path, st.File = r.place(st.Path)
				if st.Command != "" {
					st.Reads = r.tree.named(st.Command)
					st.Kind = shellKind(st.Command, st.Reads)
				}
				r.set(func() {
					// A thought lasts until the next step begins.
					if n := len(r.steps); n > 0 && r.steps[n-1].Kind == Think {
						r.steps[n-1].End = at
					}
					r.steps = append(r.steps, st)
				})
			case e.result != nil:
				res := *e.result
				changes := r.tree.changes()
				r.set(func() {
					for i := len(r.steps) - 1; i >= 0; i-- {
						if r.steps[i].ID == res.id {
							st := &r.steps[i]
							st.End, st.Output, st.Failed, st.Changes = at, res.output, res.failed, changes
							if len(changes) > 0 && st.Kind != Edit {
								st.Kind = Edit
							}
							break
						}
					}
				})
				if r.addsFiles(changes) {
					select {
					case r.remap <- struct{}{}:
					default: // an analysis is already due; it will see this change too
					}
				}
			case e.final != nil:
				r.set(func() {
					r.summary = e.final.summary
					r.usage.settle(e.final.by)
				})
			case e.init != nil:
				r.set(func() { r.usage.Model, r.usage.Version, r.usage.Session = e.init.model, e.init.version, e.init.session })
			case e.use != nil:
				r.set(func() { r.usage.add(e.use.id, e.use.model, e.use.t) })
			}
		}
	}
}

// place names a path Claude used relative to the worktree, and as a module
// file when it is one, so the map can light it.
func (r *Run) place(p string) (shown, file string) {
	if p == "" {
		return "", ""
	}
	r.mu.Lock()
	wt, mod := r.wt, r.mod
	r.mu.Unlock()
	if !filepath.IsAbs(p) {
		p = filepath.Join(mod, p)
	}
	shown = p
	if rel, err := filepath.Rel(wt, p); err == nil && !strings.HasPrefix(rel, "..") {
		shown = filepath.ToSlash(rel)
	}
	if rel, err := filepath.Rel(mod, p); err == nil && !strings.HasPrefix(rel, "..") {
		file = filepath.ToSlash(rel)
	}
	return shown, file
}

// Kinds of step, one swimlane each.
const (
	Think  = "think"
	Read   = "read"
	Search = "search"
	Edit   = "edit"
	Exec   = "run"
	Other  = "other"
)

var kinds = map[string]string{
	"Read": Read, "NotebookRead": Read,
	"Edit": Edit, "MultiEdit": Edit, "Write": Edit, "NotebookEdit": Edit,
	"Bash": Exec, "BashOutput": Exec,
	"Grep": Search, "Glob": Search, "LS": Search, "WebSearch": Search, "WebFetch": Search,
}

// clipAt bounds the text a step keeps: a rewritten file or a long test log
// would otherwise ride along on every re-render of the page.
const clipAt = 8000

type parsed struct {
	step   *Step
	result *struct {
		id, output string
		failed     bool
	}
	final *struct {
		summary string
		by      map[string]tally
	}
	init *struct{ model, version, session string }
	use  *struct {
		id, model string
		t         tokens
	}
}

// parse reads one stream-json line into steps: the assistant's thoughts and
// tool calls, the results of those calls, and the final summary with its
// tally; and what each reply used, for the cost meter.
func parse(line []byte) []parsed {
	var m struct {
		Type       string           `json:"type"`
		Subtype    string           `json:"subtype"`
		Model      string           `json:"model"`
		Version    string           `json:"claude_code_version"`
		Session    string           `json:"session_id"`
		Result     string           `json:"result"`
		ModelUsage map[string]tally `json:"modelUsage"`
		Message    struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Usage   *tokens         `json:"usage"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	_ = json.Unmarshal(m.Message.Content, &blocks)
	var out []parsed
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			out = append(out, parsed{init: &struct{ model, version, session string }{m.Model, m.Version, m.Session}})
		}
	case "result":
		out = append(out, parsed{final: &struct {
			summary string
			by      map[string]tally
		}{m.Result, m.ModelUsage}})
	case "assistant":
		if m.Message.Usage != nil {
			out = append(out, parsed{use: &struct {
				id, model string
				t         tokens
			}{m.Message.ID, m.Message.Model, *m.Message.Usage}})
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					out = append(out, parsed{step: &Step{Kind: Think, Title: firstLine(t), Text: clip(t, false)}})
				}
			case "tool_use":
				out = append(out, parsed{step: toolStep(b.ID, b.Name, b.Input)})
			}
		}
	case "user":
		for _, b := range blocks {
			if b.Type == "tool_result" {
				out = append(out, parsed{result: &struct {
					id, output string
					failed     bool
				}{b.ToolUseID, clip(resultText(b.Content), true), b.IsError}})
			}
		}
	}
	return out
}

func toolStep(id, name string, input json.RawMessage) *Step {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
		Command      string `json:"command"`
		OldString    string `json:"old_string"`
		NewString    string `json:"new_string"`
		Content      string `json:"content"`
		Edits        []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	_ = json.Unmarshal(input, &in)
	st := &Step{ID: id, Tool: name, Kind: kinds[name], Path: cmp.Or(in.FilePath, in.NotebookPath), Command: in.Command}
	if st.Kind == "" {
		st.Kind = Other
	}
	switch st.Kind {
	case Edit:
		st.Old, st.New = in.OldString, cmp.Or(in.NewString, in.Content)
		for _, e := range in.Edits {
			st.Old += e.OldString + "\n"
			st.New += e.NewString + "\n"
		}
		st.Old, st.New = clip(st.Old, false), clip(st.New, false)
	case Search:
		if st.Path == "" && in.Path != "" && !strings.ContainsAny(in.Path, "*?") {
			st.Path = in.Path
		}
	}
	arg := cmp.Or(firstLine(in.Command), in.Pattern, path.Base(st.Path))
	st.Title = strings.TrimSpace(name + " " + arg)
	return st
}

// resultText reads a tool result's content: a string, or a list of blocks
// whose text parts are joined.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type, Text string
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// firstLine is a text's first line, cut to a title's length at a word.
func firstLine(s string) string {
	l := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(l) <= 100 {
		return l
	}
	if i := strings.LastIndexByte(l[:100], ' '); i > 60 {
		return l[:i] + " …"
	}
	return l[:100] + "…"
}

// clip keeps the head of a text, or its tail for output, where a failure
// usually shows last.
func clip(s string, tail bool) string {
	if len(s) <= clipAt {
		return s
	}
	if tail {
		return "…" + s[len(s)-clipAt:]
	}
	return s[:clipAt] + "…"
}
