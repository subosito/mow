package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWorkspaceSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)

	if _, err := LoadWorkspaceSet("ghost"); err == nil {
		t.Fatal("want error when workspaces.yaml missing")
	}

	ws := t.TempDir()
	if err := os.WriteFile(WorkspaceSetsPath(), []byte(`workspaces:
  duo:
    root: `+ws+`
    extra_roots:
      - ../other:ro
      - sub/dir
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadWorkspaceSet("nope"); err == nil || !strings.Contains(err.Error(), "have:") {
		t.Fatalf("err = %v, want unknown name listing defined sets", err)
	}

	set, err := LoadWorkspaceSet("duo")
	if err != nil {
		t.Fatal(err)
	}
	got, roots, err := set.ResolveWorkspaceSet()
	if err != nil {
		t.Fatal(err)
	}
	if got != ws {
		t.Fatalf("workspace = %q, want %q", got, ws)
	}
	wantRO := filepath.Join(filepath.Dir(ws), "other") + ":ro"
	wantRW := filepath.Join(ws, "sub", "dir")
	if len(roots) != 2 || roots[0] != wantRO || roots[1] != wantRW {
		t.Fatalf("roots = %v, want [%s %s]", roots, wantRO, wantRW)
	}
}

func TestResolveWorkspaceSetRejectsMissingRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	set := WorkspaceSet{Root: filepath.Join(home, "does-not-exist")}
	if _, _, err := set.ResolveWorkspaceSet(); err == nil {
		t.Fatal("want error for nonexistent root dir")
	}
	empty := WorkspaceSet{}
	if _, _, err := empty.ResolveWorkspaceSet(); err == nil {
		t.Fatal("want error for empty root")
	}
}
