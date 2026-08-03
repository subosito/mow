package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func TestLSPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_LSP_HELPER") != "1" {
		return
	}
	defer os.Exit(0)

	reader := bufio.NewReader(os.Stdin)
	for {
		var contentLen int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				n := strings.TrimSpace(line[len("content-length:"):])
				contentLen, _ = strconv.Atoi(n)
			}
		}
		if contentLen <= 0 {
			continue
		}
		buf := make([]byte, contentLen)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return
		}

		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(buf, &req) != nil {
			continue
		}

		if len(req.ID) == 0 {
			continue // notification
		}

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{}}
		case "textDocument/hover":
			result = map[string]any{"contents": "Hover details for symbol"}
		case "textDocument/definition":
			result = map[string]any{"uri": "file:///path/to/def.go", "range": map[string]any{}}
		case "textDocument/diagnostic":
			result = map[string]any{
				"items": []any{
					map[string]any{
						"severity": 1,
						"message":  "syntax error",
						"range": map[string]any{
							"start": map[string]any{"line": 10, "character": 5},
						},
						"source": "gopls",
					},
				},
			}
		default:
			result = nil
		}

		respBody, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
		header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(respBody))
		fmt.Print(header + string(respBody))
	}
}

func TestLSPPathAndURIHelpers(t *testing.T) {
	t.Parallel()

	t.Run("pathToURI", func(t *testing.T) {
		t.Parallel()
		uri := pathToURI("/foo/bar.go")
		if uri != "file:///foo/bar.go" {
			t.Errorf("got %q, want file:///foo/bar.go", uri)
		}

		uriRel := pathToURI("relative/path.go")
		if uriRel != "file:///relative/path.go" {
			t.Errorf("got %q, want file:///relative/path.go", uriRel)
		}
	})

	t.Run("absPath", func(t *testing.T) {
		t.Parallel()
		root := "/workspace"
		res, err := absPath(root, "file.go")
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(res) || !strings.HasSuffix(res, "file.go") {
			t.Errorf("unexpected absPath result: %q", res)
		}

		absFile := filepath.Join(os.TempDir(), "file.go")
		res2, err := absPath(root, absFile)
		if err != nil {
			t.Fatal(err)
		}
		if res2 != absFile {
			t.Errorf("got %q, want %q", res2, absFile)
		}
	})

	t.Run("langID", func(t *testing.T) {
		t.Parallel()
		tests := map[string]string{
			"main.go":    "go",
			"app.ts":     "typescript",
			"script.js":  "javascript",
			"module.py":  "python",
			"lib.rs":     "rust",
			"config.txt": "plaintext",
		}
		for file, want := range tests {
			if got := langID(file); got != want {
				t.Errorf("langID(%q) = %q, want %q", file, got, want)
			}
		}
	})

	t.Run("severityName", func(t *testing.T) {
		t.Parallel()
		if severityName(1) != mow.SeverityError {
			t.Error("severity 1 should be Error")
		}
		if severityName(2) != mow.SeverityWarning {
			t.Error("severity 2 should be Warning")
		}
		if severityName(3) != mow.SeverityInformation {
			t.Error("severity 3 should be Information")
		}
		if severityName(4) != mow.SeverityHint {
			t.Error("severity 4 should be Hint")
		}
		if severityName(99) != mow.SeverityError {
			t.Error("unknown severity should default to Error")
		}
	})
}

