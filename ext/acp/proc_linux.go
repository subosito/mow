//go:build linux

package acp

import (
	"os/exec"
	"syscall"
)

// setPeerDeathSig: if mow acp dies without Close (SIGKILL from the TUI,
// SIGHUP on a closed tty), the kernel delivers this to the peer. SIGKILL
// cannot be ignored — cursor-agent-class CLIs often keep running after
// stdin EOF.
func setPeerDeathSig(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
