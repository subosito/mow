package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFencePolicyAndWrap(t *testing.T) {
	ws := t.TempDir()
	ro := filepath.Join(ws, "readonly")
	if err := os.Mkdir(ro, 0700); err != nil {
		t.Fatal(err)
	}
	f := &Fence{Bin: "/usr/bin/fence", Shell: "/bin/bash", layout: rootLayout{Workspace: ws, Roots: []Root{{Path: ro, ReadOnly: true}}}}
	cmd := exec.Command("bash", "-c", "echo '$HOME; $(false)'")
	cmd.Dir = ws
	cmd.Env = []string{"PATH=/bin", "HOME=/home/test", "MOW_API_KEY=secret", "BASH_ENV=/evil", "FENCE_CONFIG_JSON=evil"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	wrapped, err := f.Wrap(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.SysProcAttr != cmd.SysProcAttr || wrapped.Dir != ws {
		t.Fatal("lost execution properties")
	}
	if len(wrapped.Env) != 2 {
		t.Fatalf("unsafe env: %v", wrapped.Env)
	}
	if wrapped.Args[len(wrapped.Args)-1] != cmd.Args[2] {
		t.Fatal("changed shell input")
	}
	settings, err := f.settings()
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Filesystem struct {
			Strict bool     `json:"strictDenyRead"`
			Write  []string `json:"allowWrite"`
			Deny   []string `json:"denyWrite"`
		} `json:"filesystem"`
	}
	if err := json.Unmarshal(settings, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Filesystem.Strict || len(cfg.Filesystem.Write) != 1 || cfg.Filesystem.Write[0] != ws || len(cfg.Filesystem.Deny) != 1 || cfg.Filesystem.Deny[0] != ro {
		t.Fatalf("wrong policy: %s", settings)
	}
	f.layout.Workspace = filepath.Join(ws, "*")
	if _, err := f.Wrap(cmd); err == nil {
		t.Fatal("accepted glob root")
	}
}

func TestFenceMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := New(ModeFence, Spec{Workspace: t.TempDir()}); err == nil {
		t.Fatal("missing fence accepted")
	}
}

// Explicit opt-in makes unavailable isolation a failure rather than a skip.
// CI still runs policy/argv tests without requiring a Fence installation.
func TestFenceInstalledIsolation(t *testing.T) {
	if os.Getenv("MOW_TEST_FENCE") != "1" {
		t.Skip("set MOW_TEST_FENCE=1 to exercise installed Fence")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	ro := filepath.Join(ws, "readonly")
	if err := os.Mkdir(ro, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	// A permissive repository config must never override mow's generated policy.
	if err := os.WriteFile(filepath.Join(ws, "fence.json"), []byte(`{"network":{"allowedDomains":["*"]},"filesystem":{"allowWrite":["/"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	be, err := New(ModeFence, Spec{Workspace: ws, Roots: []Root{{Path: ro, ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string, args ...string) (string, error) {
		t.Helper()
		argv := append([]string{"--noprofile", "--norc", "-c", script, "test"}, args...)
		cmd := exec.Command("bash", argv...)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(), "MOW_API_KEY=must-not-leak")
		cmd, err = be.Wrap(cmd)
		if err != nil {
			t.Fatal(err)
		}
		out, e := cmd.CombinedOutput()
		return string(out), e
	}
	out, err := run(`printf ok > allowed; test -z "$MOW_API_KEY"; printf '%s' 'arguments-safe'`)
	if err != nil || !strings.Contains(out, "arguments-safe") {
		t.Fatalf("workspace: %v %s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(ws, "allowed")); err != nil || string(got) != "ok" {
		t.Fatalf("write: %q %v", got, err)
	}
	if out, err := run(`printf bad > readonly/blocked`); err == nil {
		t.Fatalf("read-only write allowed: %s", out)
	}
	if out, err := run(`IFS= read -r value < "$1"; printf '%s' "$value"`, secret); strings.Contains(out, "private") {
		t.Fatalf("outside read allowed: %v %s", err, out)
	}
	if out, err := run(`printf bad > "$1"`, filepath.Join(outside, "blocked")); err == nil {
		t.Fatalf("outside write allowed: %s", out)
	}
	if out, err := run(`exec 3<>/dev/tcp/1.1.1.1/80`); err == nil {
		t.Fatalf("raw network allowed: %s", out)
	}
	// Verify command streams and exit status survive the settings descriptor.
	input := exec.Command("bash", "--noprofile", "--norc", "-c", `IFS= read -r line; printf '%s' "$line"; exit 7`)
	input.Stdin = strings.NewReader("stdin preserved\n")
	input, err = be.Wrap(input)
	if err != nil {
		t.Fatal(err)
	}
	output, inputErr := input.CombinedOutput()
	if exit, ok := inputErr.(*exec.ExitError); !ok || exit.ExitCode() != 7 || !strings.Contains(string(output), "stdin preserved") {
		t.Fatalf("stream/exit: %v %s", inputErr, output)
	}
	// Killing the host process group must terminate the PID namespace, including
	// a background command that would otherwise leave a delayed marker.
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `(sleep 2; printf leaked > orphan) & printf ready > ready; wait`)
	cmd.Dir = ws
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd, err = be.Wrap(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for !exists(filepath.Join(ws, "ready")) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !exists(filepath.Join(ws, "ready")) {
		t.Fatal("child never ready")
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	time.Sleep(2200 * time.Millisecond)
	if exists(filepath.Join(ws, "orphan")) {
		t.Fatal("cancellation leaked child")
	}
}

func TestFenceRejectsNetworkFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux capability probe")
	}
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
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!"+shell+"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := New(ModeFence, Spec{Workspace: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "refusing proxy-only fallback") {
		t.Fatalf("accepted missing isolation: %v", err)
	}
}

func TestFenceReadOnlyWorkspaceAndWorkingDirectory(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	f := &Fence{Bin: "/usr/bin/fence", Shell: "/bin/bash", layout: rootLayout{Workspace: ws, Roots: []Root{{Path: ws}, {Path: ws, ReadOnly: true}}}}
	data, err := f.settings()
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		FS struct {
			Write []string `json:"allowWrite"`
			Deny  []string `json:"denyWrite"`
		} `json:"filesystem"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.FS.Write) != 0 || len(cfg.FS.Deny) != 1 || cfg.FS.Deny[0] != ws {
		t.Fatalf("read-only duplicate lost: %s", data)
	}
	for _, tc := range []struct{ dir, want string }{{"", ws}, {sub, sub}, {t.TempDir(), ws}} {
		cmd := exec.Command("bash", "-c", "pwd")
		cmd.Dir = tc.dir
		out, err := f.Wrap(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if out.Dir != tc.want {
			t.Fatalf("cwd %q: got %q want %q", tc.dir, out.Dir, tc.want)
		}
	}
}
