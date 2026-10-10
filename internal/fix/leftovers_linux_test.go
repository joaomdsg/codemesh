package fix

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDismiss_endsWhatLeftClaudesProcessGroup(t *testing.T) {
	t.Parallel()
	calls := t.TempDir()
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\nsetsid sleep 30 >/dev/null 2>&1 &\necho $! > "+calls+"/pid\nsleep 0.5\n"), 0o755))
	r := loopStart(t, agent)
	require.True(t, running(t, filepath.Join(calls, "pid")), "a new session outlives the group")

	r.close()
	assert.Eventually(t, func() bool { return !running(t, filepath.Join(calls, "pid")) }, 5*time.Second, 50*time.Millisecond)
}
