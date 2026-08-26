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

func signalPeerTree(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
	_ = cmd.Process.Signal(sig)
}

// killPeerTree asks the process group to exit (SIGTERM). Close follows with
// killPeerTreeHard if the reaper does not see exit.
func killPeerTree(cmd *exec.Cmd) {
	signalPeerTree(cmd, syscall.SIGTERM)
}

func killPeerTreeHard(cmd *exec.Cmd) {
	signalPeerTree(cmd, syscall.SIGKILL)
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
