package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathWithinRoot(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	child := filepath.Join(root, "session.tools", "0001-bash-aabbccdd.txt")
	if !pathWithinRoot(root, child) {
		t.Fatal("expected path under root")
	}
	if pathWithinRoot(root, "/etc/passwd") {
		t.Fatal("expected escape blocked")
	}
	if pathWithinRoot(root, root+"-evil") {
		t.Fatal("prefix sibling must be blocked")
	}
	if pathWithinRoot("relative", filepath.Join("relative", "x")) {
		t.Fatal("relative root must be rejected")
	}
}

func TestRejectSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := rejectSymlinkComponents(root, filepath.Join(link, "x")); err == nil {
		t.Fatal("expected symlink component rejected")
	}
}
