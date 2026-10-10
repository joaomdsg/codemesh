//go:build unix

package gitx_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContent_readsALinkAsItsTargetAndNoFifo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x\n"), 0o600))
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(dir, "link")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600))

	got, err := gitx.Content(filepath.Join(dir, "f"))
	require.NoError(t, err)
	assert.Equal(t, "x\n", string(got))
	got, err = gitx.Content(filepath.Join(dir, "link"))
	require.NoError(t, err)
	assert.Equal(t, "/dev/zero", string(got))

	done := make(chan []byte, 1)
	go func() { b, _ := gitx.Content(filepath.Join(dir, "fifo")); done <- b }()
	select {
	case b := <-done:
		assert.Nil(t, b, "a fifo has no content")
	case <-time.After(5 * time.Second):
		t.Fatal("reading a fifo blocked")
	}
}
