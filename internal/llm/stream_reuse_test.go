package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests guard the streaming perf rewrite: the SSE loop reuses one
// streamChunk value and one json.Decoder across deltas, and accumulates
// content/arguments in strings.Builders flushed after the loop. Behaviour
// (hooks per delta, ordering, stop reason, usage) must be unchanged.

func streamTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sseBody(lines ...string) string {
	return strings.Join(lines, "\n\n") + "\n\n"
}

// A field present in an early chunk must not leak into a later chunk that
// omits it — the decode target is reused, so reset() must clear it.
func TestChatStreamHooksReusedDecoderNoStaleFields(t *testing.T) {
	body := sseBody(
		// chunk 1: content + reasoning + tool call with id/name
		`data: {"choices":[{"delta":{"content":"a","reasoning":"think","tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":"{\"x\":"}}]}}]}`,
		// chunk 2: omits id/name/reasoning; only an argument fragment
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		// chunk 3: plain content only — no tool_calls, no reasoning
		`data: {"choices":[{"delta":{"content":"b"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"usage":{"prompt_tokens":7,"completion_tokens":3},"choices":[]}`,
		"data: [DONE]",
	)
	srv := streamTestServer(t, body)

	c := &Client{APIKey: "k", Model: "m", BaseURL: srv.URL}
	var content, reasoning []string
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{
		OnContent:   func(d string) { content = append(content, d) },
		OnReasoning: func(d string) { reasoning = append(reasoning, d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Hooks still fire once per delta, in order.
	if got := strings.Join(content, "|"); got != "a|b" {
		t.Fatalf("content deltas = %q, want %q", got, "a|b")
	}
	if got := strings.Join(reasoning, "|"); got != "think" {
		t.Fatalf("reasoning deltas = %q, want %q", got, "think")
	}
	if msg.Content != "ab" {
		t.Fatalf("content = %q, want %q", msg.Content, "ab")
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "f" {
		t.Fatalf("tool call id/name = %q/%q, want call_1/f", tc.ID, tc.Function.Name)
	}
	if tc.Function.Arguments != `{"x":1}` {
		t.Fatalf("arguments = %q, want %q", tc.Function.Arguments, `{"x":1}`)
	}
	if msg.StopReason != "tool_calls" {
		t.Fatalf("stop reason = %q, want tool_calls", msg.StopReason)
	}
	if msg.Usage.InputTokens != 7 || msg.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
}

// A malformed data line is skipped without corrupting later chunks (the
// reused decoder is re-synced on error).
func TestChatStreamHooksMalformedLineDoesNotPoisonStream(t *testing.T) {
	body := sseBody(
		`data: {"choices":[{"delta":{"content":"one"}}]}`,
		`data: {"choices":[{"delta":{"content":`, // truncated JSON
		`data: not json at all`,
		`data: {"choices":[{"delta":{"content":"two"},"finish_reason":"stop"}]}`,
		"data: [DONE]",
	)
	srv := streamTestServer(t, body)

	c := &Client{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "onetwo" {
		t.Fatalf("content = %q, want %q", msg.Content, "onetwo")
	}
	if msg.StopReason != "stop" {
		t.Fatalf("stop reason = %q, want stop", msg.StopReason)
	}
}

// Multi-delta accumulation across many chunks (the O(n²) path that was
// replaced by builders) must still concatenate in arrival order, and
// interleaved tool calls must stay ordered by index.
func TestChatStreamHooksBuilderAccumulationOrder(t *testing.T) {
	var lines []string
	want := strings.Builder{}
	for i := 0; i < 50; i++ {
		part := string(rune('a' + i%26))
		want.WriteString(part)
		lines = append(lines, `data: {"choices":[{"delta":{"content":"`+part+`"}}]}`)
	}
	// Two tool calls, fragments interleaved, index 2 announced before index 1.
	lines = append(lines,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":2,"id":"b","function":{"name":"second","arguments":"{\"p\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"a","function":{"name":"first","arguments":"{\"q\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":2,"function":{"arguments":"2}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"1}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"data: [DONE]",
	)
	srv := streamTestServer(t, sseBody(lines...))

	c := &Client{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != want.String() {
		t.Fatalf("content = %q, want %q", msg.Content, want.String())
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "first" || msg.ToolCalls[0].Function.Arguments != `{"q":1}` {
		t.Fatalf("call[0] = %+v, want first {\"q\":1}", msg.ToolCalls[0].Function)
	}
	if msg.ToolCalls[1].Function.Name != "second" || msg.ToolCalls[1].Function.Arguments != `{"p":2}` {
		t.Fatalf("call[1] = %+v, want second {\"p\":2}", msg.ToolCalls[1].Function)
	}
}

// A content-only stream must not allocate an empty non-nil ToolCalls slice.
func TestChatStreamHooksNoToolCallsStaysNil(t *testing.T) {
	srv := streamTestServer(t, sseBody(
		`data: {"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		"data: [DONE]",
	))
	c := &Client{APIKey: "k", Model: "m", BaseURL: srv.URL}
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.ToolCalls != nil {
		t.Fatalf("tool calls = %+v, want nil", msg.ToolCalls)
	}
}
