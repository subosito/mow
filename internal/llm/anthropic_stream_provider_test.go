package llm

import (
	"strings"
	"testing"
)

// mow streams by default, and the Anthropic stream reports server-side work
// only as content blocks — message_delta carries no server_tool_use counter.
// Without both halves a streamed run says zero provider calls beside an answer
// built from search results, which is what a host would show the user.
func TestAnthropicStreamReportsProviderCalls(t *testing.T) {
	events := []string{
		`event: message_start
data: {"message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`event: content_block_start
data: {"index":0,"content_block":{"type":"server_tool_use","id":"srv1","name":"web_search"}}`,
		`event: content_block_start
data: {"index":1,"content_block":{"type":"web_search_tool_result","id":"res1"}}`,
		`event: content_block_start
data: {"index":2,"content_block":{"type":"text"}}`,
		`event: content_block_delta
data: {"index":2,"delta":{"type":"text_delta","text":"cited answer"}}`,
		`event: message_delta
data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
	}

	var msg Message
	toolsByIdx := map[int]*anthToolAcc{}
	for _, ev := range events {
		lines := strings.SplitN(ev, "\n", 2)
		name := strings.TrimPrefix(lines[0], "event: ")
		data := strings.TrimPrefix(lines[1], "data: ")
		if err := applyAnthropicSSE(data, name, &msg, toolsByIdx, StreamHooks{}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	if len(msg.ToolCalls) != 0 {
		t.Fatalf("server tool became executable: %#v", msg.ToolCalls)
	}
	if len(msg.ProviderCalls) != 2 {
		t.Fatalf("provider calls not recorded: %#v", msg.ProviderCalls)
	}
	if msg.Usage.ServerSideToolCalls != 2 {
		t.Fatalf("server-side count not reported: %#v", msg.Usage)
	}
}
