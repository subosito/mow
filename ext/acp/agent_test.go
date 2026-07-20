package acp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			return mow.Message{Role: "assistant", Content: "hello-acp"}, nil
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
	stop, err := cl.prompt(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop=%q", stop)
	}
	// streaming may or may not deliver depending on chat path; content optional
	cancel()
	_ = aw.Close()
}

type pipeClient struct {
	in      io.Reader
	out     io.Writer
	next    int
	pending map[string]chan map[string]json.RawMessage
	mu      sync.Mutex
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
		if _, ok := msg["method"]; ok {
			if _, hasID := msg["id"]; !hasID {
				continue // notification
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

func (c *pipeClient) callOK(ctx context.Context, method string, params any) error {
	_, err := c.call(ctx, method, params)
	return err
}

func (c *pipeClient) call(ctx context.Context, method string, params any) (map[string]json.RawMessage, error) {
	c.next++
	id := fmt.Sprintf("%d", c.next)
	ch := make(chan map[string]json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if _, err := c.out.Write(append(raw, '\n')); err != nil {
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

func (c *pipeClient) prompt(ctx context.Context, sid, text string) (string, error) {
	msg, err := c.call(ctx, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	})
	if err != nil {
		return "", err
	}
	var res struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(msg["result"], &res)
	return res.StopReason, nil
}
