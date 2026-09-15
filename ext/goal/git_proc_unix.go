//go:build unix

package goal

import (
	"os/exec"
	"syscall"
	"time"
)

func setGitProcAttr(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	// Own process group so cancel/timeout can tear down the whole tree
	// (git → credential helpers / nested filters).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	// Bound how long Wait blocks after Cancel if a grandchild ignores the signal.
	cmd.WaitDelay = 2 * time.Second
}
