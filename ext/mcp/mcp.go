// Package mcp connects trusted MCP stdio servers and registers their tools.
//
// Config (first match wins):
//  1. extensions.mcp in -config / $MOW_HOME/config.yaml
//  2. $MOW_HOME/mcp.yaml
//
// On tools/call failure, the client restarts the server once and retries.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/packcfg"
)

// ServerConfig is one MCP server (stdio or streamable HTTP).
type ServerConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"` // stdio
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	// URL enables Streamable HTTP (POST JSON-RPC; SSE response optional).
	// When set, Command is ignored. https is required except for loopback
	// hosts — set insecure: true to allow plain http elsewhere (tokens go
	// over the wire in clear text).
	URL      string            `yaml:"url"`
	Insecure bool              `yaml:"insecure"`
	Headers  map[string]string `yaml:"headers"`
	// Auth: bearer token or oauth2 client_credentials (HTTP only).
	Auth AuthConfig `yaml:"auth"`
}

// Config is extensions.mcp.
type Config struct {
	Servers []ServerConfig `yaml:"servers"`
}

func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return registerAll(configPaths...)
	})
}

func registerAll(configPaths ...string) error {
	var c Config
	ok, err := packcfg.DecodeSection("mcp", configPaths, &c)
	if err != nil {
		return fmt.Errorf("mcp extensions: %w", err)
	}
	if !ok || len(c.Servers) == 0 {
		// fallback file
		path := filepath.Join(mow.Home(), "mcp.yaml")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("mcp: %s: %w", path, err)
		}
	}
	return RegisterServers(c.Servers)
}

// RegisterServers starts each server and registers tools.
func RegisterServers(servers []ServerConfig) error {
	for _, s := range servers {
		name := s.Name
		if name == "" {
			name = "mcp"
		}
		var tr toolTransport
		var err error
		switch {
		case strings.TrimSpace(s.URL) != "":
			tr, err = newHTTPTransport(s)
		case strings.TrimSpace(s.Command) != "":
			rc := &reconnectingClient{cfg: s}
			if err = rc.ensure(context.Background()); err != nil {
				return fmt.Errorf("mcp %s: %w", name, err)
			}
			tr = rc
		default:
			continue
		}
		if err != nil {
			return fmt.Errorf("mcp %s: %w", name, err)
		}
		// initialize for HTTP too
		if ht, ok := tr.(*httpTransport); ok {
			if err := ht.initialize(context.Background()); err != nil {
				return fmt.Errorf("mcp %s init: %w", name, err)
			}
		}
		tools, err := tr.listTools(context.Background())
		if err != nil {
			_ = tr.Close()
			return fmt.Errorf("mcp %s list: %w", name, err)
		}
		n := 0
		for _, t := range tools {
			ext.RegisterTool(&mcpTool{
				client:   tr,
				prefix:   name,
				name:     t.Name,
				desc:     t.Description,
				schema:   t.InputSchema,
				readOnly: t.Annotations.ReadOnlyHint,
			})
			n++
		}
		fmt.Fprintf(os.Stderr, "mcp: registered %d tool(s) from %q\n", n, name)
	}
	return nil
}

// toolTransport is stdio or HTTP.
type toolTransport interface {
	listTools(ctx context.Context) ([]toolInfo, error)
	callTool(ctx context.Context, name string, args json.RawMessage) (string, error)
	Close() error
}

type toolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// Annotations carries the MCP tool hints; readOnlyHint lets a tool run in
	// mow's read-only prompts.
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

// reconnectingClient restarts the stdio process after a failed call.
type reconnectingClient struct {
	cfg ServerConfig
	mu  sync.Mutex
	c   *client
}

func (r *reconnectingClient) ensure(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c != nil {
		return nil
	}
	c, err := startServer(r.cfg)
	if err != nil {
		return err
	}
	r.c = c
	return nil
}

func (r *reconnectingClient) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c != nil {
		_ = r.c.Close()
		r.c = nil
	}
}

func (r *reconnectingClient) listTools(ctx context.Context) ([]toolInfo, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	c := r.c
	r.mu.Unlock()
	return c.listTools(ctx)
}

func (r *reconnectingClient) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if err := r.ensure(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	c := r.c
	r.mu.Unlock()
	out, err := c.callTool(ctx, name, args)
	if err == nil {
		return out, nil
	}
	// reconnect once
	r.reset()
	if err2 := r.ensure(ctx); err2 != nil {
		return "", fmt.Errorf("%v (reconnect: %v)", err, err2)
	}
	r.mu.Lock()
	c = r.c
	r.mu.Unlock()
	return c.callTool(ctx, name, args)
}

func (r *reconnectingClient) Close() error {
	r.reset()
	return nil
}

type client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Int64
}

func startServer(s ServerConfig) (*client, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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
	c := &client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	_, err = c.call(context.Background(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mow", "version": "0.1.0"},
	})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.notify("notifications/initialized", map[string]any{})
	return c, nil
}

func (c *client) listTools(ctx context.Context) ([]toolInfo, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []toolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (c *client) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{
		"name": name, "arguments": json.RawMessage(args),
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return string(raw), nil
	}
	var b strings.Builder
	for _, block := range res.Content {
		if block.Text != "" {
			b.WriteString(block.Text)
			b.WriteByte('\n')
		}
	}
	out := strings.TrimSpace(b.String())
	if res.IsError {
		return "", fmt.Errorf("mcp tool error: %s", out)
	}
	return out, nil
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	raw, _ := json.Marshal(req)
	raw = append(raw, '\n')
	if _, err := c.stdin.Write(raw); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
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
		if err := json.Unmarshal(line, &msg); err != nil {
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
	req := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	raw, _ := json.Marshal(req)
	raw = append(raw, '\n')
	_, err := c.stdin.Write(raw)
	return err
}

func (c *client) Close() error {
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}

type mcpTool struct {
	client   toolTransport
	prefix   string
	name     string
	desc     string
	schema   json.RawMessage
	readOnly bool
}

// ReadOnly reports the server's readOnlyHint annotation; mow only lets tools
// that declare it run in read-only prompts.
func (t *mcpTool) ReadOnly() bool { return t.readOnly }

func (t *mcpTool) Name() string {
	return "mcp_" + t.prefix + "_" + sanitize(t.name)
}
func (t *mcpTool) Description() string {
	return fmt.Sprintf("[mcp:%s] %s", t.prefix, t.desc)
}
func (t *mcpTool) Parameters() json.RawMessage {
	if len(t.schema) > 0 {
		return t.schema
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *mcpTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	return t.client.callTool(ctx, t.name, args)
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
}
