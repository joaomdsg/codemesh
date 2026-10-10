package fix

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

// endMarked kills every process whose environment holds mark: what Claude
// or a check left running in a session of its own, out of reach of their
// process groups. A process that cleared its environment escapes it.
func endMarked(mark string) {
	if mark == "" {
		return
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err == nil && bytes.Contains(append([]byte{0}, env...), []byte("\x00"+mark+"\x00")) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
