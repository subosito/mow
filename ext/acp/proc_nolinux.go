//go:build !linux

package acp

import "os/exec"

func setPeerDeathSig(cmd *exec.Cmd) {}
