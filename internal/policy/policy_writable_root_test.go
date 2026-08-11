package policy

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyWritableRootAllowlist(t *testing.T) {
	workspace := t.TempDir()
	rw := t.TempDir()
	ro := t.TempDir()
	p := &Policy{
		Workspace:          workspace,
		ExtraRoots:         []string{rw},
		ExtraRootsReadOnly: []string{ro},
		WritableRoots:      []string{rw},
		ReadOnly:           true,
		AllowWrite:         true,
	}
	for _, tc := range []struct {
		name string
		path string
		ok   bool
	}{
		{"explicit rw root", filepath.Join(rw, "ok.txt"), true},
		{"primary workspace", filepath.Join(workspace, "denied.txt"), false},
		{"ro extra root", filepath.Join(ro, "denied.txt"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.ResolvePathFor(tc.path, true)
			if tc.ok && err != nil {
				t.Fatalf("write denied: %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "read-only")) {
				t.Fatalf("err=%v, want read-only denial", err)
			}
		})
	}
}
