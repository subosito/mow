//go:build !unix

package gitx

import (
	"os/exec"
	"time"
)

// setProcAttr installs a Cancel that kills the process (no process groups
// on this platform) and a WaitDelay so cancel does not hang forever on stuck
// grandchildren that share no group.
func setProcAttr(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second
}
