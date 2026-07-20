package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo args" }
func (echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}
func (echoTool) Exec(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Text, nil
}

func TestRunWithPriorMessages(t *testing.T) {
	var got []llm.Message
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		got = append([]llm.Message(nil), messages...)
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	_, err := agent.Run(context.Background(), chat, "next", agent.Options{
		System: "sys",
		PriorMessages: []llm.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "old"},
			{Role: "assistant", Content: "old-reply"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 {
		t.Fatalf("messages=%d want >=4", len(got))
	}
	if got[len(got)-1].Role != "user" || got[len(got)-1].Content != "next" {
		t.Fatalf("last=%+v", got[len(got)-1])
	}
}

func TestRunWithFakeLLMToolThenText(t *testing.T) {
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID:   "1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "echo",
						Arguments: `{"text":"from-tool"}`,
					},
				}},
			}, nil
		}
		found := false
		for _, m := range messages {
			if m.Role == "tool" && m.Content == "from-tool" {
				found = true
			}
		}
		if !found {
			t.Fatal("tool result missing in history")
		}
		return llm.Message{Role: "assistant", Content: "done: from-tool"}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done: from-tool" {
		t.Fatalf("text=%q", res.Text)
	}
}

func TestMaxTurnsReturnsErrMaxTurns(t *testing.T) {
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"message":"x"}`},
			}},
		}, nil
	}
	_, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 2,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err == nil || !errors.Is(err, agent.ErrMaxTurns) {
		t.Fatalf("err=%v want ErrMaxTurns", err)
	}
}

func TestAllowToolDeniesExec(t *testing.T) {
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "1", Type: "function",
					Function: llm.FunctionCall{Name: "bash", Arguments: `{}`},
				}},
			}, nil
		}
		for _, m := range messages {
			if m.Role == "tool" && len(m.Content) > 0 {
				if m.Content == "error: denied by policy" || len(m.Content) > 5 {
					return llm.Message{Role: "assistant", Content: "handled"}, nil
				}
			}
		}
		return llm.Message{Role: "assistant", Content: "fail"}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		AllowTool: func(name string) error {
			return errors.New("denied by policy")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "handled" {
		t.Fatalf("text=%q", res.Text)
	}
}

// countingTool records Exec calls; optional block until ctx done / after-callback.
type countingTool struct {
	name   string
	n      *atomic.Int32
	block  bool
	onExec func() // called once Exec starts (before block/return)
}

func (t *countingTool) Name() string        { return t.name }
func (t *countingTool) Description() string { return "count" }
func (t *countingTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *countingTool) Exec(ctx context.Context, _ json.RawMessage) (string, error) {
	t.n.Add(1)
	if t.onExec != nil {
		t.onExec()
	}
	if t.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "ok", nil
}

func TestCancelAbortsRemainingToolsInBatch(t *testing.T) {
	var n1, n2 atomic.Int32
	entered := make(chan struct{})
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "a", Arguments: `{}`}},
				{ID: "2", Type: "function", Function: llm.FunctionCall{Name: "b", Arguments: `{}`}},
			},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &countingTool{name: "a", n: &n1, block: true, onExec: func() { close(entered) }}
	second := &countingTool{name: "b", n: &n2}
	errCh := make(chan error, 1)
	go func() {
		_, err := agent.Run(ctx, chat, "hi", agent.Options{
			MaxTurns:         3,
			MaxParallelTools: 1, // sequential: second must not start while first holds
			Tools:            []agent.Tool{first, second},
		})
		errCh <- err
	}()
	<-entered
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if n1.Load() != 1 {
		t.Fatalf("first tool runs=%d want 1", n1.Load())
	}
	if n2.Load() != 0 {
		t.Fatalf("second tool runs=%d want 0 (cancel must not drain batch)", n2.Load())
	}
}

func TestCancelBetweenToolsSkipsRest(t *testing.T) {
	var n1, n2 atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "a", Arguments: `{}`}},
				{ID: "2", Type: "function", Function: llm.FunctionCall{Name: "b", Arguments: `{}`}},
			},
		}, nil
	}
	first := &countingTool{name: "a", n: &n1, onExec: cancel} // cancel after first starts (before return)
	second := &countingTool{name: "b", n: &n2}
	_, err := agent.Run(ctx, chat, "hi", agent.Options{
		MaxTurns:         3,
		MaxParallelTools: 1,
		Tools:            []agent.Tool{first, second},
	})
	// First may complete (soft or hard); second must not run.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if n1.Load() != 1 {
		t.Fatalf("first tool runs=%d want 1", n1.Load())
	}
	if n2.Load() != 0 {
		t.Fatalf("second tool runs=%d want 0", n2.Load())
	}
}

func TestCancelBeforeTurnSkipsChat(t *testing.T) {
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		t.Fatal("chat should not run on cancelled ctx")
		return llm.Message{}, nil
	}
	_, err := agent.Run(ctx, chat, "hi", agent.Options{
		MaxTurns: 2,
		Tools:    []agent.Tool{&countingTool{name: "a", n: &n}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if n.Load() != 0 {
		t.Fatalf("tool runs=%d want 0", n.Load())
	}
}

func TestParallelToolsRunConcurrently(t *testing.T) {
	var started atomic.Int32
	bothIn := make(chan struct{})
	var tools []agent.Tool
	for _, name := range []string{"a", "b"} {
		name := name
		tools = append(tools, &syncTool{
			name: name,
			fn: func(ctx context.Context) (string, error) {
				if started.Add(1) == 2 {
					close(bothIn)
				}
				select {
				case <-bothIn:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				select {
				case <-time.After(20 * time.Millisecond):
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return name, nil
			},
		})
	}

	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{
					{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "a", Arguments: `{}`}},
					{ID: "2", Type: "function", Function: llm.FunctionCall{Name: "b", Arguments: `{}`}},
				},
			}, nil
		}
		// Order preserved: first tool message then second.
		var order []string
		for _, m := range messages {
			if m.Role == "tool" {
				order = append(order, m.Content)
			}
		}
		if len(order) < 2 || order[0] != "a" || order[1] != "b" {
			t.Fatalf("tool order=%v want [a b]", order)
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := agent.Run(ctx, chat, "hi", agent.Options{
		MaxTurns:         5,
		MaxParallelTools: 4,
		Tools:            tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
	if started.Load() != 2 {
		t.Fatalf("started=%d want 2", started.Load())
	}
}

func TestParallelCancelFailFast(t *testing.T) {
	// One tool blocks; sibling may start but must not soft-complete after cancel.
	entered := make(chan struct{})
	var n1, n2 atomic.Int32
	t1 := &syncTool{name: "a", fn: func(ctx context.Context) (string, error) {
		n1.Add(1)
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	t2 := &syncTool{name: "b", fn: func(ctx context.Context) (string, error) {
		n2.Add(1)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
			return "late", nil // must not win
		}
	}}
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "a", Arguments: `{}`}},
				{ID: "2", Type: "function", Function: llm.FunctionCall{Name: "b", Arguments: `{}`}},
			},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := agent.Run(ctx, chat, "hi", agent.Options{
			MaxTurns:         3,
			MaxParallelTools: 4,
			Tools:            []agent.Tool{t1, t2},
		})
		errCh <- err
	}()
	<-entered
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if n1.Load() != 1 {
		t.Fatalf("n1=%d", n1.Load())
	}
}

func TestPostToolReceivesDuration(t *testing.T) {
	var got time.Duration
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "1", Type: "function",
					Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"x"}`},
				}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	_, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		Tools:    []agent.Tool{echoTool{}},
		Hooks: agent.Hooks{
			PostTool: []agent.PostToolFunc{
				func(ctx context.Context, e agent.PostToolEvent) (agent.PostToolDecision, error) {
					got = e.Duration
					return agent.PostToolDecision{}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got <= 0 {
		t.Fatalf("duration=%v want >0", got)
	}
}

// syncTool is a named tool with a custom Exec body (for concurrency tests).
type syncTool struct {
	name string
	fn   func(ctx context.Context) (string, error)
}

func (t *syncTool) Name() string        { return t.name }
func (t *syncTool) Description() string { return t.name }
func (t *syncTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *syncTool) Exec(ctx context.Context, _ json.RawMessage) (string, error) {
	return t.fn(ctx)
}
