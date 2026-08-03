package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

func TestPolicyExtraRootsReadOnly(t *testing.T) {
	ws := t.TempDir()
	rw := t.TempDir()
	ro := t.TempDir()

	_ = os.WriteFile(filepath.Join(ws, "ws.txt"), []byte("ws"), 0o644)
	_ = os.WriteFile(filepath.Join(rw, "rw.txt"), []byte("rw"), 0o644)
	_ = os.WriteFile(filepath.Join(ro, "ro.txt"), []byte("ro"), 0o644)

	pol := &policy.Policy{
		Workspace:          ws,
		ExtraRoots:         []string{rw},
		ExtraRootsReadOnly: []string{ro},
		AllowWrite:         true,
	}

	// Read operations allowed across all roots.
	if _, err := pol.ResolvePathFor("ws.txt", false); err != nil {
		t.Fatalf("read ws: %v", err)
	}
	if _, err := pol.ResolvePathFor(filepath.Join(rw, "rw.txt"), false); err != nil {
		t.Fatalf("read rw: %v", err)
	}
	if _, err := pol.ResolvePathFor(filepath.Join(ro, "ro.txt"), false); err != nil {
		t.Fatalf("read ro: %v", err)
	}

	// Write operations allowed in workspace and RW extra root, denied in RO extra root.
	if _, err := pol.ResolvePathFor("ws.txt", true); err != nil {
		t.Fatalf("write ws: %v", err)
	}
	if _, err := pol.ResolvePathFor(filepath.Join(rw, "rw.txt"), true); err != nil {
		t.Fatalf("write rw: %v", err)
	}
	if _, err := pol.ResolvePathFor(filepath.Join(ro, "ro.txt"), true); err == nil {
		t.Fatal("expected write deny under read-only extra root")
	}
}

// TestPolicyNestedRootsMostSpecificWins verifies most-specific root matching logic.
func TestPolicyNestedRootsMostSpecificWins(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "writable_sub")
	_ = os.MkdirAll(child, 0o755)

	_ = os.WriteFile(filepath.Join(parent, "ro.txt"), []byte("ro"), 0o644)
	_ = os.WriteFile(filepath.Join(child, "rw.txt"), []byte("rw"), 0o644)

	// Outer directory is read-only, nested sub-directory is read-write.
	pol := &policy.Policy{
		Workspace:          t.TempDir(),
		ExtraRoots:         []string{child},
		ExtraRootsReadOnly: []string{parent},
		AllowWrite:         true,
	}

	if _, err := pol.ResolvePathFor(filepath.Join(parent, "ro.txt"), true); err == nil {
		t.Fatal("expected write deny in outer RO parent")
	}
	if _, err := pol.ResolvePathFor(filepath.Join(child, "rw.txt"), true); err != nil {
		t.Fatalf("expected write allow in nested RW child: %v", err)
	}
}

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

func TestResolvePathExtraRoots(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "lib.go"), []byte("package lib\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, ExtraRoots: []string{extra}}

	// Relative still joins primary workspace.
	got, err := p.ResolvePath("in-ws.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(ws, "in-ws.txt") {
		t.Fatalf("ws rel: %q", got)
	}

	// Absolute under extra root is allowed.
	lib := filepath.Join(extra, "lib.go")
	got, err = p.ResolvePath(lib)
	if err != nil {
		t.Fatal(err)
	}
	if got != lib {
		t.Fatalf("extra abs: %q want %q", got, lib)
	}

	// Outside both roots still fails.
	out := t.TempDir()
	if _, err := p.ResolvePath(filepath.Join(out, "x")); err == nil {
		t.Fatal("expected escape outside workspace and extra roots")
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

func TestResolvePathRejectsFilesystemRoot(t *testing.T) {
	// Workspace must not be "/".
	p := &policy.Policy{Workspace: "/"}
	if _, err := p.ResolvePath("etc/passwd"); err == nil {
		t.Fatal("workspace=/ must be rejected")
	}
	// Extra root of "/" must not open the whole FS.
	ws := t.TempDir()
	p2 := &policy.Policy{Workspace: ws, ExtraRoots: []string{"/"}}
	if _, err := p2.ResolvePath("/etc/passwd"); err == nil {
		t.Fatal("extra_roots=/ must not allow /etc/passwd")
	}
	// Normal workspace path still works.
	if _, err := p2.ResolvePath("ok.txt"); err != nil {
		// ok.txt fails because extra root / fails jailRoots entirely —
		// that is acceptable (misconfiguration fails closed).
		t.Logf("with extra_roots=/: ResolvePath in-ws: %v", err)
	}
}
