package main

import (
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/joaomdsg/codemesh/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolate(t *testing.T) {
	t.Helper()
	// XDG_CACHE_HOME moves Go's build cache too, which would start every test cold.
	goCache, err := exec.Command("go", "env", "GOCACHE").Output()
	require.NoError(t, err)
	t.Setenv("GOCACHE", strings.TrimSpace(string(goCache)))
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir())
	}
}

// The arguments go into the workspace, and the server stops when the terminal
// closes.
func TestRun_addsArgumentsToTheWorkspaceAndStopsOnHangup(t *testing.T) {
	isolate(t)
	repo := testrepo.New(t, map[string]string{"go.mod": "module m\n\ngo 1.27\n", "m.go": "package m\n"}, nil)
	flag.CommandLine = flag.NewFlagSet("codemesh", flag.ContinueOnError)
	os.Args = []string{"codemesh", "-addr", "127.0.0.1:0", "-poll", "1h", repo}
	errc := make(chan error, 1)
	go func() { errc <- run() }()

	ws, err := workspace.Open()
	require.NoError(t, err)
	require.Eventually(t, func() bool { r, _ := ws.Repos(); return len(r) == 1 }, time.Minute, 50*time.Millisecond)

	self, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, self.Signal(syscall.SIGHUP))
	select {
	case err := <-errc:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatal("the server did not stop on SIGHUP")
	}
}

func TestPrognoses_printsTheTreesPlacesThatNeedAttention(t *testing.T) {
	isolate(t)
	dir := testrepo.New(t, testrepo.CalcBase, testrepo.CalcHead)
	os.Args = []string{"codemesh", "prognoses", dir}
	out := captureStdout(t, func() { require.NoError(t, listPrognoses()) })

	assert.Contains(t, out, "tangle")
	repos, err := mustRepos()
	require.NoError(t, err)
	assert.Empty(t, repos, "reading a tree does not register it")
}

func mustRepos() ([]workspace.Repo, error) {
	ws, err := workspace.Open()
	if err != nil {
		return nil, err
	}
	return ws.Repos()
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	require.NoError(t, w.Close())
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}
