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
