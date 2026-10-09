// Package fix asks Claude Code to treat one prognosis in a throwaway git
// worktree, then analyses the result against the tree it started from. The
// user's checkout is never written: the worktree is removed on Close.
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
	Old, New   string // the text an edit replaced, and its replacement
	Output     string // the call's result, clipped
	Failed     bool
	Start, End time.Duration
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
	err      error
	cost     float64
	summary  string
	before   *Side
	after    *Side
	check    Check
	review   *review.Review
	cleanup  func() error
	cancel   context.CancelFunc
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
	Cost          float64
	Summary       string
	Took          time.Duration
	Before, After *Side
	Review        *review.Review
	Check         Check
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
		Cost: r.cost, Summary: r.summary, Took: took, Before: r.before, After: r.after, Review: r.review, Check: r.check}
}

// Stop ends Claude early; the run keeps what it had.
func (r *Run) Stop() { r.cancel() }

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

	mu   sync.Mutex
	runs map[string]*Run
}

// NewRuns returns the registry for the module in dir.
func NewRuns(dir string) *Runs {
	return &Runs{Dir: dir, Updates: topic.New[int64](), Agent: "claude", runs: map[string]*Run{}}
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
	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{Key: p.Key, Started: time.Now(), state: Preparing, cancel: cancel, updates: rs.Updates}
	rs.runs[p.Key] = r
	go r.run(ctx, rs.Dir, rs.Agent, p)
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

func (r *Run) close() {
	r.cancel()
	r.mu.Lock()
	cleanup := r.cleanup
	r.cleanup = nil
	r.mu.Unlock()
	if cleanup != nil {
		_ = cleanup()
	}
}

func (r *Run) run(ctx context.Context, dir, agent string, p prognosis.Prognosis) {
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
	r.note("Copying the last commit (" + head[:7] + ") into a throwaway worktree. Uncommitted edits are not included.")
	wt, cleanup, err := repo.Worktree(head)
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.cleanup = cleanup })
	abs, _ := filepath.Abs(dir)
	rel, err := filepath.Rel(repo.Dir, abs)
	if err != nil {
		rel = "."
	}
	mod := filepath.Join(wt, rel)
	check := checkOf(wt, mod)
	r.set(func() { r.check, r.wt, r.mod = check, wt, mod })

	r.note("Analysing the starting point and running " + check.Name + " on it.")
	before, err := analyse(ctx, mod, check)
	if err != nil {
		r.fail(err)
		return
	}
	r.set(func() { r.before, r.state = before, Working })

	r.note("Claude is working on it.")
	if err := r.claude(ctx, agent, mod, p.Prompt(check.Name, check.Where)); err != nil && ctx.Err() == nil {
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

	after, err := analyse(context.Background(), mod, check)
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
				r.set(func() {
					// A thought lasts until the next step begins.
					if n := len(r.steps); n > 0 && r.steps[n-1].Kind == Think {
						r.steps[n-1].End = at
					}
					r.steps = append(r.steps, st)
				})
			case e.result != nil:
				res := *e.result
				r.set(func() {
					for i := len(r.steps) - 1; i >= 0; i-- {
						if r.steps[i].ID == res.id {
							r.steps[i].End, r.steps[i].Output, r.steps[i].Failed = at, res.output, res.failed
							break
						}
					}
				})
			case e.final != nil:
				r.set(func() { r.summary, r.cost = e.final.summary, e.final.cost })
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
		cost    float64
	}
}

// parse reads one stream-json line into steps: the assistant's thoughts and
// tool calls, the results of those calls, and the final summary with its cost.
func parse(line []byte) []parsed {
	var m struct {
		Type    string  `json:"type"`
		Result  string  `json:"result"`
		Cost    float64 `json:"total_cost_usd"`
		Message struct {
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
	case "result":
		out = append(out, parsed{final: &struct {
			summary string
			cost    float64
		}{m.Result, m.Cost}})
	case "assistant":
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
	st := &Step{ID: id, Tool: name, Kind: kinds[name], Path: cmp.Or(in.FilePath, in.NotebookPath)}
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

func firstLine(s string) string { return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0]) }

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
