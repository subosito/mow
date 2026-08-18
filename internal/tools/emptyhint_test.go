package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

// A relative pattern only ever searches the workspace. When extra roots are
// configured, an unexplained "(no matches)" costs the model a blind retry, so
// the miss names the roots and how to reach them.
func TestEmptyResultHintNamesExtraRoots(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "found.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, ExtraRoots: []string{other}}

	t.Run("glob miss points at roots", func(t *testing.T) {
		gt := &globTool{p: p}
		res, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"**/*.go"}`))
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		if !strings.Contains(res, "(no matches)") {
			t.Fatalf("want a miss, got %q", res)
		}
		if !strings.Contains(res, other) {
			t.Errorf("hint should name the extra root %q: %q", other, res)
		}
	})

	t.Run("grep miss points at roots", func(t *testing.T) {
		gt := &grepTool{p: p}
		res, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"package"}`))
		if err != nil {
			t.Fatalf("grep: %v", err)
		}
		if !strings.Contains(res, other) {
			t.Errorf("hint should name the extra root %q: %q", other, res)
		}
	})

	t.Run("absolute pattern gets no hint", func(t *testing.T) {
		// The caller already targeted a root, so the miss is real and the
		// nudge would be noise.
		if h := emptyResultHint(p, filepath.Join(other, "*.rs")); h != "" {
			t.Errorf("absolute pattern should not be hinted: %q", h)
		}
	})

	t.Run("no extra roots means no hint", func(t *testing.T) {
		plain := &policy.Policy{Workspace: ws}
		if h := emptyResultHint(plain, "**/*.go"); h != "" {
			t.Errorf("single-root setup should not be hinted: %q", h)
		}
	})
}
