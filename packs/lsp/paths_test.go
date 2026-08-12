package lsp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/subosito/mow"
)

func TestPathWithinRoot(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	child := filepath.Join(root, "pkg", "main.go")
	if !pathWithinRoot(root, child) {
		t.Fatal("expected path under root")
	}
	if pathWithinRoot(root, "/etc/passwd") {
		t.Fatal("expected escape blocked")
	}
	if pathWithinRoot(root, root+"-evil") {
		t.Fatal("prefix sibling must be blocked")
	}
}

func TestResolvePathContainment(t *testing.T) {
	root := t.TempDir()
	got, err := resolvePath(root, "sub/x.go")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "sub", "x.go")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := resolvePath(root, "../../../etc/passwd"); err == nil {
		t.Fatal("expected escape rejected")
	}
	absOutside := filepath.Join(t.TempDir(), "outside.go")
	if _, err := resolvePath(root, absOutside); err == nil {
		t.Fatal("absolute path outside root must be rejected")
	}
}

func TestResolvePathFromEngineJail(t *testing.T) {
	ws := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Workspace: ws,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ctx := mow.ContextWithEngine(context.Background(), eng)

	got, err := resolvePathFromEngine(ctx, ws, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(ws, "main.go") {
		t.Fatalf("got %q", got)
	}
	if _, err := resolvePathFromEngine(ctx, ws, "../../../etc/passwd"); err == nil {
		t.Fatal("engine jail must reject escape")
	}
}
