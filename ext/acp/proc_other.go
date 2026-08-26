//go:build !unix

package acp

import "os/exec"

func setPeerProcAttr(cmd *exec.Cmd) {}

func killPeerTree(cmd *exec.Cmd) {
	killPeerTreeHard(cmd)
}

func killPeerTreeHard(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
