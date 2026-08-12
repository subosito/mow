// Package mcp connects trusted MCP stdio servers and registers their tools.
//
// Config (first match wins):
//  1. extensions.mcp in -config / $MOW_HOME/config.yaml
//  2. $MOW_HOME/mcp.json, then $MOW_HOME/mcp.yaml
//
// Both the standard mcpServers map and a servers list are accepted.
// On tools/call failure, the client restarts the server once and retries.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/extcfg"
)

// ServerConfig is one MCP server (stdio or streamable HTTP).
type ServerConfig struct {
	Name     string            `yaml:"name"`
	Command  string            `yaml:"command"` // stdio
	Args     []string          `yaml:"args"`
	Env      map[string]string `yaml:"env"`
	URL      string            `yaml:"url"`
	Insecure bool              `yaml:"insecure"`
	Headers  map[string]string `yaml:"headers"`
	Auth     AuthConfig        `yaml:"auth"`
	MinTurns int               `yaml:"min_turns"`
}

// Config is extensions.mcp. It accepts the ecosystem-standard mcpServers map
// (Claude Desktop / Claude Code / Cursor / VS Code — a keyed object, so an
// existing config pastes in) as well as the original servers list.
type Config struct {
	// MCPServers is the standard form: name → server. Keys become tool
	// prefixes; a per-entry name overrides the key.
	MCPServers map[string]ServerConfig `yaml:"mcpServers"`
	// Servers is the list form (each entry carries its own name).
	Servers []ServerConfig `yaml:"servers"`
}

// resolved merges both config forms into a name-ordered server list.
func (c Config) resolved() []ServerConfig {
	var out []ServerConfig
	names := make([]string, 0, len(c.MCPServers))
	for name := range c.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := c.MCPServers[name]
		if strings.TrimSpace(s.Name) == "" {
			s.Name = name
		}
		out = append(out, s)
	}
	out = append(out, c.Servers...)
	return out
}

func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return registerAll(configPaths...)
	})
}

func registerAll(configPaths ...string) error {
	var c Config
	ok, err := extcfg.DecodeSection("mcp", configPaths, &c)
	if err != nil {
		return fmt.Errorf("mcp extensions: %w", err)
	}
	if !ok || (len(c.Servers) == 0 && len(c.MCPServers) == 0) {
		// Home-file fallbacks only when the host opted into user config
		// (BeforeNew paths include $MOW_HOME/config.yaml). Hermetic embedding
		// must not start MCP servers from the operator home.
		if extcfg.IncludesUserConfig(configPaths) {
			// Fallback files: $MOW_HOME/mcp.json (standard filename) then mcp.yaml.
			// yaml.v3 parses JSON, so one decoder handles both.
			for _, name := range []string{"mcp.json", "mcp.yaml"} {
				raw, rerr := os.ReadFile(filepath.Join(mow.Home(), name))
				if rerr != nil {
					continue
				}
				if err := yaml.Unmarshal(raw, &c); err != nil {
					return fmt.Errorf("mcp: %s: %w", name, err)
				}
				break
			}
		}
	}
	return RegisterServers(c.resolved())
}

// RegisterServers starts each server and registers tools.
func RegisterServers(servers []ServerConfig) error {
	gen := ext.BeforeNewGeneration()
	var registered []string
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
				rollbackTransports(registered)
				return fmt.Errorf("mcp %s: %w", name, err)
			}
			tr = rc
		default:
			continue
		}
		if err != nil {
			rollbackTransports(registered)
			return fmt.Errorf("mcp %s: %w", name, err)
		}
		// initialize for HTTP too
		if ht, ok := tr.(*httpTransport); ok {
			if err := ht.initialize(context.Background()); err != nil {
				_ = tr.Close()
				rollbackTransports(registered)
				return fmt.Errorf("mcp %s init: %w", name, err)
			}
		}
		tools, err := tr.listTools(context.Background())
		if err != nil {
			_ = tr.Close()
			rollbackTransports(registered)
			return fmt.Errorf("mcp %s list: %w", name, err)
		}
		registerTransport(name, gen, tr)
		registered = append(registered, name)
		ext.RegisterExtensionInstance("mcp", name, s.MinTurns)
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

func rollbackTransports(names []string) {
	for _, name := range names {
		unregisterTransport(name)
	}
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
	if c == nil {
		return nil, fmt.Errorf("mcp: client unavailable (server restarting)")
	}
	return c.listTools(ctx)
}

func (r *reconnectingClient) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if err := r.ensure(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	c := r.c
	r.mu.Unlock()
	// ensure() above should have populated r.c, but a concurrent reset (another
	// tool in the same batch failing and reconnecting) can clear it between the
	// two locks. Dereferencing nil here panics the whole run from inside a tool
	// goroutine, which is a far worse outcome than one failed tool call.
	if c == nil {
		return "", fmt.Errorf("mcp: %s: client unavailable (server restarting)", name)
	}
	out, err := c.callTool(ctx, name, args)
	if err == nil {
		return out, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.reset()
		return "", err
	}
	// reconnect once
	r.reset()
	if err2 := r.ensure(ctx); err2 != nil {
		return "", fmt.Errorf("%v (reconnect: %v)", err, err2)
	}
	r.mu.Lock()
	c = r.c
	r.mu.Unlock()
	if c == nil {
		return "", fmt.Errorf("mcp: %s: client unavailable after reconnect", name)
	}
	return c.callTool(ctx, name, args)
}

