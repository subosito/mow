package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeWireResponsesAlias(t *testing.T) {
	if got := NormalizeWire("openai-response"); got != WireOpenAIResponses {
		t.Fatalf("alias: got %q want %q", got, WireOpenAIResponses)
	}
	if got := NormalizeWire("OpenAI-Responses"); got != WireOpenAIResponses {
		t.Fatalf("case: got %q", got)
	}
	if !IsKnownChatWire("openai-response") {
		t.Fatal("alias should be known")
	}
	if !IsKnownChatWire(WireOpenAIResponses) {
		t.Fatal("canonical should be known")
	}
}

func TestToResponsesInputToolLoop(t *testing.T) {
	in := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			Function: FunctionCall{Name: "read", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "package a"},
		{Role: "assistant", Content: "done"},
	}
	instructions, items := toResponsesInput(in)
	if instructions != "sys" {
		t.Fatalf("instructions=%q", instructions)
	}
	if len(items) != 4 {
		t.Fatalf("items=%d want 4: %+v", len(items), items)
	}
	if items[0]["role"] != "user" || items[0]["content"] != "go" {
		t.Fatalf("user: %+v", items[0])
	}
	if items[1]["type"] != "function_call" || items[1]["call_id"] != "call_1" {
		t.Fatalf("function_call: %+v", items[1])
	}
	if items[1]["name"] != "read" {
		t.Fatalf("name: %+v", items[1])
	}
	if items[2]["type"] != "function_call_output" || items[2]["call_id"] != "call_1" {
		t.Fatalf("function_call_output: %+v", items[2])
	}
	if items[2]["output"] != "package a" {
		t.Fatalf("output: %+v", items[2])
	}
	if items[3]["role"] != "assistant" || items[3]["content"] != "done" {
		t.Fatalf("assistant: %+v", items[3])
	}
}

func TestToResponsesToolsFlattened(t *testing.T) {
	tools := []ToolSpec{{
		Type: "function",
		Function: ToolSpecFunction{
			Name:        "read",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
	}}
	out := toResponsesTools(tools)
	if len(out) != 1 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0]["type"] != "function" {
		t.Fatalf("type=%v", out[0]["type"])
	}
	if out[0]["name"] != "read" {
		t.Fatalf("name=%v", out[0]["name"])
	}
	// Must be flat (not nested under "function").
	if _, ok := out[0]["function"]; ok {
		t.Fatalf("nested function key present: %+v", out[0])
	}
	if out[0]["strict"] != false {
		t.Fatalf("strict=%v want false", out[0]["strict"])
	}
}

