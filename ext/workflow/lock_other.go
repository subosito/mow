//go:build !linux && !darwin

package workflow

import (
	"errors"
	"os"
)

var errRunLocked = errors.New("workflow run locking unsupported")

func lockRunFile(*os.File) error { return errRunLocked }
