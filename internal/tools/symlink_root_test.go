package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

// linkedDir returns a symlink to a fresh directory, the way macOS reaches
// t.TempDir() through /var → /private/var.
func linkedDir(t *testing.T) string {
	t.Helper()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// TestSymlinkedRootsDisplayAndMatch: a workspace or extra root reached
// through a symlink must still show workspace-relative paths and match
// absolute glob patterns.
func TestSymlinkedRootsDisplayAndMatch(t *testing.T) {
	ws := linkedDir(t)
	extra := linkedDir(t)
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("func Authenticate() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(extra, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "pkg", "foo.rs"), []byte("fn"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, ExtraRoots: []string{extra}, AllowWrite: true, MaxReadBytes: 1 << 20}

	for _, walk := range []bool{true, false} {
		if !walk && lookupRipgrep() == "" {
			continue
		}
		out, err := (&grepTool{p: p, noRipgrep: walk}).Exec(t.Context(), json.RawMessage(`{"pattern":"Authenticate"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "a.go") || strings.Contains(out, "..") {
			t.Fatalf("walk=%v: grep path not workspace-relative: %q", walk, out)
		}
	}

	out, err := (&editTool{p: p}).Exec(t.Context(), json.RawMessage(`{"path":"a.go","old_string":"func Authenticate() {}\n","new_string":"func Login() {}\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "edited a.go") {
		t.Fatalf("edit path not workspace-relative: %q", out)
	}

	for _, noFd := range []bool{true, false} {
		if !noFd && lookupFd() == "" {
			continue
		}
		out, err := (&globTool{p: p, noFd: noFd}).Exec(t.Context(),
			json.RawMessage(`{"pattern":`+mustJSON(filepath.Join(extra, "**", "*.rs"))+`}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "foo.rs") {
			t.Fatalf("noFd=%v: glob under symlinked extra root missed foo.rs: %q", noFd, out)
		}
	}
}
