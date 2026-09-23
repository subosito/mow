package llm

import (
	"strings"
	"testing"
)

func TestResponsesOrphanArgumentsDoNotCreateToolCall(t *testing.T) {
	msg := Message{Role: "assistant"}
	content := new(strings.Builder)
	tools := map[int]*responsesToolAcc{}
	ids := map[string]int{}
	if err := applyResponsesSSE(`{"type":"response.function_call_arguments.delta","output_index":2494,"item_id":"missing","delta":"{}"}`, "response.function_call_arguments.delta", &msg, content, tools, ids, StreamHooks{}); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("orphan accumulator=%v", tools)
	}
}
