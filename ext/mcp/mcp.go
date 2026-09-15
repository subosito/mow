// Package mcp connects trusted MCP stdio servers and registers their tools.
//
// Config (first match wins):
//  1. extensions.mcp in -config / $MOW_HOME/config.yaml / trusted .mow/config.yaml
//  2. $MOW_HOME/mcp.json, then $MOW_HOME/mcp.yaml, overlaying ~/.agents/mcp-settings.json
//  3. trusted workspace-root mcp.json (the ecosystem file next to the repo)
//
// Both the standard mcpServers map and a servers list are accepted.
// On a tools/call transport failure (dead pipe/process), the client restarts
// the server once and retries; tool and JSON-RPC errors are returned as-is.
// Project .mow/mcp.json is not read — keep MCP in extensions.mcp or root mcp.json.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/extcfg"
	"github.com/subosito/mow/internal/redact"
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
	// Prompt is operator-configured guidance appended to active requests.
	Prompt string `yaml:"prompt"`
	// TimeoutSec bounds one tools/call (and HTTP RPC). 0 uses
	// defaultMCPCallTimeout. Negative disables the extra deadline (turn
	// cancel still applies). A silent stdio server otherwise waits forever.
	TimeoutSec int `yaml:"timeout_sec"`
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

// defaultMCPCallTimeout is the tools/call bound when timeout_sec is omitted.
// context-mode (and similar) omit their own timer and expect the host to
// govern; without this, a silent stdio reply pins the turn until cancel.
const defaultMCPCallTimeout = 30 * time.Second

func (s ServerConfig) callTimeout() time.Duration {
	if s.TimeoutSec < 0 {
		return 0
	}
	if s.TimeoutSec == 0 {
		return defaultMCPCallTimeout
	}
	return time.Duration(s.TimeoutSec) * time.Second
}

