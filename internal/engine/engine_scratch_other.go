//go:build !unix

package engine

import "os"

func scratchOwned(fi os.FileInfo) bool { return true }
