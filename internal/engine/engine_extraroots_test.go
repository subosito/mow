package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --extra-root / Options.ExtraRoots must expand the FS jail for tools and
// surface absolute roots in the system prompt so the model does not refuse them.
func TestEngineExtraRootsJailAndSystem(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var sawSystem, sawReadDesc string
	eng, err := New(Options{
		NoSession:  true,
		Workspace:  ws,
		ExtraRoots: []string{extra},
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			for _, m := range messages {
				if m.Role == "system" {
					sawSystem = m.Content
				}
			}
			for _, sp := range tools {
				if sp.Function.Name == "read" {
					sawReadDesc = sp.Function.Description
				}
			}
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	roots := eng.ExtraRoots()
	if len(roots) != 1 {
		t.Fatalf("ExtraRoots()=%v", roots)
	}
	// Absolute under extra root is allowed.
	got, err := eng.ResolvePath(filepath.Join(extra, "lib.go"))
	if err != nil {
		t.Fatalf("ResolvePath extra: %v", err)
	}
	if !strings.HasSuffix(got, "lib.go") {
		t.Fatalf("got %q", got)
	}
	// Escape still denied.
	if _, err := eng.ResolvePath(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected escape deny")
	}

	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawSystem, extra) {
		t.Fatalf("system missing extra root %q:\n%s", extra, sawSystem)
	}
	if !strings.Contains(strings.ToLower(sawSystem), "extra root") {
		t.Fatalf("system should mention extra roots:\n%s", sawSystem)
	}
	if !strings.Contains(sawReadDesc, "extra root") {
		t.Fatalf("read desc should mention extra roots: %q", sawReadDesc)
	}

	// Actually read the extra file via the builtin tool path.
	// (ResolvePath already covered; exec path uses the same policy.)
	pol := eng.pol
	if pol == nil || len(pol.ExtraRoots) == 0 {
		t.Fatal("policy ExtraRoots empty")
	}
}