// withCallTimeout adds d unless ctx already has an earlier deadline.
func withCallTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= d {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// resolved merges both config forms into a name-ordered server list.
func (c Config) resolved() []ServerConfig {
	var out []ServerConfig
	names := slices.Sorted(maps.Keys(c.MCPServers))
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
	ext.RegisterBeforeNewContext(registerAllContext)
}

func registerAll(configPaths ...string) error {
	ctx := ext.SetupContext{
		Workspace:      workspaceRootFromConfigPaths(configPaths),
		LoadUserConfig: extcfg.IncludesUserConfig(configPaths),
		ConfigPaths:    configPaths,
	}
	if ctx.Workspace == "" {
		ctx.Workspace, _ = os.Getwd()
	}
	return registerAllContext(ctx)
}

func registerAllContext(ctx ext.SetupContext) error {
	// Instances are re-registered below for this BeforeNew generation; a stale
	// entry from a prior host setup must not survive (cmdhook does the same).
	clearRegisteredPrompts()
	ext.ClearExtensionKind("mcp")
	var c Config
	ok, err := extcfg.DecodeSection("mcp", ctx.ConfigPaths, &c)
	if err != nil {
		return fmt.Errorf("mcp extensions: %w", err)
	}
	if !ok || (len(c.Servers) == 0 && len(c.MCPServers) == 0) {
		// Home-file fallbacks only when the host opted into user config
		// (BeforeNew paths include $MOW_HOME/config.yaml). Hermetic embedding
		// must not start MCP servers from the operator home.
		if ctx.LoadUserConfig {
			home, herr := loadHomeMCPFiles()
			if herr != nil {
				return herr
			}
			c = home
		}
	}
	if ctx.LoadUserConfig && len(c.Servers) == 0 && len(c.MCPServers) == 0 {
		if err := loadWorkspaceMCPJSON(ctx.Workspace, &c); err != nil {
			return err
		}
	}
	return RegisterServers(mergePluginMCPServersContext(c.resolved(), ctx.ConfigPaths, ctx.LoadUserConfig))
}

// loadHomeMCPFiles reads ~/.agents/mcp-settings.json then overlays
// $MOW_HOME/mcp.json (or mcp.yaml). Mow names replace shared ones.
func loadHomeMCPFiles() (Config, error) {
	var base Config
	if std := agentsStandardMCPPath(); std != "" {
		if raw, err := os.ReadFile(std); err == nil {
			if err := yaml.Unmarshal(raw, &base); err != nil {
				return Config{}, fmt.Errorf("mcp: %s: %w", std, err)
			}
		}
	}
	var over Config
	for _, name := range []string{"mcp.json", "mcp.yaml"} {
		path := filepath.Join(mow.Home(), name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := yaml.Unmarshal(raw, &over); err != nil {
			return Config{}, fmt.Errorf("mcp: %s: %w", name, err)
		}
		return overlayMCP(base, over), nil
	}
	return base, nil
}

func agentsStandardMCPPath() string {
	h, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(h) == "" {
		return ""
	}
	return filepath.Join(h, ".agents", "mcp-settings.json")
}

func overlayMCP(base, over Config) Config {
	out := map[string]ServerConfig{}
	order := make([]string, 0)
	for _, s := range base.resolved() {
		k := strings.ToLower(strings.TrimSpace(s.Name))
		if k == "" {
			continue
		}
		if _, ok := out[k]; !ok {
			order = append(order, k)
		}
		out[k] = s
	}
	for _, s := range over.resolved() {
		k := strings.ToLower(strings.TrimSpace(s.Name))
		if k == "" {
			continue
		}
		if _, ok := out[k]; !ok {
			order = append(order, k)
		}
		out[k] = s
	}
	servers := make(map[string]ServerConfig, len(out))
	for _, k := range order {
		s := out[k]
		servers[s.Name] = s
	}
	return Config{MCPServers: servers}
}

// loadWorkspaceMCPJSON reads <workspace>/mcp.json when the project is trusted.
// It does not read .mow/mcp.json — that would duplicate extensions.mcp.
func loadWorkspaceMCPJSON(ws string, c *Config) error {
	if ws == "" || !mow.WorkspaceTrusted(ws) {
		return nil
	}
	path := filepath.Join(ws, "mcp.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var wsCfg Config
	if err := yaml.Unmarshal(raw, &wsCfg); err != nil {
		return fmt.Errorf("mcp: %s: %w", path, err)
	}
	*c = overlayMCP(*c, wsCfg)
	return nil
}

// mergePluginMCPServers appends servers declared on host-owned Agent Plugins
// (global + workspace profile). YAML / mcp.json names win: a plugin server
// with the same name is skipped so dual context-mode does not spawn twice.
func mergePluginMCPServers(servers []ServerConfig, configPaths []string) []ServerConfig {
	return mergePluginMCPServersContext(servers, configPaths, extcfg.IncludesUserConfig(configPaths))
}

func mergePluginMCPServersContext(servers []ServerConfig, configPaths []string, loadUserConfig bool) []ServerConfig {
	if !loadUserConfig {
		return servers
	}
	seen := map[string]bool{}
	for _, s := range servers {
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if name != "" {
			seen[name] = true
		}
	}
	roots := mow.HostOwnedPluginRoots(mow.Home(), configPaths)
	for _, p := range mow.ListPlugins(roots) {
		for _, ms := range p.MCPServers {
			name := strings.TrimSpace(ms.Name)
			if name == "" {
				name = p.ID
			}
			if seen[strings.ToLower(name)] {
				continue
			}
			if strings.TrimSpace(ms.Command) == "" && strings.TrimSpace(ms.URL) == "" {
				continue
			}
			seen[strings.ToLower(name)] = true
			servers = append(servers, ServerConfig{
				Name:    name,
				Command: ms.Command,
				Args:    ms.Args,
				Env:     pluginMCPEnv(p.Path, ms.Env),
				URL:     ms.URL,
				Prompt:  ms.Prompt,
			})
		}
	}
	return servers
}

// pluginMCPEnv is the stdio env for a host-owned plugin MCP server.
// CLAUDE_PLUGIN_ROOT is always the plugin folder. CLAUDE_CONFIG_DIR defaults
// to $MOW_HOME so context-mode (and similar) store sessions under mow rather
// than ~/.claude — but only when neither the process nor the plugin manifest
// already set it, so a real Claude Code session and an explicit plugin env
// still win.
func pluginMCPEnv(pluginRoot string, extra map[string]string) map[string]string {
	env := map[string]string{}
	if pluginRoot != "" {
		env["CLAUDE_PLUGIN_ROOT"] = pluginRoot
	}
	if _, ok := extra["CLAUDE_CONFIG_DIR"]; !ok && strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) == "" {
		if home := strings.TrimSpace(mow.Home()); home != "" {
			env["CLAUDE_CONFIG_DIR"] = home
		}
	}
	maps.Copy(env, extra)
	return env
}

// workspaceRootFromConfigPaths returns the workspace that owns a
// <ws>/.mow/config.yaml entry in the host path list.
func workspaceRootFromConfigPaths(configPaths []string) string {
	for _, p := range configPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if strings.EqualFold(filepath.Base(p), "config.yaml") && filepath.Base(filepath.Dir(p)) == ".mow" {
			return filepath.Dir(filepath.Dir(p))
		}
	}
	return ""
}

// RegisterServers starts each server and registers tools.
func RegisterServers(servers []ServerConfig) error {
	gen := ext.BeforeNewGeneration()
	var registered []string
	// seenTools detects two servers (or one server, after sanitize) mapping to
	// the same tool name; the later one is skipped instead of silently
	// replacing the first in ext.RegisterTool.
	seenTools := map[string]string{}
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
		// initialize for HTTP too. Both handshake calls get the same deadline
		// as tools/call: a server that never answers must not pin mow.New.
		if ht, ok := tr.(*httpTransport); ok {
			initCtx, cancel := withCallTimeout(context.Background(), s.callTimeout())
			err := ht.initialize(initCtx)
			cancel()
			if err != nil {
				_ = tr.Close()
				rollbackTransports(registered)
				return fmt.Errorf("mcp %s init: %w", name, err)
			}
		}
		listCtx, cancel := withCallTimeout(context.Background(), s.callTimeout())
		tools, err := tr.listTools(listCtx)
		cancel()
		if err != nil {
			_ = tr.Close()
			rollbackTransports(registered)
			return fmt.Errorf("mcp %s list: %w", name, err)
		}
		registerTransport(name, gen, tr)
		registered = append(registered, name)
		ext.RegisterExtensionInstance("mcp", name, s.MinTurns)
		registerServerPrompt(name, s.Prompt)
		n := 0
		for _, t := range tools {
			// Server metadata is untrusted: cap what reaches the model.
			if len(t.InputSchema) > maxToolSchemaBytes {
				fmt.Fprintf(os.Stderr, "mcp: %s: skipping tool %q: inputSchema exceeds %d bytes\n", name, t.Name, maxToolSchemaBytes)
				continue
			}
			mt := &mcpTool{
				client:   tr,
				prefix:   name,
				name:     t.Name,
				desc:     truncateBytes(t.Description, maxToolDescBytes),
				schema:   t.InputSchema,
				readOnly: t.Annotations.ReadOnlyHint,
			}
			toolName := mt.Name()
			if first, dup := seenTools[toolName]; dup {
				fmt.Fprintf(os.Stderr, "mcp: %s: skipping tool %q: name %s already registered by %q\n", name, t.Name, toolName, first)
				continue
			}
			seenTools[toolName] = name
			ext.RegisterTool(mt)
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

// reconnectingClient restarts the stdio process after a transport failure.
type reconnectingClient struct {
	cfg    ServerConfig
	mu     sync.Mutex
	c      *client
	closed bool // set by Close; ensure must not resurrect a retired transport
}

func (r *reconnectingClient) ensure(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("mcp: %s: transport closed", r.cfg.Name)
	}
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
	// A tool result with isError or a JSON-RPC error is the server's answer,
	// not a broken pipe: restarting would drop server state and re-run a
	// possibly non-idempotent tool. Only transport failures reconnect once.
	if !isTransportErr(err) {
		return "", err
	}
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
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.reset()
	return nil
}

// isTransportErr reports whether err means the stdio pipe or process is gone
// (as opposed to the server answering with an error).
func isTransportErr(err error) bool {
	if err == nil {
		return false
	}
	_, isExit := errors.AsType[*exec.ExitError](err)
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, errLineExceeds) ||
		isExit
}

// processPID is the OS pid captured at cmd.Start, or 0 if no client is live.
func (r *reconnectingClient) processPID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c == nil {
		return 0
	}
	return r.c.pid
}

