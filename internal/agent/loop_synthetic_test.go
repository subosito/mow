package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

type syntheticProbeTool struct{}

func (syntheticProbeTool) Name() string        { return "probe" }
func (syntheticProbeTool) Description() string { return "probe tool" }
func (syntheticProbeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (syntheticProbeTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	return "ok", nil
}

// Host-injected user messages (mid-turn steer, thrash warnings) must be marked
// Synthetic so Engine.Rewind skips them: edit/retry must land on the user's
// own prompt, never on a nudge string.
func TestSteerInjectedAsSyntheticUser(t *testing.T) {
	call := 0
	chat := func(ctx context.Context, msgs []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		call++
		if call == 1 {
			return llm.Message{
				Role:      "assistant",
				Content:   "run probe",
				ToolCalls: []llm.ToolCall{{ID: "t1", Type: "function", Function: llm.FunctionCall{Name: "probe", Arguments: "{}"}}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	res, err := Run(t.Context(), chat, "real prompt", Options{
		Tools: []Tool{syntheticProbeTool{}},
		Steer: func() []string { return []string{"steer note"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if call != 2 {
		t.Fatalf("calls=%d, want 2 (tool turn then reissue with steer)", call)
	}
	var real, steer int
	for _, m := range res.Messages {
		if m.Role == "user" && m.Content == "real prompt" {
			real++
			if m.Synthetic {
				t.Fatal("real user prompt must not be Synthetic")
			}
		}
		if m.Role == "user" && m.Content == "steer note" {
			steer++
			if !m.Synthetic {
				t.Fatal("steer-injected user message must be Synthetic")
			}
		}
	}
	if real != 1 || steer != 1 {
		t.Fatalf("real=%d steer=%d, want 1 each (history: %d msgs)", real, steer, len(res.Messages))
	}
}
