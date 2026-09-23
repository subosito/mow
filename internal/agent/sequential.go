package agent

import (
	"fmt"

	"github.com/subosito/mow/internal/llm"
)

func sequentialBatchErrors(calls []llm.ToolCall, sequentialID, name string) []llm.Message {
	out := make([]llm.Message, 0, len(calls))
	for _, tc := range calls {
		content := fmt.Sprintf("error: %s must be called alone; ask first, then issue other tools after the answer", name)
		if tc.ID != sequentialID {
			content = fmt.Sprintf("error: tool batch contained sequential tool %s; no sibling tools were executed", name)
		}
		out = append(out, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: content})
	}
	return out
}
