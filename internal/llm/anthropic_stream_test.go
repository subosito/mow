package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
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
		"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_b\",\"name\":\"grep\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
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
	msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
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
	msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
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
	_, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnActivity: func() { activityN++ },
	})
	// The connection dropped before message_stop: that is a failed call, not
	// an empty answer.
	if !errors.Is(err, errStreamIncomplete) {
		t.Fatalf("err=%v want errStreamIncomplete", err)
	}
	if activityN != 0 {
		t.Fatalf("OnActivity fired %d times on a ping-only stream, want 0", activityN)
	}
}

func TestAnthropicStreamFailsOnEOFBeforeMessageStop(t *testing.T) {
	// Text arrived, then the connection dropped before message_delta /
	// message_stop. Returning the partial text as a finished answer would
	// commit a truncated reply to history.
	const body = "" +
		"event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), Stream: true}
	_, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if !errors.Is(err, errStreamIncomplete) {
		t.Fatalf("err=%v want errStreamIncomplete", err)
	}
}

func TestAnthropicAuthHeadersOnBothPaths(t *testing.T) {
	// Native hosts read x-api-key; gateways that front every wire with OpenAI
	// style auth read the Bearer token. Both paths must send the same set.
	const body = "" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	var got []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	if _, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{OnContent: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(t.Context(), []Message{{Role: "user", Content: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got[0].Get("Accept") != "text/event-stream" || got[1].Get("Accept") == "text/event-stream" {
		t.Fatalf("expected one stream and one JSON request, got Accept=%q,%q", got[0].Get("Accept"), got[1].Get("Accept"))
	}
	if len(got) != 2 {
		t.Fatalf("requests=%d want 2", len(got))
	}
	for i, h := range got {
		if h.Get("x-api-key") != "k" || h.Get("Authorization") != "Bearer k" || h.Get("anthropic-version") == "" {
			t.Fatalf("request %d headers: x-api-key=%q auth=%q version=%q", i, h.Get("x-api-key"), h.Get("Authorization"), h.Get("anthropic-version"))
		}
	}
}

func TestAnthropicStreamReturnsAtMessageStopWithoutEOF(t *testing.T) {
	// A gateway that keeps the connection open after message_stop (keep-alive
	// idle, upstream lingering) must not hold the turn: the wire defines
	// message_stop as the final frame, so the reader stops there.
	const body = "" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the connection until the client gives up
	}))
	t.Cleanup(srv.Close)

	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), Stream: true}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	msg, err := c.ChatWithStream(ctx, []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if msg.Content != "ok" {
		t.Fatalf("content=%q", msg.Content)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stream waited %s for EOF after message_stop", d)
	}
}

// A cut Anthropic stream with no visible text is re-sent once, like the
// chat-completions wire, so a gateway hiccup does not end the turn.
func TestAnthropicStreamRetriesCutBeforeAnyText(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = io.WriteString(w, "event: message_start\n"+`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}`+"\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+"\n\n"+
			"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	t.Cleanup(srv.Close)
	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), Stream: true}
	msg, err := c.ChatWithStream(t.Context(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err != nil || msg.Content != "ok" || calls != 2 {
		t.Fatalf("msg=%q err=%v calls=%d, want one retry then ok", msg.Content, err, calls)
	}
}
