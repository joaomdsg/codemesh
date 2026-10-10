package main

import (
	"bytes"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/fix"
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

	self, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, self.Signal(syscall.SIGHUP))
	select {
	case err := <-errc:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatal("the server did not stop on SIGHUP")
	}
	assert.Equal(t, 0, strings.Count(worktrees(), "\n"), "only the repository itself is left")
}

func hangup(t *testing.T) {
	t.Helper()
	self, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, self.Signal(syscall.SIGHUP))
}

// A second signal while the runs close waits for them: SIGHUP's default
// would kill this test binary.
func TestRun_waitsForItsRunsThroughASecondSignal(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	closing := make(chan struct{})
	closeRuns = func(rs *fix.Runs) {
		close(closing)
		time.Sleep(time.Second)
		rs.Close()
	}
	t.Cleanup(func() { closeRuns = (*fix.Runs).Close })
	flag.CommandLine = flag.NewFlagSet("codemesh", flag.ContinueOnError)
	os.Args = []string{"codemesh", "-addr", "127.0.0.1:0", "-poll", "1h", repo}
	errc := make(chan error, 1)
	go func() { errc <- run() }()
	require.Eventually(t, func() bool {
		out, _ := exec.Command("git", "-C", repo, "worktree", "list").Output()
		return strings.Contains(string(out), "codemesh-wt-")
	}, time.Minute, 50*time.Millisecond)

	hangup(t)
	<-closing
	hangup(t)
	select {
	case err := <-errc:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatal("the server did not stop")
	}
}

func TestStopOn_quitsAtOnceOnTheThirdSignal(t *testing.T) {
	quit := make(chan int, 1)
	exit = func(code int) { quit <- code }
	t.Cleanup(func() { exit = os.Exit })
	logged := &lockedBuffer{}
	ctx, stop := stopOn(slog.New(slog.NewTextHandler(logged, nil)))
	defer stop()

	hangup(t)
	<-ctx.Done()
	hangup(t)
	require.Eventually(t, func() bool { return strings.Contains(logged.String(), "signal again") }, 5*time.Second, 10*time.Millisecond)
	hangup(t)
	select {
	case code := <-quit:
		assert.Equal(t, 1, code)
	case <-time.After(5 * time.Second):
		t.Fatal("the third signal did not quit")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
