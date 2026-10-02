//go:build unix

package engine

import (
	"os"
	"syscall"
)

func scratchOwned(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid())
}
