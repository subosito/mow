//go:build !unix

package mcp

import "os/exec"

func setServerProcAttr(cmd *exec.Cmd) {}

func killServerTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
