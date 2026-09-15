package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/policy"
	"github.com/subosito/mow/internal/proc"
	"github.com/subosito/mow/internal/sandbox"
	"github.com/subosito/mow/internal/tools"
)

func TestFenceShellAndProcessLifecycle(t *testing.T) {
	if os.Getenv("MOW_TEST_FENCE") != "1" {
		t.Skip("set MOW_TEST_FENCE=1 for live Fence test")
	}
	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws, AllowShell: true, Sandbox: sandbox.ModeFence}
	reg := tools.Registry(p, []string{"bash"})
	var shell interface {
		Exec(context.Context, json.RawMessage) (string, error)
		Description() string
	}
	for _, tool := range reg {
		if tool.Name() == "bash" {
			shell = tool
		}
	}
	if shell == nil {
		t.Fatal("missing bash")
	}
	if !strings.Contains(shell.Description(), "Fence") {
		t.Fatal("wrong sandbox description")
	}
	out, err := shell.Exec(context.Background(), json.RawMessage(`{"command":"printf shell-ok"}`))
	if err != nil || !strings.Contains(out, "shell-ok") {
		t.Fatalf("bash: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err = shell.Exec(ctx, json.RawMessage(`{"command":"sleep 2; printf leaked > bash-orphan"}`))
	if err != nil || !strings.Contains(out, "bash timed out") {
		t.Fatalf("missing timeout result: %v %s", err, out)
	}
	box, err := p.SandboxBackend()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, err = proc.Start(dir, "fenced", "printf process-ready; sleep 2; printf leaked > proc-orphan", "", ws, box)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(dir, "fenced")
	out, err = proc.Tail(dir, "fenced", 10)
	if err != nil || !strings.Contains(out, "process-ready") {
		t.Fatalf("proc start: %v %s", err, out)
	}
	if _, err = proc.Stop(dir, "fenced"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	for _, name := range []string{"bash-orphan", "proc-orphan"} {
		if _, err := os.Stat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			t.Fatalf("leaked child: %s %v", name, err)
		}
	}
}
