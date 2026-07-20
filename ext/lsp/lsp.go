// Package lsp registers workspace language tools via an LSP stdio server.
//
// Config (first match):
//  1. extensions.lsp in -config / $MOW_HOME/config.yaml
//  2. $MOW_HOME/lsp.yaml
//
// On RPC failure the client restarts once and retries.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/packcfg"
)

// Config is extensions.lsp.
type Config struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Root    string   `yaml:"root"`
}

func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return registerAll(configPaths...)
	})
}

func registerAll(configPaths ...string) error {
	var c Config
	ok, err := packcfg.DecodeSection("lsp", configPaths, &c)
	if err != nil {
		return fmt.Errorf("lsp extensions: %w", err)
	}
	if !ok || strings.TrimSpace(c.Command) == "" {
		path := filepath.Join(mow.Home(), "lsp.yaml")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("lsp: %s: %w", path, err)
		}
	}
	if strings.TrimSpace(c.Command) == "" {
		return nil
	}
	rc := &reconnecting{cfg: c}
	if err := rc.ensure(context.Background()); err != nil {
		return fmt.Errorf("lsp: start: %w", err)
	}
	ext.RegisterTool(&hoverTool{c: rc})
	ext.RegisterTool(&defTool{c: rc})
	return nil
}

type reconnecting struct {
	cfg Config
	mu  sync.Mutex
	c   *client
}

func (r *reconnecting) ensure(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c != nil {
		return nil
	}
	c, err := start(r.cfg)
	if err != nil {
		return err
	}
	r.c = c
	return nil
}

func (r *reconnecting) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c != nil {
		_ = r.c.close()
		r.c = nil
	}
}

func (r *reconnecting) hover(ctx context.Context, path string, line, col int) (string, error) {
	return r.withRetry(ctx, func(c *client) (string, error) {
		return c.hover(ctx, path, line, col)
	})
}

func (r *reconnecting) definition(ctx context.Context, path string, line, col int) (string, error) {
	return r.withRetry(ctx, func(c *client) (string, error) {
		return c.definition(ctx, path, line, col)
	})
}

func (r *reconnecting) withRetry(ctx context.Context, fn func(*client) (string, error)) (string, error) {
	if err := r.ensure(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	c := r.c
	r.mu.Unlock()
	out, err := fn(c)
	if err == nil {
		return out, nil
	}
	r.reset()
	if err2 := r.ensure(ctx); err2 != nil {
		return "", fmt.Errorf("%v (reconnect: %v)", err, err2)
	}
	r.mu.Lock()
	c = r.c
	r.mu.Unlock()
	return fn(c)
}

type client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Int64
	root   string
}

func start(cfg Config) (*client, error) {
	root := cfg.Root
	if root == "" {
		root, _ = os.Getwd()
	}
	root, _ = filepath.Abs(root)
	cmd := exec.Command(cfg.Command, cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), root: root}
	_, err = c.call(context.Background(), "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   pathToURI(root),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"hover": map[string]any{}, "definition": map[string]any{},
			},
		},
	})
	if err != nil {
		_ = c.close()
		return nil, err
	}
	_ = c.notify("initialized", map[string]any{})
	return c, nil
}

func (c *client) hover(ctx context.Context, path string, line, col int) (string, error) {
	abs, err := absPath(c.root, path)
	if err != nil {
		return "", err
	}
	_ = c.didOpen(abs)
	raw, err := c.call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(abs)},
		"position":     map[string]any{"line": line, "character": col},
	})
	if err != nil {
		return "", err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return "(no hover)", nil
	}
	var res struct {
		Contents any `json:"contents"`
	}
	_ = json.Unmarshal(raw, &res)
	return formatHover(res.Contents), nil
}

func (c *client) definition(ctx context.Context, path string, line, col int) (string, error) {
	abs, err := absPath(c.root, path)
	if err != nil {
		return "", err
	}
	_ = c.didOpen(abs)
	raw, err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(abs)},
		"position":     map[string]any{"line": line, "character": col},
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *client) didOpen(abs string) error {
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": pathToURI(abs), "languageId": langID(abs), "version": 1, "text": string(data),
		},
	})
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID.Add(1)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(body); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var contentLen int
		for {
			line, err := c.stdout.ReadString('\n')
			if err != nil {
				return nil, err
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
		if _, err := io.ReadFull(c.stdout, buf); err != nil {
			return nil, err
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(buf, &msg); err != nil {
			continue
		}
		if msg.Method != "" && len(msg.ID) == 0 {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s", msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err := c.stdin.Write(body)
	return err
}

func (c *client) close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

type hoverTool struct{ c *reconnecting }

func (hoverTool) Name() string { return "lsp_hover" }
func (hoverTool) Description() string {
	return "LSP hover at path line/col (0-based). Args: path, line, character."
}
func (hoverTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"},"character":{"type":"integer"}},"required":["path","line"]}`)
}
func (t *hoverTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return "", err
	}
	path, _ := m["path"].(string)
	return t.c.hover(ctx, path, asInt(m["line"]), asInt(m["character"]))
}

type defTool struct{ c *reconnecting }

func (defTool) Name() string { return "lsp_definition" }
func (defTool) Description() string {
	return "LSP go-to-definition at path line/col (0-based). Args: path, line, character."
}
func (defTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"},"character":{"type":"integer"}},"required":["path","line"]}`)
}
func (t *defTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return "", err
	}
	path, _ := m["path"].(string)
	return t.c.definition(ctx, path, asInt(m["line"]), asInt(m["character"]))
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func pathToURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

func absPath(root, p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Abs(filepath.Join(root, p))
}

func langID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".js":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	default:
		return "plaintext"
	}
}

func formatHover(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["value"].(string); ok {
			return s
		}
		b, _ := json.MarshalIndent(t, "", "  ")
		return string(b)
	case []any:
		var parts []string
		for _, x := range t {
			parts = append(parts, formatHover(x))
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	}
}
