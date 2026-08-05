package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/config"
)

func TestPathToURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/abs/path/file.go", "file:///abs/path/file.go"},
		{"rel/file.go", "file:///rel/file.go"}, // leading slash added
	}
	for _, c := range cases {
		if got := pathToURI(c.in); got != c.want {
			t.Errorf("pathToURI(%q)=%q want %q", c.in, got, c.want)
		}
		if !strings.HasPrefix(pathToURI(c.in), "file://") {
			t.Errorf("pathToURI(%q) not a file URI", c.in)
		}
	}
}

func TestAbsPath(t *testing.T) {
	root := t.TempDir()
	// Relative resolves against root.
	got, err := absPath(root, "sub/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "sub/x.go") {
		t.Fatalf("relative: %q", got)
	}
	// Absolute passes through unchanged.
	abs := filepath.Join(root, "y.go")
	got, err = absPath(root, abs)
	if err != nil || got != abs {
		t.Fatalf("absolute: %q err=%v", got, err)
	}
}

func TestLangID(t *testing.T) {
	for in, want := range map[string]string{
		"main.go":   "go",
		"app.ts":    "typescript",
		"app.js":    "javascript",
		"x.py":      "python",
		"lib.rs":    "rust",
		"README.md": "plaintext",
		"noext":     "plaintext",
		"UPPER.GO":  "go", // case-insensitive
	} {
		if got := langID(in); got != want {
			t.Errorf("langID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAsInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{float64(3), 3},
		{int(5), 5},
		{json.Number("7"), 7},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := asInt(c.in); got != c.want {
			t.Errorf("asInt(%v)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestFormatHover(t *testing.T) {
	// Plain string.
	if got := formatHover("hello"); got != "hello" {
		t.Errorf("string: %q", got)
	}
	// MarkupContent-ish map with "value".
	if got := formatHover(map[string]any{"kind": "markdown", "value": "func Foo()"}); got != "func Foo()" {
		t.Errorf("map value: %q", got)
	}
	// List concatenates.
	got := formatHover([]any{"a", map[string]any{"value": "b"}})
	if got != "a\nb" {
		t.Errorf("list: %q", got)
	}
	// Unexpected shape does not panic and yields something.
	if formatHover(map[string]any{"unexpected": 1}) == "" {
		t.Error("unexpected map should still render")
	}
}

// framedResponse writes an LSP Content-Length framed JSON-RPC message.
func framedResponse(id int64, result any) string {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func TestClientCallFraming(t *testing.T) {
	// Wire an in-memory server: the client writes its request into inW (drained
	// so it never blocks) and reads a framed response from srvOut.
	inR, inW := io.Pipe()
	srvR, srvW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, inR) }()

	c := &client{stdin: inW, stdout: bufio.NewReader(srvR), root: t.TempDir()}
	// Reply with id=1 (the first call increments nextID to 1).
	go func() { _, _ = io.WriteString(srvW, framedResponse(1, map[string]any{"contents": "ok"})) }()

	res, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("result not json: %v (%s)", err, res)
	}
	if got.Contents != "ok" {
		t.Fatalf("framing round-trip: %s", res)
	}
}

func TestClientCallSkipsNotifications(t *testing.T) {
	inR, inW := io.Pipe()
	srvR, srvW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, inR) }()
	c := &client{stdin: inW, stdout: bufio.NewReader(srvR), root: t.TempDir()}

	go func() {
		// A server-push notification (no id) must be skipped, then the real reply.
		notif, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]any{}})
		_, _ = io.WriteString(srvW, fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(notif), notif))
		_, _ = io.WriteString(srvW, framedResponse(1, "done"))
	}()

	res, err := c.call(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res)) != `"done"` {
		t.Fatalf("did not skip notification: %s", res)
	}
}

func TestRegisterAllNoConfigIsNoop(t *testing.T) {
	// No extensions.lsp and no $MOW_HOME/lsp.yaml → clean no-op (no server spawn).
	t.Setenv(config.EnvHome, t.TempDir())
	if err := registerAll(); err != nil {
		t.Fatalf("unconfigured registerAll should be a no-op, got %v", err)
	}
}

func TestRegisterAllYAMLFallbackMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvHome, dir)
	// A present-but-empty lsp.yaml (no command) is still a no-op, not an error.
	if err := os.WriteFile(filepath.Join(dir, "lsp.yaml"), []byte("args: [x]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registerAll(); err != nil {
		t.Fatalf("empty-command lsp.yaml should be a no-op, got %v", err)
	}
}
