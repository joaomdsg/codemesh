package fix

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// claude runs Claude Code headless in dir and turns its stream into events.
// It may run any command there, unsandboxed: the push ban and the throwaway
// worktree are the only guards.
func (r *Run) claude(ctx context.Context, agent, dir, prompt string) error {
	// A resumed session keeps the conversation, not these flags. Each round's
	// init names the session, so the latest is the one resumed.
	args := []string{"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "Bash(git push *)"}
	r.mu.Lock()
	if r.round > 1 && r.usage.Session != "" {
		args = append(args, "--resume", r.usage.Session)
	}
	r.mu.Unlock()
	cmd := exec.CommandContext(ctx, agent, args...)
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
			r.apply(e, at)
		}
	}
}

func (r *Run) apply(e parsed, at time.Duration) {
	switch {
	case e.step != nil:
		r.addStep(*e.step, at)
	case e.result != nil:
		r.settleStep(*e.result, at)
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

func (r *Run) addStep(st Step, at time.Duration) {
	st.Start, st.End = at, at
	st.Path, st.File = r.place(st.Path)
	switch {
	case st.Command != "":
		st.Reads = r.tree.reads(st.Command)
		st.Kind = shellKind(st.Command, st.Reads)
	case st.Kind == kindRead && st.File != "":
		st.Reads = []Read{{File: st.File, From: st.lines.From, To: st.lines.To}}
	}
	st.lines = Read{}
	r.set(func() {
		st.Round = r.round
		// A thought lasts until the next step begins.
		if n := len(r.steps); n > 0 && r.steps[n-1].Kind == kindThink {
			r.steps[n-1].End = at
		}
		r.steps = append(r.steps, st)
	})
}

// command is the shell command of the call with the given id, if any.
func (r *Run) command(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.steps) - 1; i >= 0; i-- {
		if r.steps[i].ID == id {
			return r.steps[i].Command
		}
	}
	return ""
}

// settleStep closes the call a result answers and, when the call changed
// files, asks for a fresh analysis if one of them is new.
func (r *Run) settleStep(res toolResult, at time.Duration) {
	changes := r.tree.changes()
	// Placing grep's matches stats files, so it runs outside the lock.
	matched := r.tree.matched(r.command(res.id), res.output)
	r.set(func() {
		for i := len(r.steps) - 1; i >= 0; i-- {
			if r.steps[i].ID == res.id {
				st := &r.steps[i]
				st.End, st.Output, st.Failed, st.Changes = at, res.output, res.failed, changes
				if matched != nil {
					st.Reads = matched
				}
				if len(changes) > 0 && st.Kind != kindEdit {
					st.Kind = kindEdit
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
	shown, ok := inside(wt, p)
	if !ok {
		shown = p
	}
	file, _ = inside(mod, p)
	return shown, file
}

// Kinds of step, one swimlane each.
const (
	kindThink  = "think"
	kindRead   = "read"
	kindSearch = "search"
	kindEdit   = "edit"
	kindExec   = "run"
	kindOther  = "other"
)

var kinds = map[string]string{
	"Read": kindRead, "NotebookRead": kindRead,
	"Edit": kindEdit, "MultiEdit": kindEdit, "Write": kindEdit, "NotebookEdit": kindEdit,
	"Bash": kindExec, "BashOutput": kindExec,
	"Grep": kindSearch, "Glob": kindSearch, "LS": kindSearch, "WebSearch": kindSearch, "WebFetch": kindSearch,
}

// clipAt bounds the text a step keeps: a rewritten file or a long test log
// would otherwise ride along on every re-render of the page.
const clipAt = 8000

type parsed struct {
	step   *Step
	result *toolResult
	final  *finalResult
	init   *initInfo
	use    *replyUse
}

type toolResult struct {
	id, output string
	failed     bool
}

type finalResult struct {
	summary string
	by      map[string]tally
}

type initInfo struct{ model, version, session string }

type replyUse struct {
	id, model string
	t         tokens
}

type streamMsg struct {
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

type streamBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// parse reads one stream-json line into steps: the assistant's thoughts and
// tool calls, the results of those calls, and the final summary with its
// tally; and what each reply used, for the cost meter.
func parse(line []byte) []parsed {
	var m streamMsg
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	var blocks []streamBlock
	_ = json.Unmarshal(m.Message.Content, &blocks)
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			return []parsed{{init: &initInfo{m.Model, m.Version, m.Session}}}
		}
	case "result":
		return []parsed{{final: &finalResult{m.Result, m.ModelUsage}}}
	case "assistant":
		return parseAssistant(m, blocks)
	case "user":
		return parseResults(blocks)
	}
	return nil
}

func parseAssistant(m streamMsg, blocks []streamBlock) []parsed {
	var out []parsed
	if m.Message.Usage != nil {
		out = append(out, parsed{use: &replyUse{m.Message.ID, m.Message.Model, *m.Message.Usage}})
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, parsed{step: &Step{Kind: kindThink, Title: firstLine(t), Text: clip(t, false)}})
			}
		case "tool_use":
			out = append(out, parsed{step: toolStep(b.ID, b.Name, b.Input)})
		}
	}
	return out
}

func parseResults(blocks []streamBlock) []parsed {
	var out []parsed
	for _, b := range blocks {
		if b.Type == "tool_result" {
			out = append(out, parsed{result: &toolResult{b.ToolUseID, clip(resultText(b.Content), true), b.IsError}})
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
		Offset       int    `json:"offset"`
		Limit        int    `json:"limit"`
		Edits        []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	_ = json.Unmarshal(input, &in)
	st := &Step{ID: id, Tool: name, Kind: kinds[name], Path: cmp.Or(in.FilePath, in.NotebookPath), Command: in.Command}
	if st.Kind == "" {
		st.Kind = kindOther
	}
	switch st.Kind {
	case kindRead:
		if in.Offset > 0 || in.Limit > 0 {
			st.lines.From = max(in.Offset, 1)
			if in.Limit > 0 {
				st.lines.To = st.lines.From + in.Limit - 1
			}
		}
	case kindEdit:
		st.Old, st.New = in.OldString, cmp.Or(in.NewString, in.Content)
		for _, e := range in.Edits {
			st.Old += e.OldString + "\n"
			st.New += e.NewString + "\n"
		}
		st.Old, st.New = clip(st.Old, false), clip(st.New, false)
	case kindSearch:
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
