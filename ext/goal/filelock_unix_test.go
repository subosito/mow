//go:build unix

package goal

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("stat sys is not Stat_t")
	}
	return st.Ino
}

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
	ino := inodeOf(t, path)
	if _, err := acquireFileLock(path, false); err == nil {
		t.Fatal("must not steal a live flock using a dead PID in the file")
	}
	if inodeOf(t, path) != ino {
		t.Fatal("stale PID check must not replace the live flock inode")
	}
}

func TestFileLockUnlockedStalePIDKeepsInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(path, []byte("pid=99999999\nhost=\nats=2000-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ino := inodeOf(t, path)
	fl, err := acquireFileLock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fl.Release()
	if inodeOf(t, path) != ino {
		t.Fatal("advisory acquire must flock the existing inode, not unlink and recreate")
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

func TestStoreRemoveDoesNotUnlinkHeldRunLock(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save(State{ID: "r1", Status: StatusPending, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunExclusive(s, "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := s.runLockPath("r1")
	ino := inodeOf(t, path)
	if err := s.Remove("r1", false); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("remove while held: got %v, want ErrGoalRunning", err)
	}
	if inodeOf(t, path) != ino {
		t.Fatal("refused Remove must not replace the held run.lock inode")
	}
	if err := s.Remove("r1", true); !errors.Is(err, ErrGoalRunning) {
		t.Fatalf("force remove while held: got %v, want ErrGoalRunning", err)
	}
	if inodeOf(t, path) != ino {
		t.Fatal("refused force Remove must not replace the held run.lock inode")
	}
}
