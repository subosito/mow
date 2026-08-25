package policy_test

import (
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

// TestBeneath: Beneath must select the same root as ResolvePathFor (longest
// match) and return a jail-relative path safe for descriptor-relative open.
func TestBeneath(t *testing.T) {
	ws := t.TempDir()
	ro := t.TempDir()

	pol := &policy.Policy{
		Workspace:          ws,
		ExtraRootsReadOnly: []string{ro},
		AllowWrite:         true,
	}

	root, rel, ok := pol.Beneath(filepath.Join(ws, "sub", "file.txt"))
	if !ok {
		t.Fatal("workspace path must be beneath")
	}
	if root != ws || rel != filepath.Join("sub", "file.txt") {
		t.Fatalf("root=%q rel=%q", root, rel)
	}

	root, rel, ok = pol.Beneath(filepath.Join(ro, "doc.md"))
	if !ok || root != ro || rel != "doc.md" {
		t.Fatalf("extra root: ok=%v root=%q rel=%q", ok, root, rel)
	}

	// The workspace root itself is a valid target.
	root, rel, ok = pol.Beneath(ws)
	if !ok || root != ws || rel != "." {
		t.Fatalf("root itself: ok=%v root=%q rel=%q", ok, root, rel)
	}

	// Outside any root.
	if _, _, ok := pol.Beneath("/etc/passwd"); ok {
		t.Fatal("outside path must not be beneath")
	}

	// Relative or empty input is rejected.
	if _, _, ok := pol.Beneath("sub/file.txt"); ok {
		t.Fatal("relative path must not be beneath")
	}
	if _, _, ok := pol.Beneath(""); ok {
		t.Fatal("empty path must not be beneath")
	}

	// Nil / unset policy never reports beneath.
	var nilPol *policy.Policy
	if _, _, ok := nilPol.Beneath(ws); ok {
		t.Fatal("nil policy must not be beneath")
	}
	if _, _, ok := (&policy.Policy{}).Beneath(ws); ok {
		t.Fatal("unset workspace must not be beneath")
	}
}
