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
	"time"
	"unicode/utf8"

	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/internal/llm"
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
	// OnProgress receives non-answer peer activity (thoughts, tool_call status).
	// Does not append to the Prompt reply buffer. Set via SetOnProgress.
	OnProgress func(kind, text string)
	// PermissionMode controls agent→client session/request_permission (reject|allow).
	// Default reject when empty.
	PermissionMode string
	// sessionID from last successful Start (for reuse).
	SessionID string
	// procMu guards started/exited/cmd across Start, Close, and Alive.
	procMu sync.Mutex
	// started is true after Start until Close.
	started bool
	// exited is closed by the reaper goroutine once the process exits.
	exited chan struct{}
	stderr *stderrRing
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
		return "", appendStderrHint(fmt.Errorf("acp client start: %w", err), c.stderrTail())
	}

	_, err = c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientInfo": map[string]any{
			"name": "mow", "version": "1.0.0-rc.1",
		},
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false},
		},
	})
	if err != nil {
		_ = c.Close()
		return "", appendStderrHint(fmt.Errorf("acp initialize: %w", err), c.stderrTail())
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
		return "", appendStderrHint(fmt.Errorf("acp session/new: %w", err), c.stderrTail())
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.SessionID == "" {
		_ = c.Close()
		return "", appendStderrHint(fmt.Errorf("acp session/new: bad result %s", string(res)), c.stderrTail())
	}
	c.SessionID = out.SessionID
	return out.SessionID, nil
}

// startProcess launches the peer and starts the read loop plus a reaper
// goroutine that owns cmd.Wait, so Alive() can observe process exit.
func (c *Client) startProcess() error {
	c.pending = map[string]chan response{}
	c.stderr = newStderrRing(defaultStderrCap)
	// Long-lived peer: do not use CommandContext(ctx) so Prompt timeout does not kill the process.
	// Own process group (unix) so Close can tear down npx/node/claude trees.
	c.cmd = exec.Command(c.Command[0], c.Command[1:]...)
	setPeerProcAttr(c.cmd)
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
	stderrPipe, err := c.cmd.StderrPipe()
	if err != nil {
		return err
	}
	c.stdin = stdin
	c.stdout = stdout
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("acp client start: %w", err)
	}
	go func() {
		_, _ = io.Copy(c.stderr, stderrPipe)
	}()
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

// cancelGrace is how long we wait after session/cancel for the peer to finish
// session/prompt before returning the context error (process may still be
// killed by dropPeer).
const cancelGrace = 2 * time.Second

// Prompt runs session/prompt and returns concatenated agent message text + stop reason.
// OnChunk receives answer text deltas; OnProgress receives tool/thought status
// (not included in the reply string).
//
// On context cancel/timeout, sends session/cancel so the peer stops work, then
// waits briefly for a response before returning ctx.Err().
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (reply string, stopReason string, usage llm.Usage, err error) {
	c.textMu.Lock()
	c.text.Reset()
	c.textMu.Unlock()

	res, err := c.call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []ContentBlock{
			{Type: "text", Text: text},
		},
	}, func() {
		// Soft stop first — better than only SIGKILL after timeout.
		c.Cancel(sessionID)
	})
	if err != nil {
		return "", "", llm.Usage{}, appendStderrHint(err, c.stderrTail())
	}
	var out struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(res, &out)
	if out.Usage != nil {
		usage = llm.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	}
	c.textMu.Lock()
	reply = c.text.String()
	c.textMu.Unlock()
	return reply, out.StopReason, usage, nil
}

// Cancel sends session/cancel for the session.
func (c *Client) Cancel(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	c.notify("session/cancel", map[string]any{"sessionId": sessionID})
}

// Close terminates the peer process (and its process group on unix).
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
		killPeerTree(cmd)
		if exited != nil {
			// The reaper goroutine owns cmd.Wait; wait for it to finish.
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
				// Reaper stuck — do not block dropPeer forever.
			}
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

// SetOnChunk installs (or clears, with nil) the answer-delta callback. It must
// be used instead of writing OnChunk directly: the read loop reads the field
// concurrently under the same lock.
func (c *Client) SetOnChunk(fn func(delta string)) {
	c.textMu.Lock()
	c.OnChunk = fn
	c.textMu.Unlock()
}

// SetOnProgress installs (or clears) the peer-activity callback (thoughts,
// tool_call lines). Not part of the final reply text.
func (c *Client) SetOnProgress(fn func(kind, text string)) {
	c.textMu.Lock()
	c.OnProgress = fn
	c.textMu.Unlock()
}

