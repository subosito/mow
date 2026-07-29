//go:build unix

package acp

import (
	"os/exec"
	"syscall"
)

// setPeerProcAttr puts the peer in its own process group so Close can kill
// the whole tree (npx → node → claude, bwrap → peer CLI, …).
func setPeerProcAttr(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killPeerTree SIGKILLs the process group, then the process itself as fallback.
func killPeerTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
