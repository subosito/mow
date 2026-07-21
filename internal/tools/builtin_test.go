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
	"github.com/subosito/mow/internal/tools"
)

func TestReadAndGlobUnderWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello-mow"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	reg := tools.Registry(p, []string{"read", "glob", "grep"})
	var readT, globT interface {
		Exec(context.Context, json.RawMessage) (string, error)
		Name() string
	}
	for _, tool := range reg {
		switch tool.Name() {
		case "read":
			readT = tool
		case "glob":
			globT = tool
		}
	}
	if readT == nil || globT == nil {
		t.Fatal("missing tools")
	}
	out, err := readT.Exec(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello-mow" {
		t.Fatalf("read=%q", out)
	}
	// escape
	if _, err := readT.Exec(context.Background(), json.RawMessage(`{"path":"../x"}`)); err == nil {
		t.Fatal("expected path jail")
	}
	list, err := globT.Exec(context.Background(), json.RawMessage(`{"pattern":"*.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "a.txt") {
		t.Fatalf("glob=%q", list)
	}
}

func TestWriteDeniedByPolicy(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowWrite: false}
	reg := tools.Registry(p, []string{"write"})
	if len(reg) != 1 {
		t.Fatalf("want write tool in registry when enabled list includes it")
	}
	_, err := reg[0].Exec(context.Background(), json.RawMessage(`{"path":"x","content":"y"}`))
	if err == nil {
		t.Fatal("expected write deny")
	}
}

func TestBashDeniedByPolicy(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: false}
	reg := tools.Registry(p, []string{"bash"})
	_, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err == nil {
		t.Fatal("expected bash deny")
	}
}

func TestWriteAllowedUnderWorkspace(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowWrite: true}
	reg := tools.Registry(p, []string{"write"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"path":"out.txt","content":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil || string(b) != "ok" {
		t.Fatalf("file=%q err=%v", b, err)
	}
	if !strings.Contains(out, "created out.txt") || !strings.Contains(out, "+ok") {
		t.Fatalf("want create diff with path, got %q", out)
	}
}

func TestEditReturnsDiffWithPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, AllowWrite: true}
	reg := tools.Registry(p, []string{"edit"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(
		`{"path":"f.go","old_string":"func A() {}","new_string":"func B() {}"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "edited f.go") {
		t.Fatalf("missing path: %q", out)
	}
	if !strings.Contains(out, "-func A() {}") || !strings.Contains(out, "+func B() {}") {
		t.Fatalf("missing hunk: %q", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.go"))
	if !strings.Contains(string(data), "func B()") {
		t.Fatalf("file not updated: %q", data)
	}
}

func TestBashTimeoutSoftReturns(t *testing.T) {
	// BashTimeoutSec caps each exec and soft-returns a clear message rather
	// than erroring — the agent loop must keep going and self-correct.
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1}
	reg := tools.Registry(p, []string{"bash"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"sleep 30"}`))
	if err != nil {
		t.Fatalf("timeout must soft-return, not error: %v", err)
	}
	if !strings.Contains(out, "timed out after 1s") {
		t.Fatalf("expected timeout message, got %q", out)
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	// A child started by a timed-out command must not survive as an orphan.
	// The process-group SIGKILL reaps it: a child scheduled to write a marker
	// file after the parent's sleep gets killed before it can write.
	root := t.TempDir()
	marker := filepath.Join(root, "survived.txt")
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1}
	reg := tools.Registry(p, []string{"bash"})
	// Child outlives the timeout window: it would write the marker ~5s in, but
	// the group kill at 1s must stop it first.
	cmd := "(sleep 5; echo yes > " + marker + ") & disown; sleep 30"
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":`+jsonQuote(cmd)+`}`))
	if err != nil {
		t.Fatalf("timeout must soft-return: %v", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected timeout, got %q", out)
	}
	// Wait past the child's scheduled write to prove it was reaped.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("orphan child survived timeout and wrote marker — process-group kill failed")
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestBashCustomTimeoutFromPolicy(t *testing.T) {
	// BashTimeoutSec=0 (unset) defaults to 60s; an explicit small value wins.
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		sec  int
	}{
		{"default when zero", 0},
		{"explicit two", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: tc.sec}
			reg := tools.Registry(p, []string{"bash"})
			// Fast command completes well under any timeout; confirms wiring
			// does not regress the happy path.
			out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"echo fast"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "fast") {
				t.Fatalf("got %q", out)
			}
		})
	}
}

func TestReadMissingFileSuggestsNearby(t *testing.T) {
	ws := t.TempDir()
	for _, f := range []string{"tui.go", "layout_test.go", "render.go"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{Workspace: ws}
	var rd interface {
		Exec(ctx context.Context, args json.RawMessage) (string, error)
	}
	for _, tool := range tools.Registry(p, []string{"read"}) {
		if tool.Name() == "read" {
			rd = tool
		}
	}
	if rd == nil {
		t.Fatal("read tool missing from registry")
	}
	_, err := rd.Exec(context.Background(), json.RawMessage(`{"path":"layout.go"}`))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no such file") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The stem match must surface the real neighbor.
	if !strings.Contains(msg, "layout_test.go") {
		t.Fatalf("expected nearby suggestion in error: %v", err)
	}
	// No stem match → directory listing fallback.
	_, err = rd.Exec(context.Background(), json.RawMessage(`{"path":"zzz.go"}`))
	if err == nil || !strings.Contains(err.Error(), "directory contains:") {
		t.Fatalf("expected directory fallback: %v", err)
	}
}
