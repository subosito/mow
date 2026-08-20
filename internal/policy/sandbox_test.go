package policy

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/sandbox"
)

func TestSandboxBackendDefaultIsNone(t *testing.T) {
	p := &Policy{Workspace: t.TempDir(), AllowShell: true}
	be, err := p.SandboxBackend()
	if err != nil {
		t.Fatal(err)
	}
	if be.Mode() != sandbox.ModeNone {
		t.Fatalf("default mode = %q, want none", be.Mode())
	}
	if p.SandboxEnabled() {
		t.Error("SandboxEnabled should be false by default")
	}
	in := exec.Command("bash", "-lc", "echo hi")
	out, err := p.WrapShell(in)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Error("default policy must not rewrite the command")
	}
}

// Without a shell there is nothing to jail, so the mode is inert rather than
// an error — an operator can leave --sandbox in a wrapper script safely.
func TestSandboxIgnoredWithoutShell(t *testing.T) {
	p := &Policy{Workspace: t.TempDir(), AllowShell: false, Sandbox: sandbox.ModeBwrap}
	be, err := p.SandboxBackend()
	if err != nil {
		t.Fatalf("sandbox without allow-shell should be inert, got %v", err)
	}
	if be.Mode() != sandbox.ModeNone {
		t.Errorf("mode = %q, want none", be.Mode())
	}
	if p.SandboxEnabled() {
		t.Error("SandboxEnabled should be false when the shell is off")
	}
}

func TestSandboxUnknownModeErrors(t *testing.T) {
	p := &Policy{Workspace: t.TempDir(), AllowShell: true, Sandbox: sandbox.Mode("docker")}
	if _, err := p.SandboxBackend(); err == nil {
		t.Fatal("unknown sandbox mode must error")
	}
	// The error must repeat, not memoize into a silent success.
	if _, err := p.SandboxBackend(); err == nil {
		t.Fatal("second call must also error")
	}
	if _, err := p.WrapShell(exec.Command("bash", "-lc", "echo hi")); err == nil {
		t.Fatal("WrapShell must refuse rather than run unsandboxed")
	}
}

func TestSandboxBackendCarriesJailRoots(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	ws := t.TempDir()
	ro := t.TempDir()
	p := &Policy{
		Workspace:          ws,
		ExtraRootsReadOnly: []string{ro},
		AllowShell:         true,
		Sandbox:            sandbox.ModeBwrap,
	}
	be, err := p.SandboxBackend()
	if err != nil {
		t.Fatal(err)
	}
	if !p.SandboxEnabled() {
		t.Error("SandboxEnabled should be true")
	}
	bw, ok := be.(*sandbox.Bwrap)
	if !ok {
		t.Fatalf("backend = %T, want *sandbox.Bwrap", be)
	}
	args := strings.Join(bw.Args(), " ")
	if !strings.Contains(args, "--bind "+ws) {
		t.Errorf("workspace not bound rw: %s", args)
	}
	if !strings.Contains(args, "--ro-bind "+ro) {
		t.Errorf("read-only extra root not passed through as --ro-bind: %s", args)
	}
}
