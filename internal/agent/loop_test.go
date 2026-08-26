package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	_, err := agent.Run(t.Context(), chat, "next", agent.Options{
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

// Durable compaction: when history overflows the budget mid-run, the compacted
// projection must become the live history — the NEXT LLM call (and the final
// Result.Messages) starts from the reduced history, not the full pre-compact
// span again. Regression for compaction being transient (projection only).
func TestRunCompactionIsDurableAcrossTurns(t *testing.T) {
	var prior []llm.Message
	prior = append(prior, llm.Message{Role: "system", Content: "sys"})
	for i := 0; i < 30; i++ {
		prior = append(prior,
			llm.Message{Role: "user", Content: strings.Repeat("u", 300)},
			llm.Message{Role: "assistant", Content: strings.Repeat("a", 300)},
		)
	}
	full := len(prior)

	// Chat: first call replies with a tool call (forces a second LLM call
	// after the tool result); second call replies with text. Record each
	// call's message count.
	var callSizes []int
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		callSizes = append(callSizes, len(messages))
		step++
		if step == 1 {
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"hi"}`},
			}}}, nil
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	res, err := agent.Run(t.Context(), chat, "continue", agent.Options{
		Tools:           []agent.Tool{echoTool{}},
		PriorMessages:   prior,
		MaxContextChars: 4_000, // well under the ~18k history → must compact
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callSizes) != 2 {
		t.Fatalf("calls=%d want 2 (tool turn then final)", len(callSizes))
	}
	// First call: compaction must have shrunk the history.
	if callSizes[0] >= full {
		t.Fatalf("first call sent %d messages, want < %d (compaction did not fire)", callSizes[0], full)
	}
	// Second call: must NOT re-grow to the full pre-compact history. Durable
	// compaction means it starts from the compacted span (+ tool result).
	if callSizes[1] >= full {
		t.Fatalf("second call sent %d messages, want < %d (compaction was transient, history re-grew)", callSizes[1], full)
	}
	if callSizes[1] <= callSizes[0] {
		t.Fatalf("second call %d should be first call %d + tool result", callSizes[1], callSizes[0])
	}
	// Result.Messages must reflect the compacted history, not the full span.
	if len(res.Messages) >= full {
		t.Fatalf("Result.Messages=%d, want < %d (compaction not durable)", len(res.Messages), full)
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
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
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
	// Fresh tool result every batch so the stall detector stays quiet and the
	// turn limit is the only thing that can end the run.
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: fmt.Sprintf(`{"text":"x%d"}`, n)},
			}},
		}, nil
	}
	_, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 2,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err == nil || !errors.Is(err, agent.ErrMaxTurns) {
		t.Fatalf("err=%v want ErrMaxTurns", err)
	}
}

func TestMaxTurnsZeroIsUnlimited(t *testing.T) {
	// With MaxTurns <= 0 the loop must not inject the old 120 default.
	// Finish after a few tool rounds so the test stays bounded.
	// Vary args each turn so the stall detector does not fire.
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 5 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		args := fmt.Sprintf(`{"text":"x%d"}`, n)
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: args},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 0,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
	if n != 6 {
		t.Fatalf("chat calls=%d want 6", n)
	}
}

type doneTool struct{}

func (doneTool) Name() string                { return "finish" }
func (doneTool) Description() string         { return "finish" }
func (doneTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (doneTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "finished-ok", agent.ErrDone
}

func TestErrDoneEndsLoopSuccessfully(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 1 {
			t.Fatal("loop continued after ErrDone")
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "finish", Arguments: `{}`},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 10,
		Tools:    []agent.Tool{doneTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("chat calls=%d", n)
	}
	// Tool result must be present; Text may be empty (tool-only assistant turn).
	var saw string
	for _, m := range res.Messages {
		if m.Role == "tool" {
			saw = m.Content
		}
	}
	if saw != "finished-ok" {
		t.Fatalf("tool content=%q", saw)
	}
}

// Identical tool calls inject a soft warn but do not hard-stop when the calls
// still return new evidence (e.g. polling a changing file). The hard stop is
// the evidence signal, not the fingerprint — see TestStallOnNoNewEvidence.
func TestSameToolWarnDoesNotHardStop(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 5 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "tick", Arguments: `{"text":"same"}`},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 20,
		Tools:    []agent.Tool{&tickTool{}},
	})
	if err != nil {
		t.Fatalf("err=%v want nil (soft warn only)", err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
	var sawWarn bool
	for _, m := range res.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "repeated the same tool") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatal("expected soft same-tool warn in history")
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
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
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
	ctx, cancel := context.WithCancel(t.Context())
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
	ctx, cancel := context.WithCancel(t.Context())
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
	ctx, cancel := context.WithCancel(t.Context())
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

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
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
	ctx, cancel := context.WithCancel(t.Context())
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
	_, err := agent.Run(t.Context(), chat, "hi", agent.Options{
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

// sameResultTool always returns the same string regardless of arguments.
type sameResultTool struct{}

func (sameResultTool) Name() string        { return "same" }
func (sameResultTool) Description() string { return "constant result" }
func (sameResultTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}
func (sameResultTool) Exec(_ context.Context, _ json.RawMessage) (string, error) {
	return "no matches", nil
}

// tickTool returns a different result on every call: identical arguments, new
// evidence each time.
type tickTool struct{ n int }

func (*tickTool) Name() string        { return "tick" }
func (*tickTool) Description() string { return "tick" }
func (*tickTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}
func (t *tickTool) Exec(_ context.Context, _ json.RawMessage) (string, error) {
	t.n++
	return fmt.Sprintf("tick %d", t.n), nil
}

// Re-running the identical call for the identical result stops the loop with
// ErrStuck once stallBarrenBatches batches in a row are barren.
func TestStallOnNoNewEvidence(t *testing.T) {
	turns := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		turns++
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"same"}`},
			}},
		}, nil
	}
	_, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 50,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err == nil || !errors.Is(err, agent.ErrStuck) {
		t.Fatalf("err=%v want ErrStuck", err)
	}
	// Batch 1 is new evidence; batches 2-4 are barren → stop on the 4th.
	if turns != 4 {
		t.Fatalf("stopped after %d turns, want 4 (1 novel + 3 barren)", turns)
	}
}

