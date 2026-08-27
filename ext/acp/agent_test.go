package acp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
)

func TestAgentRoundTrip(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hello-acp",
				Usage: mow.Usage{InputTokens: 12, OutputTokens: 3}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// agent reads ar (from client write aw); client reads cr (from agent write cw)
	ar, aw := io.Pipe()
	cr, cw := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	cl := newPipeClient(cr, aw)
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	stop, usage, err := cl.prompt(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop=%q", stop)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 3 {
		t.Fatalf("usage not carried through ACP response: %+v", usage)
	}
	// streaming may or may not deliver depending on chat path; content optional
	cancel()
	_ = aw.Close()
}

func TestSessionCancelDuringPrompt(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			// Block until the prompt context is cancelled (by session/cancel).
			<-ctx.Done()
			return mow.Message{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()

	cl := newPipeClient(cr, aw)
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	type res struct {
		stop string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		stop, _, err := cl.prompt(ctx, sid, "hang")
		ch <- res{stop, err}
	}()

	// The read loop must keep serving while the prompt blocks; resend cancel
	// until the prompt returns (first send may race cancel registration).
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("prompt: %v", r.err)
			}
			if r.stop != "cancelled" {
				t.Fatalf("stop=%q, want cancelled", r.stop)
			}
			cancel()
			_ = aw.Close()
			return
		case <-tick.C:
			_ = cl.notify("session/cancel", map[string]any{"sessionId": sid})
		case <-ctx.Done():
			t.Fatal("timeout: session/cancel never unblocked session/prompt")
		}
	}
}

type pipeClient struct {
	in        io.Reader
	out       io.Writer
	next      int
	pending   map[string]chan map[string]json.RawMessage
	mu        sync.Mutex
	writeMu   sync.Mutex
	onNotify  func(method string, params json.RawMessage)
	onRequest func(id json.RawMessage, method string, params json.RawMessage)
}

func newPipeClient(in io.Reader, out io.Writer) *pipeClient {
	return &pipeClient{
		in: in, out: out,
		pending: map[string]chan map[string]json.RawMessage{},
	}
}

func (c *pipeClient) readLoop() {
	sc := bufio.NewScanner(c.in)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		var msg map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if raw, ok := msg["method"]; ok {
			var method string
			_ = json.Unmarshal(raw, &method)
			if _, hasID := msg["id"]; !hasID {
				if fn := c.onNotify; fn != nil {
					fn(method, msg["params"])
				}
				continue // notification
			}
			if fn := c.onRequest; fn != nil {
				fn(msg["id"], method, msg["params"])
				continue
			}
		}
		var id string
		_ = json.Unmarshal(msg["id"], &id)
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (c *pipeClient) notify(method string, params any) error {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
	return c.writeLine(raw)
}

func (c *pipeClient) reply(id json.RawMessage, result any) error {
	raw, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return err
	}
	return c.writeLine(raw)
}

func (c *pipeClient) writeLine(raw []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.out.Write(append(raw, '\n'))
	return err
}

func (c *pipeClient) callOK(ctx context.Context, method string, params any) error {
	_, err := c.call(ctx, method, params)
	return err
}

func (c *pipeClient) call(ctx context.Context, method string, params any) (map[string]json.RawMessage, error) {
	ch := make(chan map[string]json.RawMessage, 1)
	c.mu.Lock()
	c.next++
	id := fmt.Sprintf("%d", c.next)
	c.pending[id] = ch
	c.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err := c.writeLine(raw); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg := <-ch:
		if e, ok := msg["error"]; ok {
			return nil, fmt.Errorf("%s", e)
		}
		return msg, nil
	}
}

func (c *pipeClient) sessionNew(ctx context.Context, cwd string) (string, error) {
	msg, err := c.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return "", err
	}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(msg["result"], &res)
	if res.SessionID == "" {
		return "", fmt.Errorf("no sessionId")
	}
	return res.SessionID, nil
}

type modelListProv struct {
	model string
	list  []mow.ModelInfo
}

func (p *modelListProv) Chat(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec, hooks mow.ChatHooks) (mow.Message, error) {
	return mow.Message{Role: "assistant", Content: "ok"}, nil
}
func (p *modelListProv) ListModels(ctx context.Context) ([]mow.ModelInfo, error) {
	return append([]mow.ModelInfo(nil), p.list...), nil
}
func (p *modelListProv) SetModel(id string) error {
	p.model = id
	return nil
}

