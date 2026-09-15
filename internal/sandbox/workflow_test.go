package sandbox

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestFenceDeveloperWorkflows(t *testing.T) {
	if os.Getenv("MOW_TEST_FENCE_WORKFLOWS") != "1" {
		t.Skip("set MOW_TEST_FENCE_WORKFLOWS=1 for developer workflow checks")
	}
	ws := t.TempDir()
	be, err := New(ModeFence, Spec{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, script string, args ...string) (string, error) {
		t.Helper()
		argv := append([]string{"--noprofile", "--norc", "-c", script, "workflow"}, args...)
		cmd := exec.Command("bash", argv...)
		cmd.Dir = ws
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd, err := be.Wrap(cmd)
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		cmd.Stdout = &output
		cmd.Stderr = &output
		started := time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			t.Logf("workflow finished in %s", time.Since(started).Round(time.Millisecond))
			return output.String(), err
		case <-time.After(90 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
			t.Fatalf("workflow timed out: %s", output.String())
			return "", nil
		}
	}
	t.Run("go_build_and_run", func(t *testing.T) {
		if _, err := exec.LookPath("go"); err != nil {
			t.Fatal("Go required")
		}
		if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"go-workflow-ok\")}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, `GOCACHE="$PWD/go-cache" GOPATH="$PWD/gopath" GOPROXY=off GOTOOLCHAIN=local GOTELEMETRY=off go build -o hello main.go && ./hello`)
		if err != nil || !strings.Contains(out, "go-workflow-ok") {
			t.Fatalf("Go build failed: %v %s", err, out)
		}
	})
	t.Run("rust_build_and_run", func(t *testing.T) {
		if _, err := exec.LookPath("cargo"); err != nil {
			t.Fatal("Cargo required")
		}
		dir := filepath.Join(ws, "rust")
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"fence-workflow\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src/main.rs"), []byte("fn main(){println!(\"rust-workflow-ok\");}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, `cd rust && CARGO_HOME="$PWD/.cargo" cargo build --offline && ./target/debug/fence-workflow`)
		if err != nil || !strings.Contains(out, "rust-workflow-ok") {
			t.Fatalf("Rust build failed: %v %s", err, out)
		}
	})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "fixture registry", http.StatusNotFound)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Run("localhost_denied", func(t *testing.T) {
		out, err := run(t, `exec 3<>/dev/tcp/127.0.0.1/"$1"`, strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
		if err == nil {
			t.Fatalf("host localhost unexpectedly reachable: %s", out)
		}
		if requests.Load() != 1 {
			t.Fatal("sandbox reached host service")
		}
		if !strings.Contains(out, "Connection refused") && !strings.Contains(out, "Operation not permitted") && !strings.Contains(out, "Permission denied") {
			t.Fatalf("not a network denial: %s", out)
		}
		t.Logf("expected denial: %s", strings.TrimSpace(out))
	})
	t.Run("uncached_package_download_denied", func(t *testing.T) {
		out, err := run(t, `GOCACHE="$PWD/go-cache" GOPATH="$PWD/download-cache" GOPROXY="$1" GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off go mod download example.com/fence-fixture@v0.0.1`, srv.URL)
		if err == nil {
			t.Fatalf("package download unexpectedly succeeded: %s", out)
		}
		if requests.Load() != 1 {
			t.Fatal("sandbox reached host package registry")
		}
		if !strings.Contains(out, "127.0.0.1") {
			t.Fatalf("failed before registry request: %s", out)
		}
		t.Logf("expected denial: %s", strings.TrimSpace(out))
	})
}
