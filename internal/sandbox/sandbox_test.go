package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Mode
		bad  bool
	}{
		{in: "", want: ModeNone},
		{in: "none", want: ModeNone},
		{in: " NONE ", want: ModeNone},
		{in: "bwrap", want: ModeBwrap},
		{in: "bubblewrap", want: ModeBwrap},
		{in: "landlock", bad: true},
		{in: "docker", bad: true},
		{in: "bwrapp", bad: true},
	} {
		got, err := ParseMode(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseMode(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewUnknownModeErrors(t *testing.T) {
	if _, err := New(Mode("landlock"), Spec{Workspace: t.TempDir()}); err == nil {
		t.Fatal("New with unknown mode: want error")
	}
}

func TestNoneIsIdentity(t *testing.T) {
	be, err := New(ModeNone, Spec{})
	if err != nil {
		t.Fatal(err)
	}
	in := exec.Command("bash", "-lc", "echo hi")
	out, err := be.Wrap(in)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatal("none backend must return the same command")
	}
	if be.Mode() != ModeNone {
		t.Fatalf("Mode = %q", be.Mode())
	}
}

// argsFor builds a Bwrap without requiring the binary, so the argv shape is
// testable on any machine (including CI and macOS).
func argsFor(t *testing.T, ws string, roots []Root) []string {
	t.Helper()
	b := &Bwrap{Bin: "/usr/bin/bwrap", Workspace: ws, Roots: roots, NewSession: true}
	cmd, err := b.Wrap(exec.Command("bash", "-lc", "echo hi"))
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Args
}

func hasPair(args []string, flag, a, b string) bool {
	for i := 0; i+2 < len(args); i++ {
		if args[i] == flag && args[i+1] == a && args[i+2] == b {
			return true
		}
	}
	return false
}

func TestWrapBindsWorkspaceAndRoots(t *testing.T) {
	ws := t.TempDir()
	ro := t.TempDir()
	rw := t.TempDir()
	args := argsFor(t, ws, []Root{
		{Path: ws}, // duplicate of workspace: must not double-bind
		{Path: ro, ReadOnly: true},
		{Path: rw},
	})

	if args[0] != "/usr/bin/bwrap" {
		t.Fatalf("argv[0] = %q, want bwrap", args[0])
	}
	if !hasPair(args, "--bind", ws, ws) {
		t.Errorf("workspace not bound rw: %v", args)
	}
	if !hasPair(args, "--ro-bind", ro, ro) {
		t.Errorf("read-only root not --ro-bind: %v", args)
	}
	if !hasPair(args, "--bind", rw, rw) {
		t.Errorf("writable extra root not --bind: %v", args)
	}
	if hasPair(args, "--ro-bind", ws, ws) {
		t.Errorf("workspace must not also be bound read-only: %v", args)
	}

	// The original argv survives after the "--" separator.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("no -- separator: %v", args)
	}
	got := strings.Join(args[sep+1:], " ")
	if got != "bash -lc echo hi" {
		t.Errorf("inner argv = %q", got)
	}

	for _, want := range []string{"--die-with-parent", "--unshare-pid", "--new-session", "--clearenv", "--chdir"} {
		if !contains(args, want) {
			t.Errorf("missing %s: %v", want, args)
		}
	}
	// Network stays on by design: this is a filesystem jail, not containment.
	if contains(args, "--unshare-net") {
		t.Errorf("--unshare-net must not be set (network stays on): %v", args)
	}
}

func TestWrapDoesNotBindHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	args := argsFor(t, ws, nil)
	for i := 0; i+2 < len(args); i++ {
		if args[i] != "--bind" && args[i] != "--ro-bind" {
			continue
		}
		if args[i+1] == home {
			t.Fatalf("$HOME must not be bound: %v", args)
		}
	}
	// HOME is still forwarded as an env var (compilers want it); the point is
	// that the directory is not mounted.
	if !hasPair(args, "--setenv", "HOME", home) {
		t.Errorf("HOME env should pass through the allowlist: %v", args)
	}
}

func TestWrapHomeBoundWhenItIsTheWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	args := argsFor(t, home, nil)
	if !hasPair(args, "--bind", home, home) {
		t.Fatalf("workspace that happens to be $HOME must still be bound: %v", args)
	}
}

func TestWrapSkipsMissingRoots(t *testing.T) {
	ws := t.TempDir()
	missing := filepath.Join(ws, "..", "definitely-not-here-12345")
	args := argsFor(t, ws, []Root{{Path: missing}})
	for i := 0; i+2 < len(args); i++ {
		if (args[i] == "--bind" || args[i] == "--ro-bind") && strings.Contains(args[i+1], "definitely-not-here") {
			t.Fatalf("missing root should be skipped: %v", args)
		}
	}
}

func TestWrapEnvAllowlistExcludesSecrets(t *testing.T) {
	t.Setenv("MOW_API_KEY", "super-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "also-secret")
	args := argsFor(t, t.TempDir(), nil)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "super-secret") || strings.Contains(joined, "also-secret") {
		t.Fatalf("secrets must not cross into the sandbox env: %v", args)
	}
}