func TestChatOpenAIResponsesNonStream(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" && !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path=%s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_abc","name":"read","arguments":"{\"path\":\"x.go\"}"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reading"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer srv.Close()

	c := &Client{
		Wire:    WireOpenAIResponses,
		BaseURL: srv.URL + "/v1",
		APIKey:  "k",
		Model:   "test-model",
		HTTP:    srv.Client(),
	}
	msg, err := c.Chat(context.Background(), []Message{
		{Role: "user", Content: "read x"},
	}, []ToolSpec{{
		Type: "function",
		Function: ToolSpecFunction{
			Name:       "read",
			Parameters: json.RawMessage(`{"type":"object"}`),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "test-model" {
		t.Fatalf("model=%v body=%v", gotBody["model"], gotBody)
	}
	if store, _ := gotBody["store"].(bool); store {
		t.Fatalf("store should be false, body=%v", gotBody)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", gotBody["tools"])
	}
	if msg.Content != "reading" {
		t.Fatalf("content=%q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_abc" {
		t.Fatalf("tool_calls=%+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("name=%q", msg.ToolCalls[0].Function.Name)
	}
	if msg.Usage.InputTokens != 10 || msg.Usage.OutputTokens != 5 {
		t.Fatalf("usage=%+v", msg.Usage)
	}
	if msg.StopReason != "tool_calls" {
		t.Fatalf("stop=%q", msg.StopReason)
	}
}

func TestChatOpenAIResponsesStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Minimal Responses SSE: text deltas + function call + completed.
		chunks := []string{
			`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","delta":"Hi"}` + "\n\n",
			`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_z","name":"glob","arguments":""}}` + "\n\n",
			`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"pattern\":"}` + "\n\n",
			`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"\"**/*.go\"}"}` + "\n\n",
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":7},"output":[]}}` + "\n\n",
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	c := &Client{
		Wire:    WireOpenAIResponses,
		BaseURL: srv.URL + "/v1",
		APIKey:  "k",
		Model:   "m",
		HTTP:    srv.Client(),
		Stream:  true,
	}
	var content strings.Builder
	msg, err := c.ChatWithStream(context.Background(), []Message{
		{Role: "user", Content: "x"},
	}, nil, StreamHooks{
		OnContent: func(d string) { content.WriteString(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hi" || content.String() != "Hi" {
		t.Fatalf("content=%q delta=%q", msg.Content, content.String())
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool_calls=%+v", msg.ToolCalls)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_z" || tc.Function.Name != "glob" {
		t.Fatalf("tc=%+v", tc)
	}
	if tc.Function.Arguments != `{"pattern":"**/*.go"}` {
		t.Fatalf("args=%q", tc.Function.Arguments)
	}
	if msg.Usage.InputTokens != 3 || msg.Usage.OutputTokens != 7 {
		t.Fatalf("usage=%+v", msg.Usage)
	}
}

func TestMessageFromResponsesProviderCalls(t *testing.T) {
	tests := []struct {
		name          string
		output        []responsesOutputItem
		wantPC        []ProviderCall
		wantText      string
		wantToolCalls int
	}{
		{
			name: "web_search_calls recorded, no ToolCalls",
			output: []responsesOutputItem{
				{Type: "web_search_call", ID: "ws_1", Status: "completed"},
				{Type: "web_search_call", ID: "ws_2", Status: "completed"},
				{Type: "message", Content: []responsesContentPart{{Type: "output_text", Text: "answer"}}},
				{Type: "web_search_call", ID: "ws_3", Status: "completed"},
				{Type: "web_search_call", ID: "ws_4", Status: "failed"},
			},
			wantPC: []ProviderCall{
				{Type: "web_search_call", ID: "ws_1", Status: "completed"},
				{Type: "web_search_call", ID: "ws_2", Status: "completed"},
				{Type: "web_search_call", ID: "ws_3", Status: "completed"},
				{Type: "web_search_call", ID: "ws_4", Status: "failed"},
			},
			wantText: "answer",
		},
		{
			name: "unknown future provider call types recorded",
			output: []responsesOutputItem{
				{Type: "x_search_call", ID: "x_1", Status: "completed"},
				{Type: "code_interpreter_call", ID: "ci_1", Status: "in_progress"},
			},
			wantPC: []ProviderCall{
				{Type: "x_search_call", ID: "x_1", Status: "completed"},
				{Type: "code_interpreter_call", ID: "ci_1", Status: "in_progress"},
			},
		},
		{
			name: "mixed function_call and provider calls",
			output: []responsesOutputItem{
				{Type: "web_search_call", ID: "ws_1", Status: "completed"},
				{Type: "function_call", CallID: "call_1", Name: "read", Arguments: `{"path":"a"}`},
			},
			wantPC:        []ProviderCall{{Type: "web_search_call", ID: "ws_1", Status: "completed"}},
			wantToolCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := messageFromResponses(responsesAPIResponse{Status: "completed", Output: tt.output})
			if len(msg.ProviderCalls) != len(tt.wantPC) {
				t.Fatalf("ProviderCalls=%+v want %+v", msg.ProviderCalls, tt.wantPC)
			}
			for i, pc := range tt.wantPC {
				if msg.ProviderCalls[i] != pc {
					t.Fatalf("ProviderCalls[%d]=%+v want %+v", i, msg.ProviderCalls[i], pc)
				}
			}
			// Critical: provider-executed calls must never become ToolCalls —
			// the agent loop would try to execute tools it does not have.
			if len(msg.ToolCalls) != tt.wantToolCalls {
				t.Fatalf("ToolCalls=%+v want %d entries", msg.ToolCalls, tt.wantToolCalls)
			}
			for _, tc := range msg.ToolCalls {
				if tc.Type != "function" {
					t.Fatalf("non-function ToolCall leaked: %+v", tc)
				}
			}
			if msg.Content != tt.wantText {
				t.Fatalf("content=%q want %q", msg.Content, tt.wantText)
			}
		})
	}
}

func TestChatOpenAIResponsesWebSearchNonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_ws",
			"status":"completed",
			"output":[
				{"type":"reasoning","id":"rs_1"},
				{"type":"web_search_call","id":"ws_1","status":"completed"},
				{"type":"web_search_call","id":"ws_2","status":"completed"},
				{"type":"web_search_call","id":"ws_3","status":"completed"},
				{"type":"web_search_call","id":"ws_4","status":"completed"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"found it"}]}
			],
			"usage":{"input_tokens":20,"output_tokens":9,"num_sources_used":4,"num_server_side_tool_calls":4}
		}`))
	}))
	defer srv.Close()

	c := &Client{
		Wire:    WireOpenAIResponses,
		BaseURL: srv.URL + "/v1",
		APIKey:  "k",
		Model:   "m",
		HTTP:    srv.Client(),
	}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "search x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "found it" {
		t.Fatalf("content=%q", msg.Content)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("ToolCalls must stay empty, got %+v", msg.ToolCalls)
	}
	if len(msg.ProviderCalls) != 4 {
		t.Fatalf("ProviderCalls=%+v want 4 entries", msg.ProviderCalls)
	}
	for _, pc := range msg.ProviderCalls {
		if pc.Type != "web_search_call" || pc.Status != "completed" {
			t.Fatalf("pc=%+v", pc)
		}
	}
	if msg.Usage.SourcesUsed != 4 || msg.Usage.ServerSideToolCalls != 4 {
		t.Fatalf("usage=%+v", msg.Usage)
	}
	if msg.Usage.InputTokens != 20 || msg.Usage.OutputTokens != 9 {
		t.Fatalf("usage tokens=%+v", msg.Usage)
	}
	if msg.StopReason != "completed" {
		t.Fatalf("stop=%q", msg.StopReason)
	}
}

func TestChatOpenAIResponsesWebSearchStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}` + "\n\n",
			`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed"}}` + "\n\n",
			`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","delta":"done"}` + "\n\n",
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":2,"num_sources_used":1,"num_server_side_tool_calls":1},"output":[]}}` + "\n\n",
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	c := &Client{
		Wire:    WireOpenAIResponses,
		BaseURL: srv.URL + "/v1",
		APIKey:  "k",
		Model:   "m",
		HTTP:    srv.Client(),
		Stream:  true,
	}
	msg, err := c.ChatWithStream(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "done" {
		t.Fatalf("content=%q", msg.Content)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("ToolCalls must stay empty, got %+v", msg.ToolCalls)
	}
	if len(msg.ProviderCalls) != 1 {
		t.Fatalf("ProviderCalls=%+v want exactly 1 (added+done deduped)", msg.ProviderCalls)
	}
	pc := msg.ProviderCalls[0]
	if pc.Type != "web_search_call" || pc.ID != "ws_1" || pc.Status != "completed" {
		t.Fatalf("pc=%+v", pc)
	}
	if msg.Usage.ServerSideToolCalls != 1 || msg.Usage.SourcesUsed != 1 {
		t.Fatalf("usage=%+v", msg.Usage)
	}
}

func TestProviderCallsNotSerializedOutbound(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: "answer",
		ProviderCalls: []ProviderCall{
			{Type: "web_search_call", ID: "ws_1", Status: "completed"},
		},
	}

	// Responses wire: only content may survive.
	_, items := toResponsesInput([]Message{msg})
	if len(items) != 1 {
		t.Fatalf("items=%+v want 1", items)
	}
	if _, ok := items[0]["type"]; ok {
		t.Fatalf("provider call leaked as item: %+v", items[0])
	}

	// Session/history JSON: json:"-" keeps it out of the wire and on-disk shape.
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "provider_calls") || strings.Contains(string(b), "web_search_call") {
		t.Fatalf("ProviderCalls serialized: %s", b)
	}

	// Chat-completions wire: openAIMessage has no such field by construction.
	out := toOpenAIMessages([]Message{msg})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "provider_calls") || strings.Contains(string(raw), "web_search_call") {
		t.Fatalf("ProviderCalls leaked to chat wire: %s", raw)
	}
}

func TestResponsesURL(t *testing.T) {
	if got := responsesURL(""); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("default=%q", got)
	}
	if got := responsesURL("http://gw/v1"); got != "http://gw/v1/responses" {
		t.Fatalf("append=%q", got)
	}
	if got := responsesURL("http://gw/v1/responses"); got != "http://gw/v1/responses" {
		t.Fatalf("full=%q", got)
	}
}
