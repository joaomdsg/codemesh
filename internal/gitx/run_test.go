package gitx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGit puts a git on PATH that runs script.
func fakeGit(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRun_neverPromptsForCredentials(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env")
	fakeGit(t, `echo "$GIT_TERMINAL_PROMPT" > `+out)
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	_, err := run(t.TempDir(), "status")
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "0\n", string(got))
}

func TestFetch_givesUpOnAStalledRemote(t *testing.T) {
	fakeGit(t, `exec sleep 30`)
	old := networkTimeout
	networkTimeout = 100 * time.Millisecond
	t.Cleanup(func() { networkTimeout = old })

	start := time.Now()
	err := (&Repo{Dir: t.TempDir()}).Fetch()

	assert.ErrorContains(t, err, "timed out")
	assert.Less(t, time.Since(start), 10*time.Second)
}
