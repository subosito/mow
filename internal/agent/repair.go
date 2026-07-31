package agent

import (
	"fmt"

	"github.com/subosito/mow/internal/llm"
)

// orphanToolResult is the placeholder content used when a tool call never
// produced a result (cancelled batch, fail-fast sibling, ctx deadline).
const orphanToolResultPrefix = "error: tool call did not complete"

// repairToolResults appends synthetic tool messages for every tool call in
// calls that has no matching tool result in results.
//
// Why: providers (OpenAI chat/completions, Anthropic) reject a conversation
// where an assistant turn advertises tool_calls that are never answered
// ("an assistant message with 'tool_calls' must be followed by tool messages
// responding to each tool_call_id"). The parallel batch runner drops in-flight
// results when a sibling fails hard or ctx is cancelled, and the partial
// history is still returned to the caller — and persisted to the session JSONL.
// Resuming that session then 400s forever. Padding keeps history replayable.
func repairToolResults(calls []llm.ToolCall, results []llm.Message, reason error) []llm.Message {
	if len(calls) == 0 {
		return results
	}
	seen := make(map[string]bool, len(results))
	for _, m := range results {
		if m.Role == "tool" {
			seen[m.ToolCallID] = true
		}
	}
	for _, tc := range calls {
		if seen[tc.ID] {
			continue
		}
		seen[tc.ID] = true
		content := orphanToolResultPrefix
		if reason != nil {
			content = fmt.Sprintf("%s: %v", orphanToolResultPrefix, reason)
		}
		results = append(results, llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    content,
		})
	}
	return results
}
