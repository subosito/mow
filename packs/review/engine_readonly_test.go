package review

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

type reviewFakeTool struct {
	name     string
	readOnly bool
	got      *string
}

func (t *reviewFakeTool) Name() string { return t.name }

func (t *reviewFakeTool) Description() string { return "fake read-only extension" }

func (t *reviewFakeTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (t *reviewFakeTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if t.got != nil {
		*t.got = t.name
	}
	return "ext-result", nil
}

func (t *reviewFakeTool) ReadOnly() bool { return t.readOnly }

func TestEngineReviewerBuiltinToolsOnly(t *testing.T) {
	var specsSeen []string
	var extCalled string
	turn := 0
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Tools:     []mow.Tool{&reviewFakeTool{name: "mcp_lookup", readOnly: true, got: &extCalled}},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			turn++
			if turn == 1 {
				specsSeen = nil
				for _, ts := range tools {
					specsSeen = append(specsSeen, ts.Function.Name)
				}
				return mow.Message{
					Role: "assistant",
					ToolCalls: []mow.ToolCall{{
						ID: "1", Type: "function",
						Function: mow.FunctionCall{Name: "mcp_lookup", Arguments: `{}`},
					}},
				}, nil
			}
			for _, m := range messages {
				if m.Role == "tool" && strings.Contains(m.Content, "not in allowed tool set") {
					return mow.Message{Role: "assistant", Content: `{}`}, nil
				}
			}
			return mow.Message{Role: "assistant", Content: `{}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	rev := NewEngineReviewer(eng)
	if _, err := rev.Ask(t.Context(), "system", "prompt"); err != nil {
		t.Fatal(err)
	}
	want := mow.BuiltinReadInspectTools()
	slices.Sort(specsSeen)
	slices.Sort(want)
	if !slices.Equal(specsSeen, want) {
		t.Fatalf("tool specs = %v want %v", specsSeen, want)
	}
	if extCalled != "" {
		t.Fatalf("extension tool executed: %q", extCalled)
	}
}