func (r *reconnectingClient) Close() error {
	r.reset()
	return nil
}

type client struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    *stderrRing
	mu        sync.Mutex
	nextID    atomic.Int64
	closeOnce sync.Once
	closeErr  error
}

func startServer(s ServerConfig) (*client, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	setServerProcAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errOut := newStderrRing(maxStderrRetain)
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: errOut}
	_, err = c.call(context.Background(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mow", "version": "0.1.0"},
	})
	if err != nil {
		tail := c.stderr.tail()
		_ = c.Close()
		return nil, appendStderrHint(err, tail)
	}
	// MCP requires this notification before any other request. A write failure
	// here means the pipe is already gone, so the server never leaves the
	// initializing state and every later call would block or fail with a
	// confusing error instead of naming the real cause.
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		tail := c.stderr.tail()
		_ = c.Close()
		return nil, appendStderrHint(fmt.Errorf("initialized notification: %w", err), tail)
	}
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
		out, limErr := aggregateToolText(string(raw))
		if limErr != nil {
			return "", limErr
		}
		return out, nil
	}
	var texts []string
	for _, block := range res.Content {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	out, err := aggregateToolText(texts...)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("mcp tool error: %s", out)
	}
	return out, nil
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	raw, _ := json.Marshal(req)
	raw = append(raw, '\n')

	c.mu.Lock()
	_, err := c.stdin.Write(raw)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	for {
		line, err := c.readLine(ctx)
		if err != nil {
			return nil, err
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		// Server→client request: it needs an answer before the server can
		// finish our call. Ignoring it deadlocks — the server waits for a
		// reply we never send while we wait for a response it never sends.
		if msg.isRequest() {
			c.mu.Lock()
			c.rejectRequest(msg.ID, msg.Method)
			c.mu.Unlock()
			continue
		}
		if !msg.isReplyTo(id) {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s", msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *client) readLine(ctx context.Context) ([]byte, error) {
	type lineResult struct {
		line []byte
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() {
		line, err := c.stdout.ReadBytes('\n')
		ch <- lineResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		c.interrupt()
		<-ch // wait for ReadBytes to unblock after process kill
		return nil, ctx.Err()
	case r := <-ch:
		if len(r.line) > maxStdioLineBytes {
			c.interrupt()
			return nil, fmt.Errorf("mcp: stdout line exceeds %d bytes", maxStdioLineBytes)
		}
		return r.line, r.err
	}
}

func (c *client) interrupt() {
	killServerTree(c.cmd)
}

// rejectRequest answers a server→client request we do not implement.
//
// mow advertises empty capabilities, so a conforming server should not send
// sampling/roots/elicitation at all — but ping is always allowed and a buggy
// server may ask anyway. A -32601 keeps the server unblocked; silence would
// hang the call that is waiting behind it. Errors are ignored: the write can
// only fail when the pipe is gone, which the pending read reports.
func (c *client) rejectRequest(id json.RawMessage, method string) {
	if method == "ping" {
		// Ping must be answered with an empty result, not an error.
		_ = c.writeRaw(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		return
	}
	_ = c.writeRaw(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": -32601, "message": "method not supported by mow: " + method},
	})
}

// writeRaw sends one frame. The caller already holds c.mu.
func (c *client) writeRaw(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = c.stdin.Write(raw)
	return err
}

// rpcMessage is any inbound JSON-RPC frame: a response to one of our calls, a
// notification, or a server→client request.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Method string `json:"method"`
}

// isRequest reports whether this frame is a server→client request: it carries
// a method (so it is not a response) and an id (so it expects an answer).
// Notifications carry a method with no id and need no reply.
func (m rpcMessage) isRequest() bool {
	return m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null"
}

// isReplyTo reports whether this frame answers the call with id want.
//
// Two frames must never be mistaken for our reply:
//
//   - Anything carrying "method" is a notification or a server→client request
//     (sampling/createMessage, roots/list, elicitation/create). A JSON-RPC
//     response never has that field. Returning one handed the caller an empty
//     Result and reported success.
//   - A response whose id is not ours is a late reply to a call we abandoned
//     (ctx cancel, timeout). Accepting it shifted every later call one reply
//     behind, so each tool returned the previous tool's output.
func (m rpcMessage) isReplyTo(want int64) bool {
	if m.Method != "" || len(m.ID) == 0 {
		return false
	}
	var got int64
	if err := json.Unmarshal(m.ID, &got); err != nil {
		return false
	}
	return got == want
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
	c.closeOnce.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		killServerTree(c.cmd)
		if c.cmd != nil {
			c.closeErr = c.cmd.Wait()
		}
	})
	return c.closeErr
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

// Untrusted marks MCP tool output as external content so the agent loop
// frames it in <untrusted-output> (prompt-injection boundary). Server text is
// never treated as harness/user instructions.
func (t *mcpTool) Untrusted() bool { return true }

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
	if !ext.IsExtensionActive("mcp:"+t.prefix, ext.TurnFromContext(ctx)) {
		return fmt.Sprintf("mcp server %q is dormant (min_turns not reached) or disabled", t.prefix), nil
	}
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
