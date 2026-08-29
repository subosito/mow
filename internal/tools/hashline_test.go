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

func TestReplaceOnceLineAnchoredKeepsFollowingLineBreak(t *testing.T) {
	content := "    description: foo\n    models:\n"
	out, err := replaceOnceLineAnchored(content, "    description: foo\n", "    description: bar")
	if err != nil {
		t.Fatal(err)
	}
	if out != "    description: bar\n    models:\n" {
		t.Fatalf("glued next line? %q", out)
	}
}

func TestReplaceOnceLineAnchoredDropsCopiedFollowingLines(t *testing.T) {
	content := "keep\n    models:\n      - x\n"
	out, err := replaceOnceLineAnchored(content, "keep", "NEW\n    models:")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "    models:") != 1 {
		t.Fatalf("duplicated models: %q", out)
	}
}

func TestWriteRejectsPagedReadBanner(t *testing.T) {
	dir := t.TempDir()
	p := &policy.Policy{Workspace: dir, AllowWrite: true}
	wt := &writeTool{p: p}
	_, err := wt.Exec(context.Background(), json.RawMessage(
		`{"path":"a.yaml","content":"groups:\n…(showing lines 1-2000 of 4000; continue with offset=2001)\n"}`,
	))
	if err == nil || !strings.Contains(err.Error(), "paged read") {
		t.Fatalf("want paging-banner error, got %v", err)
	}
}

func TestReplaceOnceLineAnchoredRejectsDuplicateWholeLine(t *testing.T) {
	content := "        surface: chat\n    foo:\n        surface: chat\n"
	_, err := replaceOnceLineAnchored(content, "        surface: chat", "        surface: image")
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("want unique-match error, got %v", err)
	}
}

func TestStripHashlineChrome(t *testing.T) {
	got := stripHashlineChrome("     12:abcd1234|  muse-image-1.0:")
	if got != "  muse-image-1.0:" {
		t.Fatalf("got %q", got)
	}
	plain := "groups:\n  reviewers:"
	if stripHashlineChrome(plain) != plain {
		t.Fatalf("must not touch plain yaml: %q", stripHashlineChrome(plain))
	}
}

func TestHashlineEditDoesNotDuplicateTail(t *testing.T) {
	content := "a\nb\nc\nd\n"
	hash := lineHash("b")
	out, err := applyHashlineEdit(content, hash, "B\nc\nd")
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\nB\nc\nd" && out != "a\nB\nc\nd\n" {
		t.Fatalf("duplicated tail? %q", out)
	}
}

func TestReplaceOnceLineAnchoredRejectsMidLineSnippet(t *testing.T) {
	content := "groups:\n  reviewers:\n    description: Code / PR review and design advice\n    models:\n      - glm-5.3\n"
	_, err := replaceOnceLineAnchored(content, "Code / PR review and ", "X")
	if err == nil || !strings.Contains(err.Error(), "line boundary") {
		t.Fatalf("want line-boundary error, got %v", err)
	}
	out, err := replaceOnceLineAnchored(content, "    description: Code / PR review and design advice", "    description: review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "description: review\n    models:") {
		t.Fatalf("whole-line replace: %q", out)
	}
}

func TestHashlineReadEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: dir, AllowWrite: true, MaxReadBytes: 1 << 20, Hashline: true}
	reg := Registry(p, []string{"read", "edit"})
	var readT, editT interface {
		Name() string
		Exec(context.Context, json.RawMessage) (string, error)
	}
	for _, tool := range reg {
		switch tool.Name() {
		case "read":
			readT = tool
		case "edit":
			editT = tool
		}
	}
	out, err := readT.Exec(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "|hello") || !strings.Contains(out, ":") {
		t.Fatalf("hashline format: %q", out)
	}
	// extract hash for hello
	var hash string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "|hello") {
			// "     1:abcd1234|hello"
			parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
			if len(parts) == 2 {
				rest := parts[1]
				hp := strings.SplitN(rest, "|", 2)
				hash = hp[0]
			}
		}
	}
	if len(hash) < 8 {
		t.Fatalf("hash %q from %q", hash, out)
	}
	args, _ := json.Marshal(map[string]string{
		"path": "a.txt", "line_hash": hash, "new_string": "hi",
	})
	if _, err := editT.Exec(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "hi\n") {
		t.Fatalf("content %q", data)
	}
}
