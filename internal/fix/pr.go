package fix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

// PR is a run's pull request: being opened, open at URL, or failed.
type PR struct {
	Opening bool
	URL     string
	Branch  string // the branch pushed for it, once created
	Err     error
}

// CanPR reports whether a run may become a pull request: Claude finished,
// the check passes on the result, and there is a branch to target.
func (s Snapshot) CanPR() bool {
	return s.State == Done && s.After != nil && s.After.CheckOK && s.Base != "" && s.Origin
}

// OpenPR commits the run's change on a new branch, pushes it to origin and
// opens a draft pull request against the branch the repository was on when
// the run started, pushing that branch up to the starting commit first if
// origin lacks it. It returns at once; the outcome shows in the snapshot.
func (r *Run) OpenPR() {
	r.mu.Lock()
	s := Snapshot{State: r.state, Before: r.before, After: r.after, Base: r.base, Check: r.check, Summary: r.summary,
		Origin: r.origin, rounds: r.rounds, SummaryRound: r.summaryRound, Undone: r.undone}
	if !s.CanPR() || r.closing || r.pr.Opening || r.pr.URL != "" {
		r.mu.Unlock()
		return
	}
	r.pr = PR{Opening: true, Branch: r.pr.Branch}
	t := prText(r.p, s)
	r.bg.Add(1)
	r.mu.Unlock()
	r.updates.Publish(time.Now().UnixNano())
	go func() {
		defer r.bg.Done()
		branch := "codemesh/" + slug(r.p) + "-" + r.Started.Format("0102-1504")
		r.set(func() { r.pr.Branch = branch })
		url, err := r.push(r.life, branch, t)
		r.set(func() { r.pr.Opening, r.pr.URL, r.pr.Err = false, url, err })
	}()
}

func (r *Run) push(ctx context.Context, branch string, t text) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := r.ensureBase(ctx); err != nil {
		return "", err
	}
	if err := r.commitChange(ctx, branch, t.title); err != nil {
		return "", err
	}
	out, err := command(ctx, r.wt, strings.NewReader(t.body), r.gh, "pr", "create", "--draft",
		"--base", r.base, "--head", branch, "--title", t.title, "--body-file", "-")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(out)
	if len(lines) == 0 {
		return "", errors.New(r.gh + " pr create printed no link")
	}
	return lines[len(lines)-1], nil
}

// git runs git in the run's worktree; never elsewhere, as undo resets and
// cleans where it runs.
func (r *Run) git(ctx context.Context, args ...string) error {
	_, err := r.gitOut(ctx, args...)
	return err
}

func (r *Run) gitOut(ctx context.Context, args ...string) (string, error) {
	if r.wt == "" {
		return "", errors.New("the run has no worktree")
	}
	return command(ctx, r.wt, nil, "git", args...)
}

// commit commits what is staged. A repository with no identity configured
// commits as codemesh; one with an identity keeps it, as it authors the pull
// request.
func (r *Run) commit(ctx context.Context, args ...string) error {
	var id []string
	for _, kv := range []string{"user.name=codemesh", "user.email=codemesh@localhost"} {
		key, _, _ := strings.Cut(kv, "=")
		// An empty value is no identity either.
		if v, err := r.gitOut(ctx, "config", key); err != nil || strings.TrimSpace(v) == "" {
			id = append(id, "-c", kv)
		}
	}
	return r.git(ctx, slices.Concat(id, []string{"commit", "--quiet"}, args)...)
}

// ensureBase makes sure origin's base branch holds the starting commit, so
// the pull request holds only the change. When it lacks it, the starting
// commit is pushed to it, never forced: a branch that moved on elsewhere
// stops here.
func (r *Run) ensureBase(ctx context.Context) error {
	err := r.git(ctx, "fetch", "--quiet", "origin", r.base)
	switch {
	case err != nil && !strings.Contains(err.Error(), "couldn't find remote ref"):
		return fmt.Errorf("could not fetch %s from origin: %w", r.base, err)
	case err != nil || r.git(ctx, "merge-base", "--is-ancestor", r.head, "FETCH_HEAD") != nil:
		if err := r.git(ctx, "push", "--quiet", "origin", r.head+":refs/heads/"+r.base); err != nil {
			if strings.Contains(err.Error(), "rejected") {
				return fmt.Errorf("origin's %s has commits the run did not start from; pull them into %s, then try a fix again", r.base, r.base)
			}
			return fmt.Errorf("could not push %s to origin: %w", r.base, err)
		}
	}
	return nil
}

// commitChange commits the worktree's change on branch and pushes it.
func (r *Run) commitChange(ctx context.Context, branch, title string) error {
	// Claude may have committed some of its work; the reset folds everything
	// since the starting commit into one.
	for _, args := range [][]string{
		{"reset", "--quiet", "--soft", r.head},
		{"switch", "--quiet", "-C", branch},
		{"add", "--all"},
	} {
		if err := r.git(ctx, args...); err != nil {
			return err
		}
	}
	if r.git(ctx, "diff", "--cached", "--quiet") == nil {
		return errors.New("there is no change to propose")
	}
	if err := r.commit(ctx, "--message", title); err != nil {
		return err
	}
	return r.git(ctx, "push", "--quiet", "--force-with-lease", "--set-upstream", "origin", branch)
}

// command runs name in dir and returns its output. Git never prompts: a
// push that needs credentials fails instead of hanging.
func command(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	group(cmd)
	cmd.Dir, cmd.Stdin = dir, stdin
	cmd.Env = gitx.Env("GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := exited(cmd.Run()); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

type text struct{ title, body string }

// prText writes the pull request from the run: the problem, whether it is
// gone, the check before and after, and Claude's own summary.
func prText(p prognosis.Prognosis, s Snapshot) text {
	before, after := s.Before, s.After
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** in `%s`: %s\n\n", p.Title, p.Name, p.Summary)
	gone := true
	for _, g := range after.Prognoses {
		if g.Key == p.Key {
			gone = false
		}
	}
	if gone {
		b.WriteString("- The problem is gone.\n")
	} else {
		b.WriteString("- The problem is still listed after the change; it may be smaller.\n")
	}
	was := "failed"
	if before.CheckOK {
		was = "passed"
	}
	fmt.Fprintf(&b, "- `%s` %s before and passes after.\n", s.Check.Name, was)
	fmt.Fprintf(&b, "- Places needing attention: %d before, %d after.\n", len(before.Prognoses), len(after.Prognoses))
	if sum := strings.TrimSpace(s.Summary); sum != "" {
		b.WriteString("\n## Summary of the change" + s.summaryFrom() + "\n\n" + sum + "\n")
	}
	fmt.Fprintf(&b, "\n<sub>codemesh prognosis `%s`</sub>\n", p.Key)
	return text{title: p.Title + ": " + p.Name, body: b.String()}
}

var unsafe = regexp.MustCompile(`[^a-z0-9]+`)

// slug names a branch after the prognosis's rule and place, as in
// hotspot-loader-declsof.
func slug(p prognosis.Prognosis) string {
	rule, _, _ := strings.Cut(p.Key, ":")
	s := strings.Trim(unsafe.ReplaceAllString(strings.ToLower(rule+"-"+p.Name), "-"), "-")
	return strings.TrimRight(s[:min(len(s), 48)], "-")
}
