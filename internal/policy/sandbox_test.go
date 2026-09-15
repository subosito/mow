package policy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	p := &Policy{Workspace: t.TempDir(), AllowShell: false, Sandbox: sandbox.ModeFence}
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
	dir := t.TempDir()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fence", "bash"} {
		if err := os.Symlink(shell, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!"+shell+"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ws := t.TempDir()
	ro := t.TempDir()
	p := &Policy{
		Workspace:          ws,
		ExtraRootsReadOnly: []string{ro},
		AllowShell:         true,
		Sandbox:            sandbox.ModeFence,
	}
	be, err := p.SandboxBackend()
	if err != nil {
		t.Fatal(err)
	}
	if !p.SandboxEnabled() {
		t.Error("SandboxEnabled should be true")
	}
	cmd, err := be.Wrap(exec.Command("bash", "-c", "true"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		FS struct {
			Write []string `json:"allowWrite"`
			Deny  []string `json:"denyWrite"`
		} `json:"filesystem"`
	}
	if err := json.Unmarshal([]byte(cmd.Args[6]), &cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.FS.Write, "") != ws || strings.Join(cfg.FS.Deny, "") != ro {
		t.Fatalf("lost roots: %+v", cfg)
	}
}
