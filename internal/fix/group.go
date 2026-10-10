package fix

import (
	"errors"
	"os/exec"
	"time"
)

// group runs cmd in a process group of its own where the platform has them,
// killed whole when cmd's context ends: a shell's children outlive it and
// hold its output open, so Wait would block until they finish. Wait gives
// up on that output five seconds after cmd exits or is killed.
func group(cmd *exec.Cmd) {
	ownGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
}

// exited reads Wait's error. A command that succeeded and left a child
// holding its output still succeeded.
func exited(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}