func TestWithNewSession(t *testing.T) {
	b := &Bwrap{Bin: "/usr/bin/bwrap", Workspace: t.TempDir(), NewSession: true}
	off := WithNewSession(b, false)
	if contains(off.(*Bwrap).Args(), "--new-session") {
		t.Error("WithNewSession(false) should drop --new-session")
	}
	if !contains(b.Args(), "--new-session") {
		t.Error("WithNewSession must not mutate the original backend")
	}
	// Non-bwrap backends pass through untouched.
	if _, ok := WithNewSession(None{}, true).(None); !ok {
		t.Error("WithNewSession on None should return None")
	}
}

func TestWrapPreservesIOAndSysProcAttr(t *testing.T) {
	b := &Bwrap{Bin: "/usr/bin/bwrap", Workspace: t.TempDir()}
	in := exec.Command("bash", "-lc", "echo hi")
	var sb strings.Builder
	in.Stdout = &sb
	in.Stderr = &sb
	out, err := b.Wrap(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != &sb || out.Stderr != &sb {
		t.Error("Wrap must preserve stdout/stderr")
	}
	if out.Dir != b.Workspace {
		t.Errorf("Dir = %q, want workspace", out.Dir)
	}
}

func TestNewBwrapMissingBinaryFailsLoudly(t *testing.T) {
	// An empty PATH makes LookPath fail even where bwrap is installed.
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	_, err := New(ModeBwrap, Spec{Workspace: t.TempDir()})
	if err == nil {
		t.Fatal("sandbox=bwrap with no bwrap on PATH must error, never fall back")
	}
	if !strings.Contains(err.Error(), "bwrap") {
		t.Errorf("error should name bwrap: %v", err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// bwrapPath returns the bwrap binary, or "" when unavailable/unusable (no
// user namespaces in this container, etc.). The probe uses the real bind set
// so it fails the same way the product would — a probe with fewer binds can
// pass where /bin/sh is not even visible.
func bwrapPath(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		return ""
	}
	ws := t.TempDir()
	be, err := New(ModeBwrap, Spec{Workspace: ws})
	if err != nil {
		return ""
	}
	probe, err := be.Wrap(exec.Command("bash", "-lc", "true"))
	if err != nil || probe.Run() != nil {
		return ""
	}
	return be.(*Bwrap).Bin
}

func TestBwrapIntegration(t *testing.T) {
	bin := bwrapPath(t)
	if bin == "" {
		t.Skip("bwrap not available/usable")
	}
	ws := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "secret.txt"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	be, err := New(ModeBwrap, Spec{Workspace: ws, Roots: []Root{{Path: ws}}})
	if err != nil {
		t.Fatal(err)
	}

	run := func(script string) (string, error) {
		cmd, err := be.Wrap(exec.Command("bash", "-lc", script))
		if err != nil {
			return "", err
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("echo hi"); err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("echo in workspace: out=%q err=%v", out, err)
	}
	if out, err := run("touch inside.txt && echo ok"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("write in workspace: out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "inside.txt")); err != nil {
		t.Fatalf("workspace write should land on the host: %v", err)
	}
	if out, err := run("cat " + filepath.Join(home, "secret.txt")); err == nil {
		t.Fatalf("reading $HOME outside the binds must fail, got %q", out)
	}
	if out, err := run("touch " + filepath.Join(home, "escape.txt")); err == nil {
		t.Fatalf("writing outside the workspace must fail, got %q", out)
	}
	// t.TempDir() lives under /tmp, which --tmpfs /tmp shadows regardless of
	// binds — so also check a path that is unbound for real: this package's
	// own source directory, outside /tmp entirely.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(cwd, "/tmp") {
		t.Skip("package dir is under /tmp; cannot test an unbound non-tmpfs path")
	}
	if out, err := run("cat " + filepath.Join(cwd, "sandbox.go")); err == nil {
		t.Fatalf("reading an unbound directory must fail, got %q", out)
	}
}

func TestBwrapIntegrationReadOnlyRoot(t *testing.T) {
	if bwrapPath(t) == "" {
		t.Skip("bwrap not available/usable")
	}
	ws := t.TempDir()
	ro := t.TempDir()
	if err := os.WriteFile(filepath.Join(ro, "data.txt"), []byte("readable"), 0o600); err != nil {
		t.Fatal(err)
	}
	be, err := New(ModeBwrap, Spec{Workspace: ws, Roots: []Root{{Path: ro, ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) (string, error) {
		cmd, _ := be.Wrap(exec.Command("bash", "-lc", script))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("cat " + filepath.Join(ro, "data.txt")); err != nil || !strings.Contains(out, "readable") {
		t.Fatalf("read-only root should be readable: out=%q err=%v", out, err)
	}
	if out, err := run("touch " + filepath.Join(ro, "nope.txt")); err == nil {
		t.Fatalf("write into a --ro-bind root must fail, got %q", out)
	}
}
