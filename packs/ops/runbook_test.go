package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRunbook(t *testing.T, profileDir, name, body string) {
	t.Helper()
	dir := filepath.Join(profileDir, "runbooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRunbookName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ok   bool
	}{
		{"high-error-rate", true},
		{"db_connections", true},
		{"v1.2", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../../etc/passwd", false},
		{`a\b`, false},
		{"a/b", false},
		{"a..b", false},
		{"has space", false},
		{strings.Repeat("x", 129), false},
	}
	for _, c := range cases {
		err := validateRunbookName(c.name)
		if (err == nil) != c.ok {
			t.Errorf("validateRunbookName(%q) err=%v, wantOK=%v", c.name, err, c.ok)
		}
	}
}

func TestListRunbooksMissingDirIsEmpty(t *testing.T) {
	t.Parallel()
	names, err := listRunbooks(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(names) != 0 {
		t.Fatalf("names=%v err=%v", names, err)
	}
}

func TestListRunbooksSortsAndFiltersNonMarkdown(t *testing.T) {
	t.Parallel()
	prof := t.TempDir()
	writeRunbook(t, prof, "zeta", "z")
	writeRunbook(t, prof, "alpha", "a")
	rbDir := filepath.Join(prof, "runbooks")
	// Non-markdown and a subdirectory must both be skipped.
	if err := os.WriteFile(filepath.Join(rbDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rbDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	names, err := listRunbooks(rbDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Fatalf("names=%v", names)
	}
}

func TestReadRunbookRejectsTraversal(t *testing.T) {
	t.Parallel()
	prof := t.TempDir()
	// A readable secret one level above the runbooks dir.
	secret := filepath.Join(prof, "secret.md")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRunbook(t, prof, "ok", "fine")
	dir := filepath.Join(prof, "runbooks")

	for _, bad := range []string{"../secret", "../../etc/passwd", "..", "a/b"} {
		out, err := readRunbook(dir, bad)
		if err == nil {
			t.Fatalf("readRunbook(%q) should fail, got %q", bad, out)
		}
		if strings.Contains(out, "TOP SECRET") {
			t.Fatalf("readRunbook(%q) leaked file contents", bad)
		}
	}
}

func TestReadRunbookTruncatesLargeBody(t *testing.T) {
	t.Parallel()
	prof := t.TempDir()
	writeRunbook(t, prof, "big", strings.Repeat("x", maxRunbookBytes+500))
	body, err := readRunbook(filepath.Join(prof, "runbooks"), "big")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body, "…(truncated)") {
		t.Fatalf("expected truncation marker, got %d bytes", len(body))
	}
	if len(body) > maxRunbookBytes+64 {
		t.Fatalf("body not capped: %d bytes", len(body))
	}
}

func TestRunbookToolListAndGet(t *testing.T) {
	cfg := "services:\n  - name: api\n"
	eng, root := newOpsEngine(t, "fleet", cfg)
	writeRunbook(t, filepath.Join(root, "fleet"), "high-errors",
		"# High error rate\n\n1. ops_action service=api action=status\n")
	ctx := ctxWithEngine(eng)

	out, err := runbookTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "action": "list"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "high-errors") {
		t.Fatalf("list out=%s", out)
	}

	out, err = runbookTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "get", "name": "high-errors",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "High error rate") {
		t.Fatalf("get out=%s", out)
	}
}

func TestRunbookToolErrors(t *testing.T) {
	eng, _ := newOpsEngine(t, "fleet", "services:\n  - name: api\n")
	ctx := ctxWithEngine(eng)

	// No runbooks dir at all.
	out, _ := runbookTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "action": "list"}))
	if !strings.Contains(out, "(none)") {
		t.Fatalf("empty list out=%s", out)
	}
	// Unknown name.
	out, _ = runbookTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "get", "name": "ghost",
	}))
	if !strings.Contains(out, "not found") {
		t.Fatalf("missing out=%s", out)
	}
	// Traversal through the tool surface.
	out, _ = runbookTool{}.Exec(ctx, mustJSON(t, map[string]any{
		"ops": "fleet", "action": "get", "name": "../../../etc/passwd",
	}))
	if !strings.Contains(out, "error:") {
		t.Fatalf("traversal should error, out=%s", out)
	}
	// Bad action.
	out, _ = runbookTool{}.Exec(ctx, mustJSON(t, map[string]any{"ops": "fleet", "action": "nope"}))
	if !strings.Contains(out, "list|get") {
		t.Fatalf("bad action out=%s", out)
	}
	// Bad JSON.
	if _, err := (runbookTool{}).Exec(ctx, []byte("{bad")); err == nil {
		t.Fatal("expected JSON error")
	}
	// No engine in context.
	out, _ = runbookTool{}.Exec(context.Background(), []byte(`{"action":"list"}`))
	if !strings.Contains(out, "engine context") {
		t.Fatalf("no-engine out=%s", out)
	}
}

func TestRunbookToolIsReadOnly(t *testing.T) {
	t.Parallel()
	if !(runbookTool{}).ReadOnly() {
		t.Fatal("ops_runbook must be read-only so it runs in read-only prompts")
	}
}

func TestSystemAppendListsRunbooksAndAllActions(t *testing.T) {
	root := t.TempDir()
	cfg := `
services:
  - name: api
    actions:
      restart: [echo, r]
      status:  [echo, s]
      drain:   [echo, d]
`
	dir := writeProfileDir(t, root, "fleet", cfg)
	writeRunbook(t, dir, "high-errors", "body")

	p, err := loadProfile("fleet", PackConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	sys := p.systemAppend()
	// A custom action must be advertised, not just restart/status.
	if !strings.Contains(sys, "drain") {
		t.Fatalf("custom action missing from system text: %s", sys)
	}
	if !strings.Contains(sys, "high-errors") {
		t.Fatalf("runbook name missing from system text: %s", sys)
	}
	if !strings.Contains(sys, "ops_runbook") {
		t.Fatalf("ops_runbook missing from tools line: %s", sys)
	}
}

func TestActionNames(t *testing.T) {
	t.Parallel()
	if got := actionNames(nil); got != nil {
		t.Fatalf("nil map = %v", got)
	}
	got := actionNames(map[string][]string{
		"status":  {"echo"},
		"restart": {"echo"},
		"empty":   {},
	})
	if len(got) != 2 || got[0] != "restart" || got[1] != "status" {
		t.Fatalf("got=%v (want sorted, empty argv dropped)", got)
	}
}