type client struct {
	cmd       *exec.Cmd
	pid       int // OS pid at cmd.Start; stable identity for the live process
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    *redact.Ring
	mu        sync.Mutex // guards stdin writes
	semOnce   sync.Once
	callSem   chan struct{} // capacity 1: one request/response exchange on stdout at a time
	callTO    time.Duration // per-exchange timeout, applied after the semaphore is acquired
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
	errOut := redact.NewRing(maxStderrRetain)
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	c := &client{cmd: cmd, pid: pid, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: errOut, callTO: s.callTimeout()}
	initCtx, cancel := withCallTimeout(context.Background(), s.callTimeout())
	defer cancel()
	_, err = c.call(initCtx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mow", "version": mow.Version},
	})
	if err != nil {
		tail := c.stderr.Tail()
		_ = c.Close()
		return nil, appendStderrHint(err, tail)
	}
	// MCP requires this notification before any other request. A write failure
	// here means the pipe is already gone, so the server never leaves the
	// initializing state and every later call would block or fail with a
	// confusing error instead of naming the real cause.
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		tail := c.stderr.Tail()
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

// sem returns the one-slot exchange semaphore, created on first use so
// test fixtures can build a client literal without initialising it.
func (c *client) sem() chan struct{} {
	c.semOnce.Do(func() { c.callSem = make(chan struct{}, 1) })
	return c.callSem
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	raw, _ := json.Marshal(req)
	raw = append(raw, '\n')

	// One exchange at a time: parallel tool batches share this stdout reader,
	// and two concurrent readers would each consume the other's reply. The
	// per-call timeout starts only once this call owns the reader, so a
	// queued sibling does not burn its budget waiting behind a slow call.
	sem := c.sem()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-sem }()
	if c.callTO > 0 {
		var cancel context.CancelFunc
		ctx, cancel = withCallTimeout(ctx, c.callTO)
		defer cancel()
	}

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
		line, err := readBoundedLine(c.stdout, maxStdioLineBytes)
		ch <- lineResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		c.interrupt()
		<-ch // wait for the read to unblock after process kill
		return nil, ctx.Err()
	case r := <-ch:
		if errors.Is(r.err, errLineExceeds) {
			c.interrupt()
			return nil, fmt.Errorf("mcp: stdout %w", r.err)
		}
		return r.line, r.err
	}
}

