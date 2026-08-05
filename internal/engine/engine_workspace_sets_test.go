package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceSetNameAppliesWorkspaceAndRoots(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	extra := t.TempDir()
	roExtra := t.TempDir()
	t.Setenv("MOW_HOME", home)

	if err := os.WriteFile(filepath.Join(home, "workspaces.yaml"), []byte(`workspaces:
  multi:
    root: `+ws+`
    extra_roots:
      - `+extra+`
      - `+roExtra+`:ro
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hybrid: the set name stands in for the path.
	eng, err := New(Options{
		Workspace: "multi",
		Model:     "model-a",
		NoSession: true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if got := eng.Workspace(); got != ws {
		t.Fatalf("Workspace() = %q, want %q", got, ws)
	}
	rw := eng.ExtraRoots()
	ro := eng.ExtraRootsReadOnly()
	if len(rw) != 1 || rw[0] != extra {
		t.Fatalf("ExtraRoots() = %v, want [%s]", rw, extra)
	}
	if len(ro) != 1 || ro[0] != roExtra {
		t.Fatalf("ExtraRootsReadOnly() = %v, want [%s]", ro, roExtra)
	}
}

func TestWorkspacePathStillWorks(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	eng, err := New(Options{
		Workspace: ws,
		Model:     "model-a",
		NoSession: true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Workspace(); got != ws {
		t.Fatalf("Workspace() = %q, want %q (plain path)", got, ws)
	}
}

func TestWorkspaceSetNameWinsOverSameNameDirectory(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)
	// A cwd-relative directory with the same name as a set must lose to the
	// set (defined names are the vocabulary once sets exist).
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "monorepo"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	if err := os.WriteFile(filepath.Join(home, "workspaces.yaml"), []byte(`workspaces:
  monorepo:
    root: `+ws+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Options{
		Workspace: "monorepo",
		Model:     "model-a",
		NoSession: true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Workspace(); got != ws {
		t.Fatalf("Workspace() = %q, want %q (set name wins over directory)", got, ws)
	}
}

func TestWorkspaceUnknownNameListsDefinedSets(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	if err := os.WriteFile(filepath.Join(home, "workspaces.yaml"), []byte(`workspaces:
  real:
    root: `+ws+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := New(Options{
		Workspace: "reel", // typo of "real", not a directory
		Model:     "model-a",
		NoSession: true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "real") {
		t.Fatalf("err = %v, want typo hint listing defined set \"real\"", err)
	}
}

func TestWorkspaceUnknownPathWithoutSetsFallsThrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	// No workspaces.yaml: a nonexistent path is a plain workspace error,
	// not a set lookup error (CI one-shot behavior is unchanged).
	_, err := New(Options{
		Workspace: filepath.Join(home, "no-such-dir"),
		Model:     "model-a",
		NoSession: true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil && strings.Contains(err.Error(), "defined sets") {
		t.Fatalf("err = %v, must not mention sets when none are defined", err)
	}
}

func TestWorkspaceSetRelativeRootsResolveAgainstWorkspace(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	sibling := filepath.Join(ws, "sub", "root")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOW_HOME", home)

	if err := os.WriteFile(filepath.Join(home, "workspaces.yaml"), []byte(`workspaces:
  rel:
    root: `+ws+`
    extra_roots:
      - sub/root
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Options{
		Workspace: "rel",
		Model:     "model-a",
		NoSession: true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	rw := eng.ExtraRoots()
	if len(rw) != 1 || rw[0] != sibling {
		t.Fatalf("ExtraRoots() = %v, want [%s]", rw, sibling)
	}
}

func TestWorkspaceExtraRootAppendsOnTopOfSet(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	setRoot := t.TempDir()
	adHoc := t.TempDir()
	t.Setenv("MOW_HOME", home)

	if err := os.WriteFile(filepath.Join(home, "workspaces.yaml"), []byte(`workspaces:
  combo:
    root: `+ws+`
    extra_roots:
      - `+setRoot+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Options{
		Workspace:  "combo",
		ExtraRoots: []string{adHoc},
		Model:      "model-a",
		NoSession:  true,
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	rw := eng.ExtraRoots()
	if len(rw) != 2 || rw[0] != setRoot || rw[1] != adHoc {
		t.Fatalf("ExtraRoots() = %v, want [%s %s]", rw, setRoot, adHoc)
	}
}