// Two identical batches are a plausible retry, not a stall: the loop must
// still be running after them.
func TestTwoBarrenBatchesDoNotStall(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 3 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"same"}`},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 50,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatalf("err=%v want nil (two barren batches is a retry, not a stall)", err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
}

// Regression: results that share a long prefix are distinct evidence. Prefix
// keys made file banners and grep context collide into false stalls.
func TestNoStallOnSharedResultPrefix(t *testing.T) {
	prefix := strings.Repeat("banner line\n", 60)
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 6 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: fmt.Sprintf(`{"text":%q}`, prefix+fmt.Sprintf("tail %d", n))},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 50,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatalf("err=%v want nil (long shared prefix, distinct tails)", err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
}

// Distinct calls that happen to return the same string are still distinct
// evidence (two empty greps, two "ok" runs) and must not stall.
func TestNoStallWhenDifferentCallsShareResult(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 6 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		// Same empty result every time, but a different query each turn.
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "same", Arguments: fmt.Sprintf(`{"text":"query-%d"}`, n)},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 50,
		Tools:    []agent.Tool{sameResultTool{}},
	})
	if err != nil {
		t.Fatalf("err=%v want nil (distinct calls, shared result)", err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
}

// Control: results that keep changing never trip the stall detector.
func TestNoStallWhenResultsDiffer(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 6 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "tick", Arguments: `{"text":"same"}`},
			}},
		}, nil
	}
	res, err := agent.Run(t.Context(), chat, "hi", agent.Options{
		MaxTurns: 50,
		Tools:    []agent.Tool{&tickTool{}},
	})
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
}

// A mid-turn steer interrupts the in-flight LLM call (Engine.Steer cancels
// it), but the OUTER ctx is still alive: the loop must drain the steer into
// messages and reissue on the same turn instead of aborting the run.
func TestRunReissuesOnMidTurnSteer(t *testing.T) {
	var calls int
	var lastContent []string
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		calls++
		lastContent = append(lastContent, messages[len(messages)-1].Content)
		if calls == 1 {
			// Simulates the engine cancelling the run ctx on Steer().
			return llm.Message{}, context.Canceled
		}
		return llm.Message{Role: "assistant", Content: "done now"}, nil
	}
	steered := false
	opt := agent.Options{
		MaxTurns: 5,
		Steer: func() []string {
			if steered {
				return nil
			}
			steered = true
			return []string{"course correct"}
		},
	}
	res, err := agent.Run(t.Context(), chat, "original ask", opt)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2 (interrupted + reissued)", calls)
	}
	if res.Text != "done now" {
		t.Fatalf("text=%q", res.Text)
	}
	// The reissued call's last message is the steer itself.
	if len(lastContent) < 2 || lastContent[1] != "course correct" {
		t.Fatalf("steer not injected before reissue: %v", lastContent)
	}
}

// Without a pending steer, an interrupted/cancelled chat still fails as
// before (no silent swallow of real cancellations).
func TestRunInterruptedChatFailsWithoutSteer(t *testing.T) {
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{}, context.Canceled
	}
	_, err := agent.Run(t.Context(), chat, "ask", agent.Options{MaxTurns: 3})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

// A REAL provider failure (not a cancel) must NOT be treated as a mid-turn
// steer interrupt just because a steer happens to be pending — otherwise
// genuine errors get silently swallowed and the run looks successful.
func TestRunRealChatErrorNotSwallowedBySteer(t *testing.T) {
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{}, errors.New("upstream 500: model exploded")
	}
	_, err := agent.Run(t.Context(), chat, "ask", agent.Options{
		MaxTurns: 3,
		Steer:    func() []string { return []string{"course correct"} },
	})
	if err == nil {
		t.Fatal("real chat error was swallowed by the steer path")
	}
	if !strings.Contains(err.Error(), "upstream 500") {
		t.Fatalf("want the original error, got %v", err)
	}
}

// bigSpecTool simulates a large registered tool (MCP-style schema).
type bigSpecTool struct{ name string }

func (t bigSpecTool) Name() string        { return t.name }
func (t bigSpecTool) Description() string { return strings.Repeat("d", 2000) }
func (t bigSpecTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"blob":{"type":"string","description":"` + strings.Repeat("s", 2000) + `"}}}`)
}
func (t bigSpecTool) Exec(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil }

