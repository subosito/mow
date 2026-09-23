//go:build linux || darwin

package workflow

import (
	"errors"
	"os"
	"syscall"
)

var errRunLocked = errors.New("workflow run locked")

func lockRunFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errRunLocked
	}
	return err
}
