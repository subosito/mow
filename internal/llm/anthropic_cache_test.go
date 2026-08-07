package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func captureAnthropic(t *testing.T, cache bool, reply string) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), PromptCache: cache}
	tools := []ToolSpec{{Type: "function", Function: ToolSpecFunction{Name: "read"}}}
	if _, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "big system prompt"},
		{Role: "user", Content: "hello"},
	}, tools); err != nil {
		t.Fatalf("chat: %v", err)
	}
	return body
}

func TestAnthropicPromptCacheMarkers(t *testing.T) {
	reply := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`

	// Caching on: system is a cacheable block array, last tool and last message
	// carry cache_control.
	on := captureAnthropic(t, true, reply)
	sys, ok := on["system"].([]any)
	if !ok || len(sys) == 0 {
		t.Fatalf("system should be a block array when caching: %T", on["system"])
	}
	if _, has := sys[0].(map[string]any)["cache_control"]; !has {
		t.Fatalf("system block missing cache_control: %v", sys[0])
	}
	tools, _ := on["tools"].([]any)
	if len(tools) == 0 || tools[len(tools)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("last tool missing cache_control: %v", on["tools"])
	}
	msgs, _ := on["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	blocks, ok := last["content"].([]any)
	if !ok || blocks[len(blocks)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("last message missing cache_control: %v", last["content"])
	}

	// Caching off: system is a plain string, no cache markers.
	off := captureAnthropic(t, false, reply)
	if _, isStr := off["system"].(string); !isStr {
		t.Fatalf("system should be a plain string without caching: %T", off["system"])
	}
	tools2, _ := off["tools"].([]any)
	if tools2[len(tools2)-1].(map[string]any)["cache_control"] != nil {
		t.Fatal("tools should not carry cache_control when caching off")
	}
}

func TestAnthropicCacheTokensSummed(t *testing.T) {
	reply := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":900}}`
	var _ = reply
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), PromptCache: true}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 10 + 100 + 900 = 1010 total input.
	if msg.Usage.InputTokens != 1010 {
		t.Fatalf("input tokens=%d want 1010 (cached + non-cached summed)", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 5 {
		t.Fatalf("output tokens=%d want 5", msg.Usage.OutputTokens)
	}
}

func TestAnthropicBreakpointBudget(t *testing.T) {
	// Anthropic rejects requests with more than 4 cache_control breakpoints.
	// With several system_prefix entries, marking every system block plus the
	// last tool and last message would blow that budget. Assert the exact
	// distribution: one breakpoint each on the last system block, the last
	// tool, and the last message content block — total exactly 3.
	reply := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m",
		HTTP: srv.Client(), PromptCache: true,
		SystemPrefix: []string{"prefix-a", "prefix-b", "prefix-c"},
	}
	tools := []ToolSpec{{Type: "function", Function: ToolSpecFunction{Name: "read"}}}
	if _, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "big system prompt"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}, tools); err != nil {
		t.Fatalf("chat: %v", err)
	}
	assertBreakpointBudget(t, body)
}

// assertBreakpointBudget checks the exhaustive breakpoint distribution for
// the shared fixture (3 system_prefix entries + main system + 1 tool + 3
// turns): cache_control only on the last system block, the last tool, and
// the last content block of the last message — exactly 3 total. Any marker
// anywhere else fails, not just a wrong total.
func assertBreakpointBudget(t *testing.T, body map[string]any) {
	t.Helper()
	count := 0
	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 4 {
		t.Fatalf("system should be 4 blocks (3 prefix + main): %v", body["system"])
	}
	for i, b := range sys {
		has := b.(map[string]any)["cache_control"] != nil
		if i < len(sys)-1 && has {
			t.Fatalf("system prefix block %d carries cache_control", i)
		}
		if i == len(sys)-1 && !has {
			t.Fatal("last system block missing cache_control")
		}
		if has {
			count++
		}
	}
	ts, ok := body["tools"].([]any)
	if !ok || len(ts) == 0 {
		t.Fatalf("tools missing from body: %v", body["tools"])
	}
	for i, tl := range ts {
		if tl.(map[string]any)["cache_control"] != nil {
			if i != len(ts)-1 {
				t.Fatalf("non-last tool %d carries cache_control", i)
			}
			count++
		}
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages missing from body")
	}
	for i, m := range msgs {
		blocks, ok := m.(map[string]any)["content"].([]any)
		if !ok {
			continue
		}
		for j, b := range blocks {
			if b.(map[string]any)["cache_control"] != nil {
				if i != len(msgs)-1 || j != len(blocks)-1 {
					t.Fatalf("cache_control on non-last block (msg %d/%d, block %d/%d)", i, len(msgs)-1, j, len(blocks)-1)
				}
				count++
			}
		}
	}
	if count != 3 {
		t.Fatalf("%d cache_control breakpoints, want exactly 3 (system + tool + last message)", count)
	}
}

func TestAnthropicStreamBreakpointBudget(t *testing.T) {
	// The streaming path assembles the request independently
	// (anthropic_stream.go) but must apply the same breakpoint budget as the
	// non-streaming one. Guard against future drift between the two.
	const body = "" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m",
		HTTP: srv.Client(), PromptCache: true, Stream: true,
		SystemPrefix: []string{"prefix-a", "prefix-b", "prefix-c"},
	}
	tools := []ToolSpec{{Type: "function", Function: ToolSpecFunction{Name: "read"}}}
	if _, err := c.ChatWithStream(context.Background(), []Message{
		{Role: "system", Content: "big system prompt"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}, tools, StreamHooks{}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	assertBreakpointBudget(t, got)
}
