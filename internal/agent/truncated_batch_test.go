package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// countingWriteTool records whether it was ever executed.
type countingWriteTool struct{ runs int }

func (t *countingWriteTool) Name() string        { return "write" }
func (t *countingWriteTool) Description() string { return "write a file" }
func (t *countingWriteTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}}}`)
}
func (t *countingWriteTool) Exec(context.Context, json.RawMessage) (string, error) {
	t.runs++
	return "wrote", nil
}

// A provider that stops at its output limit mid tool-call leaves the last
// call's arguments cut. Syntactically broken JSON is already rejected in the
// llm layer, but a cut can also land on a *valid* value — a half file body, a
// partial path. Executing that silently corrupts the workspace, so the loop
// must refuse and answer every call with an error instead.
func TestTruncatedToolBatchIsNotExecuted(t *testing.T) {
	tool := &countingWriteTool{}
	turns := 0
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		turns++
		if turns == 1 {
			return llm.Message{
				Role:       "assistant",
				StopReason: "length",
				ToolCalls: []llm.ToolCall{{
					ID:   "c1",
					Type: "function",
					Function: llm.FunctionCall{
						Name: "write",
						// Valid JSON, but content was cut at the token limit.
						Arguments: `{"path":"main.go","content":"package ma"}`,
					},
				}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "recovered"}, nil
	}

	res, err := Run(t.Context(), chat, "hi", Options{
		MaxTurns: 5,
		Tools:    []Tool{tool},
	})
	if err != nil {
		t.Fatalf("want recovery, got %v", err)
	}
	if tool.runs != 0 {
		t.Fatalf("truncated tool call executed %d times — this can write a half file", tool.runs)
	}
	if res.Text != "recovered" {
		t.Errorf("want the model to continue after the error, got %q", res.Text)
	}

	// Every advertised call still needs a result, or the history is
	// unreplayable and the provider 400s when the session resumes.
	var toolResults int
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolResults++
			if !strings.Contains(m.Content, "NOT executed") {
				t.Errorf("tool result must say the call did not run: %q", m.Content)
			}
		}
	}
	if toolResults != 1 {
		t.Errorf("want 1 synthetic tool result, got %d", toolResults)
	}
}

// A model that keeps emitting oversized arguments would otherwise retry
// forever, and every attempt is a full-context request.
func TestTruncatedToolBatchGivesUp(t *testing.T) {
	tool := &countingWriteTool{}
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{
			Role:       "assistant",
			StopReason: "length",
			ToolCalls: []llm.ToolCall{{
				ID:       "c1",
				Type:     "function",
				Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"a"}`},
			}},
		}, nil
	}
	_, err := Run(t.Context(), chat, "hi", Options{MaxTurns: 20, Tools: []Tool{tool}})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated after repeated truncation, got %v", err)
	}
	if tool.runs != 0 {
		t.Errorf("tool ran %d times despite truncated arguments", tool.runs)
	}
}

// A normal (non-truncated) batch must still execute — the guard keys on the
// stop reason only.
func TestUntruncatedToolBatchStillRuns(t *testing.T) {
	tool := &countingWriteTool{}
	turns := 0
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		turns++
		if turns == 1 {
			return llm.Message{
				Role:       "assistant",
				StopReason: "tool_calls",
				ToolCalls: []llm.ToolCall{{
					ID:       "c1",
					Type:     "function",
					Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"a"}`},
				}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	if _, err := Run(t.Context(), chat, "hi", Options{MaxTurns: 5, Tools: []Tool{tool}}); err != nil {
		t.Fatal(err)
	}
	if tool.runs != 1 {
		t.Errorf("want the tool to run once, got %d", tool.runs)
	}
}
