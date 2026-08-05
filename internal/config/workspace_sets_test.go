package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupWorkspaceSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)

	if _, _, found, err := LookupWorkspaceSet("ghost"); err == nil && found {
		t.Fatal("want miss when workspaces.yaml is absent")
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

	_, names, found, err := LookupWorkspaceSet("nope")
	if err != nil || found {
		t.Fatalf("found=%v err=%v, want a clean miss", found, err)
	}
	if !strings.Contains(strings.Join(names, ","), "duo") {
		t.Fatalf("names=%v, want the defined sets so a typo is fixable", names)
	}

	set, _, found, err := LookupWorkspaceSet("duo")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
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
