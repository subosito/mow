package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
