//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

// TestOpenatBeneathConfinement: the kernel refuses any path escaping the
// root fd — via .. and via a symlink pointing outside. Requires Linux 5.6+.
func TestOpenatBeneathConfinement(t *testing.T) {
	if !openat2Supported() {
		t.Skip("kernel lacks openat2")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	// In-jail file + escape symlink.
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("yes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}

	rootF, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootF.Close()

	// In-jail open succeeds.
	f, supported, err := openatBeneath(rootF.Fd(), "ok.txt", os.O_RDONLY, 0)
	if !supported || err != nil {
		t.Fatalf("in-jail open: supported=%v err=%v", supported, err)
	}
	f.Close()

	// Symlink escape refused by the kernel (ELOOP-family), not by our code.
	if _, _, err := openatBeneath(rootF.Fd(), "link-out", os.O_RDONLY, 0); err == nil {
		t.Fatal("symlink escape must be refused")
	}

	// .. escape refused.
	if _, _, err := openatBeneath(rootF.Fd(), "../"+filepath.Base(outside)+"/secret.txt", os.O_RDONLY, 0); err == nil {
		t.Fatal("dotdot escape must be refused")
	}
}

// TestOpenJailedUsesKernelPath: openJailed still serves reads through the
// fast path (in-jail file readable) and still rejects outside paths.
func TestOpenJailedUsesKernelPath(t *testing.T) {
	if !openat2Supported() {
		t.Skip("kernel lacks openat2")
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws}
	f, path, err := openJailed(p, "a.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("openJailed in-jail: %v", err)
	}
	f.Close()
	if path != filepath.Join(ws, "a.txt") {
		t.Fatalf("path = %q", path)
	}
	if _, _, err := openJailed(p, "/etc/passwd", os.O_RDONLY, 0); err == nil {
		t.Fatal("outside path must be rejected")
	}
}

// TestWriteFileJailedKernelCreate: the write path exercises openat2 create
// (O_WRONLY|O_CREATE|O_TRUNC) and lands inside the workspace.
func TestWriteFileJailedKernelCreate(t *testing.T) {
	if !openat2Supported() {
		t.Skip("kernel lacks openat2")
	}
	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws, AllowWrite: true}
	if _, err := WriteFileJailed(p, "new/nested.txt", []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(ws, "new", "nested.txt"))
	if err != nil || string(b) != "data" {
		t.Fatalf("readback b=%q err=%v", b, err)
	}
}
