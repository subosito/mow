package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Encrypted reasoning must survive a store:false round trip: requested via
// include, parsed from the response (stream and non-stream), persisted on the
// message, and replayed before its function_call, with the fc_ item id.
func TestResponsesReasoningRoundTrip(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC=="}`
	const nonStream = `{"status":"completed","output":[` + reasoning + `,
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}"}],
		"usage":{"input_tokens":3,"output_tokens":2}}`
	stream := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + reasoning + "}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1,\"item_id\":\"fc_1\",\"arguments\":\"{\\\"path\\\":\\\"a\\\"}\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"

	for _, streamMode := range []bool{false, true} {
		name := "non-stream"
		if streamMode {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var bodies []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var b map[string]any
				_ = json.Unmarshal(raw, &b)
				bodies = append(bodies, b)
				if streamMode {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(stream))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(nonStream))
			}))
			defer srv.Close()
			c := &Client{Wire: WireOpenAIResponses, BaseURL: srv.URL + "/v1", APIKey: "k",
				Model: "reasoner", Effort: "high", Stream: streamMode, HTTP: srv.Client()}

			msg, err := c.Chat(t.Context(), []Message{{Role: "user", Content: "go"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if inc, _ := bodies[0]["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
				t.Fatalf("include=%v", bodies[0]["include"])
			}
			if len(msg.Reasoning) != 1 || msg.Reasoning[0].Model != "reasoner" || !strings.Contains(string(msg.Reasoning[0].Item), "ENC==") {
				t.Fatalf("reasoning=%+v", msg.Reasoning)
			}
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].ItemID != "fc_1" {
				t.Fatalf("tool calls=%+v", msg.ToolCalls)
			}

			// Session round trip keeps the reasoning.
			raw, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			var stored Message
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}

			// Next step: reasoning, then its function_call (with id), then
			// the result, then a steer.
			history := []Message{
				{Role: "user", Content: "go"},
				stored,
				{Role: "tool", ToolCallID: "call_1", Content: "file a"},
				{Role: "user", Content: "also check b"},
			}
			if _, err := c.Chat(t.Context(), history, nil); err != nil {
				t.Fatal(err)
			}
			input, _ := bodies[1]["input"].([]any)
			var kinds []string
			for _, it := range input {
				m := it.(map[string]any)
				k, _ := m["type"].(string)
				if k == "" {
					k, _ = m["role"].(string)
				}
				kinds = append(kinds, k)
				if k == "reasoning" && m["encrypted_content"] != "ENC==" {
					t.Fatalf("reasoning not replayed verbatim: %v", m)
				}
				if k == "function_call" && m["id"] != "fc_1" {
					t.Fatalf("function_call item id missing: %v", m)
				}
			}
			want := "user,reasoning,function_call,function_call_output,user"
			if got := strings.Join(kinds, ","); got != want {
				t.Fatalf("input order = %s, want %s", got, want)
			}
		})
	}
}

// Encrypted reasoning is model-bound: after a model switch it is dropped, and
// the function_call goes out without its item id.
func TestResponsesReasoningDroppedOnModelSwitch(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "go"},
		{Role: "assistant",
			Reasoning: []ReasoningItem{{Model: "old-model", Item: json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"ENC=="}`)}},
			ToolCalls: []ToolCall{{ID: "call_1", ItemID: "fc_1", Type: "function", Function: FunctionCall{Name: "read", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	}
	_, input := toResponsesInput(msgs, "new-model")
	for _, it := range input {
		if it["type"] == "reasoning" {
			t.Fatalf("reasoning replayed to another model: %v", it)
		}
		if it["type"] == "function_call" {
			if _, ok := it["id"]; ok {
				t.Fatalf("item id sent without its reasoning: %v", it)
			}
		}
	}
	_, input = toResponsesInput(msgs, "old-model")
	if input[1]["type"] != "reasoning" || input[2]["id"] != "fc_1" {
		t.Fatalf("same model must replay: %v", input)
	}
}

// Without reasoning effort the include is not sent (some models/gateways
// reject it).
func TestResponsesNoIncludeWithoutEffort(t *testing.T) {
	c := &Client{Wire: WireOpenAIResponses, Model: "plain"}
	if body := c.buildResponsesBody([]Message{{Role: "user", Content: "hi"}}, nil, false); len(body.Include) != 0 {
		t.Fatalf("include=%v", body.Include)
	}
}
