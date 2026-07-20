package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

func TestResolvePathJail(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root}

	ok, err := p.ResolvePath("foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "foo.txt")
	if ok != want {
		t.Fatalf("got %q want %q", ok, want)
	}

	if _, err := p.ResolvePath("../outside"); err == nil {
		t.Fatal("expected escape error")
	}
	if _, err := p.ResolvePath(filepath.Join(root, "..", "nope")); err == nil {
		t.Fatal("expected escape via abs parent")
	}
}

func TestResolvePathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	// file outside workspace
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// symlink inside workspace pointing outside
	link := filepath.Join(root, "leak")
	if err := os.Symlink(filepath.Join(outside, "secret"), link); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root}
	if _, err := p.ResolvePath("leak"); err == nil {
		t.Fatal("expected symlink escape to fail")
	}
}

func TestResolvePathSymlinkEscapeNewFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// Symlinked directory inside the workspace: creating a NEW file through it
	// must not land outside the jail.
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root}
	if _, err := p.ResolvePath("sub/newfile.txt"); err == nil {
		t.Fatal("expected new-file write through symlinked dir to fail")
	}

	// Dangling symlink pointing outside: writing to it would create the target.
	if err := os.Symlink(filepath.Join(outside, "ghost"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ResolvePath("dangling"); err == nil {
		t.Fatal("expected dangling symlink escape to fail")
	}

	// Plain new file (and new file in a new subdir) still resolves.
	if _, err := p.ResolvePath("fresh.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ResolvePath("newdir/deep/fresh.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestAllowToolPowerDenied(t *testing.T) {
	p := &policy.Policy{Workspace: t.TempDir()}
	for _, name := range []string{"write", "edit", "bash"} {
		if err := p.AllowTool(name); err == nil {
			t.Fatalf("%s should be denied by default", name)
		}
	}
	if err := p.AllowTool("read"); err != nil {
		t.Fatal(err)
	}
	p.AllowWrite = true
	if err := p.AllowTool("write"); err != nil {
		t.Fatal(err)
	}
	p.AllowShell = true
	if err := p.AllowTool("bash"); err != nil {
		t.Fatal(err)
	}
}
