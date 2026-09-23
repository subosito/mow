package engine

import (
	"github.com/subosito/mow/internal/llm"
	"testing"
)

func TestRunFileChangesOnlyCurrentSuccessfulStructuredMutations(t *testing.T) {
	prior := []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "old", Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"old.txt"}`}}}}, {Role: "tool", ToolCallID: "old", Content: "created old.txt"}}
	msgs := append(append([]llm.Message{}, prior...),
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "w", Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"new.txt"}`}},
			{ID: "e", Function: llm.FunctionCall{Name: "edit", Arguments: `{"path":"src/a.go"}`}},
			{ID: "bad", Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"bad.txt"}`}},
		}},
		llm.Message{Role: "tool", ToolCallID: "w", Content: "created new.txt\n+hello"},
		llm.Message{Role: "tool", ToolCallID: "e", Content: "edited src/a.go\n-old\n+new"},
		llm.Message{Role: "tool", ToolCallID: "bad", Content: "error: denied"},
	)
	got := runFileChanges(prior, msgs)
	if len(got) != 2 || got[0] != (FileChange{Path: "new.txt", Op: "add"}) || got[1] != (FileChange{Path: "src/a.go", Op: "modify"}) {
		t.Fatalf("changes=%+v", got)
	}
}
