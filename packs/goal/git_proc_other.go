//go:build !unix

package goal

import "os/exec"

func setGitProcAttr(cmd *exec.Cmd) {}
