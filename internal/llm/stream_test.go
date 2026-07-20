package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamHooksSeparatesReasoningAndContent(t *testing.T) {
	// Simulate DeepSeek/ZenMux: long reasoning then short content.
	const body = "" +
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"think \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"step\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept=%q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "deepseek-v4-flash",
		HTTP:    srv.Client(),
	}

	var content, reasoning strings.Builder
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, StreamHooks{
		OnContent:   func(d string) { content.WriteString(d) },
		OnReasoning: func(d string) { reasoning.WriteString(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hi!" {
		t.Fatalf("Message.Content=%q want Hi! (reasoning must not leak)", msg.Content)
	}
	if content.String() != "Hi!" {
		t.Fatalf("content callbacks=%q", content.String())
	}
	if reasoning.String() != "think step" {
		t.Fatalf("reasoning callbacks=%q", reasoning.String())
	}
}

func TestChatStreamHooksReasoningContentField(t *testing.T) {
	const body = "" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
	var reason strings.Builder
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{
		OnReasoning: func(d string) { reason.WriteString(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "ok" {
		t.Fatalf("content=%q", msg.Content)
	}
	if reason.String() != "plan" {
		t.Fatalf("reason=%q", reason.String())
	}
}

func TestChatStreamHooksNonContiguousToolIndices(t *testing.T) {
	// Some gateways send tool_calls indices that skip values or start above 0.
	// Both calls must survive, in index order.
	cases := []struct {
		name    string
		indices [2]int
	}{
		{"gap after zero", [2]int{0, 2}},
		{"starts above zero", [2]int{1, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(""+
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"tc_a\",\"function\":{\"name\":\"glob\",\"arguments\":\"{}\"}}]}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"tc_b\",\"function\":{\"name\":\"grep\",\"arguments\":\"{}\"}}]}}]}\n\n"+
				"data: [DONE]\n\n", tc.indices[0], tc.indices[1])
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)

			c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
			msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.ToolCalls) != 2 {
				t.Fatalf("tool calls=%d want 2 (%+v)", len(msg.ToolCalls), msg.ToolCalls)
			}
			if msg.ToolCalls[0].ID != "tc_a" || msg.ToolCalls[1].ID != "tc_b" {
				t.Fatalf("order=%q,%q", msg.ToolCalls[0].ID, msg.ToolCalls[1].ID)
			}
		})
	}
}

func TestChatStreamHooksSkipsMalformedChunk(t *testing.T) {
	// A malformed data line mid-stream must not break assembly of the rest.
	const body = "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"conte\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hi!" {
		t.Fatalf("content=%q want Hi!", msg.Content)
	}
}

func TestChatStreamHooksFinishReasonLength(t *testing.T) {
	const body = "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "partial" {
		t.Fatalf("content=%q", msg.Content)
	}
	if msg.StopReason != "length" || !msg.Truncated() {
		t.Fatalf("StopReason=%q Truncated=%v want length/true", msg.StopReason, msg.Truncated())
	}
}

func TestUsageParsedAcrossPaths(t *testing.T) {
	t.Run("openai stream usage chunk", func(t *testing.T) {
		body := "" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4}}\n\n" +
			"data: [DONE]\n\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The request must ask for the usage chunk.
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), "\"include_usage\":true") {
				t.Errorf("stream request missing stream_options.include_usage: %s", raw)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
		msg, err := c.ChatStreamHooks(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, StreamHooks{})
		if err != nil {
			t.Fatal(err)
		}
		if msg.Usage.InputTokens != 11 || msg.Usage.OutputTokens != 4 {
			t.Fatalf("usage=%+v want {11 4}", msg.Usage)
		}
	})

	t.Run("openai non-stream", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
		}))
		t.Cleanup(srv.Close)
		c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
		msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Usage.InputTokens != 7 || msg.Usage.OutputTokens != 3 {
			t.Fatalf("usage=%+v want {7 3}", msg.Usage)
		}
	})

	t.Run("anthropic stream message_start plus delta", func(t *testing.T) {
		msg := Message{Role: "assistant"}
		if err := applyAnthropicSSE(`{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`, "message_start", &msg, map[int]*anthToolAcc{}, StreamHooks{}); err != nil {
			t.Fatal(err)
		}
		if err := applyAnthropicSSE(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`, "message_delta", &msg, map[int]*anthToolAcc{}, StreamHooks{}); err != nil {
			t.Fatal(err)
		}
		if msg.Usage.InputTokens != 12 || msg.Usage.OutputTokens != 9 {
			t.Fatalf("usage=%+v want {12 9}", msg.Usage)
		}
	})
}
