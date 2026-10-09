package fix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

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
	return s.State == Done && s.After != nil && s.After.CheckOK && s.Base != ""
}

// OpenPR commits the run's change on a new branch, pushes it to origin and
// opens a draft pull request against the branch the repository was on when
// the run started. It returns at once; the outcome shows in the snapshot.
func (r *Run) OpenPR() {
	r.mu.Lock()
	s := Snapshot{State: r.state, After: r.after, Base: r.base}
	if !s.CanPR() || r.pr.Opening || r.pr.URL != "" {
		r.mu.Unlock()
		return
	}
	r.pr = PR{Opening: true, Branch: r.pr.Branch}
	t := prText(r.p, r.check, r.before, r.after, r.summary)
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
	git := func(args ...string) error {
		_, err := command(ctx, r.wt, nil, "git", args...)
		return err
	}
	if err := git("fetch", "--quiet", "origin", r.base); err != nil {
		return "", fmt.Errorf("could not fetch %s from origin: %w", r.base, err)
	}
	if git("merge-base", "--is-ancestor", r.head, "FETCH_HEAD") != nil {
		return "", fmt.Errorf("the run started from %s, which origin/%s does not have; push %s first", r.head[:7], r.base, r.base)
	}
	// Claude may have committed some of its work; the reset folds everything
	// since the starting commit into one.
	for _, args := range [][]string{
		{"reset", "--quiet", "--soft", r.head},
		{"switch", "--quiet", "-C", branch},
		{"add", "--all"},
	} {
		if err := git(args...); err != nil {
			return "", err
		}
	}
	if git("diff", "--cached", "--quiet") == nil {
		return "", errors.New("there is no change to propose")
	}
	if err := git("commit", "--quiet", "--message", t.title); err != nil {
		return "", err
	}
	if err := git("push", "--quiet", "--force-with-lease", "--set-upstream", "origin", branch); err != nil {
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

// command runs name in dir and returns its output. Git never prompts: a
// push that needs credentials fails instead of hanging.
func command(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdin = dir, stdin
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

type text struct{ title, body string }

// prText writes the pull request from the run: the problem, whether it is
// gone, the check before and after, and Claude's own summary.
func prText(p prognosis.Prognosis, c Check, before, after *Side, summary string) text {
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
	fmt.Fprintf(&b, "- `%s` %s before and passes after.\n", c.Name, was)
	fmt.Fprintf(&b, "- Places needing attention: %d before, %d after.\n", len(before.Prognoses), len(after.Prognoses))
	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString("\n## Summary of the change\n\n" + s + "\n")
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
