package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// bigTool returns a result far over the per-result cap with markers at both
// ends and in the middle.
type bigTool struct{}

func (bigTool) Name() string        { return "big" }
func (bigTool) Description() string { return "big output" }
func (bigTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (bigTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "HEAD-MARK\n" + strings.Repeat("a", 3_000) + "\nMIDDLE-MARK\n" + strings.Repeat("b", 3_000) + "\nTAIL-MARK", nil
}

func TestCutToolResultSpillsFullText(t *testing.T) {
	dir := t.TempDir()
	var calls int
	var toolMsg string
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		calls++
		if calls == 1 {
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "big", Arguments: `{}`},
			}}}, nil
		}
		for _, m := range messages {
			if m.Role == "tool" {
				toolMsg = m.Content
			}
		}
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	if _, err := agent.Run(t.Context(), chat, "go", agent.Options{
		MaxTurns: 5, Tools: []agent.Tool{bigTool{}}, MaxToolResultChars: 1_000, SpillDir: dir,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolMsg, "HEAD-MARK") || !strings.Contains(toolMsg, "TAIL-MARK") || strings.Contains(toolMsg, "MIDDLE-MARK") {
		t.Fatalf("want head+tail kept and middle elided: %q", toolMsg)
	}
	const key = "full text: "
	i := strings.Index(toolMsg, key)
	if i < 0 {
		t.Fatalf("notice does not name a spill file: %q", toolMsg)
	}
	path := toolMsg[i+len(key):]
	path = path[:strings.Index(path, " ")]
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "MIDDLE-MARK") {
		t.Fatalf("spill file %q: %v", path, err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("spill outside SpillDir: %s", path)
	}
}
