package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestIsContextOverflow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New(`llm: HTTP 400: {"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens"}}`), true},
		{errors.New(`llm: anthropic HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`), true},
		{errors.New("llm: HTTP 413: Request too large"), true},
		{errors.New("llm: HTTP 400: input is too long for requested model"), true},
		{errors.New("llm: HTTP 401: invalid api key"), false},
		{errors.New("llm: HTTP 429: rate limited"), false},
		{nil, false},
	} {
		if got := IsContextOverflow(tc.err); got != tc.want {
			t.Errorf("IsContextOverflow(%v)=%v want %v", tc.err, got, tc.want)
		}
	}
}

type bigOutputTool struct {
	n     int
	calls *int
}

func (bigOutputTool) Name() string        { return "dump" }
func (bigOutputTool) Description() string { return "dump" }
func (bigOutputTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Exec returns fresh output each call (a growing log), so the stall detector
// sees new evidence and only the context size is under test.
func (b bigOutputTool) Exec(context.Context, json.RawMessage) (string, error) {
	seq := 0
	if b.calls != nil {
		*b.calls++
		seq = *b.calls
	}
	return fmt.Sprintf("batch %d\n", seq) + strings.Repeat("log line\n", b.n), nil
}

// A provider overflow rejection is recovered by forcing compaction and
// reissuing the same turn, instead of failing the run. No context window is
// configured, so before this fix nothing ever compacted.
func TestRunRecoversFromContextOverflow(t *testing.T) {
	t.Parallel()
	const limit = 30_000 // pretend context window, in chars sent
	calls, rejected := 0, 0
	chat := func(ctx context.Context, msgs []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		calls++
		if EstChars(msgs) > limit {
			rejected++
			return llm.Message{}, errors.New(`llm: HTTP 400: {"error":{"code":"context_length_exceeded"}}`)
		}
		if calls <= 6 {
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "c", Type: "function", Function: llm.FunctionCall{Name: "dump", Arguments: "{}"},
			}}}, nil
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	seq := 0
	res, err := Run(t.Context(), chat, "summarize the logs", Options{
		Tools:              []Tool{bigOutputTool{n: 1_000, calls: &seq}},
		MaxToolResultChars: 8_000,
	})
	if err != nil {
		t.Fatalf("run failed instead of recovering: %v (rejected=%d)", err, rejected)
	}
	if rejected == 0 {
		t.Fatal("test did not exercise an overflow")
	}
	t.Logf("recovered after %d overflow rejection(s) in %d calls", rejected, calls)
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
	if !strings.Contains(joinUser(res.Messages), "summarize the logs") {
		t.Fatal("the task anchor must survive the forced compaction")
	}
}

// When compaction cannot help (the request is still rejected), the provider
// error surfaces after a bounded number of attempts rather than looping.
func TestRunContextOverflowGivesUp(t *testing.T) {
	t.Parallel()
	calls := 0
	chat := func(ctx context.Context, msgs []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		calls++
		if calls == 1 {
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "c", Type: "function", Function: llm.FunctionCall{Name: "dump", Arguments: "{}"},
			}}}, nil
		}
		return llm.Message{}, errors.New("llm: HTTP 400: prompt is too long")
	}
	_, err := Run(t.Context(), chat, "task", Options{Tools: []Tool{bigOutputTool{n: 2_000}}})
	if !IsContextOverflow(err) {
		t.Fatalf("want the overflow error surfaced, got %v", err)
	}
	if calls > 2+maxOverflowRetries {
		t.Fatalf("calls=%d; overflow recovery must be bounded", calls)
	}
}

func joinUser(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "user" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