func (c *Client) call(ctx context.Context, method string, params any, onCancel ...func()) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	ch := make(chan response, 1)
	c.pendMu.Lock()
	if c.pending == nil {
		c.pending = map[string]chan response{}
	}
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
	c.procMu.Lock()
	exited := c.exited
	c.procMu.Unlock()
	select {
	case <-ctx.Done():
		for _, fn := range onCancel {
			if fn != nil {
				fn()
			}
		}
		// Give the peer a short window to acknowledge cancel (session/prompt
		// result or error). Then surface the context error so the tool loop
		// can drop a hung peer.
		timer := time.NewTimer(cancelGrace)
		defer timer.Stop()
		select {
		case <-ch:
			// Response after cancel — still report cancel/timeout to caller.
			return nil, ctx.Err()
		case <-timer.C:
			return nil, ctx.Err()
		case <-waitExited(exited):
			return nil, appendStderrHint(fmt.Errorf("acp %s: peer process exited during cancel", method), c.stderrTail())
		}
	case <-waitExited(exited):
		// Peer died without a response — fail fast instead of waiting for timeout.
		return nil, appendStderrHint(fmt.Errorf("acp %s: peer process exited", method), c.stderrTail())
	case resp := <-ch:
		if resp.Error != nil {
			return nil, appendStderrHint(fmt.Errorf("acp %s: %s", method, resp.Error.Message), c.stderrTail())
		}
		raw, _ := json.Marshal(resp.Result)
		return raw, nil
	}
}

// waitExited returns exited, or a never-ready channel when exited is nil so
// select arms remain valid before startProcess sets the reaper channel.
func waitExited(exited <-chan struct{}) <-chan struct{} {
	if exited != nil {
		return exited
	}
	return nil // nil channel blocks forever in select (safe arm)
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

func (c *Client) stderrTail() string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return c.stderr.tail()
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		kind, req, resp, ok := parseIncomingLine(line)
		if !ok {
			continue
		}
		switch kind {
		case "skip":
			continue
		case "notification":
			var n notification
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &n) == nil {
				c.onNotification(n)
			}
		case "request":
			c.handleAgentRequest(req)
		case "response":
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
}

func (c *Client) onNotification(n notification) {
	if n.Method != "session/update" {
		return
	}
	var p sessionUpdateParams
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		if u.Content == nil {
			return
		}
		delta := u.Content.Text
		c.textMu.Lock()
		c.text.WriteString(delta)
		fn := c.OnChunk
		c.textMu.Unlock()
		if fn != nil && delta != "" {
			fn(delta)
		}
	case "agent_thought_chunk":
		// Peer reasoning — progress only, not final answer.
		// Clip: hosts paint this on a spinner; multi-KB thoughts thrash the UI.
		if u.Content == nil {
			return
		}
		delta := clipProgressText(u.Content.Text)
		if delta == "" {
			return
		}
		c.textMu.Lock()
		fn := c.OnProgress
		c.textMu.Unlock()
		if fn != nil {
			fn("thought", delta)
		}
	case "tool_call", "tool_call_update":
		line := formatPeerToolProgress(u)
		if line == "" {
			return
		}
		c.textMu.Lock()
		fn := c.OnProgress
		c.textMu.Unlock()
		if fn != nil {
			fn("tool", line)
		}
	}
}

// clipProgressText keeps peer progress UI-safe: first line, ≤80 runes.
func clipProgressText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if utf8.RuneCountInString(s) <= 80 {
		return s
	}
	runes := []rune(s)
	return string(runes[:80]) + "…"
}

// formatPeerToolProgress builds a short one-line status from a peer tool update.
func formatPeerToolProgress(u sessionUpdate) string {
	kind := strings.TrimSpace(u.Kind)
	title := strings.TrimSpace(u.Title)
	status := strings.TrimSpace(u.Status)
	if kind == "" && title == "" {
		return ""
	}
	// Prefer human title; fall back to kind.
	label := title
	if label == "" {
		label = kind
	} else if kind != "" && !strings.EqualFold(kind, title) {
		label = kind + " " + title
	}
	switch strings.ToLower(status) {
	case "", "pending", "in_progress", "running":
		return label
	case "completed", "done", "success":
		return label + " ✓"
	case "failed", "error", "cancelled", "canceled":
		return label + " ✗"
	default:
		return label + " (" + status + ")"
	}
}

// toolCallKindTitle maps a mow tool event into ACP tool_call kind + title so
// hosts render "read engine.go" / "grep foo in pkg/" instead of bare tool names.
// Title is the detail only (path, pattern, command); empty when none.
func toolCallKindTitle(tool string, args json.RawMessage) (kind, title string) {
	kind = strings.TrimSpace(tool)
	if kind == "" {
		kind = "?"
	}
	line := strings.TrimSpace(cliutil.FormatToolProgress(tool, args))
	if line == "" || line == kind {
		return kind, ""
	}
	// FormatToolProgress is "kind detail…"; strip the kind prefix for Title.
	if strings.HasPrefix(line, kind+" ") {
		return kind, strings.TrimSpace(line[len(kind)+1:])
	}
	// Unknown shape (e.g. tool renamed in formatter) — keep full line as title.
	return kind, line
}