// TestCalibrationCountsToolOverhead guards the chars/token calibrator scope:
// the provider bills tool definitions + system alongside history in
// InputTokens (Anthropic sums cache reads/creation into the total), so
// observing history-only chars against that total drives the ratio to the
// floor and makes applyCompact compact well under budget. With tool
// definitions counted, the observed ratio stays near the true density and a
// history that fits the budget is left alone.
func TestCalibrationCountsToolOverhead(t *testing.T) {
	tools := make([]agent.Tool, 0, 10)
	for i := 0; i < 10; i++ {
		tools = append(tools, bigSpecTool{name: fmt.Sprintf("big%d", i)})
	}
	// ~63K chars of history, under the 85K budget. ~41K chars of tool
	// definitions ride along; billed at the same density the request totals
	// ~26K tokens (history + tools together).
	prior := []llm.Message{llm.Message{Role: "system", Content: "sys"}}
	for i := 0; i < 100; i++ {
		prior = append(prior,
			llm.Message{Role: "user", Content: strings.Repeat("u", 300)},
			llm.Message{Role: "assistant", Content: strings.Repeat("a", 300)},
		)
	}
	var compacted int32
	turn := 0
	chat := func(ctx context.Context, messages []llm.Message, specs []llm.ToolSpec) (llm.Message, error) {
		turn++
		// Three tool-call turns so the EWMA calibrator settles (alpha 0.3,
		// seeded at 4 chars/token): a history-only numerator converges toward
		// the clamped floor and estimates the history over budget, triggering
		// compaction; a numerator that includes the ~41K of tool definitions
		// converges to the true density and stays under budget.
		if turn <= 3 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "c1", Type: "function",
					Function: llm.FunctionCall{Name: "big0", Arguments: "{}"},
				}},
				Usage: llm.Usage{InputTokens: 26000, OutputTokens: 1},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "done", Usage: llm.Usage{InputTokens: 26000, OutputTokens: 1}}, nil
	}
	_, err := agent.Run(t.Context(), chat, "hello", agent.Options{
		System:             "sys",
		Tools:              tools,
		PriorMessages:      prior,
		MaxContextChars:    85_000,
		MaxToolResultChars: 24_000,
		Hooks: agent.Hooks{PreCompact: []agent.PreCompactFunc{
			func(ctx context.Context, e agent.PreCompactEvent) (agent.PreCompactDecision, error) {
				atomic.AddInt32(&compacted, 1)
				return agent.PreCompactDecision{}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&compacted); n > 0 {
		t.Fatalf("compaction ran %d time(s) on an under-budget history — tool overhead not counted in calibration", n)
	}
}
