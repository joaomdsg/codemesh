package julia

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorker_readsRootsInOneProcessAsARunOfItsOwnWould(t *testing.T) {
	if _, err := exec.LookPath("julia"); err != nil {
		t.Skip("julia is not installed")
	}
	t.Cleanup(Frontend{}.Close)
	ctx := code.Bulk(context.Background())
	for _, dir := range []string{"testdata/jl", "testdata/env"} {
		abs, err := filepath.Abs(dir)
		require.NoError(t, err)
		alone, err := loadJulia(context.Background(), abs)
		require.NoError(t, err)

		served, err := loadJulia(ctx, abs)
		require.NoError(t, err)
		assert.Equal(t, alone, served, dir)
	}
	bulk.mu.Lock()
	cmd := bulk.cmd
	bulk.mu.Unlock()
	require.NotNil(t, cmd, "the warm-up loads went to the worker")

	_, err := loadJulia(ctx, "/nonexistent")
	assert.ErrorContains(t, err, "julia: SystemError: opening file")
	abs, err := filepath.Abs("testdata/jl")
	require.NoError(t, err)
	_, err = loadJulia(ctx, abs)
	require.NoError(t, err, "a root it cannot read leaves the worker serving")
	bulk.mu.Lock()
	assert.Same(t, cmd, bulk.cmd)
	bulk.mu.Unlock()

	Frontend{}.Close()
	_, err = loadJulia(ctx, abs)
	require.NoError(t, err, "the next load starts another")
}

func TestWorker_aCancelledLoadEndsTheProcess(t *testing.T) {
	if _, err := exec.LookPath("julia"); err != nil {
		t.Skip("julia is not installed")
	}
	t.Cleanup(Frontend{}.Close)
	abs, err := filepath.Abs("testdata/jl")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(code.Bulk(context.Background()))
	cancel()

	_, err = loadJulia(ctx, abs)
	assert.ErrorIs(t, err, context.Canceled)
	bulk.mu.Lock()
	defer bulk.mu.Unlock()
	assert.Nil(t, bulk.cmd)
}

func TestWorker_aLoadWhileTheWorkerIsBusyRunsItsOwnJulia(t *testing.T) {
	if _, err := exec.LookPath("julia"); err != nil {
		t.Skip("julia is not installed")
	}
	abs, err := filepath.Abs("testdata/jl")
	require.NoError(t, err)
	bulk.mu.Lock()
	defer bulk.mu.Unlock()

	s, err := loadJulia(code.Bulk(context.Background()), abs)
	require.NoError(t, err)
	assert.Equal(t, "Shapes", s.Module)
}

func TestWorker_anIdleTimerThatFiredDuringAReadLeavesTheWorkerServing(t *testing.T) {
	if _, err := exec.LookPath("julia"); err != nil {
		t.Skip("julia is not installed")
	}
	t.Cleanup(Frontend{}.Close)
	abs, err := filepath.Abs("testdata/jl")
	require.NoError(t, err)
	ctx := code.Bulk(context.Background())
	_, err = loadJulia(ctx, abs)
	require.NoError(t, err)

	bulk.mu.Lock()
	idle = 10 * time.Millisecond
	t.Cleanup(func() { idle = 5 * time.Minute })
	bulk.timer.Reset(idle)
	time.Sleep(50 * time.Millisecond) // the timer fires and waits for mu
	idle = 5 * time.Minute
	_, err = bulk.read(ctx, abs)
	require.NoError(t, err)
	cmd := bulk.cmd
	bulk.mu.Unlock()

	time.Sleep(50 * time.Millisecond)
	bulk.mu.Lock()
	defer bulk.mu.Unlock()
	assert.Same(t, cmd, bulk.cmd, "the read just now restarted the idle wait")
}
