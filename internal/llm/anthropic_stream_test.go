package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicStreamToolAfterText(t *testing.T) {
	// Common "narrate, then call a tool" shape: text block at index 0,
	// tool_use at index 1. The tool call must not be dropped.
	const body = "" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Let me look.\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"read\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a.go\\\"}\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.ChatWithStream(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnContent: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Let me look." {
		t.Fatalf("content=%q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls=%d want 1 (%+v)", len(msg.ToolCalls), msg.ToolCalls)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "tu_1" || tc.Function.Name != "read" || tc.Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("tool call=%+v", tc)
	}
}

func TestAnthropicStreamToolsKeepOrder(t *testing.T) {
	// Two tools at indices 1 and 2 (text at 0) must both survive, in order.
	const body = "" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_a\",\"name\":\"glob\"}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_b\",\"name\":\"grep\"}}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.ChatWithStream(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnContent: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("tool calls=%d want 2 (%+v)", len(msg.ToolCalls), msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID != "tu_a" || msg.ToolCalls[1].ID != "tu_b" {
		t.Fatalf("order=%q,%q", msg.ToolCalls[0].ID, msg.ToolCalls[1].ID)
	}
}

func TestAnthropicStreamMaxTokensStopSurfaced(t *testing.T) {
	// message_delta carries stop_reason; max_tokens means truncation and must
	// reach the caller alongside the partial content.
	const body = "" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":8192}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.ChatWithStream(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnContent: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "partial" {
		t.Fatalf("content=%q", msg.Content)
	}
	if msg.StopReason != "max_tokens" || !msg.Truncated() {
		t.Fatalf("StopReason=%q Truncated=%v want max_tokens/true", msg.StopReason, msg.Truncated())
	}
}

func TestAnthropicStreamActivitySkipsPing(t *testing.T) {
	// ping keepalives arrive while the model is still silent — they must not
	// end the host's wait. message_start is the first real sign of work and
	// fires OnActivity exactly once.
	const body = "" +
		"event: ping\n" +
		"data: {\"type\":\"ping\"}\n\n" +
		"event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	var activityN int
	msg, err := c.ChatWithStream(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnActivity: func() { activityN++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "hi" {
		t.Fatalf("content=%q", msg.Content)
	}
	if activityN != 1 {
		t.Fatalf("OnActivity fired %d times, want exactly 1 (pings excluded)", activityN)
	}
}

func TestAnthropicStreamActivitySilentOnPingOnlyStream(t *testing.T) {
	// A keepalive-only stream never evidences model work: zero activity. The
	// second ping omits the event: line (some proxies strip it) so the data
	// payload's type field must carry the classification.
	const body = "" +
		"event: ping\n" +
		"data: {\"type\":\"ping\"}\n\n" +
		"data: {\"type\":\"ping\"}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	var activityN int
	_, err := c.ChatWithStream(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnActivity: func() { activityN++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if activityN != 0 {
		t.Fatalf("OnActivity fired %d times on a ping-only stream, want 0", activityN)
	}
}
