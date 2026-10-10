package fix

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

// endMarked kills every process whose environment holds mark: what Claude
// or a check left running in a session of its own, out of reach of their
// process groups. Each pass stops what it finds, and a stopped process
// cannot fork, so the passes end once one finds nothing new; then all are
// killed. A process that cleared its environment escapes it.
func endMarked(mark string) {
	if mark == "" {
		return
	}
	found := map[int]bool{}
	// A cap, should processes keep appearing faster than they are stopped.
	for range 20 {
		fresh := false
		for _, pid := range marked(mark) {
			if !found[pid] {
				found[pid], fresh = true, true
				_ = syscall.Kill(pid, syscall.SIGSTOP)
			}
		}
		if !fresh {
			break
		}
	}
	for pid := range found {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// marked lists the processes whose environment holds mark.
func marked(mark string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	want := []byte("\x00" + mark + "\x00")
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err == nil && bytes.Contains(append([]byte{0}, env...), want) {
			pids = append(pids, pid)
		}
	}
	return pids
}
