package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextSearchFindsArchiveHit(t *testing.T) {
	root := t.TempDir()
	adir := filepath.Join(root, "sess1.archive")
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# archive\n## [user]\nremember marker-zeta-99\n"
	if err := os.WriteFile(filepath.Join(adir, "0001-x.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewContextSearch(root)
	if tool == nil {
		t.Fatal("nil tool")
	}
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"marker-zeta-99"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marker-zeta-99") {
		t.Fatalf("miss: %q", out)
	}
}

func TestContextSearchEmpty(t *testing.T) {
	tool := NewContextSearch(t.TempDir())
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no context archives") {
		t.Fatalf("got %q", out)
	}
}