func TestSessionNewAdvertisesModelConfig(t *testing.T) {
	prov := &modelListProv{
		model: "m1",
		list: []mow.ModelInfo{
			{ID: "m1", Wire: "openai-chat-completions"},
			{ID: "m2", Wire: "anthropic-messages"},
		},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "m1", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()
	cl := newPipeClient(cr, aw)
	go cl.readLoop()
	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	msg, err := cl.call(ctx, "session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		SessionID     string           `json:"sessionId"`
		ConfigOptions []map[string]any `json:"configOptions"`
	}
	if err := json.Unmarshal(msg["result"], &res); err != nil {
		t.Fatal(err)
	}
	if res.SessionID == "" || len(res.ConfigOptions) == 0 {
		t.Fatalf("%+v", res)
	}
	if len(res.ConfigOptions) < 4 {
		t.Fatalf("want mode+approvals+model+effort, got %v", res.ConfigOptions)
	}
	if res.ConfigOptions[0]["id"] != "mode" || res.ConfigOptions[0]["currentValue"] != "code" {
		t.Fatalf("mode option=%v", res.ConfigOptions[0])
	}
	if res.ConfigOptions[1]["id"] != "approvals" || res.ConfigOptions[1]["currentValue"] != "prompt" {
		t.Fatalf("approvals option=%v", res.ConfigOptions[1])
	}
	if res.ConfigOptions[2]["id"] != "model" || res.ConfigOptions[2]["currentValue"] != "m1" {
		t.Fatalf("model option=%v", res.ConfigOptions[2])
	}
	if res.ConfigOptions[3]["id"] != "effort" || res.ConfigOptions[3]["category"] != "thought_level" {
		t.Fatalf("effort option=%v", res.ConfigOptions[3])
	}
	msg, err = cl.call(ctx, "session/set_config_option", map[string]any{
		"sessionId": res.SessionID,
		"configId":  "model",
		"value":     "m2",
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	var setRes struct {
		ConfigOptions []map[string]any `json:"configOptions"`
	}
	_ = json.Unmarshal(msg["result"], &setRes)
	var modelCur any
	for _, o := range setRes.ConfigOptions {
		if o["id"] == "model" {
			modelCur = o["currentValue"]
		}
	}
	if modelCur != "m2" {
		t.Fatalf("after set: %v", setRes.ConfigOptions)
	}
	if eng.Model() != "m2" {
		t.Fatalf("eng model=%q", eng.Model())
	}
	// Mode via config option
	msg, err = cl.call(ctx, "session/set_config_option", map[string]any{
		"sessionId": res.SessionID,
		"configId":  "mode",
		"value":     "ask",
	})
	if err != nil {
		t.Fatalf("set mode: %v", err)
	}
	_ = json.Unmarshal(msg["result"], &setRes)
	var modeCur any
	for _, o := range setRes.ConfigOptions {
		if o["id"] == "mode" {
			modeCur = o["currentValue"]
		}
	}
	if modeCur != "ask" {
		t.Fatalf("mode after set: %v", setRes.ConfigOptions)
	}
	cancel()
	_ = aw.Close()
}

func (c *pipeClient) prompt(ctx context.Context, sid, text string) (string, mow.Usage, error) {
	msg, err := c.call(ctx, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	})
	if err != nil {
		return "", mow.Usage{}, err
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens       int `json:"inputTokens"`
			OutputTokens      int `json:"outputTokens"`
			InputTokensSnake  int `json:"input_tokens"`
			OutputTokensSnake int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(msg["result"], &res)
	u := mow.Usage{}
	if res.Usage != nil {
		u.InputTokens = res.Usage.InputTokens
		u.OutputTokens = res.Usage.OutputTokens
		if u.InputTokens == 0 {
			u.InputTokens = res.Usage.InputTokensSnake
		}
		if u.OutputTokens == 0 {
			u.OutputTokens = res.Usage.OutputTokensSnake
		}
	}
	return res.StopReason, u, nil
}

// Regression: a nested acp_delegate answer (EventDelegateChunk) must NOT be
// forwarded as agent_message_chunk — the host accumulates chunk text as the
// peer reply and the tool result already carries the full answer, so
// forwarding committed nested answers twice and corrupted host-side markdown.
// It may surface as thought progress only.
func TestAgentPromptDelegateChunkNotForwardedAsAgentText(t *testing.T) {
	var eng *mow.Engine
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			// Mid-prompt: a nested delegate streams its answer.
			eng.Emit(mow.Event{Type: mow.EventDelegateChunk, Agent: "nested", Delta: "## nested answer"})
			return mow.Message{Role: "assistant", Content: "outer reply"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = acp.Agent(ctx, acp.AgentOptions{Engine: eng, In: ar, Out: cw})
		_ = cw.Close()
	}()
	cl := newPipeClient(cr, aw)
	// Capture every session/update notification.
	var mu sync.Mutex
	var updates []string
	cl.onNotify = func(method string, params json.RawMessage) {
		if method == "session/update" {
			mu.Lock()
			updates = append(updates, string(params))
			mu.Unlock()
		}
	}
	go cl.readLoop()

	if err := cl.callOK(ctx, "initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sid, err := cl.sessionNew(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if _, _, err := cl.prompt(ctx, sid, "hi"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, u := range updates {
		if strings.Contains(u, "agent_message_chunk") && strings.Contains(u, "nested answer") {
			t.Fatalf("delegate chunk forwarded as agent_message_chunk: %s", u)
		}
	}
}
