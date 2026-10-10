package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A server sweeps worktrees a dead server left behind when it starts, and
// removes its own when the terminal closes.
func TestRun_sweepsDeadServersWorktreesAndCleansUpOnHangup(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	worktrees := func() string {
		out, err := exec.Command("git", "-C", repo, "worktree", "list").Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	dead := exec.Command("true")
	require.NoError(t, dead.Run())
	gone := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, testrepo.Git(repo, "worktree", "add", "--quiet", "--detach", "--lock",
		"--reason", fmt.Sprintf("codemesh pid %d", dead.Process.Pid), gone, "HEAD"))

	flag.CommandLine = flag.NewFlagSet("codemesh", flag.ContinueOnError)
	os.Args = []string{"codemesh", "-addr", "127.0.0.1:0", "-poll", "1h", repo}
	errc := make(chan error, 1)
	go func() { errc <- run() }()

	// The base worktree appears once the first analysis ran, after the
	// signal handler is in place.
	require.Eventually(t, func() bool { return strings.Contains(worktrees(), "codemesh-wt-") }, time.Minute, 50*time.Millisecond)
	assert.NotContains(t, worktrees(), gone, "the dead server's worktree is swept")
	assert.NoDirExists(t, gone)

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	select {
	case err := <-errc:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatal("the server did not stop on SIGHUP")
	}
	assert.Equal(t, 0, strings.Count(worktrees(), "\n"), "only the repository itself is left")
}
