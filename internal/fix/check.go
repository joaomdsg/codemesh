package fix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

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
