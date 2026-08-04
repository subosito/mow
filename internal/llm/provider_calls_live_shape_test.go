package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// Payload shaped like a real gateway response with provider search enabled:
// reasoning + several web_search_call items + a final message. The provider
// ran those searches itself, so they must be reported but never handed to the
// agent loop as executable calls.
func TestProviderCallsLiveShape(t *testing.T) {
	raw := `{"status":"completed","output":[
	 {"type":"reasoning","id":"r1"},
	 {"type":"web_search_call","id":"ws1","status":"completed"},
	 {"type":"web_search_call","id":"ws2","status":"completed"},
	 {"type":"message","content":[{"type":"output_text","text":"answer"}]}
	],"usage":{"input_tokens":218,"output_tokens":82,"num_sources_used":7,"num_server_side_tool_calls":2}}`

	var parsed responsesAPIResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	msg := messageFromResponses(parsed)

	if len(msg.ToolCalls) != 0 {
		t.Fatalf("CRITICAL: provider calls leaked into executable ToolCalls: %#v", msg.ToolCalls)
	}
	if len(msg.ProviderCalls) != 2 {
		t.Fatalf("want 2 provider calls, got %#v", msg.ProviderCalls)
	}
	if msg.Content != "answer" {
		t.Fatalf("text lost: %q", msg.Content)
	}
	if msg.Usage.SourcesUsed != 7 || msg.Usage.ServerSideToolCalls != 2 {
		t.Fatalf("usage extras: %#v", msg.Usage)
	}
	if msg.StopReason != "completed" {
		t.Fatalf("stop reason changed: %q", msg.StopReason)
	}
	// Must not serialize onto the wire.
	b, _ := json.Marshal(msg)
	if strings.Contains(string(b), "web_search_call") {
		t.Fatalf("provider calls leaked into serialized message: %s", b)
	}
}