// errLineExceeds is returned (wrapped with the cap) by readBoundedLine.
var errLineExceeds = errors.New("line exceeds")

// readBoundedLine reads one '\n'-terminated line, failing as soon as the
// accumulated bytes pass max. ReadBytes would buffer the entire line first,
// so the cap would not bound memory against a misbehaving peer.
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(line)+len(frag) > max {
			return nil, fmt.Errorf("%w %d bytes", errLineExceeds, max)
		}
		line = append(line, frag...)
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return line, err
		}
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

// Exploratory classifies discovery tools separately from write permissions.
// Some search servers maintain an index and correctly advertise readOnlyHint
// false. Inspect the remote tool name, never the server prefix or description.
func (t *mcpTool) Exploratory() bool {
	if t.readOnly {
		return true
	}
	name := t.name
	// Split camelCase names as well as underscore/dash-separated MCP names.
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	words := strings.FieldsFunc(b.String(), func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == '/' })
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		switch w {
		case "create", "write", "edit", "update", "delete", "remove", "drop", "replace", "rebuild", "execute", "run", "send", "publish":
			return false
		}
	}
	for _, w := range []string{words[0], words[len(words)-1]} {
		switch w {
		case "search", "grep", "find", "lookup", "fetch", "read", "list", "query", "inspect", "describe":
			return true
		}
	}
	return false
}

// Untrusted marks MCP tool output as external content so the agent loop
// frames it in <untrusted-output> (prompt-injection boundary). Server text is
// never treated as harness/user instructions.
func (t *mcpTool) Untrusted() bool { return true }

func (t *mcpTool) Name() string {
	return "mcp_" + sanitize(t.prefix) + "_" + sanitize(t.name)
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
	out, err := t.client.callTool(ctx, t.name, args)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("mcp %s: tools/call timed out", t.prefix)
	}
	return out, err
}

// truncateBytes clamps s to n bytes on a rune boundary, marking the cut.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
}
