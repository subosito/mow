//go:build unix

package goal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileLockNotStolenByStalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := acquireFileLock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	// A waiter that trusted this dead PID and unlinked the flocked inode
	// would create a second lock. The live flock must still exclude.
	if err := os.WriteFile(path, []byte("pid=99999999\nhost=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireFileLock(path, false); err == nil {
		t.Fatal("must not steal a live flock using a dead PID in the file")
	}
}

func TestFileLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "x.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := acquireFileLock(link, false); err == nil {
		t.Fatal("expected symlink lock path to fail")
	}
}
