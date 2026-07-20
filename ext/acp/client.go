package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// Client talks to a peer ACP agent (subprocess) as a *client*.
// Used by the acp_delegate tool to run another harness.
type Client struct {
	// Command is the peer agent argv (e.g. ["other-agent", "--acp"]).
	Command []string
	// Dir is the peer working directory (absolute preferred).
	Dir string
	// Env extra environment for the peer.
	Env []string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	encMu  sync.Mutex
	nextID atomic.Int64
	// pending request id → response channel
	pending map[string]chan response
	pendMu  sync.Mutex
	// accumulated agent text from session/update
	textMu sync.Mutex
	text   strings.Builder
	// OnChunk receives agent_message_chunk deltas while Prompt is in flight.
	// Set it via SetOnChunk; direct writes race with the read loop.
	OnChunk func(delta string)
	// sessionID from last successful Start (for reuse).
	SessionID string
	// procMu guards started/exited/cmd across Start, Close, and Alive.
	procMu sync.Mutex
	// started is true after Start until Close.
	started bool
	// exited is closed by the reaper goroutine once the process exits.
	exited chan struct{}
}

// Start launches the peer process and completes initialize + session/new.
// The process is long-lived (not tied to ctx cancel) so sessions can be reused.
// Returns the peer session id.
func (c *Client) Start(ctx context.Context) (sessionID string, err error) {
	if len(c.Command) == 0 {
		return "", fmt.Errorf("acp client: empty command")
	}
	if c.started && c.SessionID != "" {
		return c.SessionID, nil
	}
	if err := c.startProcess(); err != nil {
		return "", err
	}

	_, err = c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientInfo": map[string]any{
			"name": "mow", "version": "0.1.0",
		},
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false},
		},
	})
	if err != nil {
		_ = c.Close()
		return "", fmt.Errorf("acp initialize: %w", err)
	}

	cwd := c.Dir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	res, err := c.call(ctx, "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		_ = c.Close()
		return "", fmt.Errorf("acp session/new: %w", err)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.SessionID == "" {
		_ = c.Close()
		return "", fmt.Errorf("acp session/new: bad result %s", string(res))
	}
	c.SessionID = out.SessionID
	return out.SessionID, nil
}

// startProcess launches the peer and starts the read loop plus a reaper
// goroutine that owns cmd.Wait, so Alive() can observe process exit.
func (c *Client) startProcess() error {
	c.pending = map[string]chan response{}
	// Long-lived peer: do not use CommandContext(ctx) so Prompt timeout does not kill the process.
	c.cmd = exec.Command(c.Command[0], c.Command[1:]...)
	if c.Dir != "" {
		c.cmd.Dir = c.Dir
	}
	if len(c.Env) > 0 {
		c.cmd.Env = append(os.Environ(), c.Env...)
	}
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	c.cmd.Stderr = io.Discard
	c.stdin = stdin
	c.stdout = stdout
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("acp client start: %w", err)
	}
	exited := make(chan struct{})
	c.procMu.Lock()
	c.started = true
	c.exited = exited
	cmd := c.cmd
	c.procMu.Unlock()
	// Reaper owns cmd.Wait; Close waits on exited instead of Wait-ing itself.
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	go c.readLoop()
	return nil
}

// Prompt runs session/prompt and returns concatenated agent message text + stop reason.
// OnChunk (if set) receives each agent_message_chunk delta as it arrives.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (reply string, stopReason string, err error) {
	c.textMu.Lock()
	c.text.Reset()
	c.textMu.Unlock()

	res, err := c.call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []ContentBlock{
			{Type: "text", Text: text},
		},
	})
	if err != nil {
		return "", "", err
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &out)
	c.textMu.Lock()
	reply = c.text.String()
	c.textMu.Unlock()
	return reply, out.StopReason, nil
}

// Cancel sends session/cancel for the session.
func (c *Client) Cancel(sessionID string) {
	c.notify("session/cancel", map[string]any{"sessionId": sessionID})
}

// Close terminates the peer process.
func (c *Client) Close() error {
	c.procMu.Lock()
	c.started = false
	cmd := c.cmd
	exited := c.exited
	c.cmd = nil
	c.exited = nil
	c.procMu.Unlock()
	c.SessionID = ""
	c.encMu.Lock()
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	c.encMu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		if exited != nil {
			// The reaper goroutine owns cmd.Wait; wait for it to finish.
			<-exited
		} else {
			_, _ = cmd.Process.Wait()
		}
	}
	return nil
}

// Alive reports whether the peer process is still running.
func (c *Client) Alive() bool {
	if c == nil {
		return false
	}
	c.procMu.Lock()
	started, exited := c.started, c.exited
	c.procMu.Unlock()
	if !started || exited == nil {
		return false
	}
	select {
	case <-exited:
		return false // reaper saw the process exit
	default:
		return true
	}
}

// SetOnChunk installs (or clears, with nil) the delta callback. It must be
// used instead of writing OnChunk directly: the read loop reads the field
// concurrently under the same lock.
func (c *Client) SetOnChunk(fn func(delta string)) {
	c.textMu.Lock()
	c.OnChunk = fn
	c.textMu.Unlock()
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	ch := make(chan response, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	rawID, _ := json.Marshal(id)
	req := request{
		JSONRPC: "2.0",
		ID:      rawID,
		Method:  method,
		Params:  mustJSON(params),
	}
	if err := c.write(req); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("acp %s: %s", method, resp.Error.Message)
		}
		raw, _ := json.Marshal(resp.Result)
		return raw, nil
	}
}

func (c *Client) notify(method string, params any) {
	_ = c.write(notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  mustJSON(params),
	})
}

func (c *Client) write(v any) error {
	c.encMu.Lock()
	defer c.encMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("acp client: closed")
	}
	enc := json.NewEncoder(c.stdin)
	return enc.Encode(v)
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		if _, ok := probe["method"]; ok {
			if _, hasID := probe["id"]; !hasID {
				var n notification
				_ = json.Unmarshal([]byte(line), &n)
				c.onNotification(n)
				continue
			}
		}
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		id := string(resp.ID)
		var idStr string
		if json.Unmarshal(resp.ID, &idStr) == nil {
			id = idStr
		}
		c.pendMu.Lock()
		ch := c.pending[id]
		c.pendMu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (c *Client) onNotification(n notification) {
	if n.Method != "session/update" {
		return
	}
	var p sessionUpdateParams
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return
	}
	if p.Update.SessionUpdate == "agent_message_chunk" && p.Update.Content != nil {
		delta := p.Update.Content.Text
		c.textMu.Lock()
		c.text.WriteString(delta)
		fn := c.OnChunk
		c.textMu.Unlock()
		if fn != nil && delta != "" {
			fn(delta)
		}
	}
}
