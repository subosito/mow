//go:build !unix

package acp

import (
	"os"
	"syscall"
)

func acpStopSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}