func TestFormatHoverAndParseDiagnostics(t *testing.T) {
	t.Parallel()

	t.Run("formatHover", func(t *testing.T) {
		t.Parallel()
		if got := formatHover("direct string"); got != "direct string" {
			t.Errorf("got %q", got)
		}
		if got := formatHover(map[string]any{"value": "map value"}); got != "map value" {
			t.Errorf("got %q", got)
		}
		if got := formatHover([]any{"line 1", "line 2"}); got != "line 1\nline 2" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("parseDiagnostics", func(t *testing.T) {
		t.Parallel()

		if parseDiagnostics(json.RawMessage("null")) != nil {
			t.Fatal("expected nil for null raw message")
		}

		rawReport := json.RawMessage(`{
			"items": [
				{
					"severity": 1,
					"message": "` + strings.Repeat("e", 250) + `",
					"range": {"start": {"line": 4, "character": 2}},
					"source": "compiler"
				}
			]
		}`)
		diags := parseDiagnostics(rawReport)
		if len(diags) != 1 {
			t.Fatalf("expected 1 diagnostic, got %d", len(diags))
		}
		if diags[0].Line != 5 || diags[0].Column != 3 {
			t.Errorf("expected 1-based line=5 col=3, got line=%d col=%d", diags[0].Line, diags[0].Column)
		}
		if !strings.HasSuffix(diags[0].Message, "…") {
			t.Errorf("expected message to be truncated with ellipsis")
		}

		rawBare := json.RawMessage(`[
			{
				"severity": 2,
				"message": "unused variable",
				"range": {"start": {"line": 0, "character": 0}}
			}
		]`)
		diagsBare := parseDiagnostics(rawBare)
		if len(diagsBare) != 1 || diagsBare[0].Severity != mow.SeverityWarning {
			t.Fatalf("bare array parse failed: %+v", diagsBare)
		}
	})

	t.Run("asInt helper", func(t *testing.T) {
		t.Parallel()
		if asInt(float64(42)) != 42 || asInt(10) != 10 || asInt(json.Number("5")) != 5 || asInt("invalid") != 0 {
			t.Fatal("asInt conversion failed")
		}
	})
}

func TestPostEditDiagnosticsHook(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.go")
	_ = os.WriteFile(filePath, []byte("package main\nfunc main() {}\n"), 0600)

	mockPull := func(ctx context.Context, path string) ([]mow.Diagnostic, error) {
		return []mow.Diagnostic{
			{Severity: mow.SeverityError, Message: "error msg", Line: 2, Column: 1},
			{Severity: mow.SeverityWarning, Message: "warn msg", Line: 1, Column: 1},
		}, nil
	}

	hook := postEditDiagnostics(mockPull)

	eng, err := mow.New(mow.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}

	ctx := mow.ContextWithEngine(context.Background(), eng)
	args, _ := json.Marshal(map[string]any{"path": filePath})

	dec, err := hook(ctx, ext.PostToolEvent{
		Name:   "write",
		Args:   args,
		Result: "file written successfully",
	})
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if !dec.Rewrite || !strings.Contains(dec.Result, "lsp diagnostics") || !strings.Contains(dec.Result, "error msg") {
		t.Fatalf("unexpected decision: %+v", dec)
	}

	// Non-edit tool should be ignored
	decIgnore, _ := hook(ctx, ext.PostToolEvent{Name: "read", Args: args})
	if decIgnore.Rewrite {
		t.Fatal("read tool should not trigger diagnostics pull")
	}
}

func TestLSPStdioIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.go")
	_ = os.WriteFile(filePath, []byte("package main\n"), 0600)

	cfg := Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestLSPHelperProcess", "--"},
		Root:    tmpDir,
	}

	t.Setenv("GO_WANT_LSP_HELPER", "1")

	rc := &reconnecting{cfg: cfg}
	defer rc.reset()

	ctx := context.Background()

	// Hover request
	hovRes, err := rc.hover(ctx, filePath, 0, 0)
	if err != nil {
		t.Fatalf("hover failed: %v", err)
	}
	if !strings.Contains(hovRes, "Hover details") {
		t.Fatalf("got hover %q, want 'Hover details'", hovRes)
	}

	// Definition request
	defRes, err := rc.definition(ctx, filePath, 0, 0)
	if err != nil {
		t.Fatalf("definition failed: %v", err)
	}
	if !strings.Contains(defRes, "def.go") {
		t.Fatalf("got definition %q", defRes)
	}

	// Diagnostics request
	diags, err := rc.diagnostics(ctx, filePath)
	if err != nil {
		t.Fatalf("diagnostics failed: %v", err)
	}
	if len(diags) != 1 || diags[0].Message != "syntax error" {
		t.Fatalf("unexpected diags: %+v", diags)
	}

	// HoverTool and DefTool execution
	hTool := &hoverTool{c: rc}
	if hTool.Name() != "lsp_hover" || string(hTool.Parameters()) == "" {
		t.Fatal("invalid hoverTool metadata")
	}
	hExecRes, err := hTool.Exec(ctx, json.RawMessage(fmt.Sprintf(`{"path":%q,"line":0,"character":0}`, filePath)))
	if err != nil || !strings.Contains(hExecRes, "Hover details") {
		t.Fatalf("hoverTool Exec failed: %v, %q", err, hExecRes)
	}

	dTool := &defTool{c: rc}
	if dTool.Name() != "lsp_definition" {
		t.Fatal("invalid defTool metadata")
	}
	dExecRes, err := dTool.Exec(ctx, json.RawMessage(fmt.Sprintf(`{"path":%q,"line":0,"character":0}`, filePath)))
	if err != nil || !strings.Contains(dExecRes, "def.go") {
		t.Fatalf("defTool Exec failed: %v, %q", err, dExecRes)
	}
}

func TestLSPRegisterAll(t *testing.T) {
	t.Run("empty config does nothing", func(t *testing.T) {
		tmpHome := t.TempDir()
		t.Setenv("MOW_HOME", tmpHome)

		if err := registerAll(); err != nil {
			t.Fatalf("registerAll empty config failed: %v", err)
		}
	})

	t.Run("lsp.yaml fallback in MOW_HOME", func(t *testing.T) {
		tmpHome := t.TempDir()
		t.Setenv("MOW_HOME", tmpHome)

		yamlContent := "command: ''\nroot: ''\n"
		_ = os.WriteFile(filepath.Join(tmpHome, "lsp.yaml"), []byte(yamlContent), 0600)

		if err := registerAll(); err != nil {
			t.Fatalf("registerAll lsp.yaml failed: %v", err)
		}
	})
}
